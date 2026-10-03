package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ── /agents 面板 ──

func newAgentTestBackend(t *testing.T, agents AgentsConfig, defaults Defaults, patched *map[string]any) *httptest.Server {
	t.Helper()
	var patchBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_ = json.NewEncoder(w).Encode(agents)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config/defaults":
			_ = json.NewEncoder(w).Encode(defaults)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			_ = json.NewEncoder(w).Encode(Agent{ID: "a-new", Name: name, Abilities: map[string]AgentAbility{}})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/agents/"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &patchBody)
			if patched != nil {
				*patched = patchBody
			}
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
			base := Agent{ID: id, Name: "n", SystemPrompt: "p"}
			if ab, ok := patchBody["abilities"]; ok {
				rawAb, _ := json.Marshal(ab)
				var abs map[string]AgentAbility
				_ = json.Unmarshal(rawAb, &abs)
				base.Abilities = abs
			}
			if sp, ok := patchBody["system_prompt"].(string); ok {
				base.SystemPrompt = sp
			}
			_ = json.NewEncoder(w).Encode(base)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/config/defaults":
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			out := defaults
			if v, ok := body["provider"].(string); ok {
				out.Provider = v
			}
			if v, ok := body["model"].(string); ok {
				out.Model = v
			}
			if v, ok := body["agent"].(string); ok {
				out.Agent = v
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/agents/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func agentsFixtureCfg() (AgentsConfig, Defaults) {
	return AgentsConfig{
		DefaultAgent: "a1",
		Agents: []Agent{
			{ID: "a1", Name: "默认", SystemPrompt: "你是默认人格。",
				Abilities: map[string]AgentAbility{
					"title":   {Enabled: new(true)},
					"compact": {Enabled: new(false)},
					"judge":   {Enabled: new(true), Provider: new("ocgo"), Model: new("m1")},
				}},
			{ID: "a2", Name: "跑团", SystemPrompt: "你是跑团主持。",
				Abilities: map[string]AgentAbility{"title": {Enabled: new(false)}}},
		},
	}, Defaults{Provider: "opencode-go", Model: "deepseek-v4-flash", Agent: "a1"}
}

func TestAgentsListShowsRowsAndDefaults(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	backend := newAgentTestBackend(t, cfg, defs, nil)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/agents")
	if cmd == nil {
		t.Fatal("/agents 该去拉列表")
	}
	after := runCmds(t, updated.(model), cmd)
	if after.viewer == nil || after.viewerMode != "agents" {
		t.Fatalf("该打开人格列表 viewer：%q", after.lastAction)
	}
	if len(after.agentRows) != 2 || after.agentRows[0] != "a1" {
		t.Fatalf("下标映射该存两条不重拉：%v", after.agentRows)
	}
	joined := strings.Join(after.viewer, "\n")
	for _, want := range []string{"★默认", "跑团", "名：默认", "[n]新增", "[d]删除"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("列表缺 %q：\n%s", want, joined)
		}
	}
}

func TestAgentsEnterDetailThreeSections(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	backend := newAgentTestBackend(t, cfg, defs, nil)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/agents")
	after := runCmds(t, updated.(model), cmd)
	updated, cmd = after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	detail := updated.(model)
	if cmd != nil {
		t.Fatal("回车进详情不许发请求")
	}
	if detail.agentDetail == nil || detail.agentDetail.ID != "a1" {
		t.Fatalf("回车该进详情：%+v", detail.agentDetail)
	}
	joined := strings.Join(detail.viewer, "\n")
	for _, want := range []string{"默认", "你是默认人格", "起标题：开", "压缩：关", "判断：开"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("详情三段缺 %q：\n%s", want, joined)
		}
	}
	if len(detail.detailAbils) != 3 {
		t.Fatalf("能力行号该记住三行：%v", detail.detailAbils)
	}
}
func TestAgentsSplitLeftRightAndKeys(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	backend := newAgentTestBackend(t, cfg, defs, nil)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/agents")
	split := runCmds(t, updated.(model), cmd)
	joined := strings.Join(split.viewer, "\n")
	// 分栏：左列表 + 右详情 + 底按钮行，一屏里全有
	for _, want := range []string{"│", "★默认", "名：默认", "[n]新增", "[d]删除"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("分栏缺 %q：\n%s", want, joined)
		}
	}
	// 左/右挪焦点：右栏后 ↑↓ 走字段行（回车不发请求 —— 字段行不是开关）
	updated, _ = split.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	right := updated.(model)
	if right.agentCol != agentColDetail {
		t.Fatalf("右键该挪焦点到详情：%d", right.agentCol)
	}
	updated, cmd = right.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("名行回车不该发请求（改走 e）")
	}
	// 回左栏：左/右键不再退出（旧 bug：左右键掉出 viewer）
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if back := updated.(model); back.viewer == nil || back.agentCol != agentColList {
		t.Fatal("左键该回列表焦点，不许退出面板")
	}
}

func TestAgentsEditFieldRoundTrip(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	var patched map[string]any
	backend := newAgentTestBackend(t, cfg, defs, &patched)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/agents")
	split := runCmds(t, updated.(model), cmd)
	// 进右栏 → e 改名 → 输入行回车 PATCH
	updated, _ = split.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if ed := updated.(model); ed.editField != "name" {
		t.Fatalf("e 该进改名编辑态：%q", ed.editField)
	}
	// 编辑态输入行直接回车（submit 走 answerAgentEdit —— viewer 开着也一样，输入行是同一根线）
	m2 := updated.(model)
	m2.input, m2.inputCursor = "新名字", 3
	updated2, cmd := m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("编辑态回车该发 PATCH")
	}
	after := runCmds(t, updated2.(model), cmd)
	if patched["name"] != "新名字" {
		t.Fatalf("改名 PATCH 体 = %v", patched)
	}
	if after.editField != "" {
		t.Fatal("收工该清编辑态")
	}
	// Esc 取消：什么都不发
	updated, _ = split.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if ed := updated.(model); ed.editField != "" {
		t.Fatal("Esc 该清编辑态")
	}
}

