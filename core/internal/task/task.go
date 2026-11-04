// Package task：**作业的身份 + 生死** —— "这会儿有哪些活在跑"的唯一登记处。
//
// 一个 Task = **一次上游请求的全程**（发起 → 收尾 → 可能被取消）。
//
// 与 `turn` 分家（不许互为镜像）：`task` 管身份与生死，`turn` 管流细节，
// `compact` 管压缩自己的细节。三份状态各管一段。
//
// **Go 没有 Rust 的 Drop**：等价物是 `defer` —— 每个任务 goroutine 的第一行就该是
// `defer guard.Interrupted()`（panic 也会执行它 ⇒ 记成"中断"，绝不留僵尸条目）；
// 正常收尾时先调 Succeed / Fail / Cancel，`once` 保证那个兜底不再改写它。
package task

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Kind：任务的类型。**加一种就在这儿加一个变体**（面板按它分组/上色）。
type Kind string

const (
	KindTurn          Kind = "turn"           // 前台：某个会话的一轮生成
	KindReroll        Kind = "reroll"         // 前台：把最后那条 assistant 回复重摇一版（**对话型调用**，不是能力）
	KindCompact       Kind = "compact"        // 后台：把最老的 N 个对话块收成一条摘要
	KindCompactJob    Kind = "compact_job"    // 后台：一次分段压缩作业（父，下面挂多个 KindCompact）
	KindTitle         Kind = "title"          // 后台：给一条会话起标题（首次拿到回复之后自动一次）
	KindJudgement     Kind = "judgement"      // 后台：JEV 决策（一段上游调用，骑会话 id）
	KindRefreshModels Kind = "refresh_models" // 后台：从上游拉某个渠道的模型列表
)

// Group：任务的**大类** —— 界面按它分组（2026-11-01 用户定：「要区分一般任务和 LLM 生成任务」）。
//
// 判据是**这一发会不会打上游**（与 `needsSession` 同一件事的两面）：
//
//	llm   —— 会调模型、烧 token：turn / reroll / compact / title（judgement 已退役但种类还在）
//	plain —— 不调模型，就是本地/HTTP 的活儿：refresh_models
//
// ⚠ **只在这一处分类**：`Kind.Group()` 是唯一权威，`Record.Group` 在 `Begin` 时由它填。
//
//	别在界面上再写一张 kind → 大类的映射表（两边一分岔，面板就会把某类任务挂到错的组里，
//	而且不报错 —— 只是"看着少了一条"）。
type Group string

const (
	GroupLLM   Group = "llm"   // LLM 生成
	GroupPlain Group = "plain" // 一般
)

// Group：这个 kind 属于哪一类。**未知的 kind 一律算一般**（保守：不会把它误挂进"烧钱"那组）。
func (k Kind) Group() Group {
	switch k {
	case KindTurn, KindReroll, KindCompact, KindCompactJob, KindTitle, KindJudgement:
		return GroupLLM
	}
	return GroupPlain
}

// needsSession：这个作业会调模型吗 —— 会调的**必须**带会话 id。
//
// 理由不是记账好看：辅助调用要**骑同一个会话 id**（`x-opencode-session` 那类头，网关按它路由、
// 前缀缓存也认它）⇒ 缺了要么被上游 400、要么把请求甩进"另一个会话"。
// **在 `Begin()` 当场断言** = 让错误在挂号那一刻就炸，而不是等发出去被上游拒。
//
// `reroll` 也在这里：它是**对话型调用**（与 `turn` 同类，只是产出先当候选）⇒ 一样骑本会话 id。
func needsSession(kind Kind) bool {
	return kind == KindTurn || kind == KindReroll || kind == KindCompact || kind == KindCompactJob || kind == KindTitle || kind == KindJudgement
}

// State：Task 的五个状态（**这套词只属于 Task** —— `idle`/`pending` 那套是 turn 的，别混）。
type State string

const (
	StateRunning     State = "进行中" // 登记时写
	StateSucceeded   State = "成功"  // 收尾方
	StateFailed      State = "失败"  // 收尾方；**原因单开字段**，不塞进这里
	StateCanceled    State = "取消"  // 用户按停 —— **正常操作**，不是错误
	StateInterrupted State = "中断"  // 兜底（没人收尾：忘了收 / panic / 进程被杀）⇒ 代码出问题的信号
)

// Label：面板上那一列中文名。
func (k Kind) Label() string {
	switch k {
	case KindTurn:
		return "生成"
	case KindReroll:
		return "重摇"
	case KindCompact:
		return "压缩"
	case KindCompactJob:
		return "分段压缩"
	case KindTitle:
		return "起标题"
	case KindJudgement:
		return "判断"
	case KindRefreshModels:
		return "刷新模型"
	default:
		return string(k)
	}
}

