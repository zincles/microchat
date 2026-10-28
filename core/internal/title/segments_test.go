package title

// 起标题的**默认模板住段库**（2026-10-28），且能力上的段**真会拼进这一发**。
//
// 为什么要单钉一遍：从前这里是裸 `abilities.Template` ⇒ 编辑器让你给 title 挑段，
// 后端一个字都不拼（**静默无效**：不报错、结果不一样，最难查的那一类）。
// 现在它与压缩走同一个 `abilities.AssemblePrompt`，于是"编辑器说会发什么"和"真发什么"
// 是同一段代码算出来的。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"microchat/internal/abilities"
	"microchat/internal/config"
)

// systemOf：把这一发的 system 解出来（体是 JSON，正文里的引号与换行会被转义 ——
// 直接 `strings.Contains` 比的是转义后的串，比不出"逐字相同"）。
func systemOf(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("请求体不是 JSON：%v", err)
	}
	for _, message := range doc.Messages {
		if message.Role == "system" {
			return message.Content
		}
	}
	t.Fatalf("请求体里没有 system：%s", body)
	return ""
}

// writeAgents：整份重写 `agents.json`（`Generate` 每次都现读配置 ⇒ 改完就生效）。
func (h *harness) writeAgents(t *testing.T, agents config.AgentsConfig) {
	t.Helper()
	if err := config.SaveJSON(h.paths.ConfigDir+"/agents.json", agents); err != nil {
		t.Fatalf("写 agents.json 失败：%v", err)
	}
}

// agentsWith：一份人格 + 一个可选的段库（能力配置由调用方给）。
func agentsWith(agent config.Agent, segments ...config.Segment) config.AgentsConfig {
	return config.AgentsConfig{Version: 1, Segments: segments, Agents: []config.Agent{agent}}
}

// stubHarness：一个假上游 + 一条会话 + 一份"能力走 stub"的人格。
func stubHarness(t *testing.T, ability config.AbilityToggle, segments ...config.Segment) (*harness, *[]byte) {
	t.Helper()
	var body []byte
	stub := stubJSON(t, "山雨欲来", &body)
	agent := config.Agent{ID: "跑团", Name: "跑团主持人", SystemPrompt: "x",
		Abilities: map[string]config.AbilityToggle{abilities.Title: ability}}
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{
		{ID: "dummy", Kind: "dummy"},
		{ID: "stub", Kind: "openai-compat", BaseURL: stub.URL, Identity: "bare"},
	}}, &agent)
	if len(segments) > 0 {
		agent.Abilities[abilities.Title] = ability
		h.writeAgents(t, agentsWith(agent, segments...))
	}
	h.say(t, "帮我写一段跑团的开幕词", "好的，山雨欲来……")
	return h, &body
}

// ① 默认模板**就是**段库里那条内置段（不是代码里另留一份字符串）。
func TestTitleDefaultTemplateLivesInSegmentLibrary(t *testing.T) {
	h, body := stubHarness(t, config.AbilityToggle{Provider: new("stub"), Model: new("m")})

	builtin, ok := config.BuiltinSegmentByID(config.TitleTemplateSegmentID)
	if !ok {
		t.Fatalf("段库里该有内置段 %q", config.TitleTemplateSegmentID)
	}
	if got := abilities.DefaultTemplate(abilities.Title); got != builtin.Text {
		t.Fatalf("起标题的默认模板该逐字等于内置段：\n得到 %q\n内置 %q", got, builtin.Text)
	}

	if _, err := h.service.Generate(context.Background(), h.session); err != nil {
		t.Fatal(err)
	}
	if got := systemOf(t, *body); got != builtin.Text {
		t.Fatalf("内置段的正文该**逐字**进 system：\n得到 %q\n内置 %q", got, builtin.Text)
	}
}

// ② 能力上挑的段真会拼进来（从前 title 这条路根本不读 `settings.Segments`）。
func TestTitleAppliesAbilitySegments(t *testing.T) {
	style := config.Segment{ID: "风格", Text: "标题只准用四个字。"}
	h, body := stubHarness(t,
		config.AbilityToggle{Provider: new("stub"), Model: new("m"), Segments: []string{"风格"}},
		style)

	result, err := h.service.Generate(context.Background(), h.session)
	if err != nil {
		t.Fatal(err)
	}
	sent := systemOf(t, *body)
	if !strings.Contains(sent, style.Text) {
		t.Fatalf("挑进来的段该进 system：%s", sent)
	}
	// 顺序：模板在前、段在后（与段列表的顺序一致）
	template := abilities.DefaultTemplate(abilities.Title)
	if strings.Index(sent, template) > strings.Index(sent, style.Text) {
		t.Fatalf("模板该排在段前面：%s", sent)
	}
	// 指纹从**拼完的**模板算 ⇒ 加一个段就该变（不然"同一版提示词"这个说法是假的）
	if result.PromptVersion == abilities.PromptVersion(template) {
		t.Fatal("加了段之后 prompt_version 该变")
	}
	if result.PromptVersion != abilities.PromptVersion(template+"\n\n"+style.Text) {
		t.Fatalf("prompt_version 该由拼完的提示词算：%d", result.PromptVersion)
	}
}

// ③ 把默认模板那条段自己挑进列表 ⇒ 按它放的位置出现一次，开头不再垫一份。
func TestTitleTemplateSegmentIsNotPrependedTwice(t *testing.T) {
	style := config.Segment{ID: "风格", Text: "标题只准用四个字。"}
	h, body := stubHarness(t,
		config.AbilityToggle{Provider: new("stub"), Model: new("m"),
			Segments: []string{config.TitleTemplateSegmentID, "风格"}},
		style)

	if _, err := h.service.Generate(context.Background(), h.session); err != nil {
		t.Fatal(err)
	}
	sent := systemOf(t, *body)
	// 一个「起标题助手」已经够多了：两份说明文只会让模型更糊涂
	if count := strings.Count(sent, "你是一名起标题助手"); count != 1 {
		t.Fatalf("默认模板只能出现一次，实际 %d 次：%s", count, sent)
	}
	if strings.Index(sent, "你是一名起标题助手") > strings.Index(sent, style.Text) {
		t.Fatalf("把模板段挑进来就是「按我这个位置放」：%s", sent)
	}
}
