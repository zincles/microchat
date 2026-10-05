package server

import (
	"errors"
	"net/http"
	"strconv"

	"microchat/internal/store"
	"microchat/internal/turn"
)

// SendReq：`POST /sessions/{session_id}/messages` 的请求体。
//
// 正文必填（缺了 / 只有空白 ⇒ 422）：空消息不是"发了一句空的"，是调用方写错了。
type SendReq struct {
	Content *string `json:"content"`
}

// sendMessage：**受理与生成分开** —— 立刻 202 回执（含 `turn.message_id`），生成在后台跑。
//
// 客户端拿 `turn.message_id` 指认那条回复（受理时就算好了，那会儿还没进库 —— **整段拿到才 INSERT**），
// 再按 `turn.phase` 轮询 `/status`。同一会话在跑时再发 ⇒ **409**（不排队）。
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == nil || trimSpace(*req.Content) == "" {
		missingField(w, "content")
		return
	}
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	accepted, err := s.chat.Accept(*session, *req.Content)
	switch {
	case errors.Is(err, turn.ErrRerollBusy):
		// 与生成共用同一把闸：重摇还没摇完时发消息 = 409（说清是哪一件在跑）
		writeError(w, http.StatusConflict, "conflict",
			"这个会话正在重摇（尾条那条回复还没定版）：先 `/reroll switch <n>` 选一版，或 `/reroll off` 退出")
	case errors.Is(err, turn.ErrBusy):
		writeError(w, http.StatusConflict, "conflict", "这个会话还在生成中，等它跑完或先按停止")
	case err != nil:
		writeStoreError(w, err)
	default:
		writeJSON(w, http.StatusAccepted, accepted)
	}
}

// resendHistory：重发历史 —— **不落新消息**，拿现有历史再跑一轮生成。
//
// 与 sendMessage 共用 202 回执形状（`Accepted`，只是 `user` 为空 —— 没落新消息，没什么可回的）；
// 空会话 ⇒ 400（没历史发什么）；在跑 ⇒ 409（同一把闸，不排队）。
func (s *Server) resendHistory(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	accepted, err := s.chat.Resend(*session)
	switch {
	case errors.Is(err, turn.ErrRerollBusy):
		writeError(w, http.StatusConflict, "conflict",
			"这个会话正在重摇（尾条那条回复还没定版）：先 `/reroll switch <n>` 选一版，或 `/reroll off` 退出")
	case errors.Is(err, turn.ErrBusy):
		writeError(w, http.StatusConflict, "conflict", "这个会话还在生成中，等它跑完或先按停止")
	case errors.As(err, new(store.InvalidError)):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case err != nil:
		writeStoreError(w, err)
	default:
		writeJSON(w, http.StatusAccepted, accepted)
	}
}

// turnStatus：这一轮现在处在哪一档（`idle` / `pending` / `streaming` / `error`）+ 字数 / 耗时。
//
// 会话不存在 ⇒ 404（与消息级路由一个口径：别对着不存在的会话答"idle"，那会让人以为它是好的）。
func (s *Server) turnStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.chat.Status(r.PathValue("session_id")))
}

// turnText：这一轮已生成正文的**增量**（两条游标：正文 + 思考）—— 只服务动画。
//
// 读**不消费**：多个客户端与断线重连各拿各的，互不偷；`done` 表示这轮结束，
// 结束后仍可补拉尾巴（那时候库里也有整条消息了）。
func (s *Server) turnText(w http.ResponseWriter, r *http.Request) {
	from, ok := queryInt(w, r, "from")
	if !ok {
		return
	}
	thinkFrom, ok := queryInt(w, r, "think_from")
	if !ok {
		return
	}
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.chat.StreamSince(r.PathValue("session_id"), from, thinkFrom))
}

// queryInt：游标参数 —— 缺省 0；写了但不是非负整数 ⇒ 422。
//
// **不静默当 0**：那会让客户端"以为拿到了增量、其实从头再来"（多画一遍还算轻的，
// 断线重连时正好把动画倒回去）。
func queryInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", name+" 要是非负整数（第几个字符）")
		return 0, false
	}
	return value, true
}

// stopTurn：按停这一轮。**幂等**：没在跑也 200（`stopped: false`）。
//
// 不查会话是否存在：这个动作问的是"进程里有没有活在跑"，
// 答案对不存在的会话同样是"没有"（回 false 比 404 更贴它的语义）。
func (s *Server) stopTurn(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": s.chat.Stop(r.PathValue("session_id"))})
}

// sessionContext：这次出站会用掉多少上下文、还剩多少 —— **只有数字**。
func (s *Server) sessionContext(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	usage, err := s.chat.ContextUsage(*session)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}
