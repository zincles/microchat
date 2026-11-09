package turn

import (
	"context"
	"testing"
	"time"
)

// 没登记过的会话就是 idle —— 后端一重启就该是这样，这是诚实的。
func TestUnknownSessionIsIdle(t *testing.T) {
	turns := NewRegistry()
	status := turns.Status("没见过的会话")
	if status.Phase != PhaseIdle || status.MessageID != nil || status.Chars != 0 {
		t.Fatalf("状态 = %+v", status)
	}
	if status.Phase.IsBusy() {
		t.Fatal("idle 不算在跑")
	}
}

// begin 是**唯一的并发闸门**：第二个挤进来要被拒（不排队）。
func TestBeginIsTheOnlyGate(t *testing.T) {
	turns := NewRegistry()
	token, err := turns.Begin("c1", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if status := turns.Status("c1"); status.Phase != PhasePending || status.MessageID == nil || *status.MessageID != "m1" {
		t.Fatalf("状态 = %+v", status)
	}
	if _, err := turns.Begin("c1", "m2"); err == nil {
		t.Fatal("已经在跑时再受理必须被拒")
	}
	// 换一个会话不受影响
	if _, err := turns.Begin("c2", "m9"); err != nil {
		t.Fatalf("别的会话不该被牵连：%v", err)
	}
	_ = token
}

// 增量进缓冲：pending 抬成 streaming；令牌对不上就丢（被 stop 顶掉的旧任务不许写进来）。
func TestAppendOnlyWithTheRightToken(t *testing.T) {
	turns := NewRegistry()
	token, _ := turns.Begin("c1", "m1")
	if !turns.AppendContent("c1", token, "你好") {
		t.Fatal("自己那一轮的增量该收下")
	}
	if turns.AppendContent("c1", token+1, "偷来的") {
		t.Fatal("令牌对不上的增量必须丢")
	}
	if !turns.AppendReasoning("c1", token, "想一下……") {
		t.Fatal("思考流单独一条缓冲")
	}
	status := turns.Status("c1")
	if status.Phase != PhaseStreaming {
		t.Fatalf("该被抬成 streaming：%+v", status)
	}
	if status.Chars != len("你好") || status.ThinkingChars != len("想一下……") {
		t.Fatalf("字数 = %d / 思考 %d", status.Chars, status.ThinkingChars)
	}
	if turns.ThinkingOf("c1") != "想一下……" {
		t.Fatal("思考正文要存着（落档用）")
	}
}

// 游标读：**读不消费**（两个客户端各拿各的），`from` 是第几个字符。
func TestCursorReadDoesNotConsume(t *testing.T) {
	turns := NewRegistry()
	token, _ := turns.Begin("c1", "m1")
	turns.AppendContent("c1", token, "0123456789")
	turns.AppendReasoning("c1", token, "abc")

	full := turns.StreamSince("c1", 0, 0)
	if full.Text != "0123456789" || full.Thinking != "abc" || full.Next != 10 || full.ThinkNext != 3 {
		t.Fatalf("全量 = %+v", full)
	}
	if full.Done {
		t.Fatal("还在跑，done 该是 false")
	}
	again := turns.StreamSince("c1", 0, 0)
	if again.Text != full.Text {
		t.Fatal("读第二次该拿到一样的东西（游标读不消费）")
	}
	tail := turns.StreamSince("c1", 7, 2)
	if tail.Text != "789" || tail.Thinking != "c" {
		t.Fatalf("增量 = %+v", tail)
	}
	// 推过头了：拿空串，游标仍指向末尾
	over := turns.StreamSince("c1", 999, 999)
	if over.Text != "" || over.Next != 10 {
		t.Fatalf("越界 = %+v", over)
	}
}

// 收尾：成功回 idle；失败进 error 并记住上一轮的耗时。
func TestFinishKeepsElapsedForError(t *testing.T) {
	turns := NewRegistry()
	token, _ := turns.Begin("c1", "m1")
	time.Sleep(2 * time.Millisecond)
	turns.Finish("c1", token, "上游 500")
	status := turns.Status("c1")
	if status.Phase != PhaseError || status.Error != "上游 500" {
		t.Fatalf("状态 = %+v", status)
	}
	if status.ElapsedMS <= 0 {
		t.Fatal("error 态要回报上一轮的总耗时")
	}
	// 令牌对不上的 Finish 不许改状态
	turns.Finish("c1", token+1, "别人的失败")
	if turns.Status("c1").Error != "上游 500" {
		t.Fatal("旧令牌不该改状态")
	}
}

// 令牌**绝不重复**：被停掉的旧任务回来时不许"撞上"新一轮的令牌。
//
// 踩过的形状：令牌若从"这个会话上一次的 +1"推，`Stop` 摘掉条目后就又从 1 重新开始 ——
// 而旧任务这时正好回来 ⇒ 令牌对上了 ⇒ 它会把自己的结果写进**新一轮**的账上。
func TestTokensNeverRepeatAcrossTurns(t *testing.T) {
	turns := NewRegistry()
	stale, _ := turns.Begin("c1", "m1")
	turns.Stop("c1")
	fresh, _ := turns.Begin("c1", "m2")
	if stale == fresh {
		t.Fatalf("新旧令牌撞了：%d", stale)
	}
	if turns.IsMine("c1", stale) {
		t.Fatal("旧令牌必须失效（否则旧任务会写进新一轮）")
	}
	if !turns.IsMine("c1", fresh) {
		t.Fatal("新一轮自己的令牌该有效")
	}
	// `Finish` / 增量也要照同一把尺子挡住过期的那个
	if turns.Finish("c1", stale, "别人的失败"); turns.Status("c1").Error != "" {
		t.Fatal("旧令牌不许改状态")
	}
	if turns.AppendContent("c1", stale, "偷来的字") {
		t.Fatal("旧令牌不许写进缓冲区")
	}
}

// stop：**幂等**（没在跑也回 false）、摘登记、abort 后台任务、清缓冲。
func TestStopIsIdempotent(t *testing.T) {
	turns := NewRegistry()
	if turns.Stop("c1") {
		t.Fatal("没在跑 ⇒ false")
	}
	token, _ := turns.Begin("c1", "m1")
	cancelled := false
	turns.Attach("c1", token, func() { cancelled = true })
	turns.AppendContent("c1", token, "半句话")
	if !turns.Stop("c1") {
		t.Fatal("在跑 ⇒ true")
	}
	if !cancelled {
		t.Fatal("该 abort 后台任务（别让上游白跑完）")
	}
	if status := turns.Status("c1"); status.Phase != PhaseIdle || status.MessageID != nil || status.Chars != 0 {
		t.Fatalf("停完该干净：%+v", status)
	}
	if turns.IsMine("c1", token) {
		t.Fatal("被停掉的旧令牌必须失效（不然旧任务回来会写库）")
	}
	if turns.Stop("c1") {
		t.Fatal("再停一次 ⇒ false（幂等）")
	}
	_ = context.Background()
}

// 压缩状态搭同一趟车回报（与轮次无关）。
func TestCompactStatusRidesAlong(t *testing.T) {
	turns := NewRegistry()
	if _, err := turns.BeginCompact("c1", 10, 1000); err != nil {
		t.Fatal(err)
	}
	status := turns.Status("c1")
	if status.Compact == nil || !status.Compact.IsRunning() || status.Compact.Blocks != 10 {
		t.Fatalf("压缩状态 = %+v", status.Compact)
	}
	if _, err := turns.BeginCompact("c1", 5, 2000); err == nil {
		t.Fatal("同一会话同时只允许一个压缩")
	}
	turns.FinishCompact("c1", "done", "sum-1", "", 8, 1, 4, false)
	status = turns.Status("c1")
	if status.Compact.State != "done" || status.Compact.Compacted != 8 || *status.Compact.SummaryID != "sum-1" {
		t.Fatalf("收尾后 = %+v", status.Compact)
	}
	if status.Compact.FromIdx != 1 || status.Compact.ToIdx != 4 || status.Compact.Merged {
		t.Fatalf("收尾该带区间与合并位：%+v", status.Compact)
	}
}

// 挂取消函数时那一趟**已经被停掉了** ⇒ 当场取消它。
//
// 场景（2026-11-01）：任务的 `Begin` 在受理那一刻就发生了，而取消函数是**后台 goroutine**
// 起来之后才 `Attach` 上去的。两次"按停"（`Stop` / `StopReroll`）都会把整条登记摘掉 ——
// 如果摘登记正好落在 `Begin` 与 `Attach` 之间，`Attach` 就找不到条目。
//
// 从前它什么都不做 ⇒ 那个 ctx 永远没人取消 ⇒ 上游请求白跑到底（结果会被 `IsMine` 丢掉，
// 所以**不会**写错库，但 token 已经烧了，而用户以为自己按住了）。
func TestAttachAfterStopCancelsImmediately(t *testing.T) {
	registry := NewRegistry()
	cancelled := false
	// 这个会话压根没有登记（= 已经被摘掉了），令牌自然对不上。
	registry.Attach("session-gone", 42, func() { cancelled = true })
	if !cancelled {
		t.Fatal("已经停掉的那一趟，挂 ctx 时就该当场取消 —— 否则上游会白跑完")
	}
}

// 正常那条路不能被误伤：登记还在、令牌对得上 ⇒ 只挂上去，**不许**顺手取消。
func TestAttachOnLiveTurnDoesNotCancel(t *testing.T) {
	registry := NewRegistry()
	token, err := registry.Begin("session-live", "message-1")
	if err != nil {
		t.Fatalf("登记该成功：%v", err)
	}
	cancelled := false
	registry.Attach("session-live", token, func() { cancelled = true })
	if cancelled {
		t.Fatal("正常挂载不该取消 —— 那会把刚受理的一轮当场掐掉")
	}
	if !registry.IsMine("session-live", token) {
		t.Fatal("挂上之后这一趟还是自己的")
	}
}

func TestToolStatusLifecycle(t *testing.T) {
	registry := NewRegistry()
	token, err := registry.Begin("s", "m")
	if err != nil {
		t.Fatal(err)
	}
	if !registry.SetTool("s", token, "web_search", "searching") {
		t.Fatal("SetTool 该收下当前令牌")
	}
	status := registry.Status("s")
	if status.Tool == nil || status.Tool.Kind != "web_search" || status.Tool.State != "searching" {
		t.Fatalf("status.tool = %+v", status.Tool)
	}
	if !registry.AppendContent("s", token, "好了") {
		t.Fatal("AppendContent 该收下当前令牌")
	}
	if got := registry.Status("s").Tool; got != nil {
		t.Fatalf("正文一到工具状态就该清掉：%+v", got)
	}
}

func TestAddSourcesDedupesAndSurvivesText(t *testing.T) {
	registry := NewRegistry()
	token, _ := registry.Begin("s", "m")
	if !registry.AddSources("s", token, []Source{
		{URL: "https://a.example/1"}, {URL: "https://a.example/1"}, {URL: "https://b.example/2"},
	}) {
		t.Fatal("AddSources 该收下当前令牌")
	}
	if !registry.AppendContent("s", token, "正文") {
		t.Fatal("AppendContent 该收下当前令牌")
	}
	status := registry.Status("s")
	if len(status.Sources) != 2 {
		t.Fatalf("sources = %+v", status.Sources)
	}
	registry.Finish("s", token, "")
	if got := registry.Status("s").Sources; len(got) != 0 {
		t.Fatalf("收尾后来源该清掉：%+v", got)
	}
}
