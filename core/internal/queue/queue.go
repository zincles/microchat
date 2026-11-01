package queue

// queue：**队列任务** —— 一串**按顺序跑**的原子任务（2026-11-01 用户定）。
//
// 用户原话：
//
//	「要区分一般任务和 LLM 生成任务。而且还要有队列任务这种说法。
//	  假设我们有 1000 个对话。我们要压缩 100 次，每次压缩 10 段。
//	  那么我们应该有个方便的办法，立刻把这 100 次压缩安排为 100 个排队的任务。
//	  每次压缩是原子任务。如果失败则整个大任务结束。一个完成，就进入下一个。
//	  在右上角小面板里，可随时取消。」
//
// 于是三个词分清楚（别混着叫）：
//
//	**原子任务**  = 一次上游请求的全程（`task.Record`：一次压缩 / 一轮生成 / 一次刷模型）
//	**队列任务**  = 这串原子任务的**外壳**（本包）：有总数、进度、和一个结局
//	**大类**      = `task.Group`：LLM 生成 / 一般（面板按它分组）
//
// 队列的三条规矩（都是用户点名的）：
//  1. **严格串行**：上一步收尾了才开下一步（不是"排 100 个 goroutine 让上游自己排"）；
//  2. **一步失败 ⇒ 整个队列结束**（不跳过、不重试、不接着跑剩下的）；
//  3. **可随时取消**：取消会把**在飞的那一发**一起掐断（ctx 一撤，压缩那边压根没落库）。
//
// ⚠ 队列**存在进程里**（与 `task.Registry` 同一套口径：重启就没有在跑的）。
//   这不是缺功能 —— `TaskID` 本来就不进库，"重启后没有任务在跑"是诚实的事实。
//
// ⚠ 本包**不认识**压缩。它只知道"有个 Runner 能把一步跑完"；压缩是 `main.go` 里接上的适配器
//   （与 `reroll` 借 `compact` 的生成半程同一条路：**消费方定义接口**，于是队列不必 import compact，
//   各包也就不会互相缠住）。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"microchat/internal/model"
	"microchat/internal/store"
	"microchat/internal/task"
)

// ErrNotFound：没有这个队列（id 打错 / 早就被清掉了）。
var ErrNotFound = errors.New("queue_not_found")

// InvalidError：入参不对（空步骤、步数太多、会话不存在……）—— 与 store.InvalidError 同一个用法。
type InvalidError string

func (e InvalidError) Error() string { return string(e) }

// Kind：队列现在**只支持压缩**。
//
// 别的原子任务要么不吃参数（`refresh_models` 一次一个渠道）、要么根本不该排队
// （`turn` 是前台，用户正等着）。将来要加，就在这儿加一个变体 + 一个 Runner 分支。
const KindCompact = "compact"

// maxSteps：一次最多排多少步。
//
// 不是性能上限，是**防手滑**：`times` 写错一个零就是 1 万次上游请求。
const maxSteps = 10000

// Step：队列里的一步 = 一次原子任务要的参数。
type Step struct {
	SessionID string `json:"session_id"`
	Blocks    int    `json:"blocks"` // 0 = 用 `chat.compact_blocks` 的默认值（与手动压缩同一个口径）
}

// Runner：把一步**跑完**（阻塞到这一发有结局才返回）。失败就返回错误 ⇒ 整个队列到此为止。
//
// 由 `main.go` 把 `compact.Service` 接上来（那层要 load 会话 + 调 `compact.Compact`）。
type Runner interface {
	RunStep(ctx context.Context, step Step) error
}

