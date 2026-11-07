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
		{ID: "icon", MatchContains: "grok", Icon: stringPtr("xai")},
		{ID: "thinking", MatchContains: "grok", ThinkingLevels: &levels},
		{ID: "stop", MatchContains: "grok", IsFinal: true, Brand: stringPtr("stop")},
		{ID: "late", MatchContains: "grok", Brand: stringPtr("late")},
	}}
	got := cfg.ModelProfile("xai", "x-ai/grok-4.7", ModelProfile{ThinkingLevels: []string{"off", "max"}})
	if got.Brand != "stop" || got.Icon != "xai" {
		t.Fatalf("覆盖顺序不对：%+v", got)
	}
	if !reflect.DeepEqual(got.ThinkingLevels, levels) {
		t.Fatalf("thinking_levels 该整组替换：%+v", got.ThinkingLevels)
	}
	want := []string{"brand", "icon", "thinking", "stop"}
	if !reflect.DeepEqual(got.MatchedRules, want) {
		t.Fatalf("matched_rules = %v，想要 %v", got.MatchedRules, want)
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
	if got := cfg.ModelProfile("openai", "gpt-5", ModelProfile{}); got.Brand != "OpenAI" || got.Icon != "openai" {
		t.Fatalf("gpt 默认规则 = %+v", got)
	}
	if got := cfg.ModelProfile("xai", "grok-4.7", ModelProfile{}); got.Brand != "xAI" || got.Icon != "xai" {
		t.Fatalf("grok 默认规则 = %+v", got)
	}
	// 图像模型也带 grok：默认规则要清空思考档，而不是露出七档。
	if got := cfg.ModelProfile("xai", "grok-imagine-image-2.0", ModelProfile{ThinkingLevels: []string{"low"}}); len(got.ThinkingLevels) != 0 ||
		len(got.MatchedRules) != 1 || got.MatchedRules[0] != "xai-image" {
		t.Fatalf("grok-imagine 默认规则 = %+v", got)
	}
	if err := ValidateModelDefineRules(cfg.ModelDefineRules); err != nil {
		t.Fatalf("默认规则该合法：%v", err)
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
