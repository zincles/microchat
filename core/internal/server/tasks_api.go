package server

import (
	"net/http"

	"microchat/internal/queue"
	"microchat/internal/task"
)

// taskBoard：任务面板 —— 进程内的事实（与 `-debug tasks` 同一份 Board）。
//
// 只读：轮询用（web 右上角那颗徽标与它的浮板就问它），不进库。TaskID 是 UUIDv7，
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
	view.Board = board
	// 队列（2026-11-01）：与原子任务一起给出去。
	//
	// ⚠ 2026-11-01 之前这里有两层**权限过滤**（非 admin 只看自己会话的任务、队列只有 admin 看得见）。
	//   账号砍掉之后**没有了** —— 面板就是"这台机器上此刻在跑什么"，本来就只有一个使用者。
	if s.queues != nil {
		view.Queues = s.queues.List()
	}
	writeJSON(w, http.StatusOK, view)
}
