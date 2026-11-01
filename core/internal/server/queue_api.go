package server

// queue_api.go —— **队列任务**（2026-11-01 用户定）。
//
//	POST /api/v1/tasks/queues                     排一个队列，立刻开跑
//	POST /api/v1/tasks/queues/{queue_id}/cancel   随时取消（在飞的那一发一起掐断）
//
// 队列的语义全在 `internal/queue` 里（串行 / 一步失败即整体结束 / 可取消）；
// 这里只做三件事：**解析、定权限、定状态码** —— 与别的 handler 同一个分工。

import (
	"errors"
	"net/http"
	"strings"

	"microchat/internal/queue"
)

// EnqueueQueueReq：排一个队列 —— **两种给法二选一**（都写 ⇒ 400，不猜）。
//
//	steps      —— 显式步骤列表，每步一个 `{session_id, blocks}`（跨会话也走这条）
//	session_id —— 同一个会话重复 `times` 次（用户那个「这条对话连压 100 次」就是它）
//
// `kind` 缺省 = `compact`（现在也只有它）；`blocks` 缺省 0 = 用 `chat.compact_blocks`。
type EnqueueQueueReq struct {
	Kind      string       `json:"kind"`
	Title     string       `json:"title"`
	Steps     []queue.Step `json:"steps"`
	SessionID string       `json:"session_id"`
	Blocks    int          `json:"blocks"`
	Times     int          `json:"times"`
}

// enqueueQueue：`POST /tasks/queues` —— 把 N 步排成一个队列。
//
// **admin 专属**：队列会绕过逐会话的归属检查（它直接调 `compact.Compact`），
// 所以不能让人拿别人的会话 id 去排队 —— 那等于给别人灌 token。口径同 `/sessions/import-st`。
func (s *Server) enqueueQueue(w http.ResponseWriter, r *http.Request) {
	var req EnqueueQueueReq
	if !decodeJSON(w, r, &req) {
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = queue.KindCompact
	}
	explicit := len(req.Steps) > 0
	repeated := strings.TrimSpace(req.SessionID) != ""
	switch {
	case explicit && repeated:
		writeError(w, http.StatusBadRequest, "invalid",
			"steps 与 session_id 只能给一种（两个都给就说不清要排什么）")
		return
	case !explicit && !repeated:
		writeError(w, http.StatusBadRequest, "invalid",
			"要么给 steps（显式步骤列表），要么给 session_id + blocks + times")
		return
	}
	steps := req.Steps
	if repeated {
		if req.Times <= 0 {
			writeError(w, http.StatusBadRequest, "invalid", "times 必须是正整数")
			return
		}
		steps = make([]queue.Step, req.Times)
		for index := range steps {
			steps[index] = queue.Step{SessionID: strings.TrimSpace(req.SessionID), Blocks: req.Blocks}
		}
	}
	created, err := s.queues.Enqueue(kind, strings.TrimSpace(req.Title), steps)
	if err != nil {
		writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// cancelQueue：`POST /tasks/queues/{queue_id}/cancel` —— **随时取消**。
//
// 幂等（已经结束的再按一次，原样回它现在的样子）；在飞的那一发会被 ctx 一起掐断 ——
// 压缩要整段拿到才落库，所以**不会留半条摘要**。
func (s *Server) cancelQueue(w http.ResponseWriter, r *http.Request) {
	canceled, err := s.queues.Cancel(r.PathValue("queue_id"))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, canceled)
}

// writeQueueError：队列那几种错的落点（与别处的 code 口径一致）。
func writeQueueError(w http.ResponseWriter, err error) {
	var invalid queue.InvalidError
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid", invalid.Error())
	case errors.Is(err, queue.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "没有这个队列")
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
