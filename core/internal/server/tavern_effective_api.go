package server

import (
	"net/http"
	"strings"

	"microchat/internal/config"
	"microchat/internal/macro"
	"microchat/internal/tavern"
)

// effectiveTavernPreset：只读提取“有效部分”——按 prompt_order 求值 setvar/getvar/trim/注释后，
// 只留下真的有正文的条目。不写 Agent、不落库。
//
// 可选 query：
//   - last_user_message：给了才替换 {{lastUserMessage}}；不给则保留宏（提取预览）。
//   - agent_id：只为取角色名（char_name）；缺省“助手”。
func (s *Server) effectiveTavernPreset(w http.ResponseWriter, r *http.Request) {
	preset, data, err := s.store.GetTavernPreset(r.PathValue("preset_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if preset == nil {
		writeError(w, http.StatusNotFound, "not_found", "没有这个酒馆预设")
		return
	}
	prefs, err := config.LoadPrefs(s.paths)
	if err != nil {
		prefs = config.DefaultPrefs()
	}
	agentName := ""
	if id := strings.TrimSpace(r.URL.Query().Get("agent_id")); id != "" {
		if agent, ok := s.loadAgents().Get(id); ok {
			agentName = agent.Name
		}
	}
	values := macro.Values(prefs.UserName, agentName, "")
	var lastUser *string
	if raw, ok := r.URL.Query()["last_user_message"]; ok {
		value := ""
		if len(raw) > 0 {
			value = raw[0]
		}
		lastUser = &value
	}
	result, err := tavern.Extract(data, tavern.ExtractOptions{Values: values, LastUserMessage: lastUser})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
