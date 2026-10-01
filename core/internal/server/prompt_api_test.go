package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/state"
	"microchat/internal/store"
)

// ── 生效的系统提示词：`GET /sessions/{session_id}/prompt` ────────────────────
//
// 三级解析的**结果** + 来源（conversation / agent / builtin）—— 三级各一条。

// promptSandbox：一条会话来一条 + 一份可选的 `agents.json`。
type promptSandbox struct {
	server *Server
	store  *store.Store
}

// mustAgents：把一段 JSON 读成 `agents.json` 的形状（测试里手写配置用）。
func mustAgents(t *testing.T, text string) config.AgentsConfig {
	t.Helper()
	var agents config.AgentsConfig
	if err := json.Unmarshal([]byte(text), &agents); err != nil {
		t.Fatal(err)
	}
	return agents
}

func newPromptSandbox(t *testing.T, agentsJSON string) *promptSandbox {
	t.Helper()
	dir := t.TempDir()
	if err := config.SaveJSON(dir+"/providers.json", mustJSON(t, `{"providers":[{"id":"dummy","kind":"dummy"}]}`)); err != nil {
		t.Fatal(err)
	}
	if agentsJSON != "" {
		if err := config.SaveJSON(dir+"/agents.json", mustAgents(t, agentsJSON)); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &promptSandbox{
		store:  st,
		server: newTestServer(st, config.DefaultConfig(), config.Paths{ConfigDir: dir, DataDir: dir}),
	}
}

// session：造一条会话（`systemPrompt` 非空 = 会话自己写了；`agentID` 非空 = 改成它）。
func (b *promptSandbox) session(t *testing.T, systemPrompt, agentID string) string {
	t.Helper()
	session, err := b.store.CreateSession("dummy", "dummy", systemPrompt, "")
	if err != nil {
		t.Fatal(err)
	}
	if agentID != "" {
		if err := b.store.SetSessionAgent(session.ID, agentID); err != nil {
			t.Fatal(err)
		}
	}
	return session.ID
}

// prompt：真发一条 `GET .../prompt`（走完整路由与鉴权/CORS 那两层）。
func (b *promptSandbox) prompt(t *testing.T, sessionID string) (int, promptView) {
	t.Helper()
	recorder := call(b.server, "GET", "/api/v1/sessions/"+sessionID+"/prompt", "")
	if recorder.Code != http.StatusOK {
		return recorder.Code, promptView{}
	}
	var view promptView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return recorder.Code, view
}

func TestPromptRouteReportsAllThreeLevels(t *testing.T) {
	box := newPromptSandbox(t, `{"default_agent":"跑团","agents":[{"id":"跑团","name":"跑团主持人","system_prompt":"你是跑团主持人。"}]}`)

	// ① 会话自己写了 ⇒ conversation（**覆盖** agent 的那份）
	own := box.session(t, "你是自定义的提示词。", "跑团")
	if code, view := box.prompt(t, own); code != http.StatusOK || view.Type != "conversation" || view.Text != "你是自定义的提示词。" {
		t.Fatalf("会话覆盖那一级：%d %+v", code, view)
	}

	// ② 会话没写、agent 在 `agents.json` 里 ⇒ agent
	fromAgent := box.session(t, "", "跑团")
	if code, view := box.prompt(t, fromAgent); code != http.StatusOK || view.Type != "agent" || view.Text != "你是跑团主持人。" {
		t.Fatalf("agent 那一级：%d %+v", code, view)
	}

	// ③ 两级都没有（agent 是内置的 `default`，`agents.json` 里根本没有这一条）⇒ builtin
	builtin := box.session(t, "", "default")
	if code, view := box.prompt(t, builtin); code != http.StatusOK ||
		view.Type != "builtin" || view.Text != config.BuiltinDefaultAgent().SystemPrompt {
		t.Fatalf("内置默认那一级：%d %+v", code, view)
	}
}

// 会话的 `agent_id` 指向一个**已不存在**的 agent（软引用悬空）⇒ 三级解析全落空。
//
// 这时**不许**回空文本（标着 `builtin` 却是空 = 误导）：用内置默认那句兜底，`source` 仍报 `builtin`。
func TestPromptRouteFallsBackToBuiltinWhenAgentIsDangling(t *testing.T) {
	box := newPromptSandbox(t, `{"default_agent":"跑团","agents":[{"id":"跑团","name":"跑团主持人","system_prompt":"你是跑团主持人。"}]}`)
	dangling := box.session(t, "", "早就删掉的agent")

	code, view := box.prompt(t, dangling)
	if code != http.StatusOK {
		t.Fatalf("该 200，得到 %d", code)
	}
	if view.Type != "builtin" || view.Text != config.BuiltinDefaultAgent().SystemPrompt {
		t.Fatalf("悬空 agent 该退回内置默认那句、且标 builtin：%+v", view)
	}
	if strings.TrimSpace(view.Text) == "" {
		t.Fatal("`/prompt` **永不给空文本**")
	}
}

// `PATCH /sessions/{session_id}` 的 `system_prompt` **真生效**（不是收下不用）：
//
//	没给（或 `null`）= 不动；`""` = 清掉覆盖（回落 agent 的）；非空 = 写进 `sessions.system_prompt`。
func TestPatchSessionSystemPromptTakesEffect(t *testing.T) {
	box := newPromptSandbox(t, `{"default_agent":"跑团","agents":[{"id":"跑团","name":"跑团主持人","system_prompt":"你是跑团主持人。"}]}`)
	id := box.session(t, "", "跑团")

	// 非空 ⇒ 真写进库，`/prompt` 跟着变 conversation
	recorder := call(box.server, "PATCH", "/api/v1/sessions/"+id, `{"system_prompt":"你是临时改的。"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH 该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	session, err := box.store.GetSession(id)
	if err != nil || session == nil {
		t.Fatalf("读会话失败：%v", err)
	}
	if session.SystemPrompt != "你是临时改的。" {
		t.Fatalf("该真写进库：%q", session.SystemPrompt)
	}
	if _, view := box.prompt(t, id); view.Type != "conversation" || view.Text != "你是临时改的。" {
		t.Fatalf("写完之后 `/prompt` 该报 conversation：%+v", view)
	}

	// 空串 ⇒ 清掉覆盖（回落到 agent 的）
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+id, `{"system_prompt":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("PATCH 空串该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if _, view := box.prompt(t, id); view.Type != "agent" || view.Text != "你是跑团主持人。" {
		t.Fatalf("清掉覆盖该回落 agent：%+v", view)
	}

	// 没给这个键 ⇒ 一个字都不动（`null` 同理 —— 指针为 nil）
	before, _ := box.store.GetSession(id)
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+id, `{"title":"新名字"}`); recorder.Code != http.StatusOK {
		t.Fatalf("PATCH 该 200，得到 %d", recorder.Code)
	}
	after, _ := box.store.GetSession(id)
	if after.Title != "新名字" || after.SystemPrompt != before.SystemPrompt {
		t.Fatalf("没给 system_prompt ⇒ 不许动它：%q", after.SystemPrompt)
	}
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+id, `{"system_prompt":null}`); recorder.Code != http.StatusOK {
		t.Fatalf("PATCH null 该 200，得到 %d", recorder.Code)
	}
	nulled, _ := box.store.GetSession(id)
	if nulled.SystemPrompt != before.SystemPrompt {
		t.Fatalf("null 该当「不动」：%q", nulled.SystemPrompt)
	}
}

// 会话不存在 ⇒ 404（错误体仍是我们那套固定形状）。
func TestPromptRouteOnMissingSessionIs404(t *testing.T) {
	box := newPromptSandbox(t, "")
	recorder := call(box.server, "GET", "/api/v1/sessions/00000000-0000-0000-0000-000000000000/prompt", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("错误体该是 not_found：%s", recorder.Body.String())
	}
}

// 出站那条链（`GET /outgoing` = 真会发出去的东西）也必须带上生效提示词那条 system ——
// 与 `/prompt` **同一处解析**，两处口径一致。悬空 agent 曾经让这里的第一条直接是 user（系统的整个丢了），
// 拿掉修复就该红。
func TestOutgoingKeepsSystemForAllThreeLevels(t *testing.T) {
	box := newPromptSandbox(t, `{"default_agent":"跑团","agents":[{"id":"跑团","name":"跑团主持人","system_prompt":"你是跑团主持人。"}]}`)
	builtin := config.BuiltinDefaultAgent().SystemPrompt

	levels := []struct {
		name         string
		systemPrompt string
		agentID      string
		want         string
	}{
		{"会话覆盖", "你是自定义的提示词。", "跑团", "你是自定义的提示词。"},
		{"agent", "", "跑团", "你是跑团主持人。"},
		{"悬空 agent ⇒ 内置默认", "", "早就删掉的agent", builtin},
	}
	for _, level := range levels {
		id := box.session(t, level.systemPrompt, level.agentID)
		_, view := box.prompt(t, id)

		recorder := call(box.server, "GET", "/api/v1/sessions/"+id+"/outgoing", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s：/outgoing 该 200，得到 %d：%s", level.name, recorder.Code, recorder.Body.String())
		}
		var outgoing []state.Outgoing
		if err := json.Unmarshal(recorder.Body.Bytes(), &outgoing); err != nil {
			t.Fatal(err)
		}
		if len(outgoing) == 0 || outgoing[0].Role != state.RoleSystem {
			t.Fatalf("%s：出站第一条该是 role=system：%+v", level.name, outgoing)
		}
		if outgoing[0].Content != level.want {
			t.Fatalf("%s：出站 system = %q，要 %q", level.name, outgoing[0].Content, level.want)
		}
		if view.Text != level.want {
			t.Fatalf("%s：/prompt = %q，要 %q", level.name, view.Text, level.want)
		}
		if view.Text != outgoing[0].Content {
			t.Fatalf("%s：/prompt 与 /outgoing[0] 口径不一致：%q vs %q", level.name, view.Text, outgoing[0].Content)
		}
	}
}
