// Package task：**作业的身份 + 生死** —— "这会儿有哪些活在跑"的唯一登记处。
//
// 与 `turn` 分家（不许互为镜像）：`task` 管身份与生死，`turn` 管流细节，
// `compact` 管压缩自己的细节。三份状态各管一段。
//
// **Go 没有 Rust 的 Drop**：等价物是 `defer` —— 每个任务 goroutine 的第一行就该是
// `defer guard.Finish(...)`（panic 也会执行它 ⇒ 记成"中断"，绝不留僵尸条目）。
package task

import (
	"sync"
	"time"
)

// Kind：任务的类型。**加一种就在这儿加一个变体**（面板按它分组/上色）。
type Kind string

const (
	KindTurn          Kind = "turn"           // 前台：某个会话的一轮生成
	KindCompact       Kind = "compact"        // 后台：把最老的 N 个对话块收成一条摘要
	KindRefreshModels Kind = "refresh_models" // 后台：从上游拉某个渠道的模型列表
)

// Label：面板上那一列中文名。
func (k Kind) Label() string {
	switch k {
	case KindTurn:
		return "生成"
	case KindCompact:
		return "压缩"
	case KindRefreshModels:
		return "刷新模型"
	default:
		return string(k)
	}
}

// Record：一条任务记录（进程内的事实，不进库）。
type Record struct {
	ID           string  `json:"id"`
	Kind         Kind    `json:"kind"`
	Conversation *string `json:"conversation,omitempty"`
	Title        string  `json:"title"`
	StartedAt    int64   `json:"started_at"`
	FinishedAt   *int64  `json:"finished_at,omitempty"`
	Outcome      string  `json:"outcome,omitempty"`
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
	nextID  int
}

func NewRegistry() *Registry { return &Registry{records: map[string]Record{}} }

// Guard：一次任务的把手。`Finish` 幂等；**忘了收 = 面板上永远"正在跑"**，
// 所以每个任务 goroutine 的第一行都该 `defer guard.Finish(...)`。
type Guard struct {
	registry *Registry
	id       string
	once     sync.Once
}

// Begin：挂号。`title` 是面板上那一行字（例如「生成 · 会话 3f2a」）。
func (r *Registry) Begin(kind Kind, conversationID *string, title string) *Guard {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := kind.Label() + "#" + itoa(r.nextID)
	r.records[id] = Record{
		ID: id, Kind: kind, Conversation: conversationID, Title: title,
		StartedAt: time.Now().UnixMilli(),
	}
	r.order = append(r.order, id)
	return &Guard{registry: r, id: id}
}

// Finish：收尾（成功 / 失败 / 中断各写一句人话）。重复调用只生效一次。
func (g *Guard) Finish(outcome string) {
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
		record.Outcome = outcome
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

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
