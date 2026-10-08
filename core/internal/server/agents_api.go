package server

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"microchat/internal/abilities"
	"microchat/internal/config"
)

// ── agents.json：生效列表 + 增删改；config.json 的 chat 段 ──
//
// id 由**后端生成**（UUIDv7，与会话/消息同一套）：不透明、不可变、不会撞车。
// 手写 id 的老路仍然通 —— 直接写进 `agents.json`，加载器照收（历史数据不受影响）。

type CreateAgentReq struct {
	// 名称是**唯一的人类句柄** —— id 由后端生成，不接受指定。
	Name         string `json:"name"`
	SystemPrompt string `json:"system_prompt"`
	// Abilities：能力开关（键 = 能力 id）。**缺省 = 默认全开**；未知的 id ⇒ 400。
	Abilities map[string]config.AbilityToggle `json:"abilities,omitempty"`
	PrependState *bool  `json:"prepend_state,omitempty"`
	DisplayMode  *string `json:"display_mode,omitempty"`
}

// DisplayModeValue：归一化（只认 roleplay，其余 ⇒ "" 缺省 chat）。
func (r CreateAgentReq) DisplayModeValue() string {
	if r.DisplayMode != nil && *r.DisplayMode == "roleplay" {
		return "roleplay"
	}
	return ""
}

type UpdateAgentReq struct {
	Name         *string `json:"name"`
	SystemPrompt *string `json:"system_prompt"`
	// Abilities：给了就**整段替换**这张映射（与 `system_prompt` 一个语义；nil = 不动）。
	// 未知的能力 id ⇒ 400（写了不生效是最难查的一类问题，不许静默忽略）。
	Abilities map[string]config.AbilityToggle `json:"abilities,omitempty"`
	// 把它设为 `agents.json` 的 `default_agent`（新建会话默认用它）。
	MakeDefault bool `json:"make_default"`
	// 改 id = **重命名**：连同 `default_agent` 与所有会话的引用一起搬（空串 = 不改）。
	NewID *string `json:"new_id"`
	PrependState *bool   `json:"prepend_state,omitempty"`
	DisplayMode  *string `json:"display_mode,omitempty"`
}

// （读 agents.json 的 loadAgents 在 server.go 里 —— 早就有，别写第二份）

func (s *Server) saveAgents(agents config.AgentsConfig) error {
	return config.SaveJSON(s.paths.Config("agents.json"), agents)
}

