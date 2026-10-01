// Package model：领域结构。**JSON 字段名与 Rust 版逐字一致**（Godot 那边按它写）。
//
// 注意点（都是从 Rust 版抓下来的活形状）：
//   - 可空字段用 `omitempty` 省略（对应 Rust 的 skip_serializing_if），别输出 null 之外的差异；
//   - `usage` / `reasoning` 等只服务显示，不参与世界状态与装配；
//   - 时间是毫秒整数。
package model

import (
	"encoding/json"
	"strings"
)

// DefaultAgentID：内置默认 agent 的 id（Rust: model::DEFAULT_AGENT_ID）。
func DefaultAgentID() string { return "default" }

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message：一条消息 —— **线性会话里的一格**（没有父指针：顺序由 `id` 定）。
//
// **字段顺序照抄 Rust 版**（serde 按声明顺序输出 ⇒ 顺序不同 = 字节不同 = 前端可能踩坑）。
type Message struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      Role   `json:"role"`
	Content   string `json:"content"` // 存档本体：<state> 块原样留着

	// 下面四样只服务显示（出站 / 世界状态一律不看它们）
	Reasoning   string `json:"reasoning,omitempty"`
	ReasoningMS *int64 `json:"reasoning_ms,omitempty"`
	DurationMS  *int64 `json:"duration_ms,omitempty"`
	// Usage：归一化后的用量（JSON 对象）。库里是 TEXT，接口上是**对象** ⇒
	// 用 RawMessage 原样透出（包成 string 就双重编码了 —— 真发生过，diff 抓出来的）。
	Usage json.RawMessage `json:"usage,omitempty"`

	SummaryID *string `json:"summary_id,omitempty"` // 收拢它的摘要：压缩只写这一格
	CreatedAt int64   `json:"created_at"`
	// UpdatedAt：**修改时**（毫秒）。插入时 = created_at ⇒ 没改过的两者相等；
	// 改正文（PATCH / 重摇接受）刷新成当前毫秒。摘要的 dirty 判定用它比 created_at 便宜也准。
	UpdatedAt int64 `json:"updated_at"`

	// Idx：这条消息在**它那条会话里的序号**（1-based）—— **派生字段，不落库、永不改变**。
	// 位置 = 按 `id` 排序后的下标（算式只有一处：`store.IndexMessages`）。线性会话只删后缀、
	// 不往中间插、编辑/重摇不改 id ⇒ 分配了就变不了（见 DEFINE.md 的「各种 id」）。
	// `0` 留给**合成的系统提示词**（它不是消息 ⇒ 消息列表里永远是 1..N）。
	// 由 store 编好 ⇒ 经手 SQL 拿到的消息都带它；手搭出来的消息没有（`omitempty` ⇒ 不带这一格）。
	Idx int `json:"idx,omitempty"`
}

// TitleFrom：首句临时标题 —— 换行压成空格、trim、按**字符**截断（不是字节：中文按字节截会切出残字）。
//
// 标题只是给人看的句柄（空串 = 还没起名）；真正的权威是这个会话的消息。
func TitleFrom(text string, chars int) string {
	flat := strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ").Replace(text))
	if chars <= 0 {
		return flat
	}
	runes := []rune(flat)
	if len(runes) <= chars {
		return flat
	}
	return string(runes[:chars])
}

// SummaryType：摘要的成员是消息还是摘要（同质，不许混）。
type SummaryType string

const (
	TypeMessages SummaryType = "message"
	TypeSummaries SummaryType = "summary"
)

// Summary：一条摘要（Compact 的产出）。派生数据：只插行，绝不碰 messages 的正文。
//
// `BeginMessageID` / `EndMessageID` = 它盖住的那一段（闭区间）：装配时按它 O(1) 跳过去。
// 都可为空（老数据）——那时装配退回逐条走，不跳。
type Summary struct {
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	ParentSummaryID *string           `json:"parent_summary_id,omitempty"`
	Type           SummaryType `json:"type"` // DB 列名是历史（`source_kind`），值与 JSON 名一致
	BeginMessageID  *string           `json:"begin_message_id,omitempty"`
	EndMessageID    *string           `json:"end_message_id,omitempty"`
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

// Session：一处会话。**线性**（没有 current_leaf：最新一条 = `ORDER BY id DESC LIMIT 1`）。
type Session struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	SystemPrompt string `json:"system_prompt"` // 空串 = 没覆盖（用 agent 的 ⇒ 世界状态底子算全局）
	Provider     string `json:"provider"`      // 软引用
	Model        string `json:"model"`
	AgentID      string `json:"agent_id"` // 软引用
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
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

// SessionView：列表项 = 会话 + 这一轮的状态 + **这条会话有几条消息**。
//
// `messages` 是**条数**（不是内容）：客户端的启动编排靠它一眼判定"空会话"，
// 不必为每一条无标题会话再发一次 `GET /sessions/{id}/messages`（会话一多就是 N 次请求）。
type SessionView struct {
	Session
	Messages int        `json:"messages"`
	Turn     TurnStatus `json:"turn"`
}

// DeletionPlan：一次"删这条及之后全部"的完整后果（`deletionPlan` 一份计算，预览与执行共用）。
//
// 数量**不单列** —— 数组长度就是（两份数字迟早打架）。五组 id 的 JSON 名字按 `DEFINE.md`：
// 预览（GET deletion-preview）与执行（DELETE 的响应）都是这个形状。
type DeletionPlan struct {
	// SessionID：执行时用来刷新会话的 updated_at。**不进 JSON** —— 接口形状是定死的五项。
	SessionID string `json:"-"`

	DeletedMessageIDs    []string `json:"deleted_message_ids"`
	DeletedSummaryIDs    []string `json:"deleted_summary_ids"`
	UnlinkedMessageIDs   []string `json:"unlinked_message_ids"`
	UnlinkedSummaryIDs   []string `json:"unlinked_summary_ids"`
	LastDeletedMessageID string   `json:"last_deleted_message_id"`
}
