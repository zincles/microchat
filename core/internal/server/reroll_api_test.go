package server

// 重摇那两个家族的契约（`/reroll-message*` 与 `/reroll-summary*`，形状一一对应）：
// 202 受理两条、切换（消息级 UUID 不变 / 摘要级换正文）、按位次删（后方位次前移、退出模式时保留当前那版）、
// 以及四条错误口径（尾条不是 assistant ⇒ 400 / 闸门占着 ⇒ 409 / 位次越界或没盖住 ⇒ 400 / 没进模式 ⇒ 404）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"microchat/internal/compact"
	"microchat/internal/model"
	"microchat/internal/reroll"
	"microchat/internal/store"
)

// rerollMessageEnter：POST .../reroll-message ⇒ 202 + 受理回执。
func (b *dummySandbox) rerollMessageEnter(t *testing.T) reroll.Accepted {
	t.Helper()
	recorder := call(b.server, "POST", b.path+"/reroll-message", "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("重摇该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var accepted reroll.Accepted
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted
}

// rerollMessageState：GET .../reroll-message。
func (b *dummySandbox) rerollMessageState(t *testing.T) reroll.State {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/reroll-message", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("取重摇状态该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var state reroll.State
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// settleMessage：等这一次摇完（候选在后台摇，受理那一刻还看不到）。
func (b *dummySandbox) settleMessage(t *testing.T) reroll.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := b.rerollMessageState(t)
		if !state.Running {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("这一版一直没摇完")
	return reroll.State{}
}

// lastMessage：库里最后那条（重摇针对的就是它）。
func (b *dummySandbox) lastMessage(t *testing.T) model.Message {
	t.Helper()
	messages := b.messages(t)
	return messages[len(messages)-1]
}

func TestRerollContract(t *testing.T) {
	box := newDummySandbox(t)
	box.send(t, "第一句话")
	box.waitPhase(t, box.sessionID, "idle")
	tail := box.lastMessage(t)
	if tail.Role != model.RoleAssistant {
		t.Fatalf("尾条该是 assistant：%+v", tail)
	}

	// 受理：202 + 两条（原文 idx1 + 正在摇的 idx2）
	accepted := box.rerollMessageEnter(t)
	if accepted.TargetMessageID != tail.ID {
		t.Fatalf("受理回执该指明摇的是哪条：%+v（尾条 %s）", accepted, tail.ID)
	}
	if accepted.TaskID == "" {
		t.Fatalf("受理回执该带 task_id：%+v", accepted)
	}
	if !accepted.State.Active || accepted.State.Count != 2 || accepted.State.CurrentIdx != 1 {
		t.Fatalf("进模式那一刻该是【原文 + 正在摇的一版】：%+v", accepted.State)
	}
	if !accepted.State.Items[1].Pending {
		t.Fatalf("第 2 版那一刻还在摇：%+v", accepted.State.Items)
	}

	// 摇完：两条都在，预览各说各的（dummy 那两句话**刻意不同** ⇒ 切没切看得出来）
	state := box.settleMessage(t)
	if state.Count != 2 || state.Items[1].Pending || state.Items[1].Preview == "" {
		t.Fatalf("摇完该是两条实打实的候选：%+v", state)
	}
	if state.Items[0].Current != true {
		t.Fatalf("没切之前，当前那版还是原文：%+v", state.Items)
	}

	// 切到第 2 版：**UUID 不变**、正文变成摇出来的那句
	if recorder := call(box.server, "POST", box.path+"/reroll-message/switch", `{"idx":2}`); recorder.Code != http.StatusOK {
		t.Fatalf("切换该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	applied := box.lastMessage(t)
	if applied.ID != tail.ID {
		t.Fatalf("重摇接受 = 就地替换 ⇒ UUID 不变：%s → %s", tail.ID, applied.ID)
	}
	if applied.Content != "（测试用空模型·重摇）" {
		t.Fatalf("第 2 版该是摇出来的那句：%q", applied.Content)
	}
	// 切回第 1 版：正文换回原文（**不保护原文，但它就是第一版**）
	if recorder := call(box.server, "POST", box.path+"/reroll-message/switch", `{"idx":1}`); recorder.Code != http.StatusOK {
		t.Fatalf("切回第 1 版该 200：%s", recorder.Body.String())
	}
	if restored := box.lastMessage(t); restored.Content != tail.Content {
		t.Fatalf("第 1 版该是原文：%q", restored.Content)
	}

	// 再摇一版 ⇒ 三条（`‹ 2/3 ›` 那个分母的来源）
	if accepted = box.rerollMessageEnter(t); accepted.State.Count != 3 {
		t.Fatalf("再摇一版该是三条：%+v", accepted.State)
	}
	if state = box.settleMessage(t); state.Count != 3 {
		t.Fatalf("三条候选：%+v", state)
	}

	// 切到第 3 版，再删掉原文那条 ⇒ 后方位次前移，当前那一版**还是同一版**
	if recorder := call(box.server, "POST", box.path+"/reroll-message/switch", `{"idx":3}`); recorder.Code != http.StatusOK {
		t.Fatalf("切到第 3 版该 200：%s", recorder.Body.String())
	}
	if recorder := call(box.server, "DELETE", box.path+"/reroll-message/1", ""); recorder.Code != http.StatusOK {
		t.Fatalf("删第 1 版该 200：%s", recorder.Body.String())
	}
	if state = box.rerollMessageState(t); state.Count != 2 || state.CurrentIdx != 2 {
		t.Fatalf("删掉原文那版之后该是两条、当前那版前移到第 2 位：%+v", state)
	}
	if state.Items[0].Idx != 1 || state.Items[1].Idx != 2 {
		t.Fatalf("后方位次该统一 -1：%+v", state.Items)
	}
	if applied = box.lastMessage(t); applied.Content != "（测试用空模型·重摇）" {
		t.Fatalf("apply 该落在同号那一位（还是摇出来的那版）：%q", applied.Content)
	}

	// 再删一条 ⇒ 只剩一条 ⇒ **退出模式 + 清列表**，且 message 保留当前这版（不得回原文）
	if recorder := call(box.server, "DELETE", box.path+"/reroll-message/2", ""); recorder.Code != http.StatusOK {
		t.Fatalf("删第 2 版该 200：%s", recorder.Body.String())
	}
	if state = box.rerollMessageState(t); state.Active || state.Count != 0 {
		t.Fatalf("只剩一条 ⇒ 退出模式 + 清列表：%+v", state)
	}
	if applied = box.lastMessage(t); applied.Content != "（测试用空模型·重摇）" {
		t.Fatalf("退出这条路上【不 apply】：message 该保留当前这版，得到 %q", applied.Content)
	}
	if applied.ID != tail.ID {
		t.Fatalf("UUID 自始至终不变：%s", applied.ID)
	}

	// 显式退出（`DELETE .../reroll-message`）：没在模式里也 204（幂等）
	if recorder := call(box.server, "DELETE", box.path+"/reroll-message", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("退出重摇该 204，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// **一发出新消息 ⇒ 候选全清**（message 保留当前那版）
	box.rerollMessageEnter(t)
	box.settleMessage(t)
	box.send(t, "接着聊")
	if state = box.rerollMessageState(t); state.Active || state.Count != 0 {
		t.Fatalf("新消息一到 ⇒ 候选全清：%+v", state)
	}
}

func TestRerollRefusals(t *testing.T) {
	box := newDummySandbox(t)

	// 还没有消息 ⇒ 400
	if recorder := call(box.server, "POST", box.path+"/reroll-message", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("空会话该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// 让尾条变成 assistant（发一轮、等它落库）⇒ 这时才轮得到闸门那道闸
	box.send(t, "第一句话")
	box.waitPhase(t, box.sessionID, "idle")

	// 生成中 ⇒ 409（闸门占着）
	if _, err := box.server.chat.Turns.Begin(box.sessionID, "reply"); err != nil {
		t.Fatal(err)
	}
	if recorder := call(box.server, "POST", box.path+"/reroll-message", ""); recorder.Code != http.StatusConflict {
		t.Fatalf("生成中重摇该 409，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	box.server.chat.Turns.Stop(box.sessionID)

	// 尾条不是 assistant（再插一句用户消息）⇒ 400，且说清那是「重发」
	ids, err := store.MintOrderedIDs(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.store.InsertMessage(model.Message{
		ID: ids[0], SessionID: box.sessionID, Role: model.RoleUser, Content: "只说了这一句"}); err != nil {
		t.Fatal(err)
	}
	recorder := call(box.server, "POST", box.path+"/reroll-message", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("尾条是用户消息该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "重发") {
		t.Fatalf("该明说那是「重发」不是重摇：%s", body)
	}

	// 没进模式：切/删 ⇒ 404；位次不是正整数 ⇒ 400；缺 idx ⇒ 422
	if recorder = call(box.server, "POST", box.path+"/reroll-message/switch", `{"idx":1}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("没进模式该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "DELETE", box.path+"/reroll-message/1", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("没进模式该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "DELETE", box.path+"/reroll-message/abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("位次不是正整数该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "POST", box.path+"/reroll-message/switch", `{}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺 idx 该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// 重摇在跑时发消息 ⇒ 409（说得出是哪一件在跑）
	if _, err := box.server.chat.Turns.BeginReroll(box.sessionID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"content": "插队"})
	if recorder = call(box.server, "POST", box.path+"/messages", string(body)); recorder.Code != http.StatusConflict {
		t.Fatalf("重摇期间发消息该 409，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "重摇") {
		t.Fatalf("409 该说清是重摇在跑：%s", recorder.Body.String())
	}
	// 同一趟车上还有 `reroll` 那一档（与 `compact` 同一条口径）—— 别的客户端据此知道"有活在跑"
	recorder = call(box.server, "GET", box.path+"/status", "")
	if body := recorder.Body.String(); !strings.Contains(body, `"reroll":{"state":"running"`) {
		t.Fatalf("/status 该带上 reroll 那一档：%s", body)
	}

	// 旧的 `/reroll*` 五条**已删**（开发阶段不做别名/兼容）⇒ 一律 404
	for _, probe := range []struct{ method, suffix string }{
		{"POST", "/reroll"}, {"GET", "/reroll"}, {"DELETE", "/reroll"},
		{"POST", "/reroll/switch"}, {"DELETE", "/reroll/1"},
	} {
		if recorder = call(box.server, probe.method, box.path+probe.suffix, `{"idx":1}`); recorder.Code != http.StatusNotFound {
			t.Fatalf("旧路由 %s %s 该 404，得到 %d：%s", probe.method, probe.suffix, recorder.Code, recorder.Body.String())
		}
	}

	// 不存在的会话 ⇒ 404
	if recorder = call(box.server, "GET", "/api/v1/sessions/no-such-session/reroll-message", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("会话不存在该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// ── 摘要重摇（`/reroll-summary`）────────────────────────────────────────────

// summaryRoller：`reroll.SummaryGenerator` 的适配器 —— 形状照 `main.go` 那个同名件
// （本任务只许碰这三个文件 ⇒ 适配器就地放测试里，把 server 手里那个 compact 接给 reroll）。
type summaryRoller struct{ compact *compact.Service }

func (r summaryRoller) RegenerateSummary(
	ctx context.Context, session model.Session, summaryID string) (reroll.GeneratedSummary, error) {
	generated, err := r.compact.RegenerateSummary(ctx, session, summaryID)
	if err != nil {
		return reroll.GeneratedSummary{}, err
	}
	return reroll.GeneratedSummary{
		Text: generated.Text, Tokens: generated.Tokens, Provider: generated.Provider,
		Model: generated.Model, PromptVersion: generated.PromptVersion, Usage: generated.Usage,
	}, nil
}

// withSummaryReroll：把摘要那一趟真接上（`newDummySandbox` 只接了消息那一趟）。
func (b *dummySandbox) withSummaryReroll() *dummySandbox {
	b.server.rerolls.Summaries = summaryRoller{compact: b.server.compact}
	return b
}

// seedRootSummary：造一条盖住 [beginIdx, endIdx]（1-based）的**消息级根摘要** —— 摘要重摇的目标。
func (b *dummySandbox) seedRootSummary(t *testing.T, beginIdx, endIdx int, text string) model.Summary {
	t.Helper()
	messages := b.messages(t)
	ids, err := store.MintOrderedIDs(1)
	if err != nil {
		t.Fatal(err)
	}
	sourceIDs := make([]string, 0, endIdx-beginIdx+1)
	for _, message := range messages[beginIdx-1 : endIdx] {
		sourceIDs = append(sourceIDs, message.ID)
	}
	summary := model.Summary{
		ID: ids[0], SessionID: b.sessionID, SourceKind: model.SourceMessages,
		BeginMessageID: new(messages[beginIdx-1].ID), EndMessageID: new(messages[endIdx-1].ID),
		Text: text, Blocks: 1, Tokens: int64(len([]rune(text))), SourceIDs: sourceIDs,
		Provider: "dummy", Model: "dummy", PromptVersion: 1, CreatedAt: 1,
	}
	if err := b.store.RecordSummary(summary, sourceIDs); err != nil {
		t.Fatal(err)
	}
	return summary
}

// summaryState：GET .../reroll-summary。
func (b *dummySandbox) summaryState(t *testing.T) reroll.State {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/reroll-summary", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("取摘要重摇状态该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var state reroll.State
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// settleSummary：等这一趟摘要摇完（受理那一刻候选还在摇）。
func (b *dummySandbox) settleSummary(t *testing.T) reroll.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := b.summaryState(t)
		if !state.Running {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("摘要这一版一直没摇完")
	return reroll.State{}
}

// summaryText：某条摘要**此刻库里**的正文（重摇 apply 的落点）。
func (b *dummySandbox) summaryText(t *testing.T, summaryID string) string {
	t.Helper()
	summaries, err := b.store.ListSummaries(b.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range summaries {
		if summary.ID == summaryID {
			return summary.Text
		}
	}
	t.Fatalf("库里找不到摘要 %s：%+v", summaryID, summaries)
	return ""
}

// TestRerollSummaryContract：摘要家族走一遍 —— 202 受理（目标 / 区间说清）、状态、切换（换正文）、
// 按位次删（只剩一版 ⇒ 退出模式且不 apply）、显式退出 204。
func TestRerollSummaryContract(t *testing.T) {
	box := newDummySandbox(t).withSummaryReroll()
	box.send(t, "第一句话")
	box.waitPhase(t, box.sessionID, "idle")
	if len(box.messages(t)) != 2 {
		t.Fatalf("先要有一轮对话：%+v", box.messages(t))
	}
	root := box.seedRootSummary(t, 1, 2, "旧的前情提要")

	// 受理：202 + 【原文 + 正在摇的一版】，目标与它盖的区间都说清
	recorder := call(box.server, "POST", box.path+"/reroll-summary", `{"idx":1}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("摘要重摇该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var accepted reroll.Accepted
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TargetSummaryID != root.ID || accepted.TaskID == "" {
		t.Fatalf("受理回执该指明摇的是哪条摘要：%+v（目标 %s）", accepted, root.ID)
	}
	if state := accepted.State; state.TargetKind != reroll.TargetSummary || !state.Active ||
		state.TargetSummaryID != root.ID || state.Count != 2 || state.CurrentIdx != 1 {
		t.Fatalf("进模式那一刻该是【原文 + 正在摇的一版】：%+v", state)
	}
	if accepted.State.FromIdx == nil || *accepted.State.FromIdx != 1 ||
		accepted.State.ToIdx == nil || *accepted.State.ToIdx != 2 {
		t.Fatalf("该给出目标盖的区间（1–2）：%+v", accepted.State)
	}
	if !accepted.State.Items[1].Pending {
		t.Fatalf("第 2 版那一刻还在摇：%+v", accepted.State.Items)
	}

	// 摇完：两条实打实的候选，当前那版还是原文
	state := box.settleSummary(t)
	if state.Count != 2 || state.Items[1].Pending || state.Items[1].Preview == "" {
		t.Fatalf("摇完该是两条候选：%+v", state)
	}
	if state.TargetKind != reroll.TargetSummary || state.TargetSummaryID != root.ID ||
		state.FromIdx == nil || *state.FromIdx != 1 || state.ToIdx == nil || *state.ToIdx != 2 {
		t.Fatalf("摘要模式的 state 该带目标与区间：%+v", state)
	}
	if !state.Items[0].Current {
		t.Fatalf("没切之前，当前那版还是原文：%+v", state.Items)
	}

	// 切到第 2 版：**id 与覆盖区间一概不动**，只换正文
	if recorder = call(box.server, "POST", box.path+"/reroll-summary/switch", `{"idx":2}`); recorder.Code != http.StatusOK {
		t.Fatalf("切换该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	rolled := box.summaryText(t, root.ID)
	if rolled == root.Text || !strings.Contains(rolled, "（测试用假摘要）") {
		t.Fatalf("第 2 版该是 dummy 摇出来的那句：%q（原文 %q）", rolled, root.Text)
	}

	// 切回第 1 版：正文换回原文（**不保护原文，但它就是第一版**）
	if recorder = call(box.server, "POST", box.path+"/reroll-summary/switch", `{"idx":1}`); recorder.Code != http.StatusOK {
		t.Fatalf("切回第 1 版该 200：%s", recorder.Body.String())
	}
	if back := box.summaryText(t, root.ID); back != root.Text {
		t.Fatalf("第 1 版该是原文：%q", back)
	}

	// 删到只剩一版 ⇒ 退出模式 + 清列表，且摘要保留当前这版（不得回原文）
	if recorder = call(box.server, "DELETE", box.path+"/reroll-summary/1", ""); recorder.Code != http.StatusOK {
		t.Fatalf("删第 1 版该 200：%s", recorder.Body.String())
	}
	if state = box.summaryState(t); state.Active || state.Count != 0 {
		t.Fatalf("只剩一条 ⇒ 退出模式 + 清列表：%+v", state)
	}
	if kept := box.summaryText(t, root.ID); kept != root.Text {
		t.Fatalf("退出这条路上【不 apply】：摘要该保留当前这版，得到 %q", kept)
	}

	// 显式退出（`DELETE .../reroll-summary`）：没在模式里也 204（幂等）
	if recorder = call(box.server, "DELETE", box.path+"/reroll-summary", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("退出摘要重摇该 204，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if state = box.summaryState(t); state.Active {
		t.Fatalf("退出之后该是没进模式：%+v", state)
	}
}

// TestRerollSummaryRefusals：摘要家族的拒绝口径 —— 缺 idx ⇒ 400、越界 / 没盖住 ⇒ 400、
// 没进模式 ⇒ 404、位次不是正整数 ⇒ 400、switch 缺 idx ⇒ 422（与消息家族同形）、未知会话 ⇒ 404。
func TestRerollSummaryRefusals(t *testing.T) {
	box := newDummySandbox(t).withSummaryReroll()
	box.send(t, "第一句话")
	box.waitPhase(t, box.sessionID, "idle")

	// 缺 idx ⇒ 400，且说清要什么（不是 422：这条路的语义错统一按 400 回）
	recorder := call(box.server, "POST", box.path+"/reroll-summary", `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 idx 该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "给 idx") {
		t.Fatalf("该说清要重摇哪一条：%s", body)
	}

	// 还没有摘要盖住这一条 ⇒ 400（引擎出的措辞）
	if recorder = call(box.server, "POST", box.path+"/reroll-summary", `{"idx":1}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("没被摘要盖住该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	} else if body := recorder.Body.String(); !strings.Contains(body, "摘要盖住") {
		t.Fatalf("该说清是没被摘要盖住：%s", body)
	}

	// 位次越界 ⇒ 400
	if recorder = call(box.server, "POST", box.path+"/reroll-summary", `{"idx":9}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("idx 越界该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	// 失败的受理不该留下模式
	if state := box.summaryState(t); state.Active {
		t.Fatalf("失败的受理不该留下模式：%+v", state)
	}

	// 有摘要，但那条 idx 不在任何摘要里 ⇒ 400
	box.seedRootSummary(t, 1, 2, "旧的前情提要")
	box.send(t, "第二句话")
	box.waitPhase(t, box.sessionID, "idle")
	if recorder = call(box.server, "POST", box.path+"/reroll-summary", `{"idx":3}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("没被摘要盖住该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	} else if body := recorder.Body.String(); !strings.Contains(body, "摘要盖住") {
		t.Fatalf("该说清是没被摘要盖住：%s", body)
	}

	// 没进模式：切 / 删 ⇒ 404；位次不是正整数 ⇒ 400；switch 缺 idx ⇒ 422
	if recorder = call(box.server, "POST", box.path+"/reroll-summary/switch", `{"idx":1}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("没进模式该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "DELETE", box.path+"/reroll-summary/1", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("没进模式该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "DELETE", box.path+"/reroll-summary/abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("位次不是正整数该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "POST", box.path+"/reroll-summary/switch", `{}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺 idx 该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// 不存在的会话 ⇒ 404（三条先要会话的路由）
	for _, probe := range []struct{ method, path string }{
		{"POST", "/api/v1/sessions/no-such-session/reroll-summary"},
		{"GET", "/api/v1/sessions/no-such-session/reroll-summary"},
		{"DELETE", "/api/v1/sessions/no-such-session/reroll-summary"},
	} {
		if recorder = call(box.server, probe.method, probe.path, `{"idx":1}`); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s 该 404，得到 %d：%s", probe.method, probe.path, recorder.Code, recorder.Body.String())
		}
	}
}
