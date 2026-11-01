package server

import (
	"net/http"

	"microchat/internal/queue"
	"microchat/internal/task"
)

// taskBoard：任务面板 —— 进程内的事实（与 `-debug tasks` 同一份 Board）。
//
// 只读：轮询用（TUI 底栏的 Tasks 指示器就问它），不进库。TaskID 是 UUIDv7，
// 重启就没 —— "后端一重启就没有在跑的"是诚实的事实，不是缺功能。
// taskBoardView：面板那一屏 —— 原子任务 + **队列任务**（2026-11-01 加）。
//
// **内嵌** `task.Board` ⇒ `running` / `tasks` / `now` 三格保持扁平（老客户端一个字都不用改），
// 队列只是多出来的一格 `queues`。面板上"队列"与"原子任务"是两层东西：
// 队列说"这一大批跑到第几步"，原子任务说"此刻在跑哪一发"。
type taskBoardView struct {
	task.Board
	Queues []queue.Queue `json:"queues"`
}

func (s *Server) taskBoard(w http.ResponseWriter, r *http.Request) {
	view := taskBoardView{Queues: []queue.Queue{}}
	board := task.Board{Tasks: []task.Record{}}
	if s.tasks != nil {
		board = s.tasks.Board()
	}
	id := identityOf(r.Context())
	admin := id.Admin || id.Sys
	if !admin {
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
	}
	view.Board = board
	// 队列：**admin 才看得见**（也只有 admin 排得了 —— 它绕过逐会话的归属检查）。
	if admin && s.queues != nil {
		view.Queues = s.queues.List()
	}
	writeJSON(w, http.StatusOK, view)
}
