package server

import (
	"fmt"
	"net/http"
	"strings"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
)

// 对话记录导出 / 导入（JSON；2026-10-11 起，2026-10-17 用户扩了元数据）：
//
// v1（2026-10-11）：**只带足够重建对话的** —— 标题 + 每条 `{role, content}`。
// v2（2026-10-17 用户定："不止导入聊天记录，还导入当前 Agent / 渠道 / 模型"）：
//   **再加三个软引用** —— `provider` / `model` / `agent_id`（+ `agent_name` 只为报错时说得出口）。
//   就这三个：它们是会话自己的三格绑定（`sessions.provider/model/agent_id`），导回去"这条对话
//   当初是谁在什么模型上说的"就齐了。
//   **仍然不带**：system prompt（会话级覆盖）/ 摘要 / 思考 / 用量 / 时间戳 / 消息 id ——
//   那些不是"重建对话"必需，而且带回来就得面对"本机对不上怎么办"（宁可少带，不许乱猜）。
//   **agent 的"定义"不在这份文件里** ✗：人设搬家走 agent 卡带（`{version:2, agent, segments}`）；
//   本机没有那个 agent ⇒ 照绑 id（界面显示「不在列表」、人设回落内置默认），并在回执里
//   `agent_missing: true` 明说 —— **不静默换成 default_agent**（那等于悄悄换了个人）。
// 导入**不经过** `createSession` ⇒ 不会插 agent 的首段对话（JSON 自带完整对话）。

// sessionExport：导出的形状（导入也收它）。`version`：1 = 老文件（没有那三格 ⇒ 用本机缺省）。
type sessionExport struct {
	Version   int             `json:"version"`
	Title     string          `json:"title"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	AgentName string          `json:"agent_name,omitempty"`
	Messages  []exportMessage `json:"messages"`
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
	out := sessionExport{
		Version: 2, Title: session.Title,
		Provider: session.Provider, Model: session.Model, AgentID: session.AgentID,
		AgentName: s.agentName(session.AgentID),
		Messages:  make([]exportMessage, 0, len(messages)),
	}
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
	if req.Version > 2 {
		writeError(w, http.StatusBadRequest, "invalid",
			fmt.Sprintf("这份文件的 version = %d 比本程序认识的（2）新：请升级后再导入", req.Version))
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
	// v2 的三格软引用：文件给了就照抄（**渠道/模型不校验存在性** —— 本机没这个渠道也照样绑，
	// 界面会显示它、真发时当场报"未配置模型"，这比偷换成缺省诚实）；没给（v1 文件）⇒ 用本机缺省。
	provider, modelID := strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)
	if provider == "" {
		provider = cfg.Defaults.Provider
	}
	if modelID == "" {
		modelID = cfg.Defaults.Model
	}
	title := strings.TrimSpace(req.Title)
	session, err := s.store.CreateSession(identityOf(r.Context()).UserID, provider, modelID, "", title)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// agent：文件里点名了就绑它（本机有没有都绑 —— 软引用），只有**没点名**才用 default_agent。
	agentID := strings.TrimSpace(req.AgentID)
	agentMissing := agentID != "" && !s.agentExists(agentID)
	if agentID == "" {
		agentID = s.defaultAgentID()
	}
	if agentID != "" && agentID != session.AgentID {
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
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": sessionFresh, "messages": len(parsed),
		// agent_missing：文件点的 agent 本机没有 ⇒ 照绑了 id，但人设会回落内置默认（界面要说一句）。
		"agent_missing": agentMissing,
	})
}
