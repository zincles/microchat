package config

import (
	"os"
	"reflect"
	"testing"
)

// withoutBuiltins：测试用户规则本身时停用全部系统内置规则，避免品牌/工具规则串入。
func withoutBuiltins(rules []ModelDefineRule) Config {
	cfg := Config{ModelDefineRules: rules}
	for _, rule := range builtinModelRules {
		cfg.DisabledBuiltinModelRules = append(cfg.DisabledBuiltinModelRules, rule.ID)
	}
	return cfg
}

// 顺序覆写：后命中的覆盖前面的；is_final 应用完就停。
func TestModelDefineRulesOverrideAndFinal(t *testing.T) {
	levels := []string{"low", "high"}
	cfg := withoutBuiltins([]ModelDefineRule{
		{ID: "brand", MatchContains: "grok", Brand: stringPtr("xAI")},
		{ID: "icon", MatchContains: "grok", Icon: stringPtr("grok")},
		{ID: "thinking", MatchContains: "grok", ThinkingLevels: &levels},
		{ID: "stop", MatchContains: "grok", IsFinal: true, Brand: stringPtr("stop")},
		{ID: "late", MatchContains: "grok", Brand: stringPtr("late")},
	})
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
	cfg := withoutBuiltins([]ModelDefineRule{
		{ID: "global", MatchContains: "grok", IsFinal: true, Brand: stringPtr("global"),
			ThinkingLevels: &globalLevels},
	})
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
	cfg := withoutBuiltins([]ModelDefineRule{
		{ID: "only-xai", MatchContains: "GROK", MatchProvider: "xai", Brand: stringPtr("xAI")},
	})
	if got := cfg.ModelProfile("other", "grok-4.7", ModelProfile{}); got.Brand != "" {
		t.Fatalf("provider 不匹配却命中了：%+v", got)
	}
	if got := cfg.ModelProfile("xai", "grok-4.7", ModelProfile{}); got.Brand != "xAI" {
		t.Fatalf("大小写/provider 匹配失败：%+v", got)
	}
	if got := (withoutBuiltins(nil)).ModelProfile("xai", "grok-4.7", ModelProfile{ThinkingLevels: []string{"low"}}); got.Brand != "" ||
		!reflect.DeepEqual(got.ThinkingLevels, []string{"low"}) {
		t.Fatalf("空规则集该保留 fallback：%+v", got)
	}
}

// 空数组 = 明确覆写成没有档位（与 nil = 不动不同）。
func TestModelDefineRulesEmptyThinkingLevels(t *testing.T) {
	empty := []string{}
	cfg := withoutBuiltins([]ModelDefineRule{
		{ID: "no-thinking", MatchContains: "grok", ThinkingLevels: &empty},
	})
	got := cfg.ModelProfile("xai", "grok", ModelProfile{ThinkingLevels: []string{"low"}})
	if len(got.ThinkingLevels) != 0 {
		t.Fatalf("空数组该明确清空：%+v", got.ThinkingLevels)
	}
}

// 系统内置规则：品牌/图标来自 builtin_model_rules.json。
func TestBuiltinModelDefineRules(t *testing.T) {
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
		{"LongCat-2.5-Preview", "longcat"},
		{"mistral-large-2512", "mistral"},
		{"Qwen3-Coder-Plus", "qwen"},
		{"MiniMax-M2.5", "minimax"},
		{"hunyuan-t1", "tencent"},
		{"hy3-preview", "tencent"},
		{"hy4", "tencent"},
		{"hy-mt2-plus", "tencent"},
		{"llama-4-scout-17b-16e-instruct", "meta"},
		{"muse-spark-1.3", "meta"},
		{"stepfun-ai/step-3.5-flash", "stepfun"},
		{"step-3.5-flash", "stepfun"},
		{"doubao-seed-1-6-251015", "bytedance"},
		{"seed-oss-36b-instruct", "bytedance"},
	} {
		if got := cfg.ModelProfile("p", tc.model, ModelProfile{}); got.Icon != tc.icon {
			t.Fatalf("%s 默认图标 = %q，想要 %q", tc.model, got.Icon, tc.icon)
		}
	}
	// 保守匹配：裸 hy / volcengine 前缀不能抢走别的家族。
	for _, model := range []string{"hymba-1.5b", "hybrid-model-x"} {
		if got := cfg.ModelProfile("p", model, ModelProfile{}); got.Icon != "" {
			t.Fatalf("%s 不该被 Hy 规则匹配，得到 %q", model, got.Icon)
		}
	}
	if got := cfg.ModelProfile("volcengine", "volcengine/deepseek-v4-pro", ModelProfile{}); got.Icon != "deepseek" {
		t.Fatalf("volcengine 上的 deepseek 被抢成 %q", got.Icon)
	}
	if err := ValidateModelDefineRules(cfg.ModelDefineRules); err != nil {
		t.Fatalf("默认规则该合法：%v", err)
	}
}

