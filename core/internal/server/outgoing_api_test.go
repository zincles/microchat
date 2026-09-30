package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/state"
)

// ── (c) `POST /sessions/{session_id}/outgoing`：把待发那句追加进去之后，真会发出去的东西 ──
//
// 三件事别混：**(a)** `GET /debug/last-payload` = 上一次真发出去的那一发（快照、不可重算）；
// **(b)** `GET /outgoing` = 当前已定历史的载荷（**不含待发那句**）；
// **(c)** `POST /outgoing` = 把这条 content 当成即将追加的那句用户消息之后（**只算不写**）。

// outgoingOf：拉一次出站载荷（`content` 为空 ⇒ 走 (b) 的 GET）。
func (b *dummySandbox) outgoingOf(t *testing.T, method, content string) []state.Outgoing {
	t.Helper()
	body := ""
	path := b.path + "/outgoing"
	if method == "POST" {
		body = `{"content":` + mustQuote(t, content) + `}`
	}
	recorder := call(b.server, method, path, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s 该 200，得到 %d：%s", method, path, recorder.Code, recorder.Body.String())
	}
	var outgoing []state.Outgoing
	if err := json.Unmarshal(recorder.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	return outgoing
}

// mustQuote：把任意文本塞进 JSON 字符串（`<state>` 里有换行，手拼会拼坏）。
func mustQuote(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 基本形状：(c) 与 (b) **逐项同字段**，且末尾**多出**那条 content 对应的 user 项。
func TestOutgoingWithPendingAppendsUserMessage(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")

	before := box.outgoingOf(t, "GET", "")
	after := box.outgoingOf(t, "POST", "你好")

	if len(after) != len(before)+1 {
		t.Fatalf("(c) 该比 (b) 正好多一条：%d vs %d\n%+v", len(after), len(before), after)
	}
	// 前 len(before) 条逐字段一样（(c) 只是把一句追加进去了）
	beforeJSON, _ := json.Marshal(before)
	afterHeadJSON, _ := json.Marshal(after[:len(before)])
	if string(beforeJSON) != string(afterHeadJSON) {
		t.Fatalf("(c) 的前 %d 条该与 (b) 逐字段一致：\n%s\n%s", len(before), beforeJSON, afterHeadJSON)
	}
	last := after[len(after)-1]
	if last.Role != state.RoleUser || last.Content != "你好" || last.Source != "message" {
		t.Fatalf("末尾该是那条待发的 user 消息：%+v", last)
	}
	// 同字段（不是"少几个字段的另一种形状"）
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"role", "content", "source", "message_id"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("末尾那条缺字段 %q：%s", name, raw)
		}
	}
	// (b) 自己不带那句（这正是两者的分别）
	for _, item := range before {
		if item.Role == state.RoleUser && item.Content == "你好" {
			t.Fatalf("(b) 不该含待发那句（它还没进库）：%+v", before)
		}
	}
	// 待发那条带 `pending: true`：它的 message_id 是**预测值**（真发时另铸）⇒ 别被误会成能查的 id
	if !last.Pending {
		t.Fatalf("待发那条该带 pending 标记：%+v", last)
	}
	if _, ok := fields["pending"]; !ok {
		t.Fatalf("形状里该多出 pending 这一格：%s", raw)
	}
	// 反过来：(b) 里一个 pending 都不许有（它每一条都真在库里）
	if strings.Contains(string(beforeJSON), `"pending"`) {
		t.Fatalf("(b) 里不该出现 pending：%s", beforeJSON)
	}
	for _, item := range before {
		if item.Pending {
			t.Fatalf("(b) 里不该有待发项：%+v", item)
		}
	}
}

// 核心：待发那句里的 `<state>` 块**真的重算状态表** ——
// (c) 的第一条 system 里有新值，(b) 里没有（拿掉"重算"这一步就该红）。
func TestOutgoingWithPendingRecomputesState(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")

	before := box.outgoingOf(t, "GET", "")
	after := box.outgoingOf(t, "POST", "<state>\nHP = 7\n</state>")

	systemOf := func(items []state.Outgoing) string {
		if len(items) == 0 || items[0].Role != state.RoleSystem {
			t.Fatalf("第一条该是 system：%+v", items)
		}
		return items[0].Content
	}
	if got := systemOf(before); containsText(got, "HP = 7") {
		t.Fatalf("(b) 不该有 HP = 7（那句还没发）：%q", got)
	}
	if got := systemOf(after); !containsText(got, "HP = 7") {
		t.Fatalf("(c) 的 system 该出现 HP = 7（状态表只在这一轮被重算）：%q", got)
	}
	// 整句就是 `<state>` 块 ⇒ 剔除后为空 ⇒ 不往上游发这句（与真发一个口径）：
	// 影响全在 system 那张表上。
	for _, item := range after {
		if containsText(item.Content, "<state>") {
			t.Fatalf("标签不许出现在出站里：%+v", item)
		}
	}
	if len(after) != len(before) {
		t.Fatalf("整句都是 <state> 块时不该多出一条消息：%d vs %d", len(after), len(before))
	}
}

