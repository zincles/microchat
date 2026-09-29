// Package server：HTTP 层。**接口形状照搬 AGENTS.md 那张表**（43 条），一条条搬。
//
// 全部挂在 /api/v1 下；错误体固定 {"error":{"code","message"}}；配了口令就全都要 Bearer。
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
)

type Server struct {
	store  *store.Store
	config config.Config
	paths  config.Paths
	mux    *http.ServeMux
}

func New(st *store.Store, cfg config.Config, paths config.Paths) *Server {
	s := &Server{store: st, config: cfg, paths: paths, mux: http.NewServeMux()}
	// Go 1.22+ 的 ServeMux 原生支持 `GET /x/{id}` 这种模式 —— 连路由库都不需要。
	s.mux.HandleFunc("GET /api/v1/health", s.health)
	s.mux.HandleFunc("GET /api/v1/debug/last-payload", s.lastPayload)
	s.mux.HandleFunc("GET /api/v1/sessions", s.listSessions)
	s.mux.HandleFunc("POST /api/v1/sessions", s.createSession)
	s.mux.HandleFunc("PATCH /api/v1/sessions/{session_id}", s.updateSession)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}", s.deleteSession)
	// Copy：线性会话里的"分岔"（新 session + 消息与摘要一并复制）
	s.mux.HandleFunc("POST /api/v1/sessions/{session_id}/copy", s.copySession)
	// provider / 模型 / 渠道（registry 那一层）
	s.mux.HandleFunc("GET /api/v1/providers", s.listProviders)
	// 内建预设清单（字面段优先于 {id}，Go 的 ServeMux 自己保证）
	s.mux.HandleFunc("GET /api/v1/providers/presets", s.listProviderPresets)
	s.mux.HandleFunc("POST /api/v1/providers", s.createProvider)
	s.mux.HandleFunc("PATCH /api/v1/providers/{provider_id}", s.updateProvider)
	s.mux.HandleFunc("DELETE /api/v1/providers/{provider_id}", s.deleteProvider)
	s.mux.HandleFunc("POST /api/v1/providers/{provider_id}/refresh", s.refreshProvider)
	s.mux.HandleFunc("GET /api/v1/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/v1/agents", s.createAgent)
	s.mux.HandleFunc("PATCH /api/v1/agents/{agent_id}", s.updateAgent)
	s.mux.HandleFunc("DELETE /api/v1/agents/{agent_id}", s.deleteAgent)
	s.mux.HandleFunc("GET /api/v1/config/chat", s.getChatConfig)
	s.mux.HandleFunc("PUT /api/v1/config/chat", s.putChatConfig)
	s.mux.HandleFunc("GET /api/v1/models", s.listModels)
	s.mux.HandleFunc("PATCH /api/v1/models", s.setModelOverride)

	// statelang：解析 + 计算（给外部工具用；不涉及会话、不落库）
	s.mux.HandleFunc("POST /api/v1/statelang", s.statelangParse)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/state", s.getSessionState)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/outgoing", s.getSessionOutgoing)
	s.mux.HandleFunc("PATCH /api/v1/sessions/{session_id}/messages/{message_id}", s.editMessage)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{session_id}/messages/{message_id}", s.deleteMessage)
	// 删除预览：**只算不动**（安全 ⇒ GET）；DELETE 照同一份计算干
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/messages/{message_id}/deletion-preview", s.deletionPreview)
	s.mux.HandleFunc("GET /api/v1/sessions/{session_id}/messages", s.listMessages)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// health：探针（Rust 版回 {"status","version"}）。
// health：版本号与 Rust 版**刻意对齐** —— 这个端口要取代它，形状必须一模一样。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "0.1.0"})
}

// listSessions：列表项 = 会话 + 这一轮的状态（Rust 版从 turn 登记表里取）。
// TurnRegistry 还没搬 ⇒ 一律 idle —— 与 Rust 版"没登记就是 idle"的口径一致。
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	views := make([]model.SessionView, 0, len(sessions))
	for _, session := range sessions {
		views = append(views, model.SessionView{Session: session, Turn: model.TurnStatus{Phase: model.PhaseIdle}})
	}
	writeJSON(w, http.StatusOK, views)
}

