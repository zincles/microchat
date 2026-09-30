package chat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/title"
	"microchat/internal/turn"
)

// harness：一条会话 + 一份 providers.json + 三个登记表（真的走 Accept / run 这条路）。
type harness struct {
	service   *Service
	store     *store.Store
	turns     *turn.Registry
	tasks     *task.Registry
	session   model.Session
	configDir string
}

func newHarness(t *testing.T, providersConfig config.ProvidersConfig, sessionProvider, sessionModel string) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := config.SaveJSON(dir+"/providers.json", providersConfig); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session, err := st.CreateSession(sessionProvider, sessionModel, "")
	if err != nil {
		t.Fatal(err)
	}
	turns, tasks := turn.NewRegistry(), task.NewRegistry()
	paths := config.Paths{ConfigDir: dir, DataDir: dir}
	return &harness{
		service: New(st, paths, turns, tasks), store: st, turns: turns, tasks: tasks,
		session: session, configDir: dir,
	}
}

func dummyProviders() config.ProvidersConfig {
	return config.ProvidersConfig{Providers: []config.Provider{{ID: "dummy", Kind: "dummy"}}}
}

// waitPhase：等到这一轮进到某一档（生成在后台跑，测试得等）。
func (h *harness) waitPhase(t *testing.T, phases ...turn.Phase) turn.Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := h.turns.Status(h.session.ID)
		for _, phase := range phases {
			if status.Phase == phase {
				return status
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到这些状态：%v（现在是 %+v）", phases, h.turns.Status(h.session.ID))
	return turn.Status{}
}

// 帮手：等这一轮结束、把消息取回来。
func (h *harness) settle(t *testing.T) []model.Message {
	t.Helper()
	h.waitPhase(t, turn.PhaseIdle, turn.PhaseError)
	messages, err := h.store.ListMessages(h.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

// turnTask：面板上最近的那条生成任务（等它收尾 —— 收尾是后台那一轮退出时才写的）。
//
// 注意：按停之后 turn 那一栏会**立刻**变 idle（登记表被摘了），但 goroutine 还没退出 ⇒
// "取消"是稍后才写上去的。所以这里要等，不能立刻断言。
func (h *harness) turnTask(t *testing.T) task.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, record := range h.tasks.Board().Tasks {
			if record.Kind == task.KindTurn && !record.Running() {
				return record
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("生成任务一直没收尾（僵尸条目）")
	return task.Record{}
}

// dummy 走完整条路：登记 → 落用户消息 → 首句标题 → 后台生成 → **整段拿到才落库**。
func TestAcceptRoundTripWithDummy(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	accepted, err := h.service.Accept(h.session, "开个头\n第二行不该进标题")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Backend != "dummy" || accepted.User == nil {
		t.Fatalf("回执 = %+v", accepted)
	}
	if accepted.Turn.Phase != model.PhasePending {
		t.Fatalf("受理那一刻该是 pending：%+v", accepted.Turn)
	}
	replyID := accepted.Turn.MessageID
	if replyID == nil || *replyID == "" {
		t.Fatal("受理时就该算好回复 id（那会儿还没进库）")
	}
	// 顺序就是 id：用户那句必须在回复前面
	if accepted.User.ID >= *replyID {
		t.Fatalf("用户消息该排在回复前面：%s / %s", accepted.User.ID, *replyID)
	}
	// 起标题**不在这儿**了：它是 title 能力的事（拿到回复之后由 title.Service 起）——
	// 这里只落用户那句（这套 harness 没接 title.Service，所以标题保持空着）
	reloaded, err := h.store.GetSession(h.session.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("取会话失败：%v", err)
	}
	if reloaded.Title != "" {
		t.Fatalf("起标题归 title 能力管，这里不该动：%q", reloaded.Title)
	}
	// 生成中：库里**只有**用户那一句（回复整段拿到才 INSERT）
	if messages, _ := h.store.ListMessages(h.session.ID); len(messages) != 1 {
		t.Fatalf("生成中库里该只有用户那句：%+v", messages)
	}
	messages := h.settle(t)
	if len(messages) != 2 {
		t.Fatalf("该有一条回复了：%+v", messages)
	}
	if messages[1].ID != *replyID || messages[1].Role != model.RoleAssistant {
		t.Fatalf("回复该用受理时那个 id：%+v（受理时给的是 %s）", messages[1], *replyID)
	}
	if messages[1].Content != dummyReply {
		t.Fatalf("正文 = %q", messages[1].Content)
	}
	if messages[1].DurationMS == nil || *messages[1].DurationMS < 0 {
		t.Fatalf("该记下这一轮花了多久：%+v", messages[1])
	}
	if record := h.turnTask(t); record.Outcome != task.StateSucceeded {
		t.Fatalf("task 该是「成功」：%+v", record)
	}
}

// 接上 title 能力之后：**拿到回复之后**自动起一次（标题从此有名字）——
// 这是"能力挂在谁身上"那条线：chat 只负责调用，起名的事全在 title 包里。
func TestTurnTriggersAutoTitle(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	h.service.Titles = title.New(h.store, config.Paths{ConfigDir: h.configDir, DataDir: h.configDir},
		h.tasks)
	if _, err := h.service.Accept(h.session, "帮我起个名字"); err != nil {
		t.Fatal(err)
	}
	h.settle(t)
	reloaded, err := h.store.GetSession(h.session.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("取会话失败：%v", err)
	}
	// dummy 渠道吐的那句假标题（真渠道时就是模型给的那句话）
	if reloaded.Title != "（测试用假标题）" {
		t.Fatalf("拿到回复之后该自动起一次标题：%q", reloaded.Title)
	}
}

// 同会话在跑时再受理 ⇒ **直接拒**（不排队），而且**一个字节都不多写**。
func TestSecondAcceptIsRefusedAndWritesNothing(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	if _, err := h.service.Accept(h.session, "先来一句"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Accept(h.session, "插队的一句"); !errors.Is(err, turn.ErrBusy) {
		t.Fatalf("该回 ErrBusy：%v", err)
	}
	if messages, _ := h.store.ListMessages(h.session.ID); len(messages) != 1 {
		t.Fatalf("被拒的那一次不许写库：%+v", messages)
	}
	h.service.Stop(h.session.ID)
	h.settle(t)
}

// 中途按停 ⇒ **库里半条都没有**；task 记「取消」（不是失败：用户按停是正常操作）。
func TestStopLeavesNoHalfMessage(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	accepted, err := h.service.Accept(h.session, "这句会被打断")
	if err != nil {
		t.Fatal(err)
	}
	status := h.waitPhase(t, turn.PhaseStreaming)
	if status.Chars == 0 {
		t.Fatal("该已经开始吐字了（不然测的不是「打断生成中」）")
	}
	if !h.service.Stop(h.session.ID) {
		t.Fatal("在跑 ⇒ stop 该回 true")
	}
	if h.service.Stop(h.session.ID) {
		t.Fatal("幂等：没在跑 ⇒ false")
	}
	// 后台那一轮发现令牌对不上，直接把结果丢掉
	messages := h.settle(t)
	if len(messages) != 1 || messages[0].ID != accepted.User.ID {
		t.Fatalf("被打断的那一轮不许留下半条：%+v", messages)
	}
	if record := h.turnTask(t); record.Outcome != task.StateCanceled || record.Error != "" {
		t.Fatalf("按停该记成「取消」且没有失败原因：%+v", record)
	}
	// 停完还能接着发（登记表摘干净了，没有僵尸在跑）
	if _, err := h.service.Accept(h.session, "接着聊"); err != nil {
		t.Fatalf("停完之后该能接着发：%v", err)
	}
	if messages := h.settle(t); len(messages) != 3 {
		t.Fatalf("该多出用户那句与回复：%+v", messages)
	}
}

// 上游失败 ⇒ 回复**不落库**、turn 进 error 且**原因就在那儿**、task 的原因**单开字段**。
func TestFailureKeepsTheReasonAndWritesNothing(t *testing.T) {
	h := newHarness(t, deadProviders(), "dead", "some-model")
	if _, err := h.service.Accept(h.session, "这一轮一定失败"); err != nil {
		t.Fatal(err)
	}
	status := h.waitPhase(t, turn.PhaseError)
	if !strings.Contains(status.Error, "1") {
		t.Fatalf("失败原因该报出来（连不上哪个地址）：%q", status.Error)
	}
	if messages, _ := h.store.ListMessages(h.session.ID); len(messages) != 1 {
		t.Fatalf("失败的那一轮不许留下半条：%+v", messages)
	}
	record := h.turnTask(t)
	if record.Outcome != task.StateFailed || record.Error == "" {
		t.Fatalf("task 该记「失败」并把原因单开字段：%+v", record)
	}
	if strings.Contains(string(record.Outcome), record.Error) {
		t.Fatal("原因不许塞进状态字段")
	}
}

// 没配那个渠道 / 模型名为空 ⇒ **兜底话术**（不联网），别去打一个不存在的上游。
func TestMissingProviderFallsBack(t *testing.T) {
	h := newHarness(t, dummyProviders(), "ghost", "ghost-model")
	accepted, err := h.service.Accept(h.session, "没配模型也要有回话")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Backend != "fallback" {
		t.Fatalf("该走兜底：%+v", accepted)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[1].Content != fallbackReply {
		t.Fatalf("兜底 = %+v", messages)
	}
	if record := h.turnTask(t); record.Outcome != task.StateSucceeded {
		t.Fatalf("兜底不是失败：%+v", record)
	}
}

// deadProviders：指向一个没人听的端口 —— 连不上就是上游错。
func deadProviders() config.ProvidersConfig {
	return config.ProvidersConfig{Providers: []config.Provider{{
		ID: "dead", Kind: "openai-compat", BaseURL: "http://127.0.0.1:1/v1",
	}}}
}

// 思考：**流式动画**照旧（切它不影响），留档由渠道的 `store_reasoning` 决定。
func TestReasoningIsStoredWhenAsked(t *testing.T) {
	for _, storeReasoning := range []bool{true, false} {
		stub := newLocalSSEStub(t, 200,
			`data: {"choices":[{"delta":{"reasoning_content":"先想"}}]}`+"\n\n"+
				`data: {"choices":[{"delta":{"reasoning_content":"一下"}}]}`+"\n\n"+
				`data: {"choices":[{"delta":{"content":"答案是 42"}}]}`+"\n\n"+
				`data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2}}`+"\n\n"+
				"data: [DONE]\n\n")
		flag := storeReasoning
		h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{{
			ID: "stub", Kind: "openai-compat", BaseURL: stub.URL, StoreReasoning: &flag,
		}}}, "stub", "m")
		if _, err := h.service.Accept(h.session, "问一句"); err != nil {
			t.Fatal(err)
		}
		messages := h.settle(t)
		if len(messages) != 2 {
			t.Fatalf("该落一条回复：%+v", messages)
		}
		reply := messages[1]
		if reply.Content != "答案是 42" {
			t.Fatalf("正文 = %q", reply.Content)
		}
		if storeReasoning {
			if reply.Reasoning != "先想一下" || reply.ReasoningMS == nil {
				t.Fatalf("该把思考留档（连用时一起）：%+v", reply)
			}
		} else if reply.Reasoning != "" || reply.ReasoningMS != nil {
			t.Fatalf("关了留档就不该写思考：%+v", reply)
		}
		// 用量归一化后落库（缓存字段名那几种都归一了）
		if len(reply.Usage) == 0 || !strings.Contains(string(reply.Usage), `"prompt_tokens":9`) {
			t.Fatalf("该落归一化后的用量：%s", reply.Usage)
		}
	}
}

// newLocalSSEStub：一个本地的假上游（瞬时回帧，测试不用等）。
func newLocalSSEStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}
