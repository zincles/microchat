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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content":   fmt.Sprintf("候选 %d", index),
					"reasoning": fmt.Sprintf("思考 %d", index),
				},
			}},
			"usage": map[string]any{"prompt_tokens": 10 + index, "completion_tokens": 2, "total_tokens": 12 + index},
		})
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
	session, err := st.CreateSession("fake", "fake-model", "")
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
	if _, err := h.service.Switch(h.sessionID, 2); err != nil {
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
	if _, err := h.service.Switch(h.sessionID, 1); err != nil {
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
	if _, err := h.service.Switch(h.sessionID, 2); err != nil {
		t.Fatal(err)
	}
	state, err := h.service.Delete(h.sessionID, 1)
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
	if _, err := h.service.Switch(h.sessionID, 3); err != nil {
		t.Fatal(err)
	}
	state, err := h.service.Delete(h.sessionID, 3)
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
	if _, err := h.service.Switch(h.sessionID, 2); err != nil {
		t.Fatal(err)
	}
	state, err := h.service.Delete(h.sessionID, 2)
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
	if _, err := h.service.Switch(h.sessionID, 2); err != nil {
		t.Fatal(err)
	}
	if tail := h.tail(t); tail.Content != "候选 1" {
		t.Fatalf("先切到第 2 版：%q", tail.Content)
	}
	before := h.tail(t)

	state, err := h.service.Delete(h.sessionID, 1) // 删掉原文那一版 ⇒ 只剩一条
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
	if got := h.service.State(h.sessionID); got.Active {
		t.Fatalf("清完列表就该是【没在重摇】：%+v", got)
	}
}

// ④b 退出（`/reroll off`）= 清列表 + 保留当前那版；在摇的那一趟顺手按停。
func TestClearKeepsCurrent(t *testing.T) {
	h := newHarness(t)
	h.turn(t, "第一轮")
	h.roll(t)
	if _, err := h.service.Switch(h.sessionID, 2); err != nil {
		t.Fatal(err)
	}
	h.service.Clear(h.sessionID)
	state := h.service.State(h.sessionID)
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
	h.service.Clear(h.sessionID)
	if state := h.service.State(h.sessionID); state.Active {
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
	if _, err := h.service.Switch(h.sessionID, 2); err != nil {
		t.Fatal(err)
	}
	// 发一句**新**消息（真的走 chat.Accept：它会先落用户消息，再把候选全清）
	if _, err := h.chat.Accept(h.session, "接着说"); err != nil {
		t.Fatal(err)
	}
	if state := h.service.State(h.sessionID); state.Active || state.Count != 0 {
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
	if state := fresh.State(h.sessionID); state.Active || state.Count != 0 {
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
	if state := h.service.State(h.sessionID); state.Active || state.Count != 0 {
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
	if _, err := h.service.Switch(h.sessionID, 1); !isCode(err, "not_found") {
		t.Fatalf("没进模式该 not_found：%v", err)
	}
	if _, err := h.service.Delete(h.sessionID, 1); !isCode(err, "not_found") {
		t.Fatalf("没进模式该 not_found：%v", err)
	}
	// 位次越界 ⇒ invalid
	h.roll(t)
	if _, err := h.service.Switch(h.sessionID, 9); !isCode(err, "invalid") {
		t.Fatalf("位次越界该 invalid：%v", err)
	}
	if _, err := h.service.Delete(h.sessionID, 0); !isCode(err, "invalid") {
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

// isCode：错误体那套词（客户端按 code 分支，这里照它断言）。
func isCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
