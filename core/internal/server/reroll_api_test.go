package server

// 重摇那几条路由的契约：202 受理两条、切换（UUID 不变）、按位次删（后方位次前移、退出模式时保留当前那版）、
// 以及四条错误口径（尾条不是 assistant ⇒ 400 / 闸门占着 ⇒ 409 / 位次越界 ⇒ 400 / 没进模式 ⇒ 404）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"microchat/internal/model"
	"microchat/internal/reroll"
	"microchat/internal/store"
)

// rerollEnter：POST .../reroll ⇒ 202 + 受理回执。
func (b *dummySandbox) rerollEnter(t *testing.T) reroll.Accepted {
	t.Helper()
	recorder := call(b.server, "POST", b.path+"/reroll", "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("重摇该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var accepted reroll.Accepted
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted
}

// rerollState：GET .../reroll。
func (b *dummySandbox) rerollState(t *testing.T) reroll.State {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/reroll", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("取重摇状态该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var state reroll.State
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// settleReroll：等这一次摇完（候选在后台摇，受理那一刻还看不到）。
func (b *dummySandbox) settleReroll(t *testing.T) reroll.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := b.rerollState(t)
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
	accepted := box.rerollEnter(t)
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
	state := box.settleReroll(t)
	if state.Count != 2 || state.Items[1].Pending || state.Items[1].Preview == "" {
		t.Fatalf("摇完该是两条实打实的候选：%+v", state)
	}
	if state.Items[0].Current != true {
		t.Fatalf("没切之前，当前那版还是原文：%+v", state.Items)
	}

	// 切到第 2 版：**UUID 不变**、正文变成摇出来的那句
	if recorder := call(box.server, "POST", box.path+"/reroll/switch", `{"idx":2}`); recorder.Code != http.StatusOK {
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
	if recorder := call(box.server, "POST", box.path+"/reroll/switch", `{"idx":1}`); recorder.Code != http.StatusOK {
		t.Fatalf("切回第 1 版该 200：%s", recorder.Body.String())
	}
	if restored := box.lastMessage(t); restored.Content != tail.Content {
		t.Fatalf("第 1 版该是原文：%q", restored.Content)
	}

	// 再摇一版 ⇒ 三条（`‹ 2/3 ›` 那个分母的来源）
	if accepted = box.rerollEnter(t); accepted.State.Count != 3 {
		t.Fatalf("再摇一版该是三条：%+v", accepted.State)
	}
	if state = box.settleReroll(t); state.Count != 3 {
		t.Fatalf("三条候选：%+v", state)
	}

	// 切到第 3 版，再删掉原文那条 ⇒ 后方位次前移，当前那一版**还是同一版**
	if recorder := call(box.server, "POST", box.path+"/reroll/switch", `{"idx":3}`); recorder.Code != http.StatusOK {
		t.Fatalf("切到第 3 版该 200：%s", recorder.Body.String())
	}
	if recorder := call(box.server, "DELETE", box.path+"/reroll/1", ""); recorder.Code != http.StatusOK {
		t.Fatalf("删第 1 版该 200：%s", recorder.Body.String())
	}
	if state = box.rerollState(t); state.Count != 2 || state.CurrentIdx != 2 {
		t.Fatalf("删掉原文那版之后该是两条、当前那版前移到第 2 位：%+v", state)
	}
	if state.Items[0].Idx != 1 || state.Items[1].Idx != 2 {
		t.Fatalf("后方位次该统一 -1：%+v", state.Items)
	}
	if applied = box.lastMessage(t); applied.Content != "（测试用空模型·重摇）" {
		t.Fatalf("apply 该落在同号那一位（还是摇出来的那版）：%q", applied.Content)
	}

	// 再删一条 ⇒ 只剩一条 ⇒ **退出模式 + 清列表**，且 message 保留当前这版（不得回原文）
	if recorder := call(box.server, "DELETE", box.path+"/reroll/2", ""); recorder.Code != http.StatusOK {
		t.Fatalf("删第 2 版该 200：%s", recorder.Body.String())
	}
	if state = box.rerollState(t); state.Active || state.Count != 0 {
		t.Fatalf("只剩一条 ⇒ 退出模式 + 清列表：%+v", state)
	}
	if applied = box.lastMessage(t); applied.Content != "（测试用空模型·重摇）" {
		t.Fatalf("退出这条路上【不 apply】：message 该保留当前这版，得到 %q", applied.Content)
	}
	if applied.ID != tail.ID {
		t.Fatalf("UUID 自始至终不变：%s", applied.ID)
	}

	// 显式退出（`DELETE .../reroll`）：没在模式里也 204（幂等）
	if recorder := call(box.server, "DELETE", box.path+"/reroll", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("退出重摇该 204，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// **一发出新消息 ⇒ 候选全清**（message 保留当前那版）
	box.rerollEnter(t)
	box.settleReroll(t)
	box.send(t, "接着聊")
	if state = box.rerollState(t); state.Active || state.Count != 0 {
		t.Fatalf("新消息一到 ⇒ 候选全清：%+v", state)
	}
}

func TestRerollRefusals(t *testing.T) {
	box := newDummySandbox(t)

	// 还没有消息 ⇒ 400
	if recorder := call(box.server, "POST", box.path+"/reroll", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("空会话该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// 让尾条变成 assistant（发一轮、等它落库）⇒ 这时才轮得到闸门那道闸
	box.send(t, "第一句话")
	box.waitPhase(t, box.sessionID, "idle")

	// 生成中 ⇒ 409（闸门占着）
	if _, err := box.server.chat.Turns.Begin(box.sessionID, "reply"); err != nil {
		t.Fatal(err)
	}
	if recorder := call(box.server, "POST", box.path+"/reroll", ""); recorder.Code != http.StatusConflict {
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
	recorder := call(box.server, "POST", box.path+"/reroll", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("尾条是用户消息该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "重发") {
		t.Fatalf("该明说那是「重发」不是重摇：%s", body)
	}

	// 没进模式：切/删 ⇒ 404；位次不是正整数 ⇒ 400；缺 idx ⇒ 422
	if recorder = call(box.server, "POST", box.path+"/reroll/switch", `{"idx":1}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("没进模式该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "DELETE", box.path+"/reroll/1", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("没进模式该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "DELETE", box.path+"/reroll/abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("位次不是正整数该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(box.server, "POST", box.path+"/reroll/switch", `{}`); recorder.Code != http.StatusUnprocessableEntity {
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

	// 不存在的会话 ⇒ 404
	if recorder = call(box.server, "GET", "/api/v1/sessions/no-such-session/reroll", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("会话不存在该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}
