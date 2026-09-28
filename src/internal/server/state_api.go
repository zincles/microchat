package server

import (
	"net/http"

	"microchat/internal/model"
	"microchat/internal/state"
)

// effectiveSystemPrompt：生效的系统提示词 —— 会话级覆盖优先，否则用 agent 的（内置默认也算）。
//
// **这就是真会发给模型的那份**：世界状态底子、出站消息、界面显示都用它，
// 免得"界面上写的提示词"和"实际发出去的"成了两份东西。返回值第二项是它的来源。
func (s *Server) effectiveSystemPrompt(conversation model.Conversation) (string, state.PromptSource) {
	if own := trimSpace(conversation.SystemPrompt); own != "" {
		return own, state.PromptFromConversation
	}
	agent, _ := s.loadAgents().Resolve(conversation.AgentID)
	return agent.SystemPrompt, state.PromptFromAgent
}

// viewOf：现演一份这条会话的状态快照（分层 + 沿路径 fold）。
func (s *Server) viewOf(conversation model.Conversation) (state.View, error) {
	messages, err := s.store.ListMessages(conversation.ID)
	if err != nil {
		return state.View{}, err
	}
	systemPrompt, source := s.effectiveSystemPrompt(conversation)
	return state.FromSources(conversation.ID, systemPrompt, source, messages), nil
}

// getConversationState：某会话的世界状态：全局打底 + 顺着正文重演出来的本会话改动 + 生效值。
func (s *Server) getConversationState(w http.ResponseWriter, r *http.Request) {
	conversation, ok := s.requireConversation(w, r)
	if !ok {
		return
	}
	view, err := s.viewOf(*conversation)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// getConversationOutgoing：「下次真会发出去的东西」—— 标签已剔除、世界状态已注入、按摘要收拢。
func (s *Server) getConversationOutgoing(w http.ResponseWriter, r *http.Request) {
	conversation, ok := s.requireConversation(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessages(conversation.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	summaries, err := s.store.ListSummaries(conversation.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	systemPrompt, source := s.effectiveSystemPrompt(*conversation)
	view := state.FromSources(conversation.ID, systemPrompt, source, messages)
	writeJSON(w, http.StatusOK, state.BuildOutgoing(systemPrompt, messages, summaries, view.Tables))
}

// requireConversation：取会话；不存在 ⇒ 404（顺带把"会话不见了"收在一处）。
func (s *Server) requireConversation(w http.ResponseWriter, r *http.Request) (*model.Conversation, bool) {
	conversation, err := s.store.GetConversation(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	if conversation == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return nil, false
	}
	return conversation, true
}
