package server

import (
	"net/http"

	"microchat/internal/model"
	"microchat/internal/state"
)

// effectiveSystemPrompt：生效的系统提示词 —— **解析只有一处**（`state.ResolveSystemPrompt`：
// 会话级覆盖优先，否则用 agent 的，再不然用内置默认那句；**永不返回空串**）。
// 这里只是转发（`chat.EffectiveSystemPrompt` 把现读的 agent 配置喂给它），免得生出第二份答案。
func (s *Server) effectiveSystemPrompt(session model.Session) (string, state.PromptSource) {
	return s.chat.EffectiveSystemPrompt(session)
}

// viewOf：现演一份这条会话的状态快照（分层 + 沿路径 fold）。
func (s *Server) viewOf(session model.Session) (state.View, error) {
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		return state.View{}, err
	}
	systemPrompt, source := s.effectiveSystemPrompt(session)
	return state.FromSources(session.ID, systemPrompt, source, messages), nil
}

// getSessionState：某会话的世界状态：全局打底 + 顺着正文重演出来的本会话改动 + 生效值。
func (s *Server) getSessionState(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	view, err := s.viewOf(*session)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// getSessionOutgoing：**(b) 当前已定历史的载荷** —— 标签已剔除、世界状态已注入、按摘要收拢。
//
// 口径钉死：它**不含还没发出去的那一句**（那正是 (c) 的事，见 `postSessionOutgoing`）；
// 它是**算出来的**（改旧消息 / 换 agent / 世界状态变了 ⇒ 立刻不同），不是历史快照
// （快照是 (a)：`GET /debug/last-payload`）。
func (s *Server) getSessionOutgoing(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	summaries, err := s.store.ListSummaries(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	systemPrompt, source := s.effectiveSystemPrompt(*session)
	view := state.FromSources(session.ID, systemPrompt, source, messages)
	writeJSON(w, http.StatusOK, state.BuildOutgoing(systemPrompt, messages, summaries, view.Tables))
}

// OutgoingReq：`POST /sessions/{session_id}/outgoing` 的请求体 —— 待发的那一句。
type OutgoingReq struct {
	Content *string `json:"content"`
}

// postSessionOutgoing：**(c) 把这条 content 当成即将追加的那句用户消息之后**，真会发出去的东西。
//
// **只算不写**：不落库、不改任何状态（消息条数一个不变）；响应形状与 (b) 逐项同字段。
// 装配不走第二条路（`chat.OutgoingWithPending` ⇒ `assemble` ⇒ `state.FromSources` +
// `state.BuildOutgoing`）—— 待发那句里带 `<state>` 块时，注入系统提示词的状态表会跟着变，
// 所以这不是"(b) + 一条消息"，必须重算。
//
// 缺 `content` / 只有空白 ⇒ **400**（空的一句不是"要发一句空的"，是调用方写错了）。
func (s *Server) postSessionOutgoing(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var req OutgoingReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == nil || trimSpace(*req.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid", "content 不能为空")
		return
	}
	outgoing, err := s.chat.OutgoingWithPending(*session, *req.Content)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, outgoing)
}

// requireSession：取会话；不存在 ⇒ 404（顺带把"会话不见了"收在一处）。
func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (*model.Session, bool) {
	session, err := s.store.GetSession(r.PathValue("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	if session == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return nil, false
	}
	return session, true
}
