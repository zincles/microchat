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
	// FromIdx/ToIdx：这一段的 1-based 消息序号区间（含）；Merged：是不是合并级。
	// **不加 omitempty**：from/to 恒>0（0 = 没填），merged 的 false 是有效值。
	FromIdx int  `json:"from_idx"`
	ToIdx   int  `json:"to_idx"`
	Merged  bool `json:"merged"`
}

func (c *CompactStatus) IsRunning() bool { return c != nil && c.State == "running" }

// RerollStatus：一次重摇的对外状态（与轮次无关 —— 与压缩同一套：**搭同一趟车回报给界面**）。
//
// 界面据此画「重摇中… 1.2s」：`elapsed_ms` 是活算的（受理那一刻到 now），跑完就定格。
// `state` 只有三个词：`running` / `done` / `error`（`error` 时原因在 `error` 那一格）。
//
// **只放"这一趟跑得怎么样"这档事** ✗：候选有几版 / 当前第几版 / 每版说的什么，那是 `reroll` 的事
// （`GET /sessions/{session_id}/reroll`）—— 两处各存一份必然打架。
type RerollStatus struct {
	State     string `json:"state"` // running / done / error
	ElapsedMS int64  `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
}

func (s *RerollStatus) IsRunning() bool { return s != nil && s.State == "running" }

// Status：`GET /sessions/{id}/status` 的响应（字段顺序即契约）。
type Status struct {
	Phase         Phase          `json:"phase"`
	MessageID     *string        `json:"message_id,omitempty"`
	ElapsedMS     int64          `json:"elapsed_ms"`
	Chars         int            `json:"chars"`
	ThinkingChars int            `json:"thinking_chars"`
	Error         string         `json:"error,omitempty"`
	Compact       *CompactStatus `json:"compact,omitempty"`
	Reroll        *RerollStatus  `json:"reroll,omitempty"`
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
	// reroll：这个会话此刻有没有在摇（**与轮次共用同一把闸**：一方在跑，另一方就进不来）。
	reroll        *RerollStatus
	rerollStarted time.Time
	token         uint64
	cancel        context.CancelFunc
}

// Registry：一个进程一份。
type Registry struct {
	mu      sync.Mutex
	entries map[string]*entry
	// nextToken：**全进程单调递增**，绝不回绕。
	//
	// 别从"这个会话上一次的令牌 +1"推 —— `Stop` 会把条目**摘掉**，于是下一条从 1 重新开始，
	// 而**被停掉的旧任务**这时正好回来 ⇒ 令牌"对上了"，它会把结果写进**新一轮**的账上
	// （库顺序、错误态都会跟着乱）。令牌的作用就是不重复。
	nextToken uint64
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
	// 重摇的耗时**活算**（受理那一刻 → now）；跑完由 `FinishReroll` 定格。
	// **读的时候拷一份**：读状态不该改到登记表里的那个对象。
	if item.reroll != nil {
		reroll := *item.reroll
		if reroll.IsRunning() {
			reroll.ElapsedMS = time.Since(item.rerollStarted).Milliseconds()
		}
		status.Reroll = &reroll
	}
	return status
}

// Begin：**唯一的并发闸门** —— 已经在跑就直接拒绝（不排队；排队会让"按了发送却什么都没发生"难以解释）。
//
// 闸门管两件事：这一轮生成、以及一次重摇 —— **两边共用它**（重摇也是一次对话型调用：
// 两个上游请求同时改同一段历史，谁也说不清谁先谁后）⇒ 重摇在跑时再发消息 = 409。
func (r *Registry) Begin(sessionID, messageID string) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item != nil && item.phase.IsBusy() {
		return 0, ErrBusy
	}
	if item != nil && item.reroll.IsRunning() {
		return 0, ErrRerollBusy
	}
	r.nextToken++
	r.entries[sessionID] = &entry{
		phase: PhasePending, messageID: &messageID, started: time.Now(), token: r.nextToken,
	}
	return r.nextToken, nil
}

// BeginReroll：登记一次重摇 —— **与一轮生成共用同一把闸**（另一方在跑就 409）。
//
// 与 `Begin` 同一条理由（不排队），只是这一趟的产出**先当候选**（不进历史）⇒ 流缓冲用不上：
// 它的进度只有「在不在摇 + 耗时」（`RerollStatus`），界面靠它画「重摇中…」。
func (r *Registry) BeginReroll(sessionID string) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item != nil && item.phase.IsBusy() {
		return 0, ErrBusy
	}
	if item != nil && item.reroll.IsRunning() {
		return 0, ErrBusy
	}
	r.nextToken++
	if item == nil {
		item = &entry{phase: PhaseIdle}
		r.entries[sessionID] = item
	}
	item.reroll = &RerollStatus{State: "running"}
	item.rerollStarted = time.Now()
	item.token = r.nextToken
	return r.nextToken, nil
}

// FinishReroll：收尾一次重摇（`failure` 非空 ⇒ `error` 态，原因摆在 `error` 那一格）。
//
// 与 `Finish` 同一条规矩：**令牌对不上就不许改**（被 stop 顶掉、或已被新的一轮顶掉）。
func (r *Registry) FinishReroll(sessionID string, token uint64, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || item.token != token || item.reroll == nil {
		return
	}
	item.reroll.ElapsedMS = time.Since(item.rerollStarted).Milliseconds()
	item.cancel = nil
	if failure == "" {
		item.reroll.State = "done"
		item.reroll.Error = ""
		return
	}
	item.reroll.State = "error"
	item.reroll.Error = failure
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
func (r *Registry) FinishCompact(sessionID, state, summaryID, message string, compacted, fromIdx, toIdx int, merged bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || item.compact == nil {
		return
	}
	item.compact.State = state
	item.compact.Compacted = compacted
	item.compact.FromIdx = fromIdx
	item.compact.ToIdx = toIdx
	item.compact.Merged = merged
	if summaryID != "" {
		item.compact.SummaryID = &summaryID
	}
	item.compact.Error = message
}

// ErrBusy：已经在跑。
var ErrBusy = errBusy{}

type errBusy struct{}

func (errBusy) Error() string { return "这一轮还在跑" }

// ErrRerollBusy：这个会话正在重摇 —— 与 `ErrBusy` 分开，是为了让调用方**说得出是哪一件在跑**
// （"还在生成中"与"重摇还没摇完"对用户是两件事，别糊成一句话）。
var ErrRerollBusy = errRerollBusy{}

type errRerollBusy struct{}

func (errRerollBusy) Error() string { return "这个会话正在重摇" }

// Attach：把取消函数挂上（`stop` 用它 abort 后台任务）—— 一轮生成与一次重摇都走它
// （令牌一样是那枚令牌：谁挂上来的，回来时就得对得上）。
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

// StopReroll：**只按停"重摇"那一趟**（不碰一轮生成）。
//
// 为什么不能拿 `Stop` 顶上：两者共用一条登记，而 `Stop` 停的是"这个会话此刻在跑的那一件"。
// 退出重摇模式、或新消息一到要清候选时，被按停的**只能是重摇**（那时生成可能刚刚受理
// —— 拿 `Stop` 去按，会把刚受理的那一轮当场掐掉）。
//
// 幂等：没在摇也回 false。
func (r *Registry) StopReroll(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || !item.reroll.IsRunning() {
		return false
	}
	if item.cancel != nil {
		item.cancel()
	}
	// 两件同时在跑不可能（同一把闸），所以摘掉整条登记：正在摇的那一趟回来时令牌对不上 ⇒ 结果丢掉。
	delete(r.entries, sessionID)
	return true
}

// Stop：摘登记 + abort 后台任务（别让上游白跑完）。**幂等**：没在跑也回 false。
//
// 管的是"这个会话此刻在跑的那一件" —— 一轮生成**或**一次重摇（两者共用这把闸）。
// 重摇被按停 ⇒ 那一版不落位（候选项由 `reroll` 自己摘掉），message 还是当前这版。
func (r *Registry) Stop(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.lookup(sessionID)
	if item == nil || (!item.phase.IsBusy() && !item.reroll.IsRunning()) {
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
