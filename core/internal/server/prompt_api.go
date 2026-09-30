package server

import (
	"net/http"

	"microchat/internal/state"
)

// promptView：`GET /sessions/{session_id}/prompt` 的形状（客户端契约）。
//
// ⚠ 这是**结果**，不是"用户存的那条"：三级解析（会话覆盖 → agent 的 → 内置默认）之后那一份，
// 也就是**真会发给模型**的提示词 —— 与出站拼装、世界状态底子读的是**同一处**（`state.ResolveSystemPrompt`）。
// 将来若做了"可拼接的系统提示词"（`{{…}}` 模板那一套），**同一个接口返回运算后的结果** ——
// 不变的就是这个形状（`text` + `source`）。
type promptView struct {
	Text string `json:"text"`
	// Source ∈ conversation / agent / builtin：
	//   conversation = 会话自己写了（`sessions.system_prompt`，覆盖了 agent 的）
	//   agent        = 用 `agents.json` 里那个 agent 的
	//   builtin      = 上面两级都没有 ⇒ 代码里的内置默认（`config.BuiltinDefaultAgent`）
	Source string `json:"source"`
}

// promptSourceLabel：把状态层给的"生效来源"翻译成对外的三级口径。
//
// **不算第二份解析**：提示词文本由 `state.ResolveSystemPrompt` 一处给出（会话覆盖 → agent → 内置默认），
// 这里只把它的来源枚举翻成接口上的字。
func promptSourceLabel(source state.PromptSource) string {
	switch source {
	case state.PromptFromSession:
		return "conversation"
	case state.PromptFromAgent:
		return "agent"
	default:
		return "builtin"
	}
}

// getSessionPrompt：某会话**生效的系统提示词**（结果 + 来源）。
//
// 调试用：客户端把它当成一条 `role=system` 的消息摆在消息区最上方，"这轮到底带了什么底子"一眼可见。
// 会话不存在 ⇒ 404（与其他会话级路由同一条口径）。
func (s *Server) getSessionPrompt(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	// **永不给空**：三级解析全落空（典型是会话的 `agent_id` 指向一个**已不存在**的 agent
	// —— 软引用悬空）时，解析本身就会退回内置默认那句并标 `builtin`（`state.ResolveSystemPrompt`），
	// 出站 / 底子走的是同一次解析 ⇒ 三处口径天然一致。这里不再自己算一遍。
	text, source := s.effectiveSystemPrompt(*session)
	writeJSON(w, http.StatusOK, promptView{Text: text, Source: promptSourceLabel(source)})
}
