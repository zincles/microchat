package server

import (
	"net/http"

	"microchat/internal/providers"
)

// lastPayload：最近一次真正**拼好**的请求（含请求头，Authorization 已打码）。
//
// **留着它是因为它必要**：网关为什么 403 只能靠它 —— 拒的往往是头不是体（见 AGENTS.md 的实测表）。
// 没发过 ⇒ `null`（照实说，不编）。
func (s *Server) lastPayload(w http.ResponseWriter, r *http.Request) {
	payload, ok := providers.LastSent()
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
