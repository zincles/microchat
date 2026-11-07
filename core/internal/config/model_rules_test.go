package config

import (
	"reflect"
	"testing"
)

// 顺序覆写：后命中的覆盖前面的；is_final 应用完就停。
func TestModelDefineRulesOverrideAndFinal(t *testing.T) {
	levels := []string{"low", "high"}
	cfg := Config{ModelDefineRules: []ModelDefineRule{
		{ID: "brand", MatchContains: "grok", Brand: stringPtr("xAI")},
		{ID: "icon", MatchContains: "grok", Icon: stringPtr("grok")},
		{ID: "thinking", MatchContains: "grok", ThinkingLevels: &levels},
		{ID: "stop", MatchContains: "grok", IsFinal: true, Brand: stringPtr("stop")},
		{ID: "late", MatchContains: "grok", Brand: stringPtr("late")},
	}}
	got := cfg.ModelProfile("xai", "x-ai/grok-4.7", ModelProfile{ThinkingLevels: []string{"off", "max"}})
	if got.Brand != "stop" || got.Icon != "grok" {
		t.Fatalf("覆盖顺序不对：%+v", got)
	}
	if !reflect.DeepEqual(got.ThinkingLevels, levels) {
		t.Fatalf("thinking_levels 该整组替换：%+v", got.ThinkingLevels)
	}
	want := []string{"global:brand", "global:icon", "global:thinking", "global:stop"}
	if !reflect.DeepEqual(got.MatchedRules, want) {
		t.Fatalf("matched_rules = %v，想要 %v", got.MatchedRules, want)
	}
}

// 全局先、渠道后；全局的 is_final 不能挡住渠道覆盖。
func TestProviderRulesWinOverGlobal(t *testing.T) {
	globalLevels := []string{"low"}
	providerLevels := []string{"high", "max"}
	cfg := Config{ModelDefineRules: []ModelDefineRule{
		{ID: "global", MatchContains: "grok", IsFinal: true, Brand: stringPtr("global"),
			ThinkingLevels: &globalLevels},
	}}
	got := cfg.ModelProfileWithRules("xai", "grok-4.7", ModelProfile{}, []ModelDefineRule{
		{ID: "custom", MatchContains: "grok", Brand: stringPtr("custom"),
			ThinkingLevels: &providerLevels},
	})
	if got.Brand != "custom" || !reflect.DeepEqual(got.ThinkingLevels, providerLevels) {
		t.Fatalf("渠道规则该覆盖全局：%+v", got)
	}
	if !reflect.DeepEqual(got.MatchedRules, []string{"global:global", "provider:custom"}) {
		t.Fatalf("matched_rules = %v", got.MatchedRules)
	}
}

// 匹配 raw id（大小写不敏感）+ provider；空规则集保留 fallback。
func TestModelDefineRulesMatchRawIDAndProvider(t *testing.T) {
	cfg := Config{ModelDefineRules: []ModelDefineRule{
		{ID: "only-xai", MatchContains: "GROK", MatchProvider: "xai", Brand: stringPtr("xAI")},
	}}
	if got := cfg.ModelProfile("other", "grok-4.7", ModelProfile{}); got.Brand != "" {
		t.Fatalf("provider 不匹配却命中了：%+v", got)
	}
	if got := cfg.ModelProfile("xai", "grok-4.7", ModelProfile{}); got.Brand != "xAI" {
		t.Fatalf("大小写/provider 匹配失败：%+v", got)
	}
	if got := (Config{}).ModelProfile("xai", "grok-4.7", ModelProfile{ThinkingLevels: []string{"low"}}); got.Brand != "" ||
		!reflect.DeepEqual(got.ThinkingLevels, []string{"low"}) {
		t.Fatalf("空规则集该保留 fallback：%+v", got)
	}
}