// listMessages：这条会话的**全部消息**，按 id 升序（线性会话 ⇒ 没有当前路径，就是它自己）。
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := s.store.ListMessages(r.PathValue("session_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// CreateSessionReq：Rust 版的 `#[serde(default)]` ⇒ 全都可以省略。
type CreateSessionReq struct {
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	AgentID      *string `json:"agent_id"`
	SystemPrompt string  `json:"system_prompt"`
}

// UpdateSessionReq：只改给出来的那些。
//
// `system_prompt` 在 Rust 版里**收了但没用**（会话级提示词只有新建时能写）—— 照抄这个行为，
// 免得两边表现不一致；补写入路径是另一个决定，不混在移植里。
type UpdateSessionReq struct {
	Title        *string `json:"title"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	AgentID      *string `json:"agent_id"`
	SystemPrompt string  `json:"system_prompt"`
}

// createSession：省略字段时取 config.json 的 defaults；
// agent 缺省 → `agents.json` 的 default_agent（**不然新会话永远带着内置 default**，
// agent 的提示词与世界状态底子对任何新会话都不生效 —— 这条踩过）。
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionReq
	if !decodeJSON(w, r, &req) {
		return
	}
	provider := s.config.Defaults.Provider
	if req.Provider != nil {
		provider = *req.Provider
	}
	modelID := s.config.Defaults.Model
	if req.Model != nil {
		modelID = *req.Model
	}
	session, err := s.store.CreateSession(provider, modelID, req.SystemPrompt)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	agentID := ""
	if req.AgentID != nil {
		agentID = strings.TrimSpace(*req.AgentID)
	}
	if agentID == "" {
		agentID = s.defaultAgentID()
	}
	if agentID != session.AgentID {
		if err := s.store.SetSessionAgent(session.ID, agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		session.AgentID = agentID
	}
	writeJSON(w, http.StatusCreated, session)
}

// loadAgents：读 `agents.json`（读不到就算空的 —— 文件可以缺失）。
func (s *Server) loadAgents() config.AgentsConfig {
	var agents config.AgentsConfig
	if body, err := os.ReadFile(s.paths.Config("agents.json")); err == nil {
		_ = json.Unmarshal(body, &agents)
	}
	return agents
}

// defaultAgentID：`agents.json` 的 default_agent（空串/缺失都算没配 ⇒ 内置默认）。
func (s *Server) defaultAgentID() string {
	if id := strings.TrimSpace(s.loadAgents().DefaultAgent); id != "" {
		return id
	}
	return model.DefaultAgentID()
}

func (s *Server) updateSession(w http.ResponseWriter, r *http.Request) {
	var req UpdateSessionReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("session_id")
	if req.Title != nil {
		if err := s.store.UpdateTitle(id, *req.Title); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.Provider != nil || req.Model != nil {
		session, err := s.store.GetSession(id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if session == nil {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		provider, modelID := session.Provider, session.Model
		if req.Provider != nil {
			provider = *req.Provider
		}
		if req.Model != nil {
			modelID = *req.Model
		}
		if err := s.store.SetSessionModel(id, provider, modelID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.AgentID != nil {
		if err := s.store.SetSessionAgent(id, *req.AgentID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	session, err := s.store.GetSession(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if session == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSession(r.PathValue("session_id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON：请求体**语法**不合法 ⇒ 400（对齐 axum 的 400）。
//
// 注意不含"缺字段"：那是**语义**错，axum 给 422 ⇒ 各处理器自己判（见 missingField）。
func decodeJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "请求体不是合法的 JSON")
		return false
	}
	return true
}

// missingField：必填字段缺失 ⇒ **422** —— 与 axum 的 JsonRejection 状态码一致。
// 错误体仍用本项目契约里的 JSON（axum 那串纯文本不合自家契约，这里刻意不照抄）。
func missingField(w http.ResponseWriter, field string) {
	writeError(w, http.StatusUnprocessableEntity, "invalid", "缺 "+field)
}

// writeStoreError：把 store 的两类错误映射成固定的错误体（客户端按 code 分支）。
func writeStoreError(w http.ResponseWriter, err error) {
	var invalid store.InvalidError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid", invalid.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// copySession：**Copy 一条会话** —— 线性会话里"分岔"就是它（不叫 fork）。
//
// 新 session（新 id）+ 消息与摘要一并复制（摘要新 id、消息的 `summary_id` 重映射）；
// 标题 / 渠道 / 模型 / agent / 提示词带着；**世界状态不复制**（它本来就现演）。
func (s *Server) copySession(w http.ResponseWriter, r *http.Request) {
	copied, err := s.store.CopySession(r.PathValue("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, copied)
}

// writeMessageError：消息级路由的 404 文案 —— "会话不存在"会误导（多半是那条消息没了）。
func writeMessageError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "这条会话里没有这条消息")
		return
	}
	writeStoreError(w, err)
}

// EditMessageReq：`content` 是**必填**（缺了 ⇒ 400，不是"改成空串"）——与 Rust 版一致。
type EditMessageReq struct {
	Content *string `json:"content"`
}

// editMessage：改正文 = 重写存档（世界状态随之现演，没有任何派生表要同步）。
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	var req EditMessageReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == nil {
		missingField(w, "content")
		return
	}
	message, err := s.store.UpdateMessage(r.PathValue("session_id"), r.PathValue("message_id"), *req.Content)
	if err != nil {
		writeMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

// DeleteMessageReq：删消息的请求体 —— 只带回**预览里的最后一条**。
//
// 预览之后、执行之前有人往尾巴上追加 ⇒ 照旧计划删会**多删**且静默 ⇒ 核对不符就 409（重新预览）。
type DeleteMessageReq struct {
	LastDeletedMessageID *string `json:"last_deleted_message_id"`
}

// deletionPreview：删除预览 —— **只算不动**（安全 ⇒ GET）。
//
// 与 DELETE 走**同一段计算**（`store.DeletionPlan`）⇒ 预览不会与真删漂移。
func (s *Server) deletionPreview(w http.ResponseWriter, r *http.Request) {
	plan, err := s.store.DeletionPlan(r.PathValue("session_id"), r.PathValue("message_id"))
	if err != nil {
		writeMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// deleteMessage：删**这条及之后的全部消息**，级联见 DEFINE.md 的"删除的级联规则"：
// 【目标及其之后的全部消息】+【覆盖区间与之相交的全部摘要】+【这些摘要的全部祖先】，
// 再把幸存者里指向死摘要的指针置空。
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	var req DeleteMessageReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.LastDeletedMessageID == nil {
		missingField(w, "last_deleted_message_id")
		return
	}
	sessionID, messageID := r.PathValue("session_id"), r.PathValue("message_id")
	plan, err := s.store.DeletionPlan(sessionID, messageID)
	if err != nil {
		writeMessageError(w, err)
		return
	}
	if *req.LastDeletedMessageID != plan.LastDeletedMessageID {
		writeError(w, http.StatusConflict, "conflict",
			"要删的这一段变了（会话末尾已经不是预览时的那条）：请重新取一次删除预览")
		return
	}
	if err := s.store.ApplyDeletion(plan); err != nil {
		writeStoreError(w, err)
		return
	}
	// 回的就是刚执行的那份计划（与预览同一个形状：真删了什么，一眼对得上）
	writeJSON(w, http.StatusOK, plan)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// 默认会把 < > & 转义成 \u003c（Go 的老毛病）。消息正文里全是 LaTeX（\(i<j\)），
	// Rust 版是原样输出的 ⇒ **必须关掉**，否则两边字节不一致、用户也会看到 \u003c。
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// Encode 会补一个 \n，axum 不补 —— 直接对齐字节（对账闸才有意义）
	payload := bytes.TrimRight(buffer.Bytes(), "\n")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

// trimSpace：收在一处（免得 import 到处飞）。
func trimSpace(text string) string { return strings.TrimSpace(text) }