func TestAgentsToggleSendsWholeAbilities(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	var patched map[string]any
	backend := newAgentTestBackend(t, cfg, defs, &patched)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/agents")
	after := runCmds(t, updated.(model), cmd)
	updated, _ = after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	detail := updated.(model)
	// 焦点挪右 + 光标移到 compact 行再拨（分栏：右栏第 0 行是"名"，第 3 行起是开关）
	updated, _ = detail.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	right := updated.(model)
	updated, _ = right.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd = updated.(model).Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd == nil {
		t.Fatal("t 该发整段 PATCH")
	}
	afterToggle := runCmds(t, updated.(model), cmd)
	abs, ok := patched["abilities"].(map[string]any)
	if !ok || len(abs) != 3 {
		t.Fatalf("该整段 PATCH 三个能力：%v", patched)
	}
	if !strings.Contains(afterToggle.lastAction, "已更新") {
		t.Fatalf("拨完该报已更新：%q", afterToggle.lastAction)
	}
}

func TestAgentsSetPromptRoundTrip(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	var patched map[string]any
	backend := newAgentTestBackend(t, cfg, defs, &patched)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/agents")
	after := runCmds(t, updated.(model), cmd)
	updated, _ = after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	detail := updated.(model)
	updated, cmd = detail.runCommand("/agents set-prompt 多行\n粘贴照吃")
	if cmd == nil {
		t.Fatal("set-prompt 该发 PATCH")
	}
	afterSet := runCmds(t, updated.(model), cmd)
	if patched["system_prompt"] != "多行 粘贴照吃" {
		t.Fatalf("PATCH 体不对（多行粘贴按空白拼回照吃）：%v", patched)
	}
	if !strings.Contains(afterSet.lastAction, "已更新") {
		t.Fatalf("写完该报已更新：%q", afterSet.lastAction)
	}
	// 没进详情 / 空文本都拒绝
	if bad, _ := fixture().runCommand("/agents set-prompt xxx"); !strings.Contains(bad.(model).lastAction, "详情") {
		t.Fatalf("没进详情该拒绝：%q", bad.(model).lastAction)
	}
	if bad, _ := detail.runCommand("/agents set-prompt   "); !strings.Contains(bad.(model).lastAction, "空文本") {
		t.Fatalf("空文本该拒绝：%q", bad.(model).lastAction)
	}
}

func TestAgentsDefaultsWizardAndUse(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	backend := newAgentTestBackend(t, cfg, defs, nil)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	m.defaults, m.defaultsLoaded = defs, true
	updated, _ := m.runCommand("/agents defaults")
	d := updated.(model)
	if d.defStep != 1 {
		t.Fatalf("该进缺省三问：%d", d.defStep)
	}
	d.input, d.inputCursor = "deepseek", len([]rune("deepseek"))
	updated, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d = updated.(model)
	d.input, d.inputCursor = "", 0
	updated, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d = updated.(model)
	if d.defDraft.Provider != "deepseek" || d.defDraft.Model != defs.Model {
		t.Fatalf("空=不动：%+v", d.defDraft)
	}
	d.input, d.inputCursor = "a2", 2
	updated, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("第三问答完该一次 SetDefaults")
	}
	after := runCmds(t, updated.(model), cmd)
	if !strings.Contains(after.lastAction, "新会话生效") {
		t.Fatalf("设完该说新会话生效：%q", after.lastAction)
	}
	// use 快捷
	updated, cmd = m.runCommand("/agents use a2")
	if cmd == nil {
		t.Fatal("use 该发 SetDefaults")
	}
	afterUse := runCmds(t, m, cmd)
	if !strings.Contains(afterUse.lastAction, "新会话生效") || afterUse.defaults.Agent != "a2" {
		t.Fatalf("use 该把缺省 agent 换成它：%q %+v", afterUse.lastAction, afterUse.defaults)
	}
}

func TestAgentCreateAndDel(t *testing.T) {
	cfg, defs := agentsFixtureCfg()
	backend := newAgentTestBackend(t, cfg, defs, nil)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	if bad, _ := m.runCommand("/agent-create   "); bad.(model).lastAction == "" || !strings.Contains(bad.(model).lastAction, "名字必填") {
		t.Fatalf("缺名该拒绝：%q", bad.(model).lastAction)
	}
	updated, cmd := m.runCommand("/agent-create 新人格")
	after := runCmds(t, updated.(model), cmd)
	if after.agentDetail == nil || after.agentDetail.Name != "新人格" {
		t.Fatalf("建完该自动进详情：%+v", after.agentDetail)
	}
	updated, cmd = m.runCommand("/agent-del a2")
	armed := updated.(model)
	if cmd != nil {
		t.Fatal("确认前不许发请求")
	}
	if armed.confirm == nil || armed.confirm.kind != confirmDeleteAgent {
		t.Fatalf("该走 confirmDeleteAgent：%+v", armed.confirm)
	}
	updated, cmd = armed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("点头该真删")
	}
	done := runCmds(t, updated.(model), cmd)
	if !strings.Contains(done.lastAction, "已删除人格") {
		t.Fatalf("删完如实报：%q", done.lastAction)
	}
}
