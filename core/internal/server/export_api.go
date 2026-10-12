package server

import (
	"fmt"
	"net/http"
	"strings"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
)

// 对话记录导出 / 导入（JSON，2026-10-11 用户定）：
//
// **只带足够重建对话的东西** —— 标题 + 每条 `{role, content}`（正文原样：`<state>` 块留着，
// 导回去状态照常现演）。system prompt / agent / 摘要 / 思考 / 用量 / 时间戳一概不带。
// 导入**不经过** `createSession` ⇒ 不会插 agent 的首段对话（JSON 自带完整对话）。

// sessionExport：导出的形状（导入也收它；`version` 先留着，将来变形状好认）。
type sessionExport struct {
	Version  int             `json:"version"`
	Title    string          `json:"title"`
	Messages []exportMessage `json:"messages"`
}

type exportMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// exportSession：`GET /sessions/{session_id}/export` —— 会话域（owner 自己就能导）。
func (s *Server) exportSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := sessionExport{Version: 1, Title: session.Title, Messages: make([]exportMessage, 0, len(messages))}
	for _, m := range messages {
		out.Messages = append(out.Messages, exportMessage{Role: string(m.Role), Content: m.Content})
	}
	writeJSON(w, http.StatusOK, out)
}

// importJSON：`POST /sessions/import-json` —— 从导出的 JSON 建一条新会话。
//
// 校验**严格**（这是我们自己的格式，不是抓来的脏数据）：role 只许 user / assistant、
// 空 messages ⇒ 400；标题给了就用，没给 ⇒ 首条 user 起（与 ST 导入同口径）。
// 写口 = 设置面（admin）——与 ST 导入一致。
func (s *Server) importJSON(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req sessionExport
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "没有可导入的消息")
		return
	}
	parsed := make([]store.ImportMessage, 0, len(req.Messages))
	for index, m := range req.Messages {
		role := strings.TrimSpace(m.Role)
		if role != "user" && role != "assistant" {
			writeError(w, http.StatusBadRequest, "invalid",
				fmt.Sprintf("第 %d 条的 role 只许 user / assistant（得到 %q）", index+1, m.Role))
			return
		}
		parsed = append(parsed, store.ImportMessage{IsUser: role == "user", Content: m.Content})
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	title := strings.TrimSpace(req.Title)
	session, err := s.store.CreateSession(identityOf(r.Context()).UserID, cfg.Defaults.Provider, cfg.Defaults.Model, "", title)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if agentID := s.defaultAgentID(); agentID != "" && agentID != session.AgentID {
		if err := s.store.SetSessionAgent(session.ID, agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		session.AgentID = agentID
	}
	if err := s.store.ImportMessages(session.ID, parsed); err != nil {
		writeStoreError(w, err)
		return
	}
	if title == "" {
		for _, item := range parsed {
			if item.IsUser && strings.TrimSpace(item.Content) != "" {
				_, _ = s.store.SetTitleIfEmpty(session.ID, model.TitleFrom(item.Content, cfg.Chat.TitleChars))
				break
			}
		}
	}
	sessionFresh, err := s.store.GetSession(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": sessionFresh, "messages": len(parsed)})
}
