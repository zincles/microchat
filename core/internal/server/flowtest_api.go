package server

import (
	"net/http"
	"strings"
)

// flowTestReq：编辑器的「测试发送」——只跑一次图，不建会话、不落库。
type flowTestReq struct {
	Message  string `json:"message"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

func (s *Server) testFlowAgent(w http.ResponseWriter, r *http.Request) {
	agents := s.loadAgents()
	agent, ok := agents.Get(r.PathValue("agent_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "agent 不存在")
		return
	}
	if agent.Kind != "flow" {
		writeError(w, http.StatusBadRequest, "invalid", "这个 Agent 不是 FlowAgent")
		return
	}
	var req flowTestReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "测试消息不能为空")
		return
	}
	result, err := s.chat.TestFlow(r.Context(), agent, req.Message, req.Provider, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
