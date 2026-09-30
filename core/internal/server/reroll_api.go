package server

import (
	"errors"
	"net/http"
	"strconv"

	"microchat/internal/reroll"
	"microchat/internal/store"
)

// ── 重摇模式：`/sessions/{session_id}/reroll` ────────────────────────────
//
// 四条路由 + 一条退出（口径见 `DEFINE.md`「重摇（Re-roll）」）：
//
//	POST   .../reroll          **202** 受理（进模式并立刻摇一次；已在模式里 ⇒ 再摇一版）
//	GET    .../reroll          状态：共几版 / 当前第几版 / 有没有在摇 / 每版的预览
//	POST   .../reroll/switch   把第 N 版 apply 回那条消息（`message_id` 不变）
//	DELETE .../reroll/{idx}    删掉第 N 版（后方位次统一 -1，再 apply 到 clamp 后的位置）
//	DELETE .../reroll          **退出重摇模式**（清列表；message 保留当前这版）
//
// 错误口径：尾条不是 assistant ⇒ **400**（明说是"重发"不是重摇）；已有重摇在跑 / 正在生成 ⇒ **409**；
// `idx` 越界 ⇒ **400**；没进模式 ⇒ **404**；那一版还在摇 ⇒ **409**。

// RerollSwitchReq：`POST .../reroll/switch` 的请求体 —— 只要一个位次。
type RerollSwitchReq struct {
	Idx *int `json:"idx"`
}

// rerollEnter：进入重摇模式并**立刻发起一次重摇**（202 受理，与一轮生成同一条脾气）。
//
// 已经在模式里 ⇒ 再摇一版（列表往后追一版）—— 这就是 `‹ 2/3 ›` 那个分母的来源。
func (s *Server) rerollEnter(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	accepted, err := s.rerolls.Enter(*session)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, accepted)
}

// rerollState：这一条会话的重摇状态（没进模式 ⇒ `active: false`，**不是 404**：
// "没在重摇"是这条会话的正常状态，问它就是问"现在有没有"）。
func (s *Server) rerollState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.rerolls.State(r.PathValue("session_id")))
}

// rerollSwitch：选中一版 ⇒ 就地重建库里那条 Message（**UUID 不变**）。
func (s *Server) rerollSwitch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	var req RerollSwitchReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Idx == nil {
		missingField(w, "idx")
		return
	}
	state, err := s.rerolls.Switch(r.PathValue("session_id"), *req.Idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// rerollDelete：删掉一版（`idx` 是**路径参数**：它是位次，不是 id）。
//
// 删到只剩一版 ⇒ 后端**退出模式并清列表**，这次**不 apply** —— message 保留当前这版。
func (s *Server) rerollDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	idx, err := strconv.Atoi(trimSpace(r.PathValue("idx")))
	if err != nil || idx <= 0 {
		writeError(w, http.StatusBadRequest, "invalid", "idx 要是正整数（第几版，1-based）")
		return
	}
	state, err := s.rerolls.Delete(r.PathValue("session_id"), idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// rerollClear：**退出重摇模式**（清列表）；在摇的那一趟顺手按停。
//
// 退出**不 apply、也不回原文**：库里那条 Message 保留当前这版（它就是用户最后选中的那版）。
func (s *Server) rerollClear(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	s.rerolls.Clear(r.PathValue("session_id"))
	w.WriteHeader(http.StatusNoContent)
}

// writeRerollError：重摇那一路的失败 → 固定的错误体（客户端按 `code` 分支）。
func writeRerollError(w http.ResponseWriter, err error) {
	var rerollError *reroll.Error
	var invalid store.InvalidError
	switch {
	case errors.As(err, &rerollError):
		writeError(w, statusForCode(rerollError.Code), rerollError.Code, rerollError.Message)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid", invalid.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
