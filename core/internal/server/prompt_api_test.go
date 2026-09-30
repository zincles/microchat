package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"microchat/internal/config"
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
	session, err := b.store.CreateSession("dummy", "dummy", systemPrompt)
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
	if code, view := box.prompt(t, own); code != http.StatusOK || view.Source != "conversation" || view.Text != "你是自定义的提示词。" {
		t.Fatalf("会话覆盖那一级：%d %+v", code, view)
	}

	// ② 会话没写、agent 在 `agents.json` 里 ⇒ agent
	fromAgent := box.session(t, "", "跑团")
	if code, view := box.prompt(t, fromAgent); code != http.StatusOK || view.Source != "agent" || view.Text != "你是跑团主持人。" {
		t.Fatalf("agent 那一级：%d %+v", code, view)
	}

	// ③ 两级都没有（agent 是内置的 `default`，`agents.json` 里根本没有这一条）⇒ builtin
	builtin := box.session(t, "", "default")
	if code, view := box.prompt(t, builtin); code != http.StatusOK ||
		view.Source != "builtin" || view.Text != config.BuiltinDefaultAgent().SystemPrompt {
		t.Fatalf("内置默认那一级：%d %+v", code, view)
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
