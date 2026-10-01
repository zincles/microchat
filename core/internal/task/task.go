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
	KindTitle         Kind = "title"          // 后台：给一条会话起标题（首次拿到回复之后自动一次）
	KindJudgement     Kind = "judgement"      // 后台：JEV 决策（一段上游调用，骑会话 id）
	KindRefreshModels Kind = "refresh_models" // 后台：从上游拉某个渠道的模型列表
)

// needsSession：这个作业会调模型吗 —— 会调的**必须**带会话 id。
//
// 理由不是记账好看：辅助调用要**骑同一个会话 id**（`x-opencode-session` 那类头，网关按它路由、
// 前缀缓存也认它）⇒ 缺了要么被上游 400、要么把请求甩进"另一个会话"。
// **在 `Begin()` 当场断言** = 让错误在挂号那一刻就炸，而不是等发出去被上游拒。
//
// `reroll` 也在这里：它是**对话型调用**（与 `turn` 同类，只是产出先当候选）⇒ 一样骑本会话 id。
func needsSession(kind Kind) bool {
	return kind == KindTurn || kind == KindReroll || kind == KindCompact || kind == KindTitle || kind == KindJudgement
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
	Session    *string `json:"session,omitempty"`
	Title      string  `json:"title"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt *int64  `json:"finished_at,omitempty"`
	Outcome    State   `json:"outcome"`
	// Error：失败原因 —— **单开一个字段**放着（状态字段里只放那五个词之一：
	// "失败：上游 429"这种串会让客户端只能匹配文案）。
	Error string `json:"error,omitempty"`
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
}

func NewRegistry() *Registry { return &Registry{records: map[string]Record{}} }

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
	if needsSession(kind) && (sessionID == nil || strings.TrimSpace(*sessionID) == "") {
		panic("task: " + kind.Label() + " 必须带会话 id（会调模型的作业要骑同一个会话 id）")
	}
	id := uuid.Must(uuid.NewV7()).String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[id] = Record{
		ID: id, Kind: kind, Session: sessionID, Title: title,
		StartedAt: time.Now().UnixMilli(), Outcome: StateRunning,
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
