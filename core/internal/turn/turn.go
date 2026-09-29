// Package turn：**这一轮的流细节** —— 一个会话此刻在跑什么、跑了多久、攒了多少字、怎么停。
//
// 三条纪律（照旧版）：
//  1. **进程内的事实，不进库**：后端一重启就没有生成在跑，回到 `idle` 是诚实的；
//  2. 每次受理铸一个**令牌**，后台任务回来时对不上就把结果丢掉（被 stop 顶掉的旧任务不许写库）；
//  3. 增量**什么都不算**（不进库、不进树、不参与世界状态）—— 落库只认流结束后那一整段。
//
// 与 `task` 分家：`task` 管身份与生死，`turn` 管流细节；**不许互为镜像**。
package turn

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Phase：一个会话此刻处在哪一档。
type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhasePending   Phase = "pending"
	PhaseStreaming Phase = "streaming"
	PhaseError     Phase = "error"
)

// IsBusy：「还在跑」。`error` 不算 —— 它是上一轮的结局。
func (p Phase) IsBusy() bool { return p == PhasePending || p == PhaseStreaming }

// CompactStatus：一次压缩任务的对外状态（与轮次无关，只是搭同一趟车回报给界面）。
type CompactStatus struct {
	State     string  `json:"state"` // running / done / error
	Blocks    int     `json:"blocks"`
	Compacted int     `json:"compacted"`
	SummaryID *string `json:"summary_id,omitempty"`
	Error     string  `json:"error,omitempty"`
	AtMS      int64   `json:"at_ms"`
}

func (c *CompactStatus) IsRunning() bool { return c != nil && c.State == "running" }

// Status：`GET /sessions/{id}/status` 的响应（字段顺序即契约）。
type Status struct {
	Phase         Phase          `json:"phase"`
	MessageID     *string        `json:"message_id,omitempty"`
	ElapsedMS     int64          `json:"elapsed_ms"`
	Chars         int            `json:"chars"`
	ThinkingChars int            `json:"thinking_chars"`
	Error         string         `json:"error,omitempty"`
	Compact       *CompactStatus `json:"compact,omitempty"`
}

// StreamSlice：一次**游标读**的结果（正文与思考各一段 + 各自的新游标 + 这轮是否结束）。
type StreamSlice struct {
	Text      string `json:"text"`
	Next      int    `json:"next"`
	Thinking  string `json:"thinking"`
	ThinkNext int    `json:"think_next"`
	Done      bool   `json:"done"`
}

type entry struct {
	phase     Phase
	messageID *string
	started   time.Time
	elapsed   time.Duration // 结束后的定格时长（error 态回报它）
	text      strings.Builder
	thinking  strings.Builder
	err       string
	compact   *CompactStatus
	token     uint64
	cancel    context.CancelFunc
}

// Registry：一个进程一份。
type Registry struct {
	mu      sync.Mutex
	entries map[string]*entry
}

func NewRegistry() *Registry { return &Registry{entries: map[string]*entry{}} }

func (r *Registry) lookup(id string) *entry { return r.entries[id] }

// Status：这个会话此刻的状态（每个会话最多一个条目 —— 旧的在 Begin/Stop/Finish 时被替换或清掉）。
func (r *Registry) Status(sessionID string) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil {
		return Status{Phase: PhaseIdle}
	}
	status := Status{
		Phase: item.phase, MessageID: item.messageID,
		Chars: item.text.Len(), ThinkingChars: item.thinking.Len(),
		Error: item.err, Compact: item.compact,
	}
	if item.phase.IsBusy() {
		status.ElapsedMS = time.Since(item.started).Milliseconds()
	} else {
		status.ElapsedMS = item.elapsed.Milliseconds()
	}
	return status
}

// Begin：**唯一的并发闸门** —— 已经在跑就直接拒绝（不排队；排队会让"按了发送却什么都没发生"难以解释）。
func (r *Registry) Begin(sessionID, messageID string) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.lookup(sessionID); item != nil && item.phase.IsBusy() {
		return 0, ErrBusy
	}
	next := uint64(1)
	if item := r.lookup(sessionID); item != nil {
		next = item.token + 1
	}
	r.entries[sessionID] = &entry{
		phase: PhasePending, messageID: &messageID, started: time.Now(), token: next,
	}
	return next, nil
}

