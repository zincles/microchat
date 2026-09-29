package task

import (
	"strings"
	"testing"
	"time"
)

// 挂号 ⇒ 面板上看得到；收尾 ⇒ 写结局，且**只生效一次**（重复 Finish 不覆盖）。
func TestGuardFinishIsOnce(t *testing.T) {
	registry := NewRegistry()
	conversation := "c1"
	guard := registry.Begin(KindTurn, &conversation, "生成 · 会话 c1")
	if registry.Running() != 1 {
		t.Fatalf("在跑数 = %d", registry.Running())
	}
	board := registry.Board()
	if len(board.Tasks) != 1 || board.Tasks[0].Kind != KindTurn || !board.Tasks[0].Running() {
		t.Fatalf("面板 = %+v", board.Tasks)
	}
	if board.Tasks[0].Title != "生成 · 会话 c1" || board.Tasks[0].Conversation == nil {
		t.Fatalf("那条记录 = %+v", board.Tasks[0])
	}
	guard.Finish("完成：42 字")
	if registry.Running() != 0 {
		t.Fatal("收尾后不该还在跑")
	}
	guard.Finish("覆盖？") // 重复调用只生效一次
	board = registry.Board()
	if board.Tasks[0].Outcome != "完成：42 字" || board.Tasks[0].FinishedAt == nil {
		t.Fatalf("重复 Finish 不该覆盖：%+v", board.Tasks[0])
	}
	// panic 路径也是 defer ⇒ 记成"中断"，绝不留僵尸条目
	func() {
		defer guard.Finish("中断：忘了收")
	}()
	if board.Tasks[0].Outcome == "中断：忘了收" {
		t.Fatal("已经收过一次的任务不该被改写")
	}
}

// 面板排序：正在跑的永远在最前；已结束的按开始时间倒序（最近的在上面）。
func TestBoardOrdering(t *testing.T) {
	registry := NewRegistry()
	first := registry.Begin(KindRefreshModels, nil, "刷新模型 · 渠道 a")
	first.Finish("完成：3 个模型")
	time.Sleep(2 * time.Millisecond)
	second := registry.Begin(KindTurn, nil, "生成 · 会话 b")
	board := registry.Board()
	if board.Running != 1 || len(board.Tasks) != 2 {
		t.Fatalf("面板 = %+v", board)
	}
	if !board.Tasks[0].Running() || board.Tasks[0].Kind != KindTurn {
		t.Fatalf("正在跑的该在最前：%+v", board.Tasks)
	}
	second.Finish("完成")
	board = registry.Board()
	if board.Tasks[0].Title != "生成 · 会话 b" {
		t.Fatalf("都结束之后，最近的在最上面：%+v", board.Tasks)
	}
	if board.Now <= 0 || board.Tasks[0].ElapsedMS(board.Now) < 0 {
		t.Fatal("耗时算得出来")
	}
}

// 只留最近 60 条已结束的；**正在跑的永远都在**。
func TestTrimKeepsRunning(t *testing.T) {
	registry := NewRegistry()
	alive := registry.Begin(KindCompact, nil, "压缩 · 一直在跑")
	for i := 0; i < keepFinished+10; i++ {
		guard := registry.Begin(KindRefreshModels, nil, "刷新")
		guard.Finish("完成")
	}
	board := registry.Board()
	if len(board.Tasks) != keepFinished+1 {
		t.Fatalf("该留 %d 条 + 1 条在跑，得到 %d", keepFinished, len(board.Tasks))
	}
	found := false
	for _, record := range board.Tasks {
		if record.ID == alive.id {
			found = true
		}
	}
	if !found {
		t.Fatal("正在跑的那条绝不能被裁掉")
	}
}

// 标签是给面板看的（中文，别在别处硬编码）。
func TestKindLabels(t *testing.T) {
	cases := map[Kind]string{KindTurn: "生成", KindCompact: "压缩", KindRefreshModels: "刷新模型"}
	for kind, want := range cases {
		if got := kind.Label(); got != want {
			t.Fatalf("%s 的标签 = %q，想要 %q", kind, got, want)
		}
	}
	if !strings.Contains(KindTurn.Label(), "生成") {
		t.Fatal("标签该是中文")
	}
}
