package queue

// 队列任务的三条规矩（用户 2026-11-01 点名的那三条）：
//  1. 严格串行 —— 上一步收尾了才开下一步；
//  2. 一步失败 ⇒ 整个队列结束（不跳过、不重试）；
//  3. 可随时取消 —— 在飞的那一发一起被掐断。
//
// 这里用**假 Runner**（不碰真上游）：排队这件事跟"哪一步怎么跑"无关，
// 真压缩那半程由 `compact` 自己的测试与 `server` 的接口测试管。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"microchat/internal/store"
	"microchat/internal/task"
)

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// fakeRunner：记下**每一步的开始/结束顺序**，按需失败或卡住。
type fakeRunner struct {
	mu     sync.Mutex
	order  []string      // "start:会话" / "done:会话"
	calls  int           // RunStep 被调了几次（**不是**日志条数：成功一次会写 2 行）
	failAt int           // 第几次调用失败（0 = 不失败）
	gate   chan struct{} // 非 nil ⇒ 卡到它被关掉（或 ctx 被撤）才返回
}

func (f *fakeRunner) RunStep(ctx context.Context, step Step) error {
	f.mu.Lock()
	f.order = append(f.order, "start:"+step.SessionID)
	f.calls++
	index := f.calls
	f.mu.Unlock()
	if f.gate != nil {
		// 卡住 —— 但 ctx 一撤就立刻返回（那正是"取消掐断在飞的那一发"）。
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.failAt != 0 && index == f.failAt {
		return errors.New("上游 429")
	}
	f.mu.Lock()
	f.order = append(f.order, "done:"+step.SessionID)
	f.mu.Unlock()
	return nil
}

func (f *fakeRunner) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

// waitState：等到队列落到某个结局（最长 2 秒）。
func waitState(t *testing.T, svc *Service, id string, want task.State) Queue {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, q := range svc.List() {
			if q.ID == id {
				if q.State == want {
					return q
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("队列 %s 没在 2 秒内落到 %q（现在：%+v）", id, want, svc.List())
	return Queue{}
}

// 严格串行 + 全部成功 ⇒ 状态是"成功"，Index == 总步数，且**一步跑完才开下一步**。
func TestQueueRunsStepsSerially(t *testing.T) {
	st := openTemp(t)
	first, err := st.CreateSession("dummy", "dummy", "", "甲")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateSession("dummy", "dummy", "", "乙")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	svc := New(st, task.NewRegistry())
	svc.Runner = runner

	created, err := svc.Enqueue(KindCompact, "两连压", []Step{
		{SessionID: first.ID, Blocks: 10},
		{SessionID: second.ID, Blocks: 10},
	})
	if err != nil {
		t.Fatalf("排队该成功：%v", err)
	}
	if created.Total != 2 || created.State != task.StateRunning {
		t.Fatalf("刚排好的队列 = %+v", created)
	}
	if created.Group != task.GroupLLM {
		t.Fatalf("压缩队列该归 LLM 生成那组，实际 %q", created.Group)
	}

	final := waitState(t, svc, created.ID, task.StateSucceeded)
	if final.Index != 2 || final.Error != "" {
		t.Fatalf("跑完该是 2/2 且没有错误，实际 %+v", final)
	}
	// 顺序：a 开始 → a 结束 → b 开始 → b 结束（**中间不许重叠**）
	want := []string{"start:" + first.ID, "done:" + first.ID, "start:" + second.ID, "done:" + second.ID}
	got := runner.snapshot()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("不是严格串行：\n该是 %v\n实际 %v", want, got)
	}
}

// 一步失败 ⇒ 整个队列停在那儿，**不再跑后面的**。
func TestQueueStopsOnFirstFailure(t *testing.T) {
	st := openTemp(t)
	a, err := st.CreateSession("dummy", "dummy", "", "甲")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateSession("dummy", "dummy", "", "乙")
	if err != nil {
		t.Fatal(err)
	}
	// failAt = 第几次 RunStep 调用失败（假的 Runner 每次调用先记一条 start）
	runner := &fakeRunner{failAt: 2}
	svc := New(st, task.NewRegistry())
	svc.Runner = runner

	created, err := svc.Enqueue(KindCompact, "", []Step{
		{SessionID: a.ID}, {SessionID: b.ID}, {SessionID: a.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitState(t, svc, created.ID, task.StateFailed)
	if !strings.Contains(final.Error, "429") {
		t.Fatalf("失败原因该原样带出来，实际 %q", final.Error)
	}
	if final.Index != 1 {
		t.Fatalf("第 2 步失败 ⇒ 完成的步数该是 1，实际 %d", final.Index)
	}
	// 只该有 2 次 start（第 1、2 步），第三步**一次都没开**
	starts := 0
	for _, entry := range runner.snapshot() {
		if strings.HasPrefix(entry, "start:") {
			starts++
		}
	}
	if starts != 2 {
		t.Fatalf("第 2 步失败之后不该再开第 3 步（该有 2 次 start），实际 %d 次：%v", starts, runner.snapshot())
	}
}

// 取消：在飞的那一发立刻返回，队列落到"取消"，后面的步骤不再开。
func TestQueueCancelAbortsInFlightStep(t *testing.T) {
	st := openTemp(t)
	a, err := st.CreateSession("dummy", "dummy", "", "甲")
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	runner := &fakeRunner{gate: gate}
	svc := New(st, task.NewRegistry())
	svc.Runner = runner

	created, err := svc.Enqueue(KindCompact, "", []Step{
		{SessionID: a.ID}, {SessionID: a.ID}, {SessionID: a.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 等第一步真的开始（Runner 已经卡在 gate 上）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(runner.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(runner.snapshot()) == 0 {
		t.Fatal("第一步没开始")
	}

	canceled, err := svc.Cancel(created.ID)
	if err != nil {
		t.Fatalf("取消该成功：%v", err)
	}
	if canceled.State != task.StateCanceled {
		t.Fatalf("取消后状态该是「取消」，实际 %q", canceled.State)
	}
	final := waitState(t, svc, created.ID, task.StateCanceled)
	if final.Index != 0 {
		t.Fatalf("在飞那一步被掐断 ⇒ 完成的步数该是 0，实际 %d", final.Index)
	}
	// 关键：ctx 撤了，卡住的 Runner 立刻返回 ⇒ 后面两步一次都没开
	time.Sleep(80 * time.Millisecond)
	starts := 0
	for _, entry := range runner.snapshot() {
		if strings.HasPrefix(entry, "start:") {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("取消之后不该再开新步（只该有 1 次 start），实际 %d 次：%v", starts, runner.snapshot())
	}
}

// 取消是幂等的：已经结束的队列再按一次，原样返回、不报错、不改结局。
func TestCancelIsIdempotent(t *testing.T) {
	st := openTemp(t)
	a, err := st.CreateSession("dummy", "dummy", "", "甲")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, task.NewRegistry())
	svc.Runner = &fakeRunner{}
	created, err := svc.Enqueue(KindCompact, "", []Step{{SessionID: a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, svc, created.ID, task.StateSucceeded)

	again, err := svc.Cancel(created.ID)
	if err != nil {
		t.Fatalf("对已结束的队列取消不该报错：%v", err)
	}
	if again.State != task.StateSucceeded {
		t.Fatalf("已成功的队列不该被改成「取消」，实际 %q", again.State)
	}
	if _, err := svc.Cancel("没有这个"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的队列该报 ErrNotFound，实际 %v", err)
	}
}

// 入参校验：空队列 / 会话不存在 / 不认识的 kind —— 全在**开跑之前**被拒。
func TestEnqueueValidatesBeforeRunning(t *testing.T) {
	st := openTemp(t)
	a, err := st.CreateSession("dummy", "dummy", "", "甲")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	svc := New(st, task.NewRegistry())
	svc.Runner = runner

	if _, err := svc.Enqueue(KindCompact, "", nil); err == nil {
		t.Fatal("空队列该被拒")
	}
	if _, err := svc.Enqueue("turn", "", []Step{{SessionID: a.ID}}); err == nil {
		t.Fatal("不认识的 kind 该被拒")
	}
	if _, err := svc.Enqueue(KindCompact, "", []Step{{SessionID: "没有这个"}}); err == nil {
		t.Fatal("会话不存在该被拒")
	}
	if _, err := svc.Enqueue(KindCompact, "", []Step{{SessionID: a.ID, Blocks: -1}}); err == nil {
		t.Fatal("负的 blocks 该被拒")
	}
	// 全被拒 ⇒ 一步都没跑
	if len(runner.snapshot()) != 0 {
		t.Fatalf("校验失败不该开跑，实际跑了：%v", runner.snapshot())
	}
	if len(svc.List()) != 0 {
		t.Fatalf("校验失败不该留下队列记录，实际 %d 条", len(svc.List()))
	}
}
