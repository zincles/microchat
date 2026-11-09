package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

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

// TurnPoll：一次轮询同时拿「状态 + 增量」—— 远程部署把两发串行 HTTP 合成一发，
// 少一个完整 RTT（CF 链路 500–1000ms 时这不是小钱）。
type TurnPoll struct {
	Status turn.Status      `json:"status"`
	Slice  turn.StreamSlice `json:"slice"`
}

// turnPoll：`GET .../turn/poll?from=&think_from=` —— status 与 text 的合并读口。
//
// 游标参数与 `/turn/text` 同一套校验；会话不存在照样 404。
func (s *Server) turnPoll(w http.ResponseWriter, r *http.Request) {
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
	sid := r.PathValue("session_id")
	writeJSON(w, http.StatusOK, TurnPoll{
		Status: s.chat.Status(sid),
		Slice:  s.chat.StreamSince(sid, from, thinkFrom),
	})
}

// turnEvents：SSE 版的一轮增量（2026-11-03：远程/CF 下不要每个增量都走一个请求）。
//
// 服务端内部 100ms 看一眼 turn 缓冲，只把**变化**写成 SSE 事件；状态最多 500ms 一次，
// 用来让前端更新耗时。`id:` 是字节游标 `next:think_next`，浏览器断线重连会自动带
// `Last-Event-ID`，这里优先用它，避免重放旧文字。
//
// 这是**只读**流：不消费、不改变 turn；事件里的 slice 与 `/turn/poll` 形状一致。
func (s *Server) turnEvents(w http.ResponseWriter, r *http.Request) {
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "当前连接不支持流式输出")
		return
	}
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		var a, b int
		if _, err := fmt.Sscanf(id, "%d:%d", &a, &b); err == nil && a >= 0 && b >= 0 {
			from, thinkFrom = a, b
		}
	}
	sid := r.PathValue("session_id")
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastText, lastThink := -1, -1
	lastPhase := turn.Phase("")
	lastTool := ""
	lastSources := ""
	var lastStatus time.Time
	for {
		status := s.chat.Status(sid)
		slice := s.chat.StreamSince(sid, from, thinkFrom)
		toolKey := ""
		if status.Tool != nil {
			toolKey = status.Tool.Kind + "\x00" + status.Tool.State
		}
		sourceKey := ""
		for _, source := range status.Sources {
			sourceKey += source.URL + "\x00"
		}
		changed := slice.Next != lastText || slice.ThinkNext != lastThink ||
			status.Phase != lastPhase || toolKey != lastTool || sourceKey != lastSources
		if changed || time.Since(lastStatus) >= 500*time.Millisecond {
			payload, err := json.Marshal(TurnPoll{Status: status, Slice: slice})
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d:%d\ndata: %s\n\n", slice.Next, slice.ThinkNext, payload); err != nil {
				return
			}
			flusher.Flush()
			from, thinkFrom = slice.Next, slice.ThinkNext
			lastText, lastThink = slice.Next, slice.ThinkNext
			lastPhase = status.Phase
			lastTool = toolKey
			lastSources = sourceKey
			lastStatus = time.Now()
		}
		if !status.Phase.IsBusy() {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
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
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
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
