package reroll

// 重摇那几条口径，一条条钉住：
//  ① 进模式 = 两条（原文 idx1 + 候选 idx2）；
//  ② 切换 ⇒ 库里那条 Message 的 **UUID 不变**、正文与**附带信息**一起换（拿掉 apply 就该红）；
//  ③ 删除中间那条 ⇒ 后方位次全部 -1，且 apply 到同号那一位；删最后一条 ⇒ apply 落到新的最后一条；
//  ④ 删到只剩一条 ⇒ 退出模式 + 清列表 + message **保留当前这版**（不得回原文）；
//  ⑤ 发新消息 ⇒ 候选全清；重启（新 Service）⇒ 列表空；
//  ⑥ 尾条不是 assistant ⇒ invalid；生成中 ⇒ conflict。
//
// 上游用 `httptest` 假服务（`kind: openai-compat`）：**每一发回的话都不一样**（能分出是哪一版），
// 并带回 usage / reasoning —— 于是"切换时附带信息也换"才有东西可断言。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microchat/internal/chat"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/turn"
)

// harness：一条会话 + 一个假上游 + 一个进程内的重摇服务（装配借真的 `chat.Service`）。
type harness struct {
	service   *Service
	chat      *chat.Service
	store     *store.Store
	turns     *turn.Registry
	tasks     *task.Registry
	session   model.Session
	sessionID string
	served    *int64 // 假上游回了几发（用来给每一版编个号）
	paths     config.Paths
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	var served int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := atomic.AddInt64(&served, 1)
		// 重摇走流式（`Chat`）⇒ 假上游也发 SSE（形状照 chat 测试：delta.content / delta.reasoning_content + usage + [DONE]）。
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"choices":[{"delta":{"reasoning_content":"思考 ` + fmt.Sprint(index) + `"}}]}` + "\n\n" +
				`data: {"choices":[{"delta":{"content":"候选 ` + fmt.Sprint(index) + `"}}]}` + "\n\n" +
				`data: {"choices":[],"usage":{"prompt_tokens":` + fmt.Sprint(10+index) + `,"completion_tokens":2,"total_tokens":` + fmt.Sprint(12+index) + `}}` + "\n\n" +
				"data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	paths := config.Paths{ConfigDir: dir, DataDir: dir}
	if err := config.SaveJSON(dir+"/providers.json", map[string]any{
		"providers": []map[string]any{{"id": "fake", "kind": "openai-compat", "base_url": upstream.URL}},
	}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session, err := st.CreateSession("fake", "fake-model", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turns, tasks := turn.NewRegistry(), task.NewRegistry()
	chatService := chat.New(st, paths, turns, tasks)
	service := New(st, paths, turns, tasks, chatService)
	chatService.Candidates = service // 与 main.go 同一条接线
	return &harness{
		service: service, chat: chatService, store: st, turns: turns, tasks: tasks,
		session: session, sessionID: session.ID, served: &served, paths: paths,
	}
}

// turn：落一轮对话（用户 + 助手，顺序就是 id）—— 尾条是 assistant（可重摇的那一条）。
//
// 两条消息的 `created_at` 钉在 1（**很久以前**）：于是"改正文真的动了 `updated_at`"能确定地断言出来
// （不然同毫秒里写完再看，两个数一样 ⇒ 断言时有时无）。
func (h *harness) turn(t *testing.T, text string) {
	t.Helper()
	ids, err := store.MintOrderedIDs(2)
	if err != nil {
		t.Fatal(err)
	}
	duration := int64(1234)
	reasoningMS := int64(321)
	for index, message := range []model.Message{
		{ID: ids[0], SessionID: h.sessionID, Role: model.RoleUser, Content: text, CreatedAt: 1, UpdatedAt: 1},
		{ID: ids[1], SessionID: h.sessionID, Role: model.RoleAssistant, Content: "原文：" + text,
			CreatedAt: 1, UpdatedAt: 1,
			DurationMS: &duration, ReasoningMS: &reasoningMS, Reasoning: "原文的思考",
			Usage: json.RawMessage(`{"prompt_tokens":5,"completion_tokens":1}`)},
	} {
		if _, err := h.store.InsertMessage(message); err != nil {
			t.Fatalf("第 %d 条落库失败：%v", index, err)
		}
	}
}

// tail：库里最后那条（重摇针对的就是它）。
func (h *harness) tail(t *testing.T) model.Message {
	t.Helper()
	messages, err := h.store.ListMessages(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Fatal("这条会话一条消息都没有")
	}
	return messages[len(messages)-1]
}

// roll：**同步**摇一版（`EnterSync` —— 与 `Enter` 同一个底，只是不等后台）。
func (h *harness) roll(t *testing.T) State {
	t.Helper()
	accepted, err := h.service.EnterSync(context.Background(), h.session)
	if err != nil {
		t.Fatalf("重摇没摇起来：%v", err)
	}
	return accepted.State
}

func contents(state State) []string {
	lines := make([]string, 0, len(state.Items))
	for _, item := range state.Items {
		lines = append(lines, fmt.Sprintf("%d:%s", item.Idx, item.Preview))
	}
	return lines
}

// ① 进模式 = 两条：原文占 idx=1、摇出来的占 idx=2；原文那一版没被"保护"，它只是第一版。
func TestEnterLandsTwoVersions(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")

	state := h.roll(t)
	if !state.Active || state.Count != 2 || state.CurrentIdx != 1 {
		t.Fatalf("进模式该是【两条、当前第一版】：%+v", state)
	}
	if got := contents(state); got[0] != "1:原文：第一轮" || got[1] != "2:候选 1" {
		t.Fatalf("两条该是【原文 + 候选】：%v", got)
	}
	if state.Items[0].Current != true || state.Items[1].Current != false {
		t.Fatalf("当前那版该是第 1 版：%+v", state.Items)
	}
	if state.Running {
		t.Fatalf("同步摇完就不该还在摇：%+v", state)
	}
	// **候选不进库**：库里那条 Message 还是原文
	if tail := h.tail(t); tail.Content != "原文：第一轮" {
		t.Fatalf("摇出来的那一版不该进库：%q", tail.Content)
	}
	// 再摇一版 ⇒ 三条（分母是这么长出来的）
	if state = h.roll(t); state.Count != 3 {
		t.Fatalf("再摇一版该变成 3 条：%+v", state)
	}
}

// ② 切换 ⇒ UUID **不变**、正文与附带信息（usage / 耗时 / 思考）一起换；切回原文也换得回去。
func TestSwitchKeepsUUIDAndSwapsMeta(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	before := h.tail(t)

	state := h.roll(t)
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	after := h.tail(t)
	if after.ID != before.ID || after.SessionID != before.SessionID {
		t.Fatalf("重摇接受 = 就地替换 ⇒ UUID 绝对不变：%s → %s", before.ID, after.ID)
	}
	if after.Content != "候选 1" {
		t.Fatalf("正文该换成第 2 版：%q", after.Content)
	}
	if after.Reasoning != "思考 1" || after.DurationMS == nil ||
		*after.DurationMS == *before.DurationMS {
		t.Fatalf("附带信息该跟着换：%+v", after)
	}
	if string(after.Usage) == string(before.Usage) || len(after.Usage) == 0 {
		t.Fatalf("usage 该跟着换：%q（原来是 %q）", after.Usage, before.Usage)
	}
	if after.UpdatedAt <= before.UpdatedAt {
		t.Fatalf("改正文该刷新 updated_at：%d（原来 %d）", after.UpdatedAt, before.UpdatedAt)
	}
	// 切回原文那一版：正文与那组附带信息一起回来
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 1); err != nil {
		t.Fatal(err)
	}
	restored := h.tail(t)
	if restored.Content != "原文：第一轮" || restored.Reasoning != "原文的思考" ||
		restored.DurationMS == nil || *restored.DurationMS != *before.DurationMS ||
		string(restored.Usage) != string(before.Usage) {
		t.Fatalf("切回原文该连附带信息一起回来：%+v", restored)
	}
	if restored.ID != before.ID {
		t.Fatalf("UUID 还是那一个：%s", restored.ID)
	}
	_ = state
}

// ③ 删除：中间那条 ⇒ 后方位次全部 -1 且 apply 到同号那一位；最后一条 ⇒ apply 落到新的最后一条（clamp）。
func TestDeleteShiftsAndApplies(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.roll(t) // 第 2 版：候选 1
	h.roll(t) // 第 3 版：候选 2

	// 当前指着第 2 版 ⇒ 删掉**前面**那条（第 1 版）：同一版换成第 1 位，名单缩成两条
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	state, err := h.service.Delete(h.sessionID, TargetMessage, 1)
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 2 || state.CurrentIdx != 1 {
		t.Fatalf("删前面那条 ⇒ 当前同位次前移：%+v", state)
	}
	if got := contents(state); got[0] != "1:候选 1" || got[1] != "2:候选 2" {
		t.Fatalf("后方位次该统一 -1：%v", got)
	}
	if tail := h.tail(t); tail.Content != "候选 1" {
		t.Fatalf("apply 该落到同号那一位：%q", tail.Content)
	}
	if state.Items[0].Current != true {
		t.Fatalf("当前那版该是第 1 版：%+v", state.Items)
	}
}

// ③a 删掉**最后**一条，而当前指的正是它 ⇒ 前移后那个号不存在 ⇒ apply 落到新的最后一条（clamp）。
func TestDeleteLastVersionClamps(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.roll(t) // 第 2 版：候选 1
	h.roll(t) // 第 3 版：候选 2
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 3); err != nil {
		t.Fatal(err)
	}
	state, err := h.service.Delete(h.sessionID, TargetMessage, 3)
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 2 || state.CurrentIdx != 2 {
		t.Fatalf("删最后一条 ⇒ clamp 到新的最后一条：%+v", state)
	}
	if tail := h.tail(t); tail.Content != "候选 1" {
		t.Fatalf("apply 该落在新的最后一条（候选 1）上：%q", tail.Content)
	}
	if state.Items[1].Current != true {
		t.Fatalf("当前那版该是第 2 版：%+v", state.Items)
	}
}

// ③b 删掉**当前**那一版（不是最后一条）⇒ 那个位次上顶上来的是它的后一版。
func TestDeleteCurrentVersionTakesNext(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.roll(t) // 2：候选 1
	h.roll(t) // 3：候选 2
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	state, err := h.service.Delete(h.sessionID, TargetMessage, 2)
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 2 || state.CurrentIdx != 2 {
		t.Fatalf("删掉当前那版 ⇒ 同位次顶上来：%+v", state)
	}
	if tail := h.tail(t); tail.Content != "候选 2" {
		t.Fatalf("第 2 位上的新那版该被 apply：%q", tail.Content)
	}
}

// ④ 删到只剩一条 ⇒ 退出模式 + 清列表 + message **保留当前这版**（这一条专门钉：不得回原文）。
func TestDeleteDownToOneExitsAndKeepsCurrent(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.roll(t) // 第 2 版
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	if tail := h.tail(t); tail.Content != "候选 1" {
		t.Fatalf("先切到第 2 版：%q", tail.Content)
	}
	before := h.tail(t)

	state, err := h.service.Delete(h.sessionID, TargetMessage, 1) // 删掉原文那一版 ⇒ 只剩一条
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || state.Count != 0 || len(state.Items) != 0 {
		t.Fatalf("只剩一条就该退出模式并清列表：%+v", state)
	}
	after := h.tail(t)
	if after.Content != "候选 1" {
		t.Fatalf("**不许回原文**：message 该保留当前这版，得到 %q", after.Content)
	}
	if after.ID != before.ID || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("退出这条路上不该再写库：%+v", after)
	}
	if got := h.service.State(h.sessionID, TargetMessage); got.Active {
		t.Fatalf("清完列表就该是【没在重摇】：%+v", got)
	}
}

// ④b 退出（`/reroll off`）= 清列表 + 保留当前那版；在摇的那一趟顺手按停。
func TestClearKeepsCurrent(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.roll(t)
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	h.service.Clear(h.sessionID, TargetMessage)
	state := h.service.State(h.sessionID, TargetMessage)
	if state.Active || state.Count != 0 {
		t.Fatalf("清完该是没在重摇：%+v", state)
	}
	if tail := h.tail(t); tail.Content != "候选 1" {
		t.Fatalf("退出不 apply、也不回原文：%q", tail.Content)
	}
	// 退出之后再 `/reroll`：重新进模式，原文那版记的是**当前**正文（候选 1）
	if state = h.roll(t); state.Count != 2 || state.Items[0].Preview != "候选 1" {
		t.Fatalf("重新进模式该以当前正文当第一版：%+v", state)
	}
	// 幂等：没在重摇时清一次也没事
	h.service.Clear(h.sessionID, TargetMessage)
	if state := h.service.State(h.sessionID, TargetMessage); state.Active {
		t.Fatalf("清两次也该是没在重摇：%+v", state)
	}
}

// ⑤ 发新消息 ⇒ 候选全清（走 `chat.Accept` 那条接线）；重启（新 Service）⇒ 列表空。
func TestCandidatesDieOnNewMessageAndRestart(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	if state := h.roll(t); state.Count != 2 {
		t.Fatalf("先摇出来两条：%+v", state)
	}
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	// 发一句**新**消息（真的走 chat.Accept：它会先落用户消息，再把候选全清）
	if _, err := h.chat.Accept(h.session, "接着说"); err != nil {
		t.Fatal(err)
	}
	if state := h.service.State(h.sessionID, TargetMessage); state.Active || state.Count != 0 {
		t.Fatalf("一发出新消息 ⇒ 候选全清：%+v", state)
	}
	// message 保留当前这版（候选从没进过库 ⇒ 没什么要回退的）
	messages, err := h.store.ListMessages(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := messages[len(messages)-2]; got.Content != "候选 1" {
		t.Fatalf("候选清掉之后，那条回复该还是当前这版：%q", got.Content)
	}

	// 重启：新的 Service（进程内的事实 ⇒ 列表本来就是空的）
	fresh := New(h.store, h.paths, turn.NewRegistry(), task.NewRegistry(), h.chat)
	if state := fresh.State(h.sessionID, TargetMessage); state.Active || state.Count != 0 {
		t.Fatalf("重启之后该是空的（只剩你选中的那版）：%+v", state)
	}
}

// ⑤b 那条消息被删掉（`/cut`）⇒ 候选自然消失（问状态时自愈）。
func TestCandidatesDieWhenTailIsDeleted(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	if state := h.roll(t); state.Count != 2 {
		t.Fatalf("先摇出来两条：%+v", state)
	}
	tail := h.tail(t)
	plan, err := h.store.DeletionPlan(h.sessionID, tail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ApplyDeletion(plan); err != nil {
		t.Fatal(err)
	}
	if state := h.service.State(h.sessionID, TargetMessage); state.Active || state.Count != 0 {
		t.Fatalf("尾巴没了 ⇒ 候选自然消失：%+v", state)
	}
}

// ⑥ 尾条不是 assistant ⇒ invalid（**明说**：那是重发，不是重摇）；生成中 ⇒ conflict。
func TestEnterRefusals(t *testing.T) {
	h := newHarness(t)
	// 一条消息都没有
	if _, err := h.service.EnterSync(context.Background(), h.session); !isCode(err, "invalid") {
		t.Fatalf("空会话该 invalid：%v", err)
	}
	// 尾条是**用户**消息（用铸出来的 id ⇒ 它的顺序真的在后面）
	ids, err := store.MintOrderedIDs(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.InsertMessage(model.Message{
		ID: ids[0], SessionID: h.sessionID, Role: model.RoleUser, Content: "只有我说了一句"}); err != nil {
		t.Fatal(err)
	}
	_, err = h.service.EnterSync(context.Background(), h.session)
	if !isCode(err, "invalid") || !strings.Contains(err.Error(), "重发") {
		t.Fatalf("尾条是用户消息该 invalid 并说清是【重发】：%v", err)
	}
	// 生成中（闸门被占）⇒ conflict
	h.turn(t, "第一轮")
	if _, err := h.turns.Begin(h.sessionID, "reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.EnterSync(context.Background(), h.session); !isCode(err, "conflict") {
		t.Fatalf("生成中该 conflict：%v", err)
	}
	h.turns.Stop(h.sessionID)
	// 没进模式时切/删 ⇒ not_found
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 1); !isCode(err, "not_found") {
		t.Fatalf("没进模式该 not_found：%v", err)
	}
	if _, err := h.service.Delete(h.sessionID, TargetMessage, 1); !isCode(err, "not_found") {
		t.Fatalf("没进模式该 not_found：%v", err)
	}
	// 位次越界 ⇒ invalid
	h.roll(t)
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 9); !isCode(err, "invalid") {
		t.Fatalf("位次越界该 invalid：%v", err)
	}
	if _, err := h.service.Delete(h.sessionID, TargetMessage, 0); !isCode(err, "invalid") {
		t.Fatalf("位次 0 该 invalid：%v", err)
	}
}

// ⑥b 重摇在跑时发消息 ⇒ 闸门回 ErrRerollBusy（**说得出是哪一件在跑**）。
func TestBusyGateIsSharedWithTurn(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	// 手工占住"重摇"那一档（不真发上游：这里只钉闸门）
	token, err := h.turns.BeginReroll(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer h.turns.FinishReroll(h.sessionID, token, "")
	if _, err := h.chat.Accept(h.session, "插队的一句"); !errors.Is(err, turn.ErrRerollBusy) {
		t.Fatalf("重摇期间发消息该 ErrRerollBusy：%v", err)
	}
	if status := h.turns.Status(h.sessionID).Reroll; status == nil || !status.IsRunning() {
		t.Fatalf("状态那一档该说在摇：%+v", status)
	}
	// 反向：生成期间不许重摇
	h.turns.FinishReroll(h.sessionID, token, "")
	if _, err := h.turns.Begin(h.sessionID, "reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.EnterSync(context.Background(), h.session); !isCode(err, "conflict") {
		t.Fatalf("生成中该 conflict：%v", err)
	}
}

// ── 摘要重摇（摘要级）────────────────────────────────────────────────────────

// fakeGenerator：假生成器 —— 回的话按调用次数编号（能分出是哪一版），不必真起 compact。
type fakeGenerator struct {
	calls int
	err   error
}

func (f *fakeGenerator) RegenerateSummary(_ context.Context, _ model.Session, _ string) (GeneratedSummary, error) {
	if f.err != nil {
		return GeneratedSummary{}, f.err
	}
	f.calls++
	return GeneratedSummary{
		Text:   fmt.Sprintf("摘要候选 %d", f.calls),
		Tokens: 100 + f.calls,
		// Provider/Model/PromptVersion 与"那一版"一起换 ⇒ `ReplaceSummaryText` 要它们
		Provider: "fake", Model: "fake-model", PromptVersion: 7,
		Usage: json.RawMessage(fmt.Sprintf(`{"prompt_tokens":%d}`, f.calls)),
	}, nil
}

// blockingGenerator：摇起来就卡住，直到 release 关掉 —— 用来在"正在摇"的那一刻观察别的入口。
type blockingGenerator struct {
	started chan struct{}
	release chan struct{}
}

func (g *blockingGenerator) RegenerateSummary(_ context.Context, _ model.Session, _ string) (GeneratedSummary, error) {
	close(g.started)
	<-g.release
	return GeneratedSummary{Text: "卡住之后摇出来的", Tokens: 1, Provider: "fake", Model: "fake-model",
		PromptVersion: 1}, nil
}

// pyramid：造一块金字塔摘要 —— 四条消息（两轮）上的两条消息级摘要（s1: 1–2、s2: 3–4）
// 底下的**一条合并级根**（s3: 1–4）。返回那四条消息（顺序 = idx 1..4）。
func (h *harness) pyramid(t *testing.T) []model.Message {
	t.Helper()
	h.turn(t, "第一轮")
	h.turn(t, "第二轮")
	messages, err := h.store.ListMessages(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("金字塔要四条消息，拿到 %d 条", len(messages))
	}
	record := func(id string, kind model.SummaryType, begin, end, text string, sourceIDs []string) {
		t.Helper()
		if err := h.store.RecordSummary(model.Summary{
			ID: id, SessionID: h.sessionID, Type: kind,
			BeginMessageID: &begin, EndMessageID: &end, Text: text, Tokens: 50,
			Provider: "fake", Model: "fake-model", PromptVersion: 1, CreatedAt: 1,
		}, sourceIDs); err != nil {
			t.Fatalf("落摘要 %s 失败：%v", id, err)
		}
	}
	record("s1", model.TypeMessages, messages[0].ID, messages[1].ID, "摘要一",
		[]string{messages[0].ID, messages[1].ID})
	record("s2", model.TypeMessages, messages[2].ID, messages[3].ID, "摘要二",
		[]string{messages[2].ID, messages[3].ID})
	record("s3", model.TypeSummaries, messages[0].ID, messages[3].ID, "摘要根", []string{"s1", "s2"})
	return messages
}

// summary：从库里取一条摘要（apply 之后核对用）。
func (h *harness) summary(t *testing.T, id string) model.Summary {
	t.Helper()
	summaries, err := h.store.ListSummaries(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range summaries {
		if summary.ID == id {
			return summary
		}
	}
	t.Fatalf("摘要 %s 不在库里", id)
	return model.Summary{}
}

// enterSummary：同步摇一版摘要（`EnterSummarySync`），断言成功。
func (h *harness) enterSummary(t *testing.T, idx int) Accepted {
	t.Helper()
	accepted, err := h.service.EnterSummarySync(context.Background(), h.session, idx)
	if err != nil {
		t.Fatalf("摘要重摇没摇起来（idx=%d）：%v", idx, err)
	}
	return accepted
}

// ⑦ 同树任意 idx（消息 idx）指向**同一个根摘要**；进模式两条；再摇 ⇒ 加一版；
// 切换 ⇒ 就地换那条摘要（text/tokens/usage/provider/model/prompt_version 换了），
// 而 id / 区间 / parent / children 一概不动。
func TestSummaryTargetResolvesToRootAndApply(t *testing.T) {
	h := newHarness(t)
	messages := h.pyramid(t)
	h.service.Summaries = &fakeGenerator{}

	// 同树四个 idx 都指向 s3（同一目标）
	for _, idx := range []int{1, 2, 3, 4} {
		accepted := h.enterSummary(t, idx)
		if accepted.TargetSummaryID != "s3" {
			t.Fatalf("第 %d 条该指向根 s3，得到 %q", idx, accepted.TargetSummaryID)
		}
		if accepted.State.TargetKind != TargetSummary || accepted.State.TargetMessageID != "" {
			t.Fatalf("摘要模式该报 kind=summary 且不给 target_message_id：%+v", accepted.State)
		}
		if accepted.State.FromIdx == nil || *accepted.State.FromIdx != 1 ||
			accepted.State.ToIdx == nil || *accepted.State.ToIdx != 4 {
			t.Fatalf("目标盖的区间该是 1..4：%+v", accepted.State)
		}
		h.service.Clear(h.sessionID, TargetSummary) // 换回干净状态，逐条验
	}

	// 进模式 = 两条：原文（目标摘要当前那一版）+ 摇出一版
	h.service.Summaries = &fakeGenerator{} // 计数重置：下面的断言要从"候选 1"数起
	accepted := h.enterSummary(t, 2)
	state := accepted.State
	if state.Count != 2 || state.CurrentIdx != 1 {
		t.Fatalf("进模式该是【两条、当前第一版】：%+v", state)
	}
	if got := contents(state); got[0] != "1:摘要根" || got[1] != "2:摘要候选 1" {
		t.Fatalf("两条该是【原文摘要 + 摇出来的】：%v", got)
	}
	// 再摇一版（同根）⇒ 加一版
	if state = h.enterSummary(t, 4).State; state.Count != 3 {
		t.Fatalf("同根再摇该加一版：%+v", state)
	}

	// 切换：库里那条摘要就地换正文与随行数据
	if _, err := h.service.Switch(h.sessionID, TargetSummary, 2); err != nil {
		t.Fatal(err)
	}
	after := h.summary(t, "s3")
	if after.Text != "摘要候选 1" {
		t.Fatalf("正文该换成第 2 版：%q", after.Text)
	}
	if after.Tokens != 101 || string(after.Usage) != `{"prompt_tokens":1}` ||
		after.Provider != "fake" || after.Model != "fake-model" || after.PromptVersion != 7 {
		t.Fatalf("随行数据该跟着换：%+v", after)
	}
	// id / 区间 / parent / children 一个字不动
	if after.ID != "s3" || after.ParentSummaryID != nil ||
		after.BeginMessageID == nil || *after.BeginMessageID != messages[0].ID ||
		after.EndMessageID == nil || *after.EndMessageID != messages[3].ID {
		t.Fatalf("id / 区间 / parent 不该动：%+v", after)
	}
	if after.Dirty {
		t.Fatalf("孩子都干净 ⇒ apply 之后该是干净的：%+v", after)
	}
	for _, child := range []string{"s1", "s2"} {
		got := h.summary(t, child)
		if got.ParentSummaryID == nil || *got.ParentSummaryID != "s3" {
			t.Fatalf("孩子 %s 的父不该动：%+v", child, got)
		}
	}
	// 原样切回去：正文回到"摘要根"
	if _, err := h.service.Switch(h.sessionID, TargetSummary, 1); err != nil {
		t.Fatal(err)
	}
	if back := h.summary(t, "s3"); back.Text != "摘要根" || back.Tokens != 50 {
		t.Fatalf("切回原文那版该连正文与 token 一起回来：%+v", back)
	}
}

// ⑧ 未被摘要盖住的 idx / 越界 / 非正 ⇒ invalid（说清是哪一种）。
func TestSummaryEnterRefusals(t *testing.T) {
	h := newHarness(t)
	h.service.Summaries = &fakeGenerator{}
	h.turn(t, "第一轮")
	messages, err := h.store.ListMessages(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	// 只盖住第 1 条：第 2 条于是"还没被摘要盖住"
	begin, end := messages[0].ID, messages[0].ID
	if err := h.store.RecordSummary(model.Summary{
		ID: "only1", SessionID: h.sessionID, Type: model.TypeMessages,
		BeginMessageID: &begin, EndMessageID: &end, Text: "只盖住第一条", Tokens: 5,
		Provider: "fake", Model: "fake-model", PromptVersion: 1, CreatedAt: 1,
	}, []string{messages[0].ID}); err != nil {
		t.Fatal(err)
	}
	if accepted := h.enterSummary(t, 1); accepted.TargetSummaryID != "only1" {
		t.Fatalf("第 1 条该指向 only1：%q", accepted.TargetSummaryID)
	}
	h.service.Clear(h.sessionID, TargetSummary)

	_, err = h.service.EnterSummarySync(context.Background(), h.session, 2)
	if !isCode(err, "invalid") || !strings.Contains(err.Error(), "还没被摘要盖住") {
		t.Fatalf("没被盖住的 idx 该 invalid 并说清：%v", err)
	}
	if _, err := h.service.EnterSummarySync(context.Background(), h.session, 3); !isCode(err, "invalid") {
		t.Fatalf("越界该 invalid：%v", err)
	}
	if _, err := h.service.EnterSummarySync(context.Background(), h.session, 0); !isCode(err, "invalid") {
		t.Fatalf("非正该 invalid：%v", err)
	}
	// 另一个 kind 正活着 ⇒ 本模式没进 ⇒ not_found
	if _, err := h.service.Switch(h.sessionID, TargetMessage, 1); !isCode(err, "not_found") {
		t.Fatalf("没进消息模式该 not_found：%v", err)
	}
}

// ⑨ 目标被并走（给它挂个父）⇒ State 回 inactive。
func TestSummaryTargetMergedAwayGoesInactive(t *testing.T) {
	h := newHarness(t)
	h.pyramid(t)
	h.service.Summaries = &fakeGenerator{}
	if accepted := h.enterSummary(t, 1); accepted.TargetSummaryID != "s3" {
		t.Fatalf("该指向 s3：%q", accepted.TargetSummaryID)
	}
	// 把 s3 并到一个新的父下面（它不再是根 ⇒ 没有可重摇的摘要了）
	s3 := h.summary(t, "s3")
	if err := h.store.RecordSummary(model.Summary{
		ID: "s4", SessionID: h.sessionID, Type: model.TypeSummaries,
		BeginMessageID: s3.BeginMessageID, EndMessageID: s3.EndMessageID, Text: "更高的根", Tokens: 50,
		Provider: "fake", Model: "fake-model", PromptVersion: 1, CreatedAt: 1,
	}, []string{"s3"}); err != nil {
		t.Fatal(err)
	}
	if state := h.service.State(h.sessionID, TargetSummary); state.Active || state.Count != 0 {
		t.Fatalf("目标被并走 ⇒ 该退出模式：%+v", state)
	}
}

// ⑩ 不同根再进 ⇒ 清旧进新；在摇时切换目标 ⇒ conflict。
func TestSummarySwitchTargetAndBusy(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.turn(t, "第二轮")
	messages, err := h.store.ListMessages(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	h.service.Summaries = &fakeGenerator{}
	record := func(id string, begin, end string) {
		t.Helper()
		if err := h.store.RecordSummary(model.Summary{
			ID: id, SessionID: h.sessionID, Type: model.TypeMessages,
			BeginMessageID: &begin, EndMessageID: &end, Text: "摘要 " + id, Tokens: 50,
			Provider: "fake", Model: "fake-model", PromptVersion: 1, CreatedAt: 1,
		}, []string{begin, end}); err != nil {
			t.Fatal(err)
		}
	}
	record("r1", messages[0].ID, messages[1].ID) // 第 1–2 条
	record("r2", messages[2].ID, messages[3].ID) // 第 3–4 条

	// 先在第 1 条那个根（r1）里进模式并摇一版
	if accepted := h.enterSummary(t, 1); accepted.TargetSummaryID != "r1" {
		t.Fatalf("该指向 r1：%q", accepted.TargetSummaryID)
	}
	// 不进"另一种模式"的冲突路：换到另一个根（r2）⇒ 清旧进新
	accepted := h.enterSummary(t, 3)
	if accepted.TargetSummaryID != "r2" || accepted.State.Count != 2 {
		t.Fatalf("换根该清旧进新（r2、两条）：%+v", accepted)
	}
	h.service.Clear(h.sessionID, TargetSummary)

	// 在摇时切换目标 ⇒ conflict（说清在摇什么）
	blocking := &blockingGenerator{started: make(chan struct{}), release: make(chan struct{})}
	h.service.Summaries = blocking
	go func() { _, _ = h.service.EnterSummary(h.session, 1) }()
	<-blocking.started
	if _, err := h.service.EnterSummary(h.session, 3); !isCode(err, "conflict") {
		t.Fatalf("在摇时换目标该 conflict：%v", err)
	}
	close(blocking.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status := h.turns.Status(h.sessionID).Reroll; status == nil || !status.IsRunning() {
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// isCode：错误体那套词（客户端按 code 分支，这里照它断言）。
func isCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

// 查表那一行：model_route 有 responses 行 ⇒ 这一发走 /responses，候选正文从 output_text 来；
// 无行 ⇒ 还是 /chat/completions（零行为变化）。
func TestMessageRerollRoutesByModelRouteTable(t *testing.T) {
	var path string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		// 重摇走流式（`Chat`）⇒ 两个协议都发 SSE（形状照 chat 测试）。
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasSuffix(path, "/responses") {
			_, _ = w.Write([]byte(
				`data: {"type":"response.created","response":{"id":"r1"}}` + "\n\n" +
					`data: {"type":"response.output_text.delta","delta":"重摇候选"}` + "\n\n" +
					`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n" +
					"data: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte(
			`data: {"choices":[{"delta":{"content":"重摇候选"}}]}` + "\n\n" +
				"data: [DONE]\n\n"))
	}))
	t.Cleanup(stub.Close)
	providers := map[string]any{
		"providers": []map[string]any{{"id": "fake", "kind": "openai-compat", "base_url": stub.URL}},
	}

	// 有行 ⇒ /responses。
	h := newHarness(t)
	if err := config.SaveJSON(h.paths.Config("providers.json"), providers); err != nil {
		t.Fatal(err)
	}
	if err := h.store.ReplaceModelRoutes("fake", map[string]string{"fake-model": "openai-responses"}, 7); err != nil {
		t.Fatal(err)
	}
	h.turn(t, "第一轮")
	state := h.roll(t)
	if path != "/responses" {
		t.Fatalf("有 responses 行该走 /responses：path = %q", path)
	}
	if got := contents(state); len(got) != 2 || got[1] != "2:重摇候选" {
		t.Fatalf("候选正文该从 output_text 来：%v", got)
	}

	// 无行 ⇒ 还是 /chat/completions。
	g := newHarness(t)
	if err := config.SaveJSON(g.paths.Config("providers.json"), providers); err != nil {
		t.Fatal(err)
	}
	g.turn(t, "第一轮")
	gstate := g.roll(t)
	if path != "/chat/completions" {
		t.Fatalf("无行该还是 /chat/completions：path = %q", path)
	}
	if got := contents(gstate); len(got) != 2 || got[1] != "2:重摇候选" {
		t.Fatalf("chat 体也该出同一句：%v", got)
	}
}

