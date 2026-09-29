// Package model：领域结构。**JSON 字段名与 Rust 版逐字一致**（Godot 那边按它写）。
//
// 注意点（都是从 Rust 版抓下来的活形状）：
//   - 可空字段用 `omitempty` 省略（对应 Rust 的 skip_serializing_if），别输出 null 之外的差异；
//   - `usage` / `reasoning` 等只服务显示，不参与世界状态与装配；
//   - 时间是毫秒整数。
package model

import "encoding/json"

// DefaultAgentID：内置默认 agent 的 id（Rust: model::DEFAULT_AGENT_ID）。
func DefaultAgentID() string { return "default" }

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message：一条消息，**同时是树上的一个节点**。
//
// **字段顺序照抄 Rust 版**（serde 按声明顺序输出 ⇒ 顺序不同 = 字节不同 = 前端可能踩坑）。
type Message struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      Role   `json:"role"`
	Content   string `json:"content"` // 存档本体：<state> 块原样留着

	// 下面四样只服务显示（出站 / 世界状态 / 分支一律不看它们）
	Reasoning   string `json:"reasoning,omitempty"`
	ReasoningMS *int64 `json:"reasoning_ms,omitempty"`
	DurationMS  *int64 `json:"duration_ms,omitempty"`
	// Usage：归一化后的用量（JSON 对象）。库里是 TEXT，接口上是**对象** ⇒
	// 用 RawMessage 原样透出（包成 string 就双重编码了 —— 真发生过，diff 抓出来的）。
	Usage json.RawMessage `json:"usage,omitempty"`

	ParentID  *string `json:"parent_id"`
	SummaryID *string `json:"summary_id,omitempty"` // 收拢它的摘要：压缩只写这一格
	CreatedAt int64   `json:"created_at"`
}

// SummarySourceKind：摘要的成员是消息还是摘要（同质，不许混）。
type SummarySourceKind string

const (
	SourceMessages  SummarySourceKind = "message"
	SourceSummaries SummarySourceKind = "summary"
)

// Summary：一条摘要（Compact 的产出）。派生数据：只插行，绝不碰 messages 的正文与树。
type Summary struct {
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	ParentSummaryID *string           `json:"parent_summary_id,omitempty"`
	SourceKind      SummarySourceKind `json:"source_kind"`
	Text            string            `json:"text"`
	Blocks          int64             `json:"blocks"`
	Tokens          int64             `json:"tokens"`
	SourceIDs       []string          `json:"source_ids"`
	Provider        string            `json:"provider"`
	Model           string            `json:"model"`
	PromptVersion   int64             `json:"prompt_version"`
	Usage           json.RawMessage   `json:"usage,omitempty"`
	Dirty           bool              `json:"dirty"`
	CreatedAt       int64             `json:"created_at"`
}

// Session：一处会话。
type Session struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	SystemPrompt string  `json:"system_prompt"` // 空串 = 没覆盖（用 agent 的 ⇒ 世界状态底子算全局）
	Provider     string  `json:"provider"`      // 软引用
	Model        string  `json:"model"`
	AgentID      string  `json:"agent_id"` // 软引用
	CurrentLeaf  *string `json:"current_leaf"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

// TurnPhase：一轮生成的状态（Rust 版在 turn.rs 的进程内登记表里）。
type TurnPhase string

const (
	PhaseIdle      TurnPhase = "idle"
	PhasePending   TurnPhase = "pending"
	PhaseStreaming TurnPhase = "streaming"
	PhaseError     TurnPhase = "error"
)

// TurnStatus：给界面看的这一轮状态。
type TurnStatus struct {
	Phase         TurnPhase `json:"phase"`
	MessageID     *string   `json:"message_id,omitempty"`
	ElapsedMS     int64     `json:"elapsed_ms"`
	Chars         int       `json:"chars"`
	ThinkingChars int       `json:"thinking_chars"`
	Error         string    `json:"error,omitempty"`
}

// SessionView：列表项 = 会话 + 这一轮的状态。
type SessionView struct {
	Session
	Turn TurnStatus `json:"turn"`
}

// BranchInfo：每条消息在同龄兄弟里排第几、共几条（界面上的「‹ 2/3 ›」）。
type BranchInfo struct {
	Index    int      `json:"index"`
	Total    int      `json:"total"`
	Siblings []string `json:"siblings"`
}
