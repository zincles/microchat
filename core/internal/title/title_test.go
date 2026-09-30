package title

// 起标题的四条口径，一条条钉住：
//  1. 开着 ⇒ 标题被填成**模型给的那句**（httptest 上游固定回一句话）；
//  2. agent 覆盖的 `prompt` 就是发出去的 system（覆盖真生效）+ `prompt_version` 对得上；
//  3. 关掉 ⇒ 标题停在**首句截断**这个兜底值，且**不再自动覆盖**；
//  4. **用户先改过名 ⇒ 后续不再被自动覆盖**（判定在 SQL 的 WHERE 里）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microchat/internal/abilities"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
	"microchat/internal/task"
)

// harness：一条会话 + 一个进程内的起标题服务。
type harness struct {
	service *Service
	store   *store.Store
	paths   config.Paths
	session model.Session
	seq     int // 自己铸 id：顺序 = id 的字典序（不依赖时钟，测起来才稳）
}

// newHarness：`agents.json` 里一份人格（能力覆盖照传）；渠道默认给 dummy。
func newHarness(t *testing.T, providersConfig config.ProvidersConfig, agent *config.Agent) *harness {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{ConfigDir: dir, DataDir: dir}
	if err := config.SaveJSON(dir+"/providers.json", providersConfig); err != nil {
		t.Fatal(err)
	}
	if agent != nil {
		if err := config.SaveJSON(dir+"/agents.json",
			config.AgentsConfig{Agents: []config.Agent{*agent}}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session, err := st.CreateSession("dummy", "dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	if agent != nil {
		if err := st.SetSessionAgent(session.ID, agent.ID); err != nil {
			t.Fatal(err)
		}
		session.AgentID = agent.ID
	}
	return &harness{service: New(st, paths, task.NewRegistry()), store: st, paths: paths, session: session}
}

func dummyProviders() config.ProvidersConfig {
	return config.ProvidersConfig{Providers: []config.Provider{{ID: "dummy", Kind: "dummy"}}}
}

// say：落两条消息（用户 + 助手）—— 起标题的材料取自这几条。
func (h *harness) say(t *testing.T, user, assistant string) {
	t.Helper()
	h.seq++
	for index, message := range []model.Message{
		{ID: fmt.Sprintf("0000-%06d-a", h.seq), SessionID: h.session.ID,
			Role: model.RoleUser, Content: user},
		{ID: fmt.Sprintf("0000-%06d-b", h.seq), SessionID: h.session.ID,
			Role: model.RoleAssistant, Content: assistant},
	} {
		if _, err := h.store.InsertMessage(message); err != nil {
			t.Fatalf("第 %d 条落库失败：%v", index, err)
		}
	}
}

func (h *harness) title(t *testing.T) string {
	t.Helper()
	session, err := h.store.GetSession(h.session.ID)
	if err != nil || session == nil {
		t.Fatalf("取会话失败：%v", err)
	}
	return session.Title
}

// stubJSON：一个只回一句话的假上游（非流式），把收到的请求体记下来。
func stubJSON(t *testing.T, title string, body *[]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*body = raw
		w.Header().Set("Content-Type", "application/json")
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": title}}},
			"usage":   map[string]any{"prompt_tokens": 30, "completion_tokens": 4, "total_tokens": 34},
		})
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// ① 开着（dummy 渠道）⇒ `Auto` 把标题填成模型给的那句。
func TestAutoFillsTitleFromModel(t *testing.T) {
	h := newHarness(t, dummyProviders(), nil)
	h.say(t, "帮我写一段跑团的开幕词", "好的，山雨欲来……")
	h.service.Auto(h.session)
	if got := h.title(t); got != dummyReply {
		t.Fatalf("标题 = %q（想要模型那句 %q）", got, dummyReply)
	}
}

// ② agent 覆盖了 `prompt` ⇒ 发出去的 system 就是新模板；`prompt_version` 由**生效模板**算。
func TestAgentPromptOverrideReachesTheWire(t *testing.T) {
	var body []byte
	stub := stubJSON(t, "山雨欲来", &body)
	agent := &config.Agent{ID: "跑团", Name: "跑团主持人", SystemPrompt: "x",
		Abilities: map[string]config.AbilityToggle{
			abilities.Title: {Provider: new("stub"), Model: new("m"), Prompt: new("用武侠腔起名字，七个字以内。")},
		}}
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{
		{ID: "dummy", Kind: "dummy"},
		{ID: "stub", Kind: "openai-compat", BaseURL: stub.URL, Identity: "bare"},
	}}, agent)
	h.say(t, "帮我写一段跑团的开幕词", "好的，山雨欲来……")

	result, err := h.service.Generate(context.Background(), h.session)
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "山雨欲来" || result.Provider != "stub" || result.Model != "m" {
		t.Fatalf("起标题的结果 = %+v", result)
	}
	if result.PromptVersion != abilities.PromptVersion("用武侠腔起名字，七个字以内。") {
		t.Fatalf("prompt_version 该由**生效模板**算：%d", result.PromptVersion)
	}
	sent := string(body)
	if !strings.Contains(sent, "用武侠腔起名字") {
		t.Fatalf("覆盖的模板该进 system：%s", sent)
	}
	if strings.Contains(sent, "起标题助手") {
		t.Fatalf("默认模板不该再出现（覆盖要真生效）：%s", sent)
	}
	if !strings.Contains(sent, "帮我写一段跑团的开幕词") {
		t.Fatalf("材料该是会话开头那几条：%s", sent)
	}
	// 覆盖的渠道也真用上了（会话自己是 dummy，能力上换成 stub ⇒ 这一发去了 stub）
	if !strings.Contains(sent, `"model":"m"`) {
		t.Fatalf("模型该用能力上覆盖的那个：%s", sent)
	}
}