// listAgents：对外给"生效列表"——含内置默认 agent（文件里没有 `default` 时补上）。
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.loadAgents()
	writeJSON(w, http.StatusOK, config.AgentsConfig{
		Version:      agents.Version,
		DefaultAgent: agents.DefaultAgent,
		Agents:       agents.Effective(),
	})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req CreateAgentReq
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		// id 不可见之后，名称就是它唯一的句柄：没名字的 agent 在界面上没法认。
		writeError(w, http.StatusBadRequest, "invalid", "agent 名称不能为空")
		return
	}
	agents := s.loadAgents()
	agent := config.Agent{
		ID:           uuid.Must(uuid.NewV7()).String(),
		Name:         name,
		SystemPrompt: req.SystemPrompt,
		Abilities:    req.Abilities,
		PrependState: req.PrependState,
		DisplayMode:  req.DisplayModeValue(),
	}
	// 未知的能力 id 写进去只会"配了不生效" ⇒ 当场拒（口径见 `internal/abilities`）
	if err := abilities.Validate(config.AgentsConfig{Agents: []config.Agent{agent}}); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	agents.Agents = append(agents.Agents, agent)
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	var req UpdateAgentReq
	if !decodeJSON(w, r, &req) {
		return
	}
	agents := s.loadAgents()

	// 定位：文件里没有这个 id、但**能 resolve 出来**（内置默认 agent）⇒ 先把它落地到文件再改。
	if _, ok := agents.Get(id); !ok {
		if builtin, resolvable := agents.Resolve(id); resolvable {
			agents.Agents = append([]config.Agent{builtin}, agents.Agents...)
		}
	}
	if _, ok := agents.Get(id); !ok {
		writeError(w, http.StatusNotFound, "not_found", "agent 不存在")
		return
	}

	// 改 id = **重命名**：库里的会话引用**先**搬，再动文件。
	//
	// 顺序是刻意的 —— 库改了、文件写失败，还能再跑一次（那时旧 id 仍在文件里）；
	// 反过来先把旧 id 从文件里抹掉，就再也找不到它，会话会永久指向一个不存在的 agent
	// （而 `Resolve()` 找不到不会报错 —— 会话的生效提示词只是回落成内置默认那句，
	//   人设悄悄换掉，仍是最难查的一类问题）。
	current := id
	if req.NewID != nil {
		newID := strings.TrimSpace(*req.NewID)
		if newID != "" && newID != current {
			if _, taken := agents.Get(newID); taken {
				writeError(w, http.StatusConflict, "conflict", "已有同名 agent")
				return
			}
			if _, err := s.store.RenameAgentReferences(current, newID); err != nil {
				writeStoreError(w, err)
				return
			}
			for index := range agents.Agents {
				if agents.Agents[index].ID == current {
					agents.Agents[index].ID = newID
				}
			}
			if agents.DefaultAgent == current {
				agents.DefaultAgent = newID
			}
			current = newID
		}
	}

	index := -1
	for i := range agents.Agents {
		if agents.Agents[i].ID == current {
			index = i
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, "not_found", "agent 不存在")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "invalid", "agent 名称不能为空")
			return
		}
		agents.Agents[index].Name = name
	}
	if req.SystemPrompt != nil {
		agents.Agents[index].SystemPrompt = *req.SystemPrompt
	}
	if req.Abilities != nil {
		agents.Agents[index].Abilities = req.Abilities
	}
	if req.PrependState != nil {
		agents.Agents[index].PrependState = req.PrependState
	}
	if req.DisplayMode != nil {
		agents.Agents[index].DisplayMode = *req.DisplayMode
	}
	// 未知的能力 id 一律在场拒（`POST` 与 `PATCH` 同一个口径）
	if err := abilities.Validate(config.AgentsConfig{Agents: []config.Agent{agents.Agents[index]}}); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if req.MakeDefault {
		agents.DefaultAgent = current
	}
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents.Agents[index])
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	agents := s.loadAgents()
	kept := make([]config.Agent, 0, len(agents.Agents))
	for _, agent := range agents.Agents {
		if agent.ID == id {
			continue
		}
		kept = append(kept, agent)
	}
	if len(kept) == len(agents.Agents) {
		// 内置默认 agent 不在文件里，删了也会立刻回来 —— 明确拒绝，别让用户以为删掉了
		if _, resolvable := agents.Resolve(id); resolvable {
			writeError(w, http.StatusBadRequest, "invalid", "内置默认 agent 不能删除")
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "agent 不存在")
		return
	}
	if agents.DefaultAgent == id {
		if len(kept) > 0 {
			agents.DefaultAgent = kept[0].ID
		} else {
			agents.DefaultAgent = ""
		}
	}
	agents.Agents = kept
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getChatConfig：`config.json` 的 chat 段（不含密钥 —— 密钥在 providers.json 里）。
func (s *Server) getChatConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(s.paths) // 每次现读：文件缺失 ⇒ 默认值
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg.Chat)
}

// putChatConfig：**整段替换** chat（没给的字段取缺省）；server / defaults 原样保留。
func (s *Server) putChatConfig(w http.ResponseWriter, r *http.Request) {
	var chat config.ChatConfig
	if !decodeJSON(w, r, &chat) {
		return
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	cfg.Chat = chat
	// 与 `LoadConfig` 同一个兜底：0 会被读成默认值 ⇒ 落盘时就写默认值（免得文件与接口两套数）
	if cfg.Chat.CompactBlocks == 0 {
		cfg.Chat.CompactBlocks = config.DefaultCompactBlocks
	}
	if err := config.SaveJSON(s.paths.Config("config.json"), cfg); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Chat)
}

// getDefaults：新建会话的缺省三件（provider / model / agent）—— 只读。
func (s *Server) getDefaults(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(s.paths) // 每次现读：文件缺失 ⇒ 默认值
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg.Defaults)
}

// putDefaults：改缺省三件 —— 三格各改各的（nil = 不动；"" = 清回内置缺省）。
// 整段替换另有 `PUT /config/chat` 管 chat 段 —— 两段各改各的，别混。
func (s *Server) putDefaults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider *string `json:"provider"`
		Model    *string `json:"model"`
		Agent    *string `json:"agent"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if req.Provider != nil {
		cfg.Defaults.Provider = *req.Provider
	}
	if req.Model != nil {
		cfg.Defaults.Model = *req.Model
	}
	if req.Agent != nil {
		cfg.Defaults.Agent = *req.Agent
	}
	// 与 `LoadConfig` 同一个兜底：空 ⇒ 落盘时就写默认值（免得文件与接口两套数）
	if cfg.Defaults.Provider == "" {
		cfg.Defaults.Provider = "dummy"
	}
	if cfg.Defaults.Model == "" {
		cfg.Defaults.Model = config.DummyModelID
	}
	if cfg.Defaults.Agent == "" {
		cfg.Defaults.Agent = config.DefaultAgentID
	}
	if err := config.SaveJSON(s.paths.Config("config.json"), cfg); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Defaults)
}
