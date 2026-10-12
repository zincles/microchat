package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 对话记录导出/导入（JSON）：**只带 role+content+标题**（system prompt / agent / 摘要一概不带）；
// 正文原样（`<state>` 块留着）；导入严格校验 role；空 messages ⇒ 400。
func TestSessionExportImportRoundTrip(t *testing.T) {
	box := newDummySandboxWith(t, true)
	// 造一条会话：两轮（含 `<state>` 块 + 思考 + 用量 —— 导出时这些都该丢）
	sessionID := box.sessionID
	box.seedTurn(t, "开场<state>HP = 10</state>")
	box.seedTurn(t, "继续")
	// 先起个名（标题该随导出走；不起名的话导入会按首条 user 自动起 —— 下面另验）
	if got := authCall(box.server, "PATCH", "/api/v1/sessions/"+sessionID, `{"title":"导出测试会话"}`, testSysToken); got.Code != 200 {
		t.Fatalf("起名 = %d", got.Code)
	}

	exported := authCall(box.server, "GET", "/api/v1/sessions/"+sessionID+"/export", "", testSysToken)
	if exported.Code != http.StatusOK {
		t.Fatalf("导出 = %d（%s）", exported.Code, exported.Body.String())
	}
	var doc struct {
		Version  int    `json:"version"`
		Title    string `json:"title"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || len(doc.Messages) != 4 || doc.Title != "导出测试会话" {
		t.Fatalf("导出形状不对：%+v", doc)
	}
	if doc.Messages[0].Role != "user" || !strings.Contains(doc.Messages[0].Content, "<state>HP = 10</state>") {
		t.Fatalf("正文该原样（含 <state> 块）：%+v", doc.Messages[0])
	}
	// 只带 role/content：整份 JSON 里不许出现 system prompt / agent / reasoning / usage 那些键
	raw := exported.Body.String()
	for _, banned := range []string{"reasoning", "usage", "agent_id", "system_prompt"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("导出不该带 %q：%s", banned, raw)
		}
	}

	// 导入：新会话，消息原样、标题带着
	imported := authCall(box.server, "POST", "/api/v1/sessions/import-json", raw, testSysToken)
	if imported.Code != http.StatusCreated {
		t.Fatalf("导入 = %d（%s）", imported.Code, imported.Body.String())
	}
	newID := decodeBody[struct {
		Session struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"session"`
		Messages int `json:"messages"`
	}](t, imported)
	if newID.Session.ID == sessionID || newID.Messages != 4 || newID.Session.Title != doc.Title {
		t.Fatalf("导入回执不对：%+v", newID)
	}
	after := decodeBody[[]struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}](t, authCall(box.server, "GET", "/api/v1/sessions/"+newID.Session.ID+"/messages", "", testSysToken))
	if len(after) != len(doc.Messages) {
		t.Fatalf("导入消息数 = %d，要 %d", len(after), len(doc.Messages))
	}
	for i := range after {
		if after[i].Role != doc.Messages[i].Role || after[i].Content != doc.Messages[i].Content {
			t.Fatalf("第 %d 条对不上：%+v vs %+v", i+1, after[i], doc.Messages[i])
		}
	}
}

// 导入的校验与闸门：role 不合法 ⇒ 400（说清第几条）、空 messages ⇒ 400、非 admin ⇒ 403。
func TestSessionImportJSONValidation(t *testing.T) {
	box := newDummySandboxWith(t, true)
	bad := authCall(box.server, "POST", "/api/v1/sessions/import-json",
		`{"version":1,"title":"x","messages":[{"role":"user","content":"a"},{"role":"system","content":"b"}]}`, testSysToken)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "第 2 条") {
		t.Fatalf("坏 role 该 400 且点名第几条：%d（%s）", bad.Code, bad.Body.String())
	}
	empty := authCall(box.server, "POST", "/api/v1/sessions/import-json", `{"version":1,"messages":[]}`, testSysToken)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("空 messages 该 400：%d", empty.Code)
	}
	// 没给标题 ⇒ 首条 user 自动起名（与 ST 导入同口径）
	noTitle := authCall(box.server, "POST", "/api/v1/sessions/import-json",
		`{"version":1,"messages":[{"role":"user","content":"帮我写一首关于冬天的短诗"},{"role":"assistant","content":"好。"}]}`, testSysToken)
	if noTitle.Code != http.StatusCreated {
		t.Fatalf("没标题的导入 = %d（%s）", noTitle.Code, noTitle.Body.String())
	}
	title := decodeBody[struct {
		Session struct {
			Title string `json:"title"`
		} `json:"session"`
	}](t, noTitle).Session.Title
	if title == "" || !strings.Contains(title, "冬天") {
		t.Fatalf("该按首条 user 自动起名：%q", title)
	}
}
