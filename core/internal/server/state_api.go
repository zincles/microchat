package server

import (
	"net/http"

	"microchat/internal/model"
	"microchat/internal/state"
)

// effectiveSystemPrompt：生效的系统提示词 —— **解析只有一处**（`chat.EffectiveSystemPrompt`：
// 会话级覆盖优先，否则用 agent 的，内置默认也算）。这里只是转发，免得生出第二份答案。
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

// getSessionOutgoing：「下次真会发出去的东西」—— 标签已剔除、世界状态已注入、按摘要收拢。
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
