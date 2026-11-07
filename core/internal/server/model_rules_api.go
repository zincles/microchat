package server

import (
	"net/http"

	"microchat/internal/config"
)

// getModelRules：`config.json` 的 model_define_rules 集合（纯数据，不含任何模型行）。
func (s *Server) getModelRules(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	rules := cfg.ModelDefineRules
	if rules == nil {
		rules = []config.ModelDefineRule{}
	}
	writeJSON(w, http.StatusOK, config.ModelRulesConfig{Rules: rules})
}

// putModelRules：**整份替换**规则集合（空数组 = 清空）。
//
// 先校验再落盘：坏规则一个字都不写。写入成功后同步 server 里的快照，
// 让下一次 `GET /models` 立刻按新规则派生，而不是等重启。
func (s *Server) putModelRules(w http.ResponseWriter, r *http.Request) {
	var req config.ModelRulesConfig
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Rules == nil {
		req.Rules = []config.ModelDefineRule{}
	}
	if err := config.ValidateModelDefineRules(req.Rules); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	cfg.ModelDefineRules = req.Rules
	if err := config.SaveJSON(s.paths.Config("config.json"), cfg); err != nil {
		writeStoreError(w, err)
		return
	}
	s.config.ModelDefineRules = req.Rules
	writeJSON(w, http.StatusOK, config.ModelRulesConfig{Rules: req.Rules})
}
