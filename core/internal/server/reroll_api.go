package server

import (
	"errors"
	"net/http"
	"strconv"

	"microchat/internal/reroll"
	"microchat/internal/store"
)

// ── 重摇模式：两个平行家族（口径见 `DEFINE.md`「重摇（Re-roll）」）─────────────
//
//	消息级 `.../reroll-message`：
//	  POST   .../reroll-message          **202** 受理（进模式并立刻摇一次；已在模式里 ⇒ 再摇一版）
//	  GET    .../reroll-message          状态：共几版 / 当前第几版 / 有没有在摇 / 每版的预览
//	  POST   .../reroll-message/switch   把第 N 版 apply 回那条消息（`message_id` 不变）
//	  DELETE .../reroll-message/{idx}    删掉第 N 版（后方位次统一 -1，再 apply 到 clamp 后的位置）
//	  DELETE .../reroll-message          **退出重摇模式**（清列表；message 保留当前这版）
//
//	摘要级 `.../reroll-summary`（形状与上面一一对应，只是 `kind = TargetSummary`）：
//	  POST   .../reroll-summary          体 `{"idx": N}`（1-based 消息序号）⇒ 上溯到根摘要
//	  GET    .../reroll-summary          外加 `target_kind=summary` 与目标盖的 `from_idx/to_idx`
//	  POST   .../reroll-summary/switch   把第 N 版 apply 回那条摘要（摘要 id / 区间一概不变）
//	  DELETE .../reroll-summary/{idx}    删掉第 N 版
//	  DELETE .../reroll-summary          退出摘要重摇模式
//
// 错误口径：尾条不是 assistant ⇒ **400**（明说是"重发"不是重摇）；已有重摇在跑 / 正在生成 ⇒ **409**；
// `idx` 越界 / 摘要没盖住这一条 ⇒ **400**；没进模式 ⇒ **404**；那一版还在摇 ⇒ **409**。

// RerollSwitchReq：`POST .../reroll-*/switch` 的请求体 —— 只要一个位次（两个家族同一个形状）。
type RerollSwitchReq struct {
	Idx *int `json:"idx"`
}

// RerollSummaryReq：`POST .../reroll-summary` 的请求体 —— `idx` 是 **1-based 消息序号**
// （不是摘要 id：同树任意一条都会上溯到同一个根）。
type RerollSummaryReq struct {
	Idx *int `json:"idx"`
}

// ── 消息级 ────────────────────────────────────────────────────────────────

// rerollMessageEnter：进入重摇模式并**立刻发起一次重摇**（202 受理，与一轮生成同一条脾气）。
//
// 已经在模式里 ⇒ 再摇一版（列表往后追一版）—— 这就是 `‹ 2/3 ›` 那个分母的来源。
func (s *Server) rerollMessageEnter(w http.ResponseWriter, r *http.Request) {
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

// rerollMessageState：这一条会话的重摇状态（没进模式 ⇒ `active: false`，**不是 404**：
// "没在重摇"是这条会话的正常状态，问它就是问"现在有没有"）。
func (s *Server) rerollMessageState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.rerolls.State(r.PathValue("session_id"), reroll.TargetMessage))
}

// rerollMessageSwitch：选中一版 ⇒ 就地重建库里那条 Message（**UUID 不变**）。
func (s *Server) rerollMessageSwitch(w http.ResponseWriter, r *http.Request) {
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
	state, err := s.rerolls.Switch(r.PathValue("session_id"), reroll.TargetMessage, *req.Idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// rerollMessageDelete：删掉一版（`idx` 是**路径参数**：它是位次，不是 id）。
//
// 删到只剩一版 ⇒ 后端**退出模式并清列表**，这次**不 apply** —— message 保留当前这版。
func (s *Server) rerollMessageDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	idx, err := strconv.Atoi(trimSpace(r.PathValue("idx")))
	if err != nil || idx <= 0 {
		writeError(w, http.StatusBadRequest, "invalid", "idx 要是正整数（第几版，1-based）")
		return
	}
	state, err := s.rerolls.Delete(r.PathValue("session_id"), reroll.TargetMessage, idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// rerollMessageClear：**退出重摇模式**（清列表）；在摇的那一趟顺手按停。
//
// 退出**不 apply、也不回原文**：库里那条 Message 保留当前这版（它就是用户最后选中的那版）。
func (s *Server) rerollMessageClear(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	s.rerolls.Clear(r.PathValue("session_id"), reroll.TargetMessage)
	w.WriteHeader(http.StatusNoContent)
}

// ── 摘要级 ────────────────────────────────────────────────────────────────

// rerollSummaryEnter：进入摘要重摇模式并立刻摇一版（**202**）—— 目标由体里的 `idx`（1-based **消息**序号）
// 上溯到它的根摘要；同一棵树里给哪一条都指向同一个目标。
//
// `idx` 是**必填**：摘要有的是"我到底要重摇哪一段"，缺了没得猜 ⇒ **400**（不是 422：
// 这条路的语义错统一按 400 回，别让客户端为同一件事记两套）。
func (s *Server) rerollSummaryEnter(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var req RerollSummaryReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Idx == nil {
		writeError(w, http.StatusBadRequest, "invalid", "要重摇哪一条：给 idx（1-based 消息序号）")
		return
	}
	accepted, err := s.rerolls.EnterSummary(*session, *req.Idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, accepted)
}

// rerollSummaryState：摘要重摇的状态（没进这一档 ⇒ `active: false`；另一个家族活着不算本档进模式）。
func (s *Server) rerollSummaryState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.rerolls.State(r.PathValue("session_id"), reroll.TargetSummary))
}

// rerollSummarySwitch：选中一版 ⇒ 就地换库里那条摘要的正文（**摘要 id / 覆盖区间一概不变**）。
func (s *Server) rerollSummarySwitch(w http.ResponseWriter, r *http.Request) {
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
	state, err := s.rerolls.Switch(r.PathValue("session_id"), reroll.TargetSummary, *req.Idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// rerollSummaryDelete：删掉一版（`idx` 是**位次不是 id**，与消息家族同一个形状）。
func (s *Server) rerollSummaryDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	idx, err := strconv.Atoi(trimSpace(r.PathValue("idx")))
	if err != nil || idx <= 0 {
		writeError(w, http.StatusBadRequest, "invalid", "idx 要是正整数（第几版，1-based）")
		return
	}
	state, err := s.rerolls.Delete(r.PathValue("session_id"), reroll.TargetSummary, idx)
	if err != nil {
		writeRerollError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// rerollSummaryClear：**退出摘要重摇模式**（清列表）；只清摘要那一档 —— 消息重摇不受影响。
func (s *Server) rerollSummaryClear(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	s.rerolls.Clear(r.PathValue("session_id"), reroll.TargetSummary)
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
