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
	writeJSON(w, http.StatusOK, cfg.ModelRulesView())
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
	if err := config.ValidateUserModelDefineRules(req.Rules); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// nil = 没给这一格（老客户端）⇒ 保留现有停用列表；显式 [] = 全部启用。
	disabled := cfg.DisabledBuiltinModelRules
	if req.DisabledBuiltinModelRules != nil {
		disabled = req.DisabledBuiltinModelRules
	}
	if err := config.ValidateDisabledBuiltinModelRules(disabled); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	cfg.ModelDefineRules = req.Rules
	cfg.DisabledBuiltinModelRules = disabled
	if err := config.SaveJSON(s.paths.Config("config.json"), cfg); err != nil {
		writeStoreError(w, err)
		return
	}
	s.config.ModelDefineRules = req.Rules
	s.config.DisabledBuiltinModelRules = disabled
	writeJSON(w, http.StatusOK, cfg.ModelRulesView())
}
