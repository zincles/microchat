package server

import (
	"net/http"
	"strconv"
	"strings"

	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/state"

	"github.com/google/uuid"
)

// effectiveSystemPrompt：生效的系统提示词 —— **解析只有一处**（`state.ResolveSystemPrompt`：
// 会话级覆盖优先，否则用 agent 的，再不然用内置默认那句；**永不返回空串**）。
// 这里只是转发（`chat.EffectiveSystemPrompt` 把现读的 agent 配置喂给它），免得生出第二份答案。
func (s *Server) effectiveSystemPrompt(session model.Session) (string, state.PromptSource) {
	return s.chat.EffectiveSystemPrompt(session)
}

// viewOf：现演一份这条会话的状态快照（分层 + 沿路径 fold）。
func (s *Server) viewOf(session model.Session) (state.View, error) {
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		return state.View{}, err
	}
	systemPrompt, source := s.effectiveSystemPrompt(session)
	return state.FromSources(session.ID, systemPrompt, source, messages), nil
}

// getSessionState：某会话的世界状态：底子（生效提示词里的块）+ 顺着正文重演出来的本会话改动 + 生效值。
//
// 查询参数 `at_idx=N`（**同一条路由加参数**，不开新路）⇒ **截至第 N 条的现演**：
//
//   - **底子永远用当前的生效提示词** ✗（不追究历史）⇒ 它是"用**今天**的底子 + 到那一条为止的正文
//     算出来的"，**不是真快照**（换提示词，同一个 N 的答案立刻跟着变）；
//   - 正文只 fold **到第 N 条（含）**；`at_idx=0` ⇒ 只有底子；
//   - `N` 超过现有条数 ⇒ 当作"到最后一条"（与 `/messages` 的区间查询一个口吻：**越界不是错**）；
//   - 不给参数 ⇒ **当前状态**（现状一字不改）；给了但不是非负整数（`-1` / `abc` / 空）⇒ **400**。
//
// 响应仍是同一个 `StateView`（`baseline` / `session` / `baseline_values` / `effective` / `tables`）。
func (s *Server) getSessionState(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	at, given, ok := atIdx(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	systemPrompt, source := s.effectiveSystemPrompt(*session)
	if !given {
		// 不给参数 = 当前状态：走**原来那条**（一条消息都不截）
		writeJSON(w, http.StatusOK, state.FromSources(session.ID, systemPrompt, source, messages))
		return
	}
	writeJSON(w, http.StatusOK, state.StateAt(session.ID, systemPrompt, source, messages, at))
}

// atIdx：读 `at_idx`：不给 ⇒ (0, false, true)；给了就要非负整数（`0` 合法 —— 只有底子）⇒ 否则 400。
//
// **`0` 与"不给"是两件事**（前者 = 只有底子，后者 = 当前状态）⇒ 判定用 `Has` 而不是"空串当没给"。
func atIdx(w http.ResponseWriter, r *http.Request) (int, bool, bool) {
	query := r.URL.Query()
	if !query.Has("at_idx") {
		return 0, false, true
	}
	value, err := strconv.Atoi(trimSpace(query.Get("at_idx")))
	if err != nil || value < 0 {
		writeError(w, http.StatusBadRequest, "invalid", "at_idx 要是非负整数（0 = 只有底子；不给 = 当前状态）")
		return 0, false, false
	}
	return value, true, true
}

// getSessionOutgoing：旧 (b) 逐项形状已删 —— 调了就 410 `gone`。
//
// 自拼的"给人看的载荷"不是真请求：`message_id`/`idx`/`type` 全是库内账，
// 上游一格都不要 —— 留着就是糊弄，该报错就报错（别画个假的）。
// 真要看"发出去什么"问 (c) `POST /outgoing`（真请求 method/url/headers/体）。
func (s *Server) getSessionOutgoing(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "gone", "GET /outgoing 已删：看真请求问 POST /outgoing（只算不写，method/url/headers/体）")
}

// OutgoingReq：`POST /sessions/{session_id}/outgoing` 的请求体 —— 待发的那一句。
type OutgoingReq struct {
	Content *string `json:"content"`
}

// postSessionOutgoing：**(c) 把这条 content 当成即将追加的那句用户消息之后**，真会发出去的那一发。
//
// **只算不写**：不落库、不改任何状态（消息条数一个不变）。
// 回的是**真请求**（method/url/headers/体 —— 与 (a) 同一支笔 `Snapshot`），不是"给人看的载荷"：
// 调试工具必须说真话 —— `chat.PreviewWire` 与真发（`Accept → outgoingFor → run → Build`）
// 共用每一段（装配 → wireMessages → 选后端查表 → `providers.Build`），两份对不上就地就炸。
//
// 空 `content`（缺 / 空白）⇒ **现有历史会发出去的那一发**（与空发 `resend` 同一段装配
// `PreviewHistoryWire`）；空会话 ⇒ 与 resend 同 400（没历史可重发）。
func (s *Server) postSessionOutgoing(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var req OutgoingReq
	if !decodeJSON(w, r, &req) {
		return
	}
	var payload providers.LastPayload
	var err error
	if req.Content == nil || trimSpace(*req.Content) == "" {
		payload, err = s.chat.PreviewHistoryWire(*session)
	} else {
		payload, err = s.chat.PreviewWire(*session, *req.Content)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// DraftOutgoingReq：`POST /outgoing` 的请求体 —— **还没有会话**（web 的草稿态）时的预演。
//
// `id` = 客户端为这张草稿铸的 UUIDv7（`POST /sessions` 会拿同一个 id 落库 ⇒ 预演那一发的
// 请求头与真发逐字节一致）；省略 ⇒ 现铸一个（curl 手玩也能用，只是头上那个 id 将无处落）。
type DraftOutgoingReq struct {
	ID       *string `json:"id"`
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
	AgentID  *string `json:"agent_id"`
	Content  *string `json:"content"`
}

// postDraftOutgoing：草稿的第一句"真会发出去的那一发" —— 与 (c) 同一支笔（`chat.PreviewWire`），
// 区别只有一个：会话**还没进库**（临时种子会话走 assemble，`ListMessages` 空 ⇒ 无历史）。
//
// **只算不写**：一条会话都不建（草稿的"不落库"由这里守着）。
// content 缺 / 空白 ⇒ 400（草稿没有历史可看，空 content 不是"续写"是调用方写错了）。
func (s *Server) postDraftOutgoing(w http.ResponseWriter, r *http.Request) {
	var req DraftOutgoingReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == nil || trimSpace(*req.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid", "草稿预演要一句 content（还没有历史可看）")
		return
	}
	id := ""
	if req.ID != nil && strings.TrimSpace(*req.ID) != "" {
		normalized, ok := normalizeUUIDv7(strings.TrimSpace(*req.ID))
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid", "id 要是 UUIDv7（草稿铸的那个）")
			return
		}
		id = normalized
	} else {
		minted, err := uuid.NewV7()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		id = minted.String()
	}
	seed, err := s.newSessionSeed(id, req.Provider, req.Model, req.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	payload, err := s.chat.PreviewWire(seed, *req.Content)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// requireSession：取会话；不存在 ⇒ 404（顺带把"会话不见了"收在一处）。
func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (*model.Session, bool) {
	session, err := s.store.GetSession(r.PathValue("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	if session == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return nil, false
	}
	return session, true
}