// Queue：给界面看的一屏（**只是快照**，不是活的把手）。
//
// ⚠ 故意**不带** `steps`：100 步的队列每 2 秒轮询一次会把面板撑爆，
// 而界面上真正要显示的只有「第几步 / 共几步」和「这一步在压哪条会话」。
type Queue struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`  // "compact"
	Group      task.Group `json:"group"` // 继承成员的分类（压缩 ⇒ llm）
	Title      string     `json:"title"`
	Index      int        `json:"index"` // **已经跑完**几步（0-based 的"当前步"就是它）
	Total      int        `json:"total"`
	State      task.State `json:"state"`
	Error      string     `json:"error,omitempty"`
	StartedAt  int64      `json:"started_at"`
	FinishedAt *int64     `json:"finished_at,omitempty"`
	// Current：正在跑的那一步（空闲 / 已结束 ⇒ 缺席）。界面拿它显示"正在压哪条会话"。
	Current *Step `json:"current,omitempty"`
}

// Running：还在跑吗。
func (q Queue) Running() bool { return q.FinishedAt == nil }

// entry：队列的**活体**（带 cancel，只在进程里）。
type entry struct {
	ID         string
	Kind       string
	Title      string
	Steps      []Step
	Index      int // 已完成的步数
	State      task.State
	Error      string
	StartedAt  int64
	FinishedAt *int64
	cancel     context.CancelFunc
	ctx        context.Context
}

// view：拷一份出去（**不交出活体**：外面拿到的是快照，改不动队列）。
func (e *entry) view() Queue {
	out := Queue{
		ID: e.ID, Kind: e.Kind, Group: groupOf(e.Kind), Title: e.Title,
		Index: e.Index, Total: len(e.Steps), State: e.State, Error: e.Error,
		StartedAt: e.StartedAt, FinishedAt: e.FinishedAt,
	}
	if e.State == task.StateRunning && e.Index < len(e.Steps) {
		step := e.Steps[e.Index] // 拷一份（别把 slice 里的格子递出去）
		out.Current = &step
	}
	return out
}

// groupOf：队列的大类跟着成员走（压缩是 LLM 生成）。未知 kind ⇒ 一般（保守）。
func groupOf(kind string) task.Group {
	if kind == KindCompact {
		return task.KindCompact.Group()
	}
	return task.GroupPlain
}

// Service：队列的登记处 + 跑队列的那个人。
type Service struct {
	Store    *store.Store
	Registry *task.Registry
	Runner   Runner

	mu     sync.Mutex
	order  []string // 登记顺序（= 开始顺序）
	queues map[string]*entry
}

// New：建一个空登记处（Runner 由调用方随后接上 —— 与 `reroll.Summaries` 同一个接法）。
func New(st *store.Store, registry *task.Registry) *Service {
	return &Service{Store: st, Registry: registry, queues: map[string]*entry{}}
}

// Enqueue：把一串步骤排成**一个**队列，立刻开跑。返回刚建好的那一份快照。
//
// 校验全在**开跑之前**做完（会话存不存在、步数合不合法）⇒ 排错的队列当场被拒，
// 而不是跑了一半才发现第 87 步的会话 id 是错的。
func (s *Service) Enqueue(kind, title string, steps []Step) (Queue, error) {
	if kind != KindCompact {
		return Queue{}, InvalidError("队列现在只支持 compact（收到 " + kind + "）")
	}
	if len(steps) == 0 {
		return Queue{}, InvalidError("队列是空的：至少排一步")
	}
	if len(steps) > maxSteps {
		return Queue{}, InvalidError("一次最多排 " + strconv.Itoa(maxSteps) + " 步（收到 " + strconv.Itoa(len(steps)) + " 步）")
	}
	// 每个会话都要真的在（且 Blocks 不许是负的）—— 一次查清楚，别等跑到某一步才发现。
	for index, step := range steps {
		if strings.TrimSpace(step.SessionID) == "" {
			return Queue{}, InvalidError("第 " + strconv.Itoa(index+1) + " 步没给 session_id")
		}
		if step.Blocks < 0 {
			return Queue{}, InvalidError("第 " + strconv.Itoa(index+1) + " 步的 blocks 不能是负数")
		}
		session, err := s.Store.GetSession(step.SessionID)
		if err != nil {
			return Queue{}, err
		}
		// ⚠ `GetSession` 找不到时回 `(nil, nil)`（不是错误）—— 只判 err 会让"会话不存在"
		//   一路排进队列，等跑到那一步才炸。
		if session == nil {
			return Queue{}, InvalidError("第 " + strconv.Itoa(index+1) + " 步的会话不存在：" + step.SessionID)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &entry{
		ID: uuid.Must(uuid.NewV7()).String(), Kind: kind, Title: title,
		Steps: steps, State: task.StateRunning, StartedAt: time.Now().UnixMilli(),
		ctx: ctx, cancel: cancel,
	}
	s.mu.Lock()
	s.queues[run.ID] = run
	s.order = append(s.order, run.ID)
	out := run.view()
	s.mu.Unlock()
	go s.run(run)
	return out, nil
}

// run：一步一步跑，直到跑完 / 失败 / 被取消。**严格串行**。
func (s *Service) run(q *entry) {
	// 兜底：panic / 意外退出也要留个结局（与 `task.Guard.Interrupted` 同一个道理）。
	defer func() {
		if recovered := recover(); recovered != nil {
			s.finish(q, task.StateInterrupted, fmt.Sprint(recovered))
		}
	}()
	for {
		s.mu.Lock()
		if q.State != task.StateRunning {
			s.mu.Unlock()
			return // 被取消了（Cancel 已经把状态与时间写好了）
		}
		if q.Index >= len(q.Steps) {
			s.mu.Unlock()
			s.finish(q, task.StateSucceeded, "")
			return
		}
		step := q.Steps[q.Index]
		index := q.Index
		s.mu.Unlock()

		err := s.runStep(q, step, index)
		if err != nil {
			s.mu.Lock()
			canceled := q.State != task.StateRunning
			s.mu.Unlock()
			if canceled {
				return // 是取消掐断的：状态已经由 Cancel 写好了，别改成"失败"
			}
			s.finish(q, task.StateFailed, err.Error())
			return
		}
		s.mu.Lock()
		q.Index++
		s.mu.Unlock()
	}
}

// runStep：跑一步 —— 先挂一枚**原子任务**的号（面板上那一条），跑完再定它的生死。
//
// 于是面板上永远看得到"队列在跑，当前这一步是哪个会话的压缩"，而不用去猜。
func (s *Service) runStep(q *entry, step Step, index int) error {
	session, err := s.Store.GetSession(step.SessionID)
	if err != nil {
		return err
	}
	if session == nil { // 排的时候还在，跑的时候被删了 —— 这一步就算失败
		return InvalidError("会话已经不在了：" + step.SessionID)
	}
	title := fmt.Sprintf("队列压缩 · %s · 第 %d/%d 步", titleOf(session), index+1, len(q.Steps))
	guard := s.Registry.Begin(task.KindCompact, &step.SessionID, title)
	defer guard.Interrupted()
	if err := s.Runner.RunStep(q.ctx, step); err != nil {
		guard.Fail(err.Error())
		return err
	}
	guard.Succeed()
	return nil
}

// finish：写结局（**只有第一次算数** —— 已经结束的队列不会被后来的失败改写）。
func (s *Service) finish(q *entry, state task.State, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.FinishedAt != nil {
		return
	}
	now := time.Now().UnixMilli()
	q.FinishedAt = &now
	q.State = state
	q.Error = reason
	if q.cancel != nil {
		q.cancel() // 把 ctx 收掉，别留下悬挂的 context
	}
	s.trim() // 已结束的只留最近 keepQueues 条（跑着的永远在）
}

// Cancel：**随时取消**。在飞的那一发会被 ctx 一起掐断（压缩要整段拿到才落库 ⇒ 不会留半条）。
//
// 幂等：已经结束的队列再按一次，原样回它现在的样子（不报错、不改状态）。
func (s *Service) Cancel(id string) (Queue, error) {
	s.mu.Lock()
	q, ok := s.queues[id]
	if !ok {
		s.mu.Unlock()
		return Queue{}, ErrNotFound
	}
	if q.FinishedAt == nil {
		now := time.Now().UnixMilli()
		q.FinishedAt = &now
		q.State = task.StateCanceled
		if q.cancel != nil {
			q.cancel()
		}
	}
	out := q.view()
	s.mu.Unlock()
	return out, nil
}

// List：面板要的那一屏（跑着的在最前，其余按开始时间倒序）。
func (s *Service) List() []Queue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Queue, 0, len(s.queues))
	for _, id := range s.order {
		if q, ok := s.queues[id]; ok {
			out = append(out, q.view())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, rj := out[i].Running(), out[j].Running()
		if li != rj {
			return li // 跑着的靠前
		}
		return out[i].StartedAt > out[j].StartedAt
	})
	return out
}

// Running：还有几个队列在跑（面板的环 / 徽标看它）。
func (s *Service) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, q := range s.queues {
		if q.FinishedAt == nil {
			count++
		}
	}
	return count
}

// keepQueues：保留多少条已结束的队列（跑着的永远都在）。与 `task.keepFinished` 同一个口径。
const keepQueues = 20

// trim：扔掉最老的已结束队列。**调用方持锁**。
func (s *Service) trim() {
	if len(s.order) <= keepQueues {
		return
	}
	kept := make([]string, 0, len(s.order))
	finished := 0
	for _, id := range s.order {
		q, ok := s.queues[id]
		if !ok {
			continue
		}
		if q.FinishedAt == nil {
			kept = append(kept, id) // 跑着的永远留
			continue
		}
		finished++
		if finished <= keepQueues {
			kept = append(kept, id)
			continue
		}
		delete(s.queues, id)
	}
	s.order = kept
}

// titleOf：会话在面板上那一行的名字（空标题回"未命名"）—— 与别处同一个口径。
func titleOf(session *model.Session) string {
	if session == nil {
		return "未命名"
	}
	if strings.TrimSpace(session.Title) != "" {
		return session.Title
	}
	return "未命名"
}