func containsText(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// content 空白 / 缺失 ⇒ 400（固定错误体）；会话不存在 ⇒ 404。
func TestOutgoingWithPendingRejectsBlank(t *testing.T) {
	box := newDummySandbox(t)
	for _, body := range []string{`{}`, `{"content":""}`, `{"content":"  \n "}`, `{"content":null}`} {
		recorder := call(box.server, "POST", box.path+"/outgoing", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 该 400，得到 %d：%s", body, recorder.Code, recorder.Body.String())
		}
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.Error.Code != "invalid" || failure.Error.Message == "" {
			t.Fatalf("%s 的错误体不合契约：%s", body, recorder.Body.String())
		}
	}
	recorder := call(box.server, "POST", "/api/v1/sessions/00000000-0000-0000-0000-000000000000/outgoing", `{"content":"你好"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("会话不存在该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 只算不写：调完 (c) 后，库里的消息条数一个不变。
func TestOutgoingWithPendingDoesNotWrite(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")
	count := len(box.messages(t))

	for _, content := range []string{"你好", "<state>\nHP = 7\n</state>", "第三句"} {
		box.outgoingOf(t, "POST", content)
	}
	if got := len(box.messages(t)); got != count {
		t.Fatalf("(c) 只算不写：消息条数该还是 %d，成了 %d", count, got)
	}
}

// ── 出站每一项的 `idx` ────────────────────────────────────────────────────
//
// `system` ⇒ **0**（合成项：它不是消息，消息从 1 起）；`message` ⇒ 它自己那条的序号
// （与 `/messages` 里的 `idx` 同一个号）；`summary` ⇒ 它**替代的**那段范围（from_idx/to_idx）。

// 三档 source 各自的 `idx`（含合成项的 0），以及 (c) 那条待发项 = **下一条**（现有最大 + 1）。
func TestOutgoingCarriesIndexes(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句") // 消息 1、2
	box.seedTurn(t, "第二句") // 消息 3、4
	box.seedTurn(t, "第三句") // 消息 5、6

	items := box.outgoingOf(t, "GET", "")
	if len(items) == 0 || items[0].Source != "system" {
		t.Fatalf("第一条该是 system：%+v", items)
	}
	// 合成项：0（它不是消息）
	if items[0].Idx == nil || *items[0].Idx != 0 {
		t.Fatalf("系统提示词那项该是 idx 0（合成的）：%+v", items[0])
	}
	if items[0].FromIdx != nil || items[0].ToIdx != nil {
		t.Fatalf("system 不是摘要 ⇒ 不该带范围：%+v", items[0])
	}

	// message 项：序号与 `/messages` 里同一条的 `idx` **逐条对得上**
	indexOf := map[string]int{}
	for _, message := range box.messages(t) {
		indexOf[message.ID] = message.Idx
	}
	seen := 0
	for _, item := range items {
		if item.Source != "message" {
			continue
		}
		seen++
		if item.MessageID == nil || item.Idx == nil {
			t.Fatalf("message 项该带 message_id 与 idx：%+v", item)
		}
		if want := indexOf[*item.MessageID]; want != *item.Idx {
			t.Fatalf("消息 %s 在 /messages 里是 idx %d，在 /outgoing 里成了 %d", *item.MessageID, want, *item.Idx)
		}
	}
	if seen != len(indexOf) {
		t.Fatalf("六条消息都该在装配里：%d ≠ %d", seen, len(indexOf))
	}

	// (c)：待发那条 = **下一条**（现有最大 + 1），与它的 `pending: true` 一致
	next := len(indexOf) + 1
	after := box.outgoingOf(t, "POST", "第四句")
	last := after[len(after)-1]
	if !last.Pending || last.Idx == nil || *last.Idx != next {
		t.Fatalf("待发那条该是 idx %d 且 pending：%+v", next, last)
	}
	// (b) 里没有这个号（它还没进库）
	for _, item := range items {
		if item.Idx != nil && *item.Idx == next {
			t.Fatalf("(b) 里不该有第 %d 条的号：(b) 是已定历史：%+v", next, item)
		}
	}
}

// 摘要那一项报的是**它替代的范围**（不是它自己的号）：压掉前两块 ⇒ `from_idx`/`to_idx` = 1..4。
func TestOutgoingSummaryReportsItsSpan(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":2}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理压缩该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	box.waitCompact(t)

	summaries := []state.Outgoing{}
	for _, item := range box.outgoingOf(t, "GET", "") {
		if item.Source == "summary" {
			summaries = append(summaries, item)
		}
	}
	if len(summaries) != 1 {
		t.Fatalf("该正好一条摘要：%+v", summaries)
	}
	item := summaries[0]
	if item.FromIdx == nil || item.ToIdx == nil || *item.FromIdx != 1 || *item.ToIdx != 4 {
		t.Fatalf("摘要该报它替代的那一段（1..4）：%+v", item)
	}
	// 摘要**不是消息** ⇒ 它没有自己的 idx（有的话就说不清是"第几条"了）
	if item.Idx != nil {
		t.Fatalf("摘要不该有自己的 idx：%+v", item)
	}
}