// 空数组 = 明确覆写成没有档位（与 nil = 不动不同）。
func TestModelDefineRulesEmptyThinkingLevels(t *testing.T) {
	empty := []string{}
	cfg := Config{ModelDefineRules: []ModelDefineRule{
		{ID: "no-thinking", MatchContains: "grok", ThinkingLevels: &empty},
	}}
	got := cfg.ModelProfile("xai", "grok", ModelProfile{ThinkingLevels: []string{"low"}})
	if len(got.ThinkingLevels) != 0 {
		t.Fatalf("空数组该明确清空：%+v", got.ThinkingLevels)
	}
}

// 默认规则：gpt/grok 各自有品牌和图标；用户可以写 [] 关掉。
func TestDefaultModelDefineRules(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.ModelProfile("openai", "gpt-5", ModelProfile{}); got.Brand != "OpenAI" || got.Icon != "gpt" {
		t.Fatalf("gpt 默认规则 = %+v", got)
	}
	if got := cfg.ModelProfile("xai", "grok-4.7", ModelProfile{}); got.Brand != "xAI" || got.Icon != "grok" {
		t.Fatalf("grok 默认规则 = %+v", got)
	}
	// 图像模型也带 grok：默认规则要清空思考档，而不是露出七档。
	if got := cfg.ModelProfile("xai", "grok-imagine-image-2.0", ModelProfile{ThinkingLevels: []string{"low"}}); len(got.ThinkingLevels) != 0 ||
		len(got.MatchedRules) != 1 || got.MatchedRules[0] != "global:xai-image" {
		t.Fatalf("grok-imagine 默认规则 = %+v", got)
	}
	for _, tc := range []struct {
		model string
		icon  string
	}{
		{"gemini-4-argon", "gemini"},
		{"claude-opus-5-5", "claude"},
		{"deepseek-v4-flash", "deepseek"},
		{"mimo-v2.6-flash", "mimo"},
		{"glm-5.2", "glm"},
		{"kimi-k2", "kimi"},
	} {
		if got := cfg.ModelProfile("p", tc.model, ModelProfile{}); got.Icon != tc.icon {
			t.Fatalf("%s 默认图标 = %q，想要 %q", tc.model, got.Icon, tc.icon)
		}
	}
	if err := ValidateModelDefineRules(cfg.ModelDefineRules); err != nil {
		t.Fatalf("默认规则该合法：%v", err)
	}
}

func TestModelIconsAreClosedSetAndLegacyIsNormalized(t *testing.T) {
	for _, icon := range ModelIconIDs {
		if !ValidModelIcon(icon) {
			t.Fatalf("%q 该是合法图标", icon)
		}
	}
	if ValidModelIcon("openai") || ValidModelIcon("xai") {
		t.Fatal("旧图标名不该再是合法写入值")
	}
	old := []ModelDefineRule{
		{Icon: stringPtr("openai")},
		{Icon: stringPtr("xai")},
	}
	normalizeLegacyModelIcons(old)
	if *old[0].Icon != "gpt" || *old[1].Icon != "grok" {
		t.Fatalf("旧图标没归一化：%+v", old)
	}
	if err := ValidateModelDefineRules([]ModelDefineRule{{MatchContains: "x", Icon: stringPtr("openai")}}); err == nil {
		t.Fatal("未知图标该报错")
	}
}

func TestValidateModelDefineRules(t *testing.T) {
	badLevel := []string{"ultra"}
	for _, tc := range []struct {
		name  string
		rules []ModelDefineRule
	}{
		{"空匹配", []ModelDefineRule{{Brand: stringPtr("x")}}},
		{"空定义", []ModelDefineRule{{MatchContains: "x"}}},
		{"未知档位", []ModelDefineRule{{MatchContains: "x", ThinkingLevels: &badLevel}}},

		{"重复 id", []ModelDefineRule{{ID: "a", MatchContains: "x", Brand: stringPtr("x")}, {ID: "a", MatchContains: "y", Brand: stringPtr("y")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateModelDefineRules(tc.rules); err == nil {
				t.Fatalf("该报错：%+v", tc.rules)
			}
		})
	}
	if err := ValidateModelDefineRules(nil); err != nil {
		t.Fatalf("空规则集是合法清空：%v", err)
	}
}
