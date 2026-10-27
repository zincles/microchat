package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 对话记录导出/导入（JSON）：正文 + 标题 + **v2 起再带会话的三格软引用**
// （provider / model / agent_id / agent_name）+ **v3 起带整张摘要金字塔**（见 export_v3_test.go）；
// system prompt / 用量一概不带；
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
		Version   int    `json:"version"`
		Title     string `json:"title"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		AgentID   string `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 3 || len(doc.Messages) != 4 || doc.Title != "导出测试会话" {
		t.Fatalf("导出形状不对：%+v", doc)
	}
	// v2 的三格软引用：渠道/模型/agent 都要带（导出那一刻会话绑的是谁）
	if doc.Provider == "" || doc.Model == "" || doc.AgentID == "" || doc.AgentName == "" {
		t.Fatalf("v2 该带 provider/model/agent_id/agent_name：%+v", doc)
	}
	if doc.Messages[0].Role != "user" || !strings.Contains(doc.Messages[0].Content, "<state>HP = 10</state>") {
		t.Fatalf("正文该原样（含 <state> 块）：%+v", doc.Messages[0])
	}
	// 内容级的账一概不带：整份 JSON 里不许出现 system prompt / usage 那些键。
	// （`reasoning` 是 v3 起**要**带的：它是模型说过的话的一部分；`summary` 只在真压过时才出现，
	//  这里这条会话没压过 ⇒ 整份文件里连这个键都不该有。）
	raw := exported.Body.String()
	for _, banned := range []string{"usage", "system_prompt", "summary"} {
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
			ID       string `json:"id"`
			Title    string `json:"title"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			AgentID  string `json:"agent_id"`
		} `json:"session"`
		Messages     int  `json:"messages"`
		AgentMissing bool `json:"agent_missing"`
	}](t, imported)
	// 导回去的会话**绑的是当初那三格**（不是本机缺省）；agent 本机有 ⇒ agent_missing=false
	if newID.Session.Provider != doc.Provider || newID.Session.Model != doc.Model || newID.Session.AgentID != doc.AgentID {
		t.Fatalf("导入该照抄会话的三格绑定：%+v（原 %s/%s/%s）", newID.Session, doc.Provider, doc.Model, doc.AgentID)
	}
	if newID.AgentMissing {
		t.Fatal("agent 本机有 ⇒ agent_missing 该是 false")
	}
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

// 本机没有那个 agent ⇒ **照绑 id**（软引用、不静默换 default_agent），回执里 agent_missing=true；
// 渠道/模型同理照抄（本机没有也照抄）—— 偷换成缺省比"绑了个本机没有的"更坏（会悄悄改模型）。
func TestSessionImportKeepsUnknownBindings(t *testing.T) {
	box := newDummySandboxWith(t, true)
	doc := `{"version":2,"title":"搬来的会话","provider":"没人有这个渠道","model":"没人有这个模型",
		"agent_id":"跑团主持人","agent_name":"跑团主持人（原机器）",
		"messages":[{"role":"user","content":"继续上次的"},{"role":"assistant","content":"好。"}]}`
	rec := authCall(box.server, "POST", "/api/v1/sessions/import-json", doc, testSysToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("导入 = %d（%s）", rec.Code, rec.Body.String())
	}
	got := decodeBody[struct {
		Session struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			AgentID  string `json:"agent_id"`
		} `json:"session"`
		AgentMissing bool `json:"agent_missing"`
	}](t, rec)
	if got.Session.Provider != "没人有这个渠道" || got.Session.Model != "没人有这个模型" || got.Session.AgentID != "跑团主持人" {
		t.Fatalf("三格该照抄（不许换成缺省）：%+v", got.Session)
	}
	if !got.AgentMissing {
		t.Fatal("agent 本机没有 ⇒ agent_missing 该是 true（界面据此提醒「人设会回落内置」）")
	}
}

// 比本程序新的 version ⇒ 400（自己的格式，别瞎猜字段意思）。
func TestSessionImportRejectsNewerVersion(t *testing.T) {
	box := newDummySandboxWith(t, true)
	rec := authCall(box.server, "POST", "/api/v1/sessions/import-json",
		`{"version":4,"messages":[{"role":"user","content":"a"}]}`, testSysToken)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "version = 4") {
		t.Fatalf("更新的 version 该 400 并说清：%d（%s）", rec.Code, rec.Body.String())
	}
}
