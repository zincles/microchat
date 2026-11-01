package server

import (
	"net/http"

	"microchat/internal/config"
)

// telegramView：`GET /config/telegram` 的形状 —— token 只回"有没有"，内容永不回显。
type telegramView struct {
	HasToken  bool  `json:"has_token"`
	AllowedID int64 `json:"allowed_id"`
	Enabled   bool  `json:"enabled"`
	Running   bool  `json:"running"`
}

// telegramStatus：bot 的活状态 —— 有 runner 登记才知道跑没跑（见 `SetTelegramRunner`）；
// 没有登记（单测 / -debug）⇒ running=false，配置照读。
func (s *Server) telegramStatus() telegramView {
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		return telegramView{}
	}
	view := telegramView{HasToken: cfg.BotToken() != "", AllowedID: cfg.Telegram.AllowedID, Enabled: cfg.Telegram.Enabled}
	if s.telegram != nil {
		view.Running = s.telegram.Running()
	}
	return view
}

// getTelegramConfig：TG bot 的绑定状态（token 有没有 + 绑了谁 + 开了没 + 跑没跑）。
func (s *Server) getTelegramConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.telegramStatus())
}

// setTelegramReq：`PUT /config/telegram` 的请求体 —— 三个各改各的（nil = 不动）。
type setTelegramReq struct {
	AllowedID *int64  `json:"allowed_id"`
	BotToken  *string `json:"bot_token"` // "" = 清掉文件里的（回落到环境变量）
	Enabled   *bool   `json:"enabled"`
}

// setTelegram：改 TG 配置（三格各改各的；绑定的 id 照旧顶掉旧的）。
//
// token 内容只进文件（0600），永不回显。开了（enabled=true）要重启生效 ——
// runner 的生命周期归 main（启动时决定跑不跑），这里只写配置不管启停。
func (s *Server) setTelegram(w http.ResponseWriter, r *http.Request) {
	var req setTelegramReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AllowedID != nil && *req.AllowedID <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "allowed_id 是那串纯数字 id（不是 @用户名）")
		return
	}
	cfg, err := config.LoadConfig(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if req.AllowedID != nil {
		cfg.Telegram.AllowedID = *req.AllowedID
	}
	if req.BotToken != nil {
		cfg.Telegram.BotToken = *req.BotToken
	}
	if req.Enabled != nil {
		cfg.Telegram.Enabled = *req.Enabled
	}
	if err := config.SaveJSON(s.paths.Config("config.json"), cfg); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.telegramStatus())
}
