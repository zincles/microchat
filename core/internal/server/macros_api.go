package server

import (
	"net/http"

	"microchat/internal/macro"
)

// MacroReport：`GET /sessions/{session_id}/macros` 的形状。
//
// `macros` = 注册表 + 当前值（面板上半“可用宏”）；
// `usage` = **本会话全文**（生效系统提示词 + 全部消息正文）里每个宏出现的次数，
// 已注册与未知（如 ST 的 `getvar::x`）都算 —— 不知道的宏不该从统计里消失。
type MacroReport struct {
	Macros []macro.View  `json:"macros"`
	Usage  []macro.Count `json:"usage"`
	Total  int           `json:"total"`
}

func (s *Server) sessionMacros(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ListMessages(session.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	values := s.chat.MacroValues(*session)
	prompt, _ := s.effectiveSystemPrompt(*session)
	usage := macro.Scan(prompt)
	for _, message := range messages {
		usage = macro.Merge(usage, macro.Scan(message.Content))
	}
	total := 0
	for _, count := range usage {
		total += count
	}
	writeJSON(w, http.StatusOK, MacroReport{
		Macros: macro.Registered(values),
		Usage:  macro.Counts(usage),
		Total:  total,
	})
}
