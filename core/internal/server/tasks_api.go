package server

import (
	"net/http"
)

// taskBoard：任务面板 —— 进程内的事实（与 `-debug tasks` 同一份 Board）。
//
// 只读：轮询用（TUI 底栏的 Tasks 指示器就问它），不进库。TaskID 是 UUIDv7，
// 重启就没 —— "后端一重启就没有在跑的"是诚实的事实，不是缺功能。
func (s *Server) taskBoard(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"running": 0, "tasks": []any{}, "now": 0})
		return
	}
	writeJSON(w, http.StatusOK, s.tasks.Board())
}