// BeginCompact：登记一次压缩（同一会话同时只允许一个）。
func (r *Registry) BeginCompact(sessionID string, blocks int, atMS int64) (*CompactStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.lookup(sessionID); item != nil && item.compact.IsRunning() {
		return nil, ErrBusy
	}
	status := &CompactStatus{State: "running", Blocks: blocks, AtMS: atMS}
	if item := r.lookup(sessionID); item != nil {
		item.compact = status
	} else {
		r.entries[sessionID] = &entry{phase: PhaseIdle, compact: status}
	}
	return status, nil
}

// FinishCompact：收尾一次压缩。
func (r *Registry) FinishCompact(sessionID, state, summaryID, message string, compacted int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || item.compact == nil {
		return
	}
	item.compact.State = state
	item.compact.Compacted = compacted
	if summaryID != "" {
		item.compact.SummaryID = &summaryID
	}
	item.compact.Error = message
}

// ErrBusy：已经在跑。
var ErrBusy = errBusy{}

type errBusy struct{}

func (errBusy) Error() string { return "这一轮还在跑" }

// Attach：把取消函数挂上（`stop` 用它 abort 后台任务）。
func (r *Registry) Attach(sessionID string, token uint64, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.lookup(sessionID); item != nil && item.token == token {
		item.cancel = cancel
	}
}

// IsMine：令牌还对得上吗？—— 被 `stop` 顶掉的旧任务回来时会对不上，结果直接丢掉。
func (r *Registry) IsMine(sessionID string, token uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	return item != nil && item.token == token
}

// AppendContent：增量进缓冲区，顺手把 `pending` 抬成 `streaming`。令牌对不上就不收。
func (r *Registry) AppendContent(sessionID string, token uint64, delta string) bool {
	return r.append(sessionID, token, delta, false)
}

// AppendReasoning：**思考流单独一条缓冲**（只服务动画，不参与任何计算，但会落档）。
func (r *Registry) AppendReasoning(sessionID string, token uint64, delta string) bool {
	return r.append(sessionID, token, delta, true)
}

func (r *Registry) append(sessionID string, token uint64, delta string, thinking bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || item.token != token || item.phase == PhaseIdle {
		return false
	}
	if thinking {
		item.thinking.WriteString(delta)
	} else {
		item.text.WriteString(delta)
	}
	if item.phase == PhasePending {
		item.phase = PhaseStreaming
	}
	return true
}

// StreamSince：**游标读**（正文与思考各一条游标）—— 多个客户端与断线重连各拿各的，互不偷。
// 读**不消费**：同一个游标读两次拿到的东西一样。
func (r *Registry) StreamSince(sessionID string, from, thinkFrom int) StreamSlice {
	r.mu.Lock()
	defer r.mu.Unlock()
	slice := StreamSlice{}
	item := r.lookup(sessionID)
	if item == nil {
		return slice
	}
	text, thinking := item.text.String(), item.thinking.String()
	slice.Next, slice.ThinkNext = len(text), len(thinking)
	if from < len(text) {
		slice.Text = text[from:]
	}
	if thinkFrom < len(thinking) {
		slice.Thinking = thinking[thinkFrom:]
	}
	slice.Done = !item.phase.IsBusy()
	return slice
}

// ThinkingOf / ThinkingMSOf：思考正文与用时（受理 → 第一段正文）—— 落档时要用。
func (r *Registry) ThinkingOf(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.lookup(sessionID); item != nil {
		return item.thinking.String()
	}
	return ""
}

// Finish：这一轮结束。`failure` 非空 ⇒ 进 `error` 态（并记住上一轮的总耗时）。
func (r *Registry) Finish(sessionID string, token uint64, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || item.token != token {
		return
	}
	item.elapsed = time.Since(item.started)
	item.cancel = nil
	if failure == "" {
		item.phase = PhaseIdle
		item.err = ""
		return
	}
	item.phase = PhaseError
	item.err = failure
}

// Stop：摘登记 + abort 后台任务（别让上游白跑完）。**幂等**：没在跑也回 false。
func (r *Registry) Stop(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || !item.phase.IsBusy() {
		return false
	}
	if item.cancel != nil {
		item.cancel()
	}
	// **摘掉登记**（照旧版）：旧任务回来时令牌对不上（查不到条目 ⇒ `IsMine` 为 false），
	// 就不会把过期结果写进库 —— 这正是令牌存在的理由。顺带状态回到 idle、缓冲清空。
	delete(r.entries, sessionID)
	return true
}
