package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/providers"
	"microchat/internal/state"
)

// ── (c) `POST /sessions/{session_id}/outgoing`：把待发那句追加进去之后，真会发出去的那一发 ──
//
// 三件事别混：**(a)** `GET /debug/last-payload` = 上一次真发出去的那一发（快照、不可重算）；
// **(b)** `GET /outgoing` = 当前已定历史的载荷（**不含待发那句**，给人看的形状）；
// **(c)** `POST /outgoing` = 真请求（method/url/headers/体 —— 与 (a) 同一支笔 `Snapshot`）。
//
// (c) 说的是"发出去什么"，不是"库里有什么" ⇒ 它**不走** `state.Outgoing` 那套给人看的形状：
// 调试工具必须说真话 —— 体里只有上游要的 `role/content`（外加思考回传字段），
// `message_id`/`idx`/`pending`/`type` 这些库内账一个都不发（`wireMessages` 早就丢了它们）。

// mustQuote：把任意文本塞进 JSON 字符串（`<state>` 里有换行，手拼会拼坏）。
func mustQuote(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// outgoingOf：(b) 的 GET 形状（给人看的逐项载荷；(c) 改调 wireOf）。
func (b *dummySandbox) outgoingOf(t *testing.T, method, content string) []state.Outgoing {
	t.Helper()
	if method != "GET" {
		t.Fatalf("outgoingOf 只走 GET（(c) 改调 wireOf）：%s", method)
	}
	recorder := call(b.server, "GET", b.path+"/outgoing", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s/outgoing 该 200，得到 %d：%s", b.path, recorder.Code, recorder.Body.String())
	}
	var outgoing []state.Outgoing
	if err := json.Unmarshal(recorder.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	return outgoing
}

// wireOf：拉一次真请求（content 为空 ⇒ 400，见 TestOutgoingWireRejectsBlank）。
func (b *dummySandbox) wireOf(t *testing.T, content string) providers.LastPayload {
	t.Helper()
	body := `{"content":` + mustQuote(t, content) + `}`
	recorder := call(b.server, "POST", b.path+"/outgoing", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST %s/outgoing 该 200，得到 %d：%s", b.path, recorder.Code, recorder.Body.String())
	}
	var payload providers.LastPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// 基本形状：(c) 的体 = (b) 的正文 + 待发那句（末尾多一条 user；库内账一个不发）。
func TestOutgoingWireAppendsUserMessage(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")

	before := box.outgoingOf(t, "GET", "")
	payload := box.wireOf(t, "你好")
	if payload.Method != "POST" || !strings.HasSuffix(payload.URL, "/chat/completions") {
		t.Fatalf("(c) 该是打上游的那一发：%s %s", payload.Method, payload.URL)
	}
	messages := wireBody(t, payload)
	if len(messages) != len(before)+1 {
		t.Fatalf("(c) 该比 (b) 正好多一条：%d vs %d", len(messages), len(before))
	}
	for i, item := range before {
		if messages[i]["role"] != string(item.Role) || messages[i]["content"] != item.Content {
			t.Fatalf("第 %d 条对不上 (b)：%v vs %+v", i, messages[i], item)
		}
	}
	last := messages[len(messages)-1]
	if last["role"] != "user" || last["content"] != "你好" {
		t.Fatalf("末尾该是待发那句：%v", last)
	}
	raw := string(payload.Body)
	for _, banned := range []string{"message_id", "pending", `"type"`, "from_idx", "to_idx"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("体里不许有库内账 %q：%s", banned, raw)
		}
	}
}

// wireBody：真请求体的 messages（上游要的形状：role/content 数组）。
func wireBody(t *testing.T, payload providers.LastPayload) []map[string]any {
	t.Helper()
	var body struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
		Stream   bool             `json:"stream"`
	}
	if err := json.Unmarshal(payload.Body, &body); err != nil {
		t.Fatalf("体不是 chat 请求：%s", payload.Body)
	}
	return body.Messages
}

// 核心：待发那句里的 `<state>` 块**真的重算状态表** ——
// (c) 的第一条 system 里有新值，(b) 里没有（拿掉"重算"这一步就该红）。
func TestOutgoingWireRecomputesState(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")

	messages := wireBody(t, box.wireOf(t, "<state>\nHP = 7\n</state>"))
	if len(messages) == 0 || messages[0]["role"] != "system" {
		t.Fatalf("第一条该是 system：%v", messages)
	}
	if got, _ := messages[0]["content"].(string); !containsText(got, "HP = 7") {
		t.Fatalf("(c) 的 system 该出现 HP = 7（状态表只在这一轮被重算）：%q", got)
	}
	// 整句就是 `<state>` 块 ⇒ 剔除后为空 ⇒ 体里不许多出一条带标签的 user 消息
	for _, m := range messages {
		if c, _ := m["content"].(string); containsText(c, "<state>") {
			t.Fatalf("标签不许出现在体里：%v", m)
		}
	}
}

func containsText(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// content 空白 / 缺失 ⇒ 400（固定错误体）；会话不存在 ⇒ 404。
func TestOutgoingWireRejectsBlank(t *testing.T) {
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
func TestOutgoingWireDoesNotWrite(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")
	count := len(box.messages(t))

	for _, content := range []string{"你好", "<state>\nHP = 7\n</state>", "第三句"} {
		box.wireOf(t, content)
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
	if len(items) == 0 || items[0].Type != "system" {
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
		if item.Type != "message" {
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

	// (c)：体 = (b) 的 system + 六条正文 + 待发那句（(b) 长度含 system 那条）
	messages := wireBody(t, box.wireOf(t, "第四句"))
	if len(messages) != len(items)+1 {
		t.Fatalf("(c) 该比 (b) 多一条：%d vs %d", len(messages), len(indexOf))
	}
	last := messages[len(messages)-1]
	if last["role"] != "user" || last["content"] != "第四句" {
		t.Fatalf("末尾该是待发那句：%v", last)
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
		if item.Type == "summary" {
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

// children：一父两子只展一层（只 id+idx，不给正文）；叶子是空数组；pending 原样过。
func TestOutgoingSummaryHasChildren(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":2}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理压缩该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	box.waitCompact(t)

	var found *state.Outgoing
	for _, item := range box.outgoingOf(t, "GET", "") {
		if item.Type == "summary" {
			cp := item
			found = &cp
		}
	}
	if found == nil {
		t.Fatal("该正好一条摘要")
	}
	if found.Children == nil || len(found.Children) == 0 {
		t.Fatalf("摘要该带 children：%+v", found)
	}
	for _, child := range found.Children {
		if child.Content != "" {
			t.Fatalf("孩子只给 id+idx，不给正文：%+v", child)
		}
		if child.Type == "message" && (child.MessageID == nil || child.Idx == nil) {
			t.Fatalf("消息孩子该带 id+idx：%+v", child)
		}
		if len(child.Children) != 0 {
			t.Fatalf("只展一层：%+v", child)
		}
	}
	// (c) 体里摘要照旧是一条（真请求不展 children 那套给人看的形状）
	messages := wireBody(t, box.wireOf(t, "第四句"))
	if messages[len(messages)-1]["role"] != "user" {
		t.Fatalf("末尾该是待发那句：%v", messages)
	}
}
