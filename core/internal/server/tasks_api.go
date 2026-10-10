package server

import (
	"microchat/internal/task"
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
	board := s.tasks.Board()
	id := identityOf(r.Context())
	if id.Admin || id.Sys {
		writeJSON(w, http.StatusOK, board)
		return
	}
	// 非 admin：只看得见**自己会话**的任务（面板跨用户，别拿别人的会话 id 打招呼）。
	kept := make([]task.Record, 0, len(board.Tasks))
	running := 0
	for _, rec := range board.Tasks {
		if rec.Session == nil {
			continue // 无会话的（刷新模型那类）只在 admin 面板出现
		}
		session, err := s.store.GetSession(*rec.Session)
		if err != nil || session == nil || session.OwnerID != id.UserID {
			continue
		}
		kept = append(kept, rec)
		if rec.Running() {
			running++
		}
	}
	board.Tasks = kept
	board.Running = running
	writeJSON(w, http.StatusOK, board)
}
