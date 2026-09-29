package task

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 挂号 ⇒ 面板上看得到；收尾 ⇒ 写结局，且**只生效一次**（后到的收尾不覆盖先到的）。
func TestGuardFinishIsOnce(t *testing.T) {
	registry := NewRegistry()
	session := "c1"
	guard := registry.Begin(KindTurn, &session, "生成 · 会话 c1")
	if registry.Running() != 1 {
		t.Fatalf("在跑数 = %d", registry.Running())
	}
	board := registry.Board()
	if len(board.Tasks) != 1 || board.Tasks[0].Kind != KindTurn || !board.Tasks[0].Running() {
		t.Fatalf("面板 = %+v", board.Tasks)
	}
	if board.Tasks[0].Title != "生成 · 会话 c1" || board.Tasks[0].Session == nil {
		t.Fatalf("那条记录 = %+v", board.Tasks[0])
	}
	if board.Tasks[0].Outcome != StateRunning {
		t.Fatalf("挂号时的状态该是「%s」：%+v", StateRunning, board.Tasks[0])
	}
	guard.Succeed()
	if registry.Running() != 0 {
		t.Fatal("收尾后不该还在跑")
	}
	guard.Fail("后到的不算") // 已经收过尾了：只生效一次
	board = registry.Board()
	if board.Tasks[0].Outcome != StateSucceeded || board.Tasks[0].FinishedAt == nil {
		t.Fatalf("重复收尾不该覆盖：%+v", board.Tasks[0])
	}
	// 兜底那条路：忘了收 / panic 时 `defer guard.Interrupted()` 一定会跑到
	func() {
		defer guard.Interrupted()
	}()
	board = registry.Board()
	if board.Tasks[0].Outcome != StateSucceeded {
		t.Fatalf("已经收过尾的任务不该被兜底改写：%+v", board.Tasks[0])
	}
}

// 四个状态各有各的字段：**失败原因单开一个字段**，状态字段里只放那五个词之一。
func TestFailureKeepsTheReasonInItsOwnField(t *testing.T) {
	registry := NewRegistry()
	session := "c1"
	registry.Begin(KindCompact, &session, "压缩 · 会话 c1").Fail("上游 429（额度）")
	record := registry.Board().Tasks[0]
	if record.Outcome != StateFailed || record.Error != "上游 429（额度）" {
		t.Fatalf("失败 = %+v", record)
	}
	if strings.Contains(string(record.Outcome), "429") {
		t.Fatal("原因不许塞进状态字段（客户端就只能匹配文案了）")
	}
	// 中断（没人收尾）与失败**不是一回事**：混在一起，这类 bug 就永远看不见了
	registry.Begin(KindRefreshModels, nil, "刷新模型").Interrupted()
	found := false
	for _, record := range registry.Board().Tasks {
		if record.Outcome == StateInterrupted {
			found = true
		}
	}
	if !found {
		t.Fatal("没人收尾的活该记成「中断」")
	}
}

// TaskID 是 **UUIDv7**（自增小整数重启后计数归零 ⇒ 日志里"task 1"会撞车）；不进库。
func TestTaskIDsAreUUIDv7(t *testing.T) {
	registry := NewRegistry()
	first := registry.Begin(KindRefreshModels, nil, "刷新 a")
	second := registry.Begin(KindRefreshModels, nil, "刷新 b")
	if first.id == second.id {
		t.Fatal("两次挂号的 id 不该相同")
	}
	parsed, err := uuid.Parse(first.id)
	if err != nil {
		t.Fatalf("id 该是 UUID：%q（%v）", first.id, err)
	}
	if version := parsed.Version(); version != 7 {
		t.Fatalf("id 该是 UUIDv7，得到 v%d：%q", version, first.id)
	}
}

// **会调模型的作业必须带会话 id** ⇒ `Begin()` 当场炸（不是等发出去被上游拒）。
func TestModelCallingTasksMustCarryASession(t *testing.T) {
	for _, kind := range []Kind{KindTurn, KindCompact} {
		registry := NewRegistry()
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s 缺会话 id 该当场炸", kind)
				}
			}()
			registry.Begin(kind, nil, "缺会话")
		}()
		empty := ""
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s 的会话 id 是空串也该炸", kind)
				}
			}()
			registry.Begin(kind, &empty, "空会话 id")
		}()
	}
	// 拉模型列表与会话无关 ⇒ 不给会话 id 完全正常
	registry := NewRegistry()
	registry.Begin(KindRefreshModels, nil, "刷新模型").Succeed()
	if registry.Running() != 0 {
		t.Fatal("不该留下在跑的条目")
	}
}

// 面板排序：正在跑的永远在最前；已结束的按开始时间倒序（最近的在上面）。
func TestBoardOrdering(t *testing.T) {
	registry := NewRegistry()
	first := registry.Begin(KindRefreshModels, nil, "刷新模型 · 渠道 a")
	first.Succeed()
	time.Sleep(2 * time.Millisecond)
	session := "c1"
	second := registry.Begin(KindTurn, &session, "生成 · 会话 b")
	board := registry.Board()
	if board.Running != 1 || len(board.Tasks) != 2 {
		t.Fatalf("面板 = %+v", board)
	}
	if !board.Tasks[0].Running() || board.Tasks[0].Kind != KindTurn {
		t.Fatalf("正在跑的该在最前：%+v", board.Tasks)
	}
	second.Succeed()
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
	session := "c1"
	alive := registry.Begin(KindCompact, &session, "压缩 · 一直在跑")
	for range keepFinished + 10 {
		guard := registry.Begin(KindRefreshModels, nil, "刷新")
		guard.Succeed()
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
