package server

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

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
}

type UpdateAgentReq struct {
	Name         *string `json:"name"`
	SystemPrompt *string `json:"system_prompt"`
	// 把它设为 `agents.json` 的 `default_agent`（新建会话默认用它）。
	MakeDefault bool `json:"make_default"`
	// 改 id = **重命名**：连同 `default_agent` 与所有会话的引用一起搬（空串 = 不改）。
	NewID *string `json:"new_id"`
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
	}
	agents.Agents = append(agents.Agents, agent)
	if err := s.saveAgents(agents); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	// （而 `Resolve()` 找不到只是静默回空提示词，不报错 —— 那是最难查的一类问题）。
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
	id := r.PathValue("id")
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
	if err := config.SaveJSON(s.paths.Config("config.json"), cfg); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Chat)
}
