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
