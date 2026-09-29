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
	s.mux.HandleFunc("GET /api/v1/debug/state", s.debugState)
	s.mux.HandleFunc("GET /api/v1/conversations", s.listConversations)
	s.mux.HandleFunc("POST /api/v1/conversations", s.createConversation)
	s.mux.HandleFunc("PATCH /api/v1/conversations/{id}", s.updateConversation)
	s.mux.HandleFunc("DELETE /api/v1/conversations/{id}", s.deleteConversation)
	s.mux.HandleFunc("GET /api/v1/conversations/{id}/branches", s.listBranches)
	// provider / 模型 / 渠道（registry 那一层）
	s.mux.HandleFunc("GET /api/v1/providers", s.listProviders)
	// 内建预设清单（字面段优先于 {id}，Go 的 ServeMux 自己保证）
	s.mux.HandleFunc("GET /api/v1/providers/presets", s.listProviderPresets)
	s.mux.HandleFunc("POST /api/v1/providers", s.createProvider)
	s.mux.HandleFunc("PATCH /api/v1/providers/{id}", s.updateProvider)
	s.mux.HandleFunc("DELETE /api/v1/providers/{id}", s.deleteProvider)
	s.mux.HandleFunc("POST /api/v1/providers/{id}/refresh", s.refreshProvider)
	s.mux.HandleFunc("DELETE /api/v1/providers/{id}/models", s.deleteProviderModels)
	s.mux.HandleFunc("POST /api/v1/models/probe", s.probeProvider)
	s.mux.HandleFunc("GET /api/v1/models", s.listModels)
	s.mux.HandleFunc("PATCH /api/v1/models", s.setModelOverride)

	// statelang：解析 + 计算（给外部工具用；不涉及会话、不落库）
	s.mux.HandleFunc("POST /api/v1/statelang", s.statelangParse)
	s.mux.HandleFunc("GET /api/v1/conversations/{id}/state", s.getConversationState)
	s.mux.HandleFunc("GET /api/v1/conversations/{id}/outgoing", s.getConversationOutgoing)
	s.mux.HandleFunc("PATCH /api/v1/conversations/{id}/messages/{messageId}", s.editMessage)
	s.mux.HandleFunc("DELETE /api/v1/conversations/{id}/messages/{messageId}", s.deleteMessage)
	s.mux.HandleFunc("DELETE /api/v1/conversations/{id}/messages/{messageId}/siblings", s.deleteSiblings)
	s.mux.HandleFunc("GET /api/v1/conversations/{id}/messages", s.listMessages)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// health：探针（Rust 版回 {"status","version"}）。
// health：版本号与 Rust 版**刻意对齐** —— 这个端口要取代它，形状必须一模一样。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "0.1.0"})
}

// debugState：后端自述 —— 老老实实的一眼账，先只有计数。
func (s *Server) debugState(w http.ResponseWriter, r *http.Request) {
	conversations, messages, summaries, err := s.store.Counts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"store": map[string]any{
			"conversations": conversations,
			"messages":      messages,
			"summaries":     summaries,
		},
		"note": "Go 版的骨架：表结构已照搬，接口一条条搬",
	})
}

// listConversations：列表项 = 会话 + 这一轮的状态（Rust 版从 turn 登记表里取）。
// TurnRegistry 还没搬 ⇒ 一律 idle —— 与 Rust 版"没登记就是 idle"的口径一致。
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	conversations, err := s.store.ListConversations()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	views := make([]model.ConversationView, 0, len(conversations))
	for _, conversation := range conversations {
		views = append(views, model.ConversationView{Conversation: conversation, Turn: model.TurnStatus{Phase: model.PhaseIdle}})
	}
	writeJSON(w, http.StatusOK, views)
}

// listMessages：**按树上的当前路径**（不是 rowid，也不是全部）。
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := s.store.ListMessages(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// CreateConversationReq：Rust 版的 `#[serde(default)]` ⇒ 全都可以省略。
type CreateConversationReq struct {
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	AgentID      *string `json:"agent_id"`
	SystemPrompt string  `json:"system_prompt"`
}

// UpdateConversationReq：只改给出来的那些。`CurrentLeaf` = 界面上的「‹ 2/3 ›」（切分支）。
//
// `system_prompt` 在 Rust 版里**收了但没用**（会话级提示词只有新建时能写）—— 照抄这个行为，
// 免得两边表现不一致；补写入路径是另一个决定，不混在移植里。
type UpdateConversationReq struct {
	Title        *string `json:"title"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	AgentID      *string `json:"agent_id"`
	CurrentLeaf  *string `json:"current_leaf"`
	SystemPrompt string  `json:"system_prompt"`
}

// createConversation：省略字段时取 config.json 的 defaults；
// agent 缺省 → `agents.json` 的 default_agent（**不然新会话永远带着内置 default**，
// agent 的提示词与世界状态底子对任何新会话都不生效 —— 这条踩过）。
func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	var req CreateConversationReq
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
	conversation, err := s.store.CreateConversation(provider, modelID, req.SystemPrompt)
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
	if agentID != conversation.AgentID {
		if err := s.store.SetConversationAgent(conversation.ID, agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		conversation.AgentID = agentID
	}
	writeJSON(w, http.StatusCreated, conversation)
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

func (s *Server) updateConversation(w http.ResponseWriter, r *http.Request) {
	var req UpdateConversationReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if req.Title != nil {
		if err := s.store.UpdateTitle(id, *req.Title); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.Provider != nil || req.Model != nil {
		conversation, err := s.store.GetConversation(id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if conversation == nil {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		provider, modelID := conversation.Provider, conversation.Model
		if req.Provider != nil {
			provider = *req.Provider
		}
		if req.Model != nil {
			modelID = *req.Model
		}
		if err := s.store.SetConversationModel(id, provider, modelID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.AgentID != nil {
		if err := s.store.SetConversationAgent(id, *req.AgentID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.CurrentLeaf != nil {
		if err := s.store.SwitchLeafToSibling(id, *req.CurrentLeaf); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	conversation, err := s.store.GetConversation(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if conversation == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return
	}
	writeJSON(w, http.StatusOK, conversation)
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteConversation(r.PathValue("id")); err != nil {
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

// listBranches：每条在同龄兄弟里第几/共几（界面上的「‹ 2/3 ›」）。
func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	info, err := s.store.BranchInfo(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
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
	message, err := s.store.UpdateMessage(r.PathValue("id"), r.PathValue("messageId"), *req.Content)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

// deleteMessage：删**整棵子树**（返回删了几条）。
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.store.DeleteMessage(r.PathValue("id"), r.PathValue("messageId"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// deleteSiblings：「删除全部」——清掉一组兄弟（连同各自子树），只留上文。
func (s *Server) deleteSiblings(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.store.DeleteSiblings(r.PathValue("id"), r.PathValue("messageId"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
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