func TestModelIconsAreClosedSet(t *testing.T) {
	for _, icon := range ModelIconIDs {
		if !ValidModelIcon(icon) {
			t.Fatalf("%q 该是合法图标", icon)
		}
	}
	if ValidModelIcon("openai") || ValidModelIcon("xai") {
		t.Fatal("旧图标名不该再是合法写入值")
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

// 回归：config.json 里有 model_define_rules 时，不能再继承代码默认规则的字段
// （2026-11-09 实测过“gemini 继承了默认列表某条 native 规则”的静默串值）。
func TestLoadConfigDoesNotMergeDefaultRuleFields(t *testing.T) {
	dir := t.TempDir()
	old := `{"model_define_rules":[
		{"id":"gpt","match_contains":"gpt","brand":"OpenAI","icon":"gpt"},
		{"id":"xai-image","match_contains":"grok-imagine","is_final":true,"thinking_levels":[]},
		{"id":"grok","match_contains":"grok","brand":"xAI","icon":"grok"},
		{"id":"gemini","match_contains":"gemini","brand":"Google","icon":"gemini"},
		{"id":"claude","match_contains":"claude","brand":"Anthropic","icon":"claude"},
		{"id":"deepseek","match_contains":"deepseek","brand":"DeepSeek","icon":"deepseek"},
		{"id":"mimo","match_contains":"mimo","brand":"Xiaomi","icon":"mimo"},
		{"id":"glm","match_contains":"glm","brand":"Zhipu","icon":"glm"},
		{"id":"kimi","match_contains":"kimi","brand":"Moonshot","icon":"kimi"}
	]}`
	paths := Paths{ConfigDir: dir}
	if err := os.WriteFile(paths.Config("config.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range cfg.ModelDefineRules {
		if rule.ID == "gemini" && (rule.MatchProtocol != "" || (rule.ServerTools != nil && len(*rule.ServerTools) != 0)) {
			t.Fatalf("gemini 串到了默认 native 规则字段：%+v", rule)
		}
	}
}

func TestConfiguredSearchToolRules(t *testing.T) {
	// 这些条目住在「全局模型规则」里（用户或内置），不在 Go 内存里的特例分支。
	// 这里只验证规则机制：只要 server_tools 命中，chat / response 两种协议都一致。
	cfg := withoutBuiltins([]ModelDefineRule{
		{ID: "openai-image", MatchContains: "gpt-image", IsFinal: true},
		{ID: "openai-gpt-native", MatchContains: "gpt", ServerTools: stringSlicePtr("web_search")},
		{ID: "gemini-native", MatchContains: "gemini", ServerTools: stringSlicePtr("web_search")},
		{ID: "xai-grok-4-native", MatchContains: "grok-4", MatchProtocol: "openai-response",
			ServerTools: stringSlicePtr("x_search", "web_search", "code_execution")},
	})
	cases := []struct {
		name     string
		model    string
		protocol string
		want     []string
	}{
		{"gpt response", "gpt-5", "openai-response", []string{"web_search"}},
		{"gpt chat", "gpt-5", "openai-chat-completion", []string{"web_search"}},
		{"gemini response", "gemini-4-argon", "openai-response", []string{"web_search"}},
		{"gemini chat", "gemini-4-argon", "openai-chat-completion", []string{"web_search"}},
		{"gpt 图像排除", "gpt-image-1", "openai-response", nil},
		{"grok response", "grok-4.7", "openai-response", []string{"x_search", "web_search", "code_execution"}},
		{"grok chat 受规则协议限制", "grok-4.7", "openai-chat-completion", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.ModelProfileFor("p", tc.model, tc.protocol, ModelProfile{})
			if len(got.ServerTools) != len(tc.want) {
				t.Fatalf("server_tools = %v，想要 %v", got.ServerTools, tc.want)
			}
			for i := range tc.want {
				if got.ServerTools[i] != tc.want[i] {
					t.Fatalf("server_tools = %v，想要 %v", got.ServerTools, tc.want)
				}
			}
		})
	}
}

func TestBuiltinRulesMergeDisableAndValidation(t *testing.T) {
	user := []ModelDefineRule{{ID: "user-gpt", MatchContains: "gpt", Brand: stringPtr("用户品牌")}}
	cfg := Config{ModelDefineRules: user}
	got := cfg.ModelProfile("p", "gpt-5", ModelProfile{})
	if got.Brand != "用户品牌" {
		t.Fatalf("用户规则应覆盖内置品牌：%+v", got)
	}
	if !containsString(got.MatchedRules, "global:gpt") || !containsString(got.MatchedRules, "global:user-gpt") {
		t.Fatalf("matched_rules 应含内置与用户：%v", got.MatchedRules)
	}

	cfg.DisabledBuiltinModelRules = []string{"gpt"}
	got = cfg.ModelProfile("p", "gpt-5", ModelProfile{})
	if containsString(got.MatchedRules, "global:gpt") {
		t.Fatalf("停用后不该命中内置 gpt：%v", got.MatchedRules)
	}
	if got.Brand != "用户品牌" {
		t.Fatalf("停用内置后用户规则仍生效：%+v", got)
	}

	// 内置 grok 工具规则也可停用；停用后没有任何搜索工具。
	grok := Config{DisabledBuiltinModelRules: []string{"xai-grok-4-native"}}
	got = grok.ModelProfileFor("xai", "grok-4.7", "openai-response", ModelProfile{})
	if len(got.ServerTools) != 0 {
		t.Fatalf("停用内置 grok 工具后仍有 tools：%v", got.ServerTools)
	}

	// 用户规则不能复用内置 id；停用列表只能引用真实内置 id。
	if err := ValidateUserModelDefineRules([]ModelDefineRule{{ID: "gpt", MatchContains: "gpt", Brand: stringPtr("x")}}); err == nil {
		t.Fatal("用户规则复用内置 id 该报错")
	}
	if err := ValidateDisabledBuiltinModelRules([]string{"no-such-rule"}); err == nil {
		t.Fatal("未知停用 id 该报错")
	}

	view := cfg.ModelRulesView()
	if len(view.BuiltinModelRules) == 0 || !view.BuiltinModelRules[0].Builtin {
		t.Fatalf("内置规则视图不对：%+v", view.BuiltinModelRules)
	}
	found := false
	for _, rule := range view.BuiltinModelRules {
		if rule.ID == "gpt" {
			found = rule.Disabled
		}
	}
	if !found {
		t.Fatal("已停用的内置规则没有标记 disabled")
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestLocalToolsAreSeparateFromServerTools(t *testing.T) {
	cfg := withoutBuiltins([]ModelDefineRule{
		{ID: "local", MatchContains: "grok", MatchProtocol: "response", LocalTools: stringSlicePtr("roll_dice", "roll_dice", "lookup")},
		{ID: "server", MatchContains: "grok", MatchProtocol: "response", ServerTools: stringSlicePtr("x_search")},
	})
	got := cfg.ModelProfileFor("p", "grok-4.7", "openai-response", ModelProfile{})
	if !reflect.DeepEqual(got.LocalTools, []string{"roll_dice", "lookup"}) {
		t.Fatalf("local_tools = %v", got.LocalTools)
	}
	if !reflect.DeepEqual(got.ServerTools, []string{"x_search"}) {
		t.Fatalf("server_tools = %v", got.ServerTools)
	}
}
