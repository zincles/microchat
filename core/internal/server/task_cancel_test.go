package server

// ── 按停任务 + **原子性**（2026-11-01 用户定）──────────────────────────
//
// 用户口径：「任务菜单里，我希望有办法取消任务。而且要考虑原子性。如果任务被取消了，
// 那么就相当于没做过。例如，A B C .... E 序列。我们提交了一个压缩任务：AB。
// 此时如果失败或者被取消，我们再次提交压缩任务，仍然应该压缩 AB 才对。」
//
// 这条测试就是那句话的可执行版。它钉的**不是**"取消之后状态是取消"（那只是记账），
// 而是**库里一个字节都没动**，于是重提一次压的还是同一段。
//
// 机制（不是事后回滚）：压缩在 `prepare` 里把区间算好，然后调上游，**最后**才由
// `store.RecordSummary` 在一个事务里落库。取消发生在调上游那一步 ⇒ `call` 根本没走到落库
// ⇒ 天然"没做过"。所以这里要造一个"卡住的上游"，让取消真的落在那个窗口里。

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"microchat/internal/model"
	"microchat/internal/task"
)

// hungUpstream：第一发**挂住**，之后正常回。
//
// 为什么第一发要挂：不挂的话 dummy 渠道瞬间就压完了，取消落在"已经落库之后"——
// 那测的就不是原子性了（而且会得出错误的结论：以为取消没生效）。
//
// ⚠ 一个踩出来的坑：**别指望在 handler 里等 `r.Context().Done()` 来证明"连接被掐断"**。
// 实测（Go 1.26 + httptest）：客户端 ctx 一取消，客户端**立刻**返回 `context canceled`，
// 但 server 那边那个 handler 的 `r.Context()` 并不会及时触发 —— 于是 handler 一直挂着，
// `httptest.Server.Close()` 又一直等这条 active 连接 ⇒ 整条测试挂死（真踩过）。
//
// 所以这里的 handler 由测试**自己放行**（`release`），而"取消真的生效了"改由**客户端侧**证明：
// 压缩状态翻成 `canceled` 只可能来自 `ctx.Err() == context.Canceled`（见 `compact.finish`），
// 加上"上游被打了两发"，两条一起就说明了"第一发确实发出去了、也确实被掐断了"。
type hungUpstream struct {
	server   *httptest.Server
	release  chan struct{}
	attempts int32
}

func newHungUpstream(t *testing.T, body string) *hungUpstream {
	t.Helper()
	h := &hungUpstream{release: make(chan struct{})}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&h.attempts, 1) == 1 {
			<-h.release // 挂到测试收尾放行
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(func() {
		close(h.release) // 先放行，再 Close（顺序反了就是上面那个死锁）
		h.server.Close()
	})
	return h
}

// attemptCount：上游被打了几发。
func (h *hungUpstream) attemptCount() int32 { return atomic.LoadInt32(&h.attempts) }

