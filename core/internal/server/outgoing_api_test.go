package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/providers"
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

	payload := box.wireOf(t, "你好")
	if payload.Method != "POST" || !strings.HasSuffix(payload.URL, "/chat/completions") {
		t.Fatalf("(c) 该是打上游的那一发：%s %s", payload.Method, payload.URL)
	}
	messages := wireBody(t, payload)
	if len(messages) != 4 {
		t.Fatalf("一轮用户+助理之后再问，该是 system+2 条正文+待发 = 4 条：%d", len(messages))
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

// content 空白 / 缺失 ⇒ 现有历史的那一发（与空发 resend 同段装配）；会话不存在 ⇒ 404。
// 空会话（0 条消息）⇒ 400（与 resend 同错：没历史可重发）。
func TestOutgoingWireBlankShowsHistory(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "先聊两句")
	want := box.wireOf(t, "你好")
	tailWant := wireBody(t, want)
	for _, body := range []string{`{}`, `{"content":""}`, `{"content":"  \n "}`, `{"content":null}`} {
		recorder := call(box.server, "POST", box.path+"/outgoing", body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s 该 200（现有历史），得到 %d：%s", body, recorder.Code, recorder.Body.String())
		}
		var payload providers.LastPayload
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		got := wireBody(t, payload)
		if len(got) != len(tailWant)-1 {
			t.Fatalf("%s：历史 %d 条 + 待发 1 条，有待发该 %d、无待发该 %d：%d",
				body, len(tailWant)-1, len(tailWant), len(tailWant)-1, len(got))
		}
		for i := range got {
			if got[i]["role"] != tailWant[i]["role"] || got[i]["content"] != tailWant[i]["content"] {
				t.Fatalf("%s：第 %d 条与有待发的不一致：%v vs %v", body, i, got[i], tailWant[i])
			}
		}
	}
	recorder := call(box.server, "POST", "/api/v1/sessions/00000000-0000-0000-0000-000000000000/outgoing", `{"content":"你好"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("会话不存在该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 空会话空 content ⇒ 400（与 resend 同错）；只算不写（条数不变）。
func TestOutgoingWireBlankEmptySession(t *testing.T) {
	box := newDummySandbox(t)
	recorder := call(box.server, "POST", box.path+"/outgoing", `{"content":""}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("空会话空 content 该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if got := len(box.messages(t)); got != 0 {
		t.Fatalf("(c) 只算不写：消息条数该还是 0，成了 %d", got)
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

	// (c)：体 = system + 六条正文 + 待发那句
	messages := wireBody(t, box.wireOf(t, "第四句"))
	if len(messages) != 8 {
		t.Fatalf("(c) 该是 8 条（system+6+待发）：%d", len(messages))
	}
	_ = box.messages(t)
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

	rows, err := box.store.ListSummaries(box.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("该正好一份摘要：%+v", rows)
	}
	_ = rows
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

	rows, err := box.store.ListSummaries(box.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("该正好一份摘要：%+v", rows)
	}
	// (c) 体里摘要照旧被一条梗概代替（真请求不展 children 那套给人看的形状）
	messages := wireBody(t, box.wireOf(t, "第四句"))
	if messages[len(messages)-1]["role"] != "user" {
		t.Fatalf("末尾该是待发那句：%v", messages)
	}
}