// ③ 关掉 ⇒ 不起（**明确拒绝**），`Auto` 退回首句截断兜底；下一次 `Auto` 不再动它。
func TestDisabledAbilityFallsBackAndStopsCovering(t *testing.T) {
	agent := &config.Agent{ID: "闭嘴的", Name: "闭嘴的", SystemPrompt: "x",
		Abilities: map[string]config.AbilityToggle{abilities.Title: {Enabled: new(false)}}}
	h := newHarness(t, dummyProviders(), agent)
	h.say(t, "开个头\n第二行不该进标题", "……")

	if _, err := h.service.Generate(context.Background(), h.session); err == nil ||
		!strings.Contains(err.Error(), "abilities.title.enabled") {
		t.Fatalf("关掉能力该明确拒绝：%v", err)
	}
	h.service.Auto(h.session)
	if got := h.title(t); got != "开个头 第二行不该进标题" {
		t.Fatalf("关掉能力该退回首句截断兜底：%q", got)
	}
	// 已经有名字 ⇒ **永不再自动覆盖**（再喊一次也不动）
	h.say(t, "后来又聊了点别的", "嗯")
	h.service.Auto(h.session)
	if got := h.title(t); got != "开个头 第二行不该进标题" {
		t.Fatalf("已经有名字就不该再动它：%q", got)
	}
}

// ④ **用户先改过名 ⇒ 后续不再被自动覆盖**（这条一定要有）。
func TestUserRenamedTitleIsNeverOverwritten(t *testing.T) {
	h := newHarness(t, dummyProviders(), nil)
	if err := h.store.UpdateTitle(h.session.ID, "我自己起的名字"); err != nil {
		t.Fatal(err)
	}
	h.say(t, "帮我写一段跑团的开幕词", "好的，山雨欲来……")

	h.service.Auto(h.session)
	if got := h.title(t); got != "我自己起的名字" {
		t.Fatalf("用户改过的名字被覆盖了：%q", got)
	}
	// 手动那条路（`Generate`）不受影响：人要换名字就换（落库由调用方做）
	result, err := h.service.Generate(context.Background(), h.session)
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != dummyReply {
		t.Fatalf("手动那条路该照常起：%+v", result)
	}
}

// 材料取自会话**开头**那几条（克制），且剔掉 `<state>` 块。
func TestMaterialIsRestrainedAndStripsState(t *testing.T) {
	var body []byte
	stub := stubJSON(t, "假标题", &body)
	agent := &config.Agent{ID: "跑团", Abilities: map[string]config.AbilityToggle{
		abilities.Title: {Provider: new("stub"), Model: new("m")},
	}}
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{
		{ID: "dummy", Kind: "dummy"},
		{ID: "stub", Kind: "openai-compat", BaseURL: stub.URL, Identity: "bare"},
	}}, agent)
	h.say(t, "第一句\n<state>季节 = 初冬</state>", "第一答")
	h.say(t, "第二句", "第二答")
	h.say(t, "第三句", "第三答")

	if _, err := h.service.Generate(context.Background(), h.session); err != nil {
		t.Fatal(err)
	}
	sent := string(body)
	if strings.Contains(sent, "<state>") || strings.Contains(sent, "初冬") {
		t.Fatalf("材料里的 <state> 块该被剔掉：%s", sent)
	}
	if !strings.Contains(sent, "第一句") || !strings.Contains(sent, "第二答") {
		t.Fatalf("材料该含会话开头那几条：%s", sent)
	}
	if strings.Contains(sent, "第三句") {
		t.Fatalf("材料只取开头那几条（第 %d 条以内的），不该拖上整条会话：%s", materialMessages, sent)
	}
}