// runningTask：等面板上出现一枚"能按停"的、指定种类的任务。
func runningTask(t *testing.T, box *dummySandbox, kind task.Kind) task.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, record := range box.boardOf(t).Tasks {
			if record.Kind == kind && record.Running() {
				return record
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到一枚在跑的 %s 任务", kind)
	return task.Record{}
}

// summariesOf：这条会话此刻的摘要（按区间）。
func summariesOf(t *testing.T, box *dummySandbox) []model.Summary {
	t.Helper()
	rows, err := box.store.ListSummaries(box.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// 取消一次压缩 ⇒ 库里**一个字节都没动** ⇒ 重提一次压的还是同一段。
func TestCanceledCompactWritesNothingAndRetryCoversSameSpan(t *testing.T) {
	upstream := newHungUpstream(t, `{"choices":[{"message":{"content":"第一次成功才该出现的摘要"}}]}`)

	box := newDummySandbox(t)
	// 三块对话 = 消息 1..6（每块 user+assistant）。
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	// 切到一个会挂住的渠道（照 `TestCompactRouteConflictsWhileRunning` 那条路建渠道）。
	if recorder := call(box.server, "POST", "/api/v1/providers",
		`{"id":"hung","vendor":"openai-compat","base_url":"`+upstream.server.URL+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("建渠道该 201：%d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+box.sessionID,
		`{"provider":"hung","model":"m"}`); recorder.Code != http.StatusOK {
		t.Fatalf("切渠道该 200：%d %s", recorder.Code, recorder.Body.String())
	}

	// ① 提交压缩：只压**第一块**（= 用户说的 AB）。
	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// ② 等它真的挂在上游上（那枚任务出现了 ⇒ 它已经到了"调上游"这一步）。
	record := runningTask(t, box, task.KindCompact)
	if !record.Cancelable {
		t.Fatal("压缩任务该是**能按停**的（面板据此决定画不画按钮）")
	}

	// ③ 按停它。
	cancel := call(box.server, "POST", "/api/v1/tasks/"+record.ID+"/cancel", "")
	if cancel.Code != http.StatusOK {
		t.Fatalf("按停该 200，得到 %d：%s", cancel.Code, cancel.Body.String())
	}
	if got := decodeBody[map[string]bool](t, cancel); !got["canceled"] {
		t.Fatalf("该回 canceled:true，得到 %v", got)
	}

	// ④ 等压缩状态翻 —— 而且必须是 **canceled**，不是 error
	//    （用户按停是正常操作，界面不该弹一条红色失败）。
	status := waitCompactState(t, box, "canceled")

	// ⑤ **原子性的正面证据**：库里一条摘要都没有，消息上的指针也一个都没动。
	if rows := summariesOf(t, box); len(rows) != 0 {
		t.Fatalf("被取消的压缩**不该留下任何摘要**，却留下了 %d 条：%+v", len(rows), rows)
	}
	for _, message := range mustMessages(t, box) {
		if message.SummaryID != nil && *message.SummaryID != "" {
			t.Fatalf("消息 %s 的 summary_id 被写上了（压缩没落库，它不该动）：%q",
				message.ID, *message.SummaryID)
		}
	}
	if status.SummaryID != "" {
		t.Fatalf("取消的压缩不该报出一个 summary_id：%q", status.SummaryID)
	}

	// ⑥ **重提一次，压的还是 AB**（用户那句话的正题）。
	//    这一发上游会正常回（`hungUpstream` 只有第一发挂）。
	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("重提该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	done := box.waitCompact(t)

	// ⑦ 压的正是**最开头那一段**（第 1–2 条 = 第一块 = AB）。
	//
	// ⚠ 这条断言才是整件事的要害：如果上一发留下了任何痕迹（哪怕只是一个游标），
	// 这一发就会从第 3 条开始压 —— 那 AB 就**永远压不上了**（而用户会看到"重提之后跳过了开头"）。
	if done.FromIdx != 1 || done.ToIdx != 2 {
		t.Fatalf("重提该压第 1–2 条（AB），实际 %d–%d", done.FromIdx, done.ToIdx)
	}

	// ⑧ 这一发才真的落库。
	rows := summariesOf(t, box)
	if len(rows) != 1 {
		t.Fatalf("该只有一条摘要，得到 %d 条", len(rows))
	}
	if rows[0].BeginMessageID == nil || *rows[0].BeginMessageID != mustMessages(t, box)[0].ID {
		t.Fatal("摘要该从第一条消息起（AB），而不是往后跳")
	}

	// ⑨ 上游一共被打了两发：被掐断的那一发 + 成功的那一发（证明第一发确实发出去了，
	//    否则"取消"什么都没做，上面那些断言就是空转）。
	if got := upstream.attemptCount(); got != 2 {
		t.Fatalf("上游该被打两发（一发被掐断 + 一发成功），实际 %d 发", got)
	}
}

// 按停一条**不存在**的任务：200 + `canceled:false`（不是 404）。
//
// 为什么不是 404：面板轮询里那些 id 随时可能"刚好跑完"，回 404 会让界面在那一帧
// 弹一条假错误。三种"没什么可停的"（没这个 id / 已收尾 / 压根不能停）对调用方是同一件事。
func TestCancelUnknownTaskIsNotAnError(t *testing.T) {
	box := newDummySandbox(t)
	recorder := call(box.server, "POST", "/api/v1/tasks/does-not-exist/cancel", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if got := decodeBody[map[string]bool](t, recorder); got["canceled"] {
		t.Fatalf("不存在的任务该回 canceled:false，得到 %v", got)
	}
}

// 反复按同一个任务：第二下回 false（已经叫过了），不该再叫一遍。
func TestCancelTaskTwice(t *testing.T) {
	upstream := newHungUpstream(t, `{"choices":[{"message":{"content":"摘要"}}]}`)
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	if recorder := call(box.server, "POST", "/api/v1/providers",
		`{"id":"hung","vendor":"openai-compat","base_url":"`+upstream.server.URL+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("建渠道该 201：%d", recorder.Code)
	}
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+box.sessionID,
		`{"provider":"hung","model":"m"}`); recorder.Code != http.StatusOK {
		t.Fatalf("切渠道该 200：%d", recorder.Code)
	}
	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202：%d", recorder.Code)
	}
	record := runningTask(t, box, task.KindCompact)

	first := decodeBody[map[string]bool](t, call(box.server, "POST", "/api/v1/tasks/"+record.ID+"/cancel", ""))
	if !first["canceled"] {
		t.Fatal("第一下该 true")
	}
	second := decodeBody[map[string]bool](t, call(box.server, "POST", "/api/v1/tasks/"+record.ID+"/cancel", ""))
	if second["canceled"] {
		t.Fatal("第二下该 false（已经叫过了，不该再叫一遍）")
	}
	waitCompactState(t, box, "canceled")
}

// —— 小工具 ——

// waitCompactState：等压缩翻到某一档（`canceled` 之类），并把那一份状态交回来。
func waitCompactState(t *testing.T, box *dummySandbox, state string) compactStateShape {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := box.compactStatus(t)
		if status != nil && status.State == state {
			return compactStateShape{FromIdx: status.FromIdx, ToIdx: status.ToIdx, SummaryID: derefString(status.SummaryID)}
		}
		if status != nil && status.State != "running" && status.State != state {
			t.Fatalf("压缩该翻到 %q，却翻到了 %q：%+v", state, status.State, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等不到压缩翻成 %q", state)
	return compactStateShape{}
}

// compactStateShape：测试里只关心这三格（`turn.CompactStatus` 的字段类型带指针，逐处取有点吵）。
type compactStateShape struct {
	FromIdx   int
	ToIdx     int
	SummaryID string
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// mustMessages：这条会话的全部消息。
func mustMessages(t *testing.T, box *dummySandbox) []model.Message {
	t.Helper()
	messages, err := box.store.ListMessages(box.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}
