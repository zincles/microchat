package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/providers"
)

// ── 压缩那发的载荷预演：`POST /sessions/{session_id}/compact/payload` ─────────
//
// 这条口存在的唯一理由：**预演到的必须就是真发的那一发**。所以这里最重的一条不是"字段对不对"，
// 而是把预演的体与真跑一次压缩实际发出去的体**逐字节比**。

type payloadPreviewShape struct {
	Payload struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"payload"`
	FromIdx       int  `json:"from_idx"`
	ToIdx         int  `json:"to_idx"`
	Merged        bool `json:"merged"`
	Blocks        int  `json:"blocks"`
	Messages      int  `json:"messages"`
	MaterialChars int  `json:"material_chars"`
	StateBefore   bool `json:"state_before"`
	StateAfter    bool `json:"state_after"`
}

// previewPayload：调一次载荷预演，解出那一份形状。
func (b *dummySandbox) previewPayload(t *testing.T, body string) payloadPreviewShape {
	t.Helper()
	recorder := call(b.server, "POST", b.path+"/compact/payload", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("载荷预演该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var preview payloadPreviewShape
	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

// wireMaterial：从那一发请求体里把**材料**（第 2 条 user 的 content）整段抠出来。
func (p payloadPreviewShape) wireMaterial(t *testing.T) string {
	t.Helper()
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(p.Payload.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("压缩那一发该正好两条（system 模板 + user 材料），得到 %d 条", len(body.Messages))
	}
	if body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		t.Fatalf("两条该是 system 然后 user：%q / %q", body.Messages[0].Role, body.Messages[1].Role)
	}
	return body.Messages[1].Content
}

// 预演的体 == 真跑一次压缩发出去的体（**逐字节**）。
//
// 这是"预演能当证据"的全部意义：两个口径一旦分叉，用户照着预演排错就会排到不存在的方向上。
func TestCompactPayloadMatchesWhatARealCompactionSends(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	// 预演**不往证据簿里写**：`/debug/last-payload` 记的是"真发过什么"，预演不该污染它。
	// 它是**进程级**的一份（同包里别的测试可能已经留过一发）⇒ 比的是"有没有变"，不是"空不空"。
	before, hadBefore := providers.LastSent()
	preview := box.previewPayload(t, `{"blocks":2}`)
	after, hadAfter := providers.LastSent()
	if hadAfter != hadBefore || after.At != before.At {
		t.Fatalf("预演不该改写 last-payload，却改成了 %s %s", after.Method, after.URL)
	}

	if preview.FromIdx != 1 || preview.ToIdx != 4 || preview.Blocks != 2 || preview.Merged {
		t.Fatalf("预演该说压第 1–4 条（2 块、消息级）：%+v", preview)
	}
	if preview.Messages != 4 {
		t.Fatalf("这段该 4 条消息，得到 %d", preview.Messages)
	}
	if preview.Payload.Method != http.MethodPost || preview.Payload.URL == "" {
		t.Fatalf("载荷该是一发 POST + 有 url：%+v", preview.Payload)
	}
	if preview.MaterialChars <= 0 {
		t.Fatalf("材料字数该 > 0，得到 %d", preview.MaterialChars)
	}
	// 预演里就该看得见材料本身（用户问的正是"即将被压缩的文本"）
	material := preview.wireMaterial(t)
	if !strings.Contains(material, "【要压缩的这段对话】") || !strings.Contains(material, "第一句") {
		t.Fatalf("材料里该有抬头与正文：%q", material)
	}

	// 预演**只算不动**：包里还没有摘要
	if rows, err := box.store.ListSummaries(box.sessionID); err != nil || len(rows) != 0 {
		t.Fatalf("预演不该落摘要：%+v / %v", rows, err)
	}

	// 真跑同一趟
	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":2}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	box.waitCompact(t)

	last, ok := providers.LastSent()
	if !ok {
		t.Fatal("真压一趟之后该留下 last-payload")
	}
	if !bytes.Equal(bytes.TrimSpace(last.Body), bytes.TrimSpace(preview.Payload.Body)) {
		t.Fatalf("预演的体与真发的体不一致（预演就失去意义了）\n预演：%s\n真发：%s",
			preview.Payload.Body, last.Body)
	}
}

// 合并级：预演也要看得见"这次是把几坨梗概并起来"（不是原文）—— 抬头就该说清。
func TestCompactPayloadPreviewsMergeLevel(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")
	box.seedTurn(t, "第四句")

	// 先把 1–4、5–6 各压成一条（同一层的两条叶子），再点 1–6 ⇒ 那一段全被同层顶层摘要盖满 ⇒ 合并级
	for _, body := range []string{`{"blocks":2}`, `{"begin_idx":5,"end_idx":6}`} {
		if recorder := call(box.server, "POST", box.path+"/compact", body); recorder.Code != http.StatusAccepted {
			t.Fatalf("受理 %s 该 202，得到 %d：%s", body, recorder.Code, recorder.Body.String())
		}
		box.waitCompact(t)
	}

	preview := box.previewPayload(t, `{"begin_idx":1,"end_idx":6}`)
	if !preview.Merged {
		t.Fatalf("同一段已被两条同层摘要盖满 ⇒ 该是合并级：%+v", preview)
	}
	if preview.Messages != 6 {
		t.Fatalf("合并级该报被这两坨盖住的原文条数 = 6，得到 %d", preview.Messages)
	}
	if material := preview.wireMaterial(t); !strings.Contains(material, "不是对话原文") {
		t.Fatalf("合并级的抬头该说明下面是梗概：%q", material)
	}
}

// 世界状态：材料里附了段首/段末状态时，预演要**说一声**；材料里的 `<state>` 块该被剔干净。
//
// 段首状态只有在"这一段的开头**不是**第 1 条"时才有内容（开头之前没有历史 ⇒ 没有变量）——
// 所以先压掉第一块，再预演第二块。
func TestCompactPayloadReportsAttachedState(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句\n<state>好感度 = 3</state>")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	box.waitCompact(t)

	preview := box.previewPayload(t, `{"blocks":1}`)
	if preview.FromIdx != 3 || preview.ToIdx != 4 {
		t.Fatalf("第二块该是第 3–4 条，得到 %d–%d", preview.FromIdx, preview.ToIdx)
	}
	if !preview.StateBefore || !preview.StateAfter {
		t.Fatalf("这段有变量 ⇒ 段首与段末状态都该附上：%+v", preview)
	}
	material := preview.wireMaterial(t)
	if !strings.Contains(material, "好感度 = 3") {
		t.Fatalf("材料里该看得见那份状态（渲染过的）：%q", material)
	}
	// 正文里的标签本身要剔掉 —— 附的是**渲染好的** `<current_state>`，不是原样的 `<state>` 块
	if strings.Contains(material, "<state>") {
		t.Fatalf("材料正文里的 <state> 块该被剔掉：%q", material)
	}
}

// 两种给法同时出现 ⇒ 400（与 `POST .../compact` 同一口径）。
func TestCompactPayloadRejectsBothWays(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	recorder := call(box.server, "POST", box.path+"/compact/payload", `{"blocks":1,"begin_idx":1,"end_idx":2}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("两种给法同时给该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 关掉 compact 能力 ⇒ 预演也说"这条能力关着"。
//
// 理由：预演要是能算、真压却做不了，那才是骗人 —— 两条路必须过同一道闸（它们走的是同一个 `prepare`）。
func TestCompactPayloadReflectsDisabledAbility(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")

	recorder := call(box.server, "POST", "/api/v1/agents",
		`{"name":"闭嘴的","system_prompt":"x","abilities":{"compact":{"enabled":false}}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("建 agent 该 201，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var agent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if recorder := call(box.server, "PATCH", box.path, `{"agent_id":"`+agent.ID+`"}`); recorder.Code != http.StatusOK {
		t.Fatalf("把会话切到这个 agent 该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	recorder = call(box.server, "POST", box.path+"/compact/payload", `{"blocks":1}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("能力关着该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "abilities.compact.enabled") {
		t.Fatalf("错误里要说清是哪一格关的：%s", recorder.Body.String())
	}
}
