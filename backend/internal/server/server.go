// Package server：HTTP 层。**接口形状照搬 AGENTS.md 那张表**（43 条），一条条搬。
//
// 全部挂在 /api/v1 下；错误体固定 {"error":{"code","message"}}；配了口令就全都要 Bearer。
package server

import (
	"encoding/json"
	"net/http"

	"microchat/backend/internal/store"
)

type Server struct {
	store *store.Store
	mux   *http.ServeMux
}

func New(st *store.Store) *Server {
	s := &Server{store: st, mux: http.NewServeMux()}
	// Go 1.22+ 的 ServeMux 原生支持 `GET /x/{id}` 这种模式 —— 连路由库都不需要。
	s.mux.HandleFunc("GET /api/v1/health", s.health)
	s.mux.HandleFunc("GET /api/v1/debug/state", s.debugState)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// health：探针（Rust 版回 {"status","version"}）。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "0.1.0-go"})
}

// debugState：后端自述 —— 老老实实的一眼账，先只有计数。
func (s *Server) debugState(w http.ResponseWriter, r *http.Request) {
	conversations, messages, summaries, err := s.store.Counts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"store": map[string]any{
			"conversations": conversations,
			"messages":      messages,
			"summaries":     summaries,
		},
		"note": "Go 版的骨架：表结构已照搬，接口一条条搬",
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
