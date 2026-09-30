package server

import (
	"errors"
	"net/http"

	"microchat/internal/compact"
	"microchat/internal/providers"
	"microchat/internal/store"
)

// ── 压缩：`POST /sessions/{session_id}/compact` ─────────────────────────────
//
// **受理与生成分开**（与一轮生成同一条脾气）：校验与挂号同步做完（错就当场回），
// 真调上游 + 落库在后台 ⇒ 成功回 **202** + 这一份状态；同一个会话同时只允许一次（在跑 ⇒ 409）。
// 跑完的结局落在 `GET /sessions/{session_id}/status` 的 `compact` 那一档里（同一份状态对象）。

// CompactReq：一次压缩要压哪儿 —— **两种给法二选一**（`blocks` 与 `begin/end` 不许同时给）。
//
// 都不给 ⇒ 用 `config.json` 的 `chat.compact_blocks` 默认值。
type CompactReq struct {
	Blocks         *int    `json:"blocks"`
	BeginMessageID *string `json:"begin_message_id"`
	EndMessageID   *string `json:"end_message_id"`
}

// compactSession：受理一次压缩（202 + `CompactStatus`）。
func (s *Server) compactSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var req CompactReq
	if !decodeJSON(w, r, &req) {
		return
	}
	// 两种给法同时出现 ⇒ 说不清要压哪儿（**不猜**：猜错了压的是另一段，还不好发现）
	if req.Blocks != nil && (req.BeginMessageID != nil || req.EndMessageID != nil) {
		writeError(w, http.StatusBadRequest, "invalid", "blocks 与 begin_message_id/end_message_id 只能给一种")
		return
	}
	request := compact.Request{}
	switch {
	case req.BeginMessageID != nil || req.EndMessageID != nil:
		if req.BeginMessageID == nil || req.EndMessageID == nil {
			writeError(w, http.StatusBadRequest, "invalid", "按区间压要两端都给：begin_message_id + end_message_id")
			return
		}
		request.Begin, request.End = *req.BeginMessageID, *req.EndMessageID
	case req.Blocks != nil:
		request.Blocks = *req.Blocks
	}
	status, err := s.compact.Start(*session, request)
	if err != nil {
		writeCompactError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

// writeCompactError：压缩那一路的失败 → 固定的错误体（客户端按 `code` 分支）。
func writeCompactError(w http.ResponseWriter, err error) {
	var compactError *compact.Error
	var invalid store.InvalidError
	var upstream *providers.UpstreamError
	switch {
	case errors.As(err, &compactError):
		writeError(w, statusForCode(compactError.Code), compactError.Code, compactError.Message)
	case errors.As(err, &upstream):
		writeError(w, http.StatusBadGateway, "upstream", upstream.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid", invalid.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// statusForCode：错误体的 code → HTTP 状态码（与别处的映射同一个口径）。
func statusForCode(code string) int {
	switch code {
	case "invalid":
		return http.StatusBadRequest
	case "not_found":
		return http.StatusNotFound
	case "conflict":
		return http.StatusConflict
	case "upstream":
		return http.StatusBadGateway
	case "unauthorized":
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}