// Record：一条任务记录（进程内的事实，不进库）。
type Record struct {
	// ID：UUIDv7。**不是自增小整数** —— 那种重启后计数归零 ⇒ 日志里"task 1"会撞车
	// （两个不同时间的活对不上）。
	ID         string  `json:"id"`
	Kind       Kind    `json:"kind"`
	Group      Group   `json:"group"` // 大类：llm / plain（`Kind.Group()` 在 Begin 时填，**别手写**）
	Session    *string `json:"session,omitempty"`
	Title      string  `json:"title"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt *int64  `json:"finished_at,omitempty"`
	Outcome    State   `json:"outcome"`
	// Error：失败原因 —— **单开一个字段**放着（状态字段里只放那五个词之一：
	// "失败：上游 429"这种串会让客户端只能匹配文案）。
	Error string `json:"error,omitempty"`
	// Cancelable：**这个任务能不能被按停**（2026-11-01）。
	//
	// 面板据此决定要不要画那颗按钮 —— 画了按不动是最糟的一种（"点了没反应"）。
	// 由任务自己在 `Guard.Cancellable(...)` 时置位：手上有 ctx / 有 stop 口的那些能停，
	// 纯本地一口气跑完的（`refresh_models`）不能 —— 它快到没必要，也没有可断的地方。
	Cancelable bool `json:"cancelable,omitempty"`
	// ParentID：分段压缩这类复合作业的子任务指向父 Job；普通任务为空。
	// 面板据此缩进显示（Job 一行，下面挂它正在跑的 Task），而不是把子任务摊成平级。
	ParentID string `json:"parent_id,omitempty"`
}

func (r Record) Running() bool { return r.FinishedAt == nil }

// ElapsedMS：已经跑了多久（正在跑）或一共跑了多久（已结束）。
func (r Record) ElapsedMS(now int64) int64 {
	if r.FinishedAt != nil {
		return *r.FinishedAt - r.StartedAt
	}
	return now - r.StartedAt
}

// Board：给界面看的一屏（正在跑的在最前）。
type Board struct {
	Running int      `json:"running"`
	Tasks   []Record `json:"tasks"`
	Now     int64    `json:"now"`
}

// keepFinished：保留多少条已结束的记录（正在跑的永远都在）。
const keepFinished = 60

type Registry struct {
	mu      sync.Mutex
	order   []string // 登记顺序（= 开始顺序）
	records map[string]Record
	// cancels：**能停的那几个**的停止函数（id → cancel）。收尾时摘掉。
	cancels map[string]func()
}

func NewRegistry() *Registry {
	return &Registry{records: map[string]Record{}, cancels: map[string]func(){}}
}

// Guard：一次任务的把手。四个收尾方法**幂等且只生效一次**（先到的那次算数）；
// **忘了收 = 兜底的 `Interrupted`**（面板上绝不留下僵尸条目）。
type Guard struct {
	registry *Registry
	id       string
	once     sync.Once
}

// Begin：挂号。`title` 是面板上那一行字（例如「生成 · 会话 3f2a」）。
//
// **会调模型的作业必须带会话 id** ⇒ 缺了**当场炸**（见 `needsSession`）。
func (r *Registry) Begin(kind Kind, sessionID *string, title string) *Guard {
	return r.BeginChild(kind, sessionID, title, "")
}

// BeginChild：挂一个子 Task（高级压缩的每个 chunk）。parentID 只影响面板缩进，不影响生死。
func (r *Registry) BeginChild(kind Kind, sessionID *string, title, parentID string) *Guard {
	if needsSession(kind) && (sessionID == nil || strings.TrimSpace(*sessionID) == "") {
		panic("task: " + kind.Label() + " 必须带会话 id（会调模型的作业要骑同一个会话 id）")
	}
	id := uuid.Must(uuid.NewV7()).String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[id] = Record{
		ID: id, Kind: kind, Group: kind.Group(), Session: sessionID, Title: title,
		StartedAt: time.Now().UnixMilli(), Outcome: StateRunning, ParentID: parentID,
	}
	r.order = append(r.order, id)
	return &Guard{registry: r, id: id}
}

// ID：这次 Task 的 id（UUIDv7，进程内、不进库）。
//
// 给"受理回执"用：客户端拿到它就能在日志/面板里对上这一次上游请求（`POST .../reroll` 的 202 带它）。
func (g *Guard) ID() string {
	if g == nil {
		return ""
	}
	return g.id
}

// Cancellable：把这枚任务的"停止方式"交给登记表（**能停才登记**，不能停就别调）。
//
// 为什么要登记而不是让调用方自己留着：任务面板是按 **TaskID** 说话的（面板上只有那个 id），
// 它够不到任务自己的 ctx。所以"按下按钮"这条路必须是：面板 → `Registry.Cancel(id)` → 这里存下的那个函数。
//
// ⚠ 这个函数**只是请求停止**（Go 的 ctx 语义）：真正让它停的是任务自己 —— 它得在拿到 ctx 之后
// 的每一步看一眼 ctx。已经发出去的上游请求会被掐断，但"掐断"是异步的，所以
// `Cancel()` 回 true 不等于"它现在就已经停了"。
//
// ⚠ 幂等：任务已经收尾了就别登记（否则面板上那颗按钮会去叫一个跑完的活）。
func (g *Guard) Cancellable(cancel func()) {
	if g == nil || cancel == nil {
		return
	}
	r := g.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[g.id]
	if !ok || record.FinishedAt != nil {
		return
	}
	record.Cancelable = true
	r.records[g.id] = record
	r.cancels[g.id] = cancel
}

// Cancel：请第 id 个任务停下来。回 true = **找到并叫了它**（不代表它现在已经停了，见 `Cancellable`）。
//
// 回 false 的三种情况都算"没什么可停的"，不是错误：没有这个 id（早被 trim 掉了）、
// 它已经收尾、或者它压根就没登记停止方式（`refresh_models` 那种纯本地的活）。
func (r *Registry) Cancel(id string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	if ok {
		// 只叫一次：连按两下不该叫两遍（第二下应当是"已经叫过了" ⇒ false）。
		delete(r.cancels, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// Succeed：干完了。
func (g *Guard) Succeed() { g.finish(StateSucceeded, "") }

// Fail：干不成 —— 原因**单开字段**放着。
func (g *Guard) Fail(reason string) { g.finish(StateFailed, reason) }

// Cancel：用户按停（正常操作，不是错误）。
func (g *Guard) Cancel() { g.finish(StateCanceled, "") }

// Interrupted：兜底 —— 忘了收尾 / panic / 进程被杀。
//
// 每个任务 goroutine 的第一行写 `defer guard.Interrupted()`。
func (g *Guard) Interrupted() { g.finish(StateInterrupted, "") }

func (g *Guard) finish(state State, reason string) {
	if g == nil {
		return
	}
	g.once.Do(func() {
		g.registry.mu.Lock()
		defer g.registry.mu.Unlock()
		record, ok := g.registry.records[g.id]
		if !ok {
			return
		}
		now := time.Now().UnixMilli()
		record.FinishedAt = &now
		record.Outcome = state
		record.Error = reason
		g.registry.records[g.id] = record
		// 收尾了就没什么可停的：摘掉（否则 Cancel 会去叫一个已经跑完的活，
		// 而那个函数闭着的 ctx 早就 cancel 过了 —— 静默无害，但谎报 true）。
		delete(g.registry.cancels, g.id)
		g.registry.trim()
	})
}

// trim：只留最近 keepFinished 条**已结束**的（正在跑的永远在）。调用方持锁。
func (r *Registry) trim() {
	finished := 0
	for _, id := range r.order {
		if record, ok := r.records[id]; ok && !record.Running() {
			finished++
		}
	}
	if finished <= keepFinished {
		return
	}
	drop := finished - keepFinished
	kept := make([]string, 0, len(r.order))
	for _, id := range r.order {
		if record, ok := r.records[id]; ok && !record.Running() && drop > 0 {
			delete(r.records, id)
			drop--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

// Running：正在跑几个。
func (r *Registry) Running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, record := range r.records {
		if record.Running() {
			count++
		}
	}
	return count
}

// Board：一屏（正在跑的在最前；其余按开始时间倒序 —— 最近的在上面）。
func (r *Registry) Board() Board {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixMilli()
	records := make([]Record, 0, len(r.records))
	for _, record := range r.records {
		records = append(records, record)
	}
	// 正在跑的在前（按开始时间正序），已结束的在后（按结束时间倒序 = 最近的在上面）
	for i := 1; i < len(records); i++ {
		for j := i; j > 0; j-- {
			if less(records[j], records[j-1]) {
				records[j], records[j-1] = records[j-1], records[j]
			} else {
				break
			}
		}
	}
	board := Board{Running: 0, Tasks: records, Now: now}
	for _, record := range records {
		if record.Running() {
			board.Running++
		}
	}
	return board
}

func less(a, b Record) bool {
	if a.Running() != b.Running() {
		return a.Running() // 正在跑的在前
	}
	if a.Running() {
		return a.StartedAt < b.StartedAt // 都在跑：先开始的在前
	}
	return a.StartedAt > b.StartedAt // 都结束：后开始的（= 最近的）在前
}