// TestRerollStreamsCandidate：候选也走流（2026-10-10 从 Complete 改 Chat）——
// 摇的过程中 `turn` 缓冲里就能读到正文在长（web 靠它做气泡内预览），摇完清空。
func TestRerollStreamsCandidate(t *testing.T) {
	h := newHarness(t)
	var served int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&served, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"第一段"}}]}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"第二段"}}]}` + "\n\n" + "data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)
	// 把这个会话的假上游换成"慢流"（与 main.go 同一条接线：providers.json 是唯一来源）。
	if err := config.SaveJSON(h.paths.Config("providers.json"), map[string]any{
		"providers": []map[string]any{{"id": "fake", "kind": "openai-compat", "base_url": upstream.URL}},
	}); err != nil {
		t.Fatal(err)
	}
	h.turn(t, "第一轮")

	if _, err := h.service.Enter(h.session); err != nil {
		t.Fatalf("进模式失败：%v", err)
	}
	// 摇的过程中：缓冲里该已经能看到第一段（还没摇完）。
	deadline := time.Now().Add(2 * time.Second)
	var mid string
	for time.Now().Before(deadline) {
		slice := h.turns.StreamSince(h.sessionID, 0, 0)
		if slice.Text != "" {
			mid = slice.Text
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mid == "" {
		t.Fatal("摇的过程中没在缓冲里看到任何正文（流没走 turn 缓冲？）")
	}
	if mid != "第一段" && mid != "第一段第二段" {
		t.Fatalf("中途正文该是前缀，得到 %q", mid)
	}
	// 等摇完：候选正文 = 两段之和；缓冲清空。
	for i := 0; i < 100; i++ {
		if !h.service.State(h.sessionID, TargetMessage).Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := h.service.State(h.sessionID, TargetMessage)
	if len(st.Items) != 2 || st.Items[1].Preview != "第一段第二段" {
		t.Fatalf("候选该是整段两条：%+v", st.Items)
	}
	if got := h.turns.StreamSince(h.sessionID, 0, 0).Text; got != "" {
		t.Fatalf("摇完缓冲该清空，得到 %q", got)
	}
}
