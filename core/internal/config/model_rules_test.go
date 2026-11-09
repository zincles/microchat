package config

import (
	"os"
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

func TestEnsureModelRuleDefaultsMigratesOldDefaultSet(t *testing.T) {
	old := []ModelDefineRule{
		{ID: "gpt", MatchContains: "gpt"},
		{ID: "xai-image", MatchContains: "grok-imagine"},
		{ID: "grok", MatchContains: "grok"},
		{ID: "gemini", MatchContains: "gemini"},
		{ID: "claude", MatchContains: "claude"},
		{ID: "deepseek", MatchContains: "deepseek"},
		{ID: "mimo", MatchContains: "mimo"},
		{ID: "glm", MatchContains: "glm"},
		{ID: "kimi", MatchContains: "kimi"},
	}
	got := ensureModelRuleDefaults(old)
	if len(got) != len(DefaultModelDefineRules()) {
		t.Fatalf("旧默认规则没有补齐：got %d want %d", len(got), len(DefaultModelDefineRules()))
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	for _, r := range DefaultModelDefineRules() {
		if !ids[r.ID] {
			t.Fatalf("缺少新默认规则 %s", r.ID)
		}
	}
}

// 2026-11-09：grok-4.x + Response 默认追加 xAI native tools；协议不对/不是 grok-4 不追加。
func TestDefaultGrok4NativeToolRule(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		name     string
		provider string
		model    string
		protocol string
		want     []string
	}{
		{"response 4.7 任意 vendor", "openrouter", "x-ai/grok-4.7", "openai-response", []string{"x_search", "web_search", "code_execution"}},
		{"response 4.6", "xai", "grok-4.6", "openai-response", []string{"x_search", "web_search", "code_execution"}},
		{"response 4.x", "custom", "grok-4.20-0309", "openai-response", []string{"x_search", "web_search", "code_execution"}},
		{"chat 不追加", "xai", "grok-4.7", "openai-chat-completion", nil},
		{"grok-3 不追加", "xai", "grok-3", "openai-response", nil},
		{"图像模型不追加", "xai", "grok-imagine-image-2.0", "openai-response", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.ModelProfileFor(tc.provider, tc.model, tc.protocol, ModelProfile{})
			if len(got.NativeTools) != len(tc.want) {
				t.Fatalf("native_tools = %v，想要 %v", got.NativeTools, tc.want)
			}
			for i := range tc.want {
				if got.NativeTools[i] != tc.want[i] {
					t.Fatalf("native_tools = %v，想要 %v", got.NativeTools, tc.want)
				}
			}
		})
	}
}

func TestNativeToolsAppendAndDedupe(t *testing.T) {
	cfg := Config{ModelDefineRules: []ModelDefineRule{
		{ID: "one", MatchContains: "grok", MatchProtocol: "response", NativeTools: stringSlicePtr("x_search", "web_search")},
		{ID: "two", MatchContains: "grok", MatchProtocol: "response", NativeTools: stringSlicePtr("web_search", "code_execution")},
	}}
	got := cfg.ModelProfileFor("p", "grok-4.7", "openai-response", ModelProfile{})
	want := []string{"x_search", "web_search", "code_execution"}
	if !reflect.DeepEqual(got.NativeTools, want) {
		t.Fatalf("追加/去重 = %v，想要 %v", got.NativeTools, want)
	}
	if !reflect.DeepEqual(got.MatchedRules, []string{"global:one", "global:two"}) {
		t.Fatalf("matched_rules = %v", got.MatchedRules)
	}
}

func TestValidateNativeToolRules(t *testing.T) {
	valid := []ModelDefineRule{{MatchContains: "x", NativeTools: stringSlicePtr("x_search")}}
	if err := ValidateModelDefineRules(valid); err != nil {
		t.Fatalf("只追加 native tool 的规则该合法：%v", err)
	}
	for name, rule := range map[string]ModelDefineRule{
		"未知工具":  {MatchContains: "x", NativeTools: stringSlicePtr("nope")},
		"重复工具":  {MatchContains: "x", NativeTools: stringSlicePtr("x_search", "x_search")},
		"空工具数组": {MatchContains: "x", NativeTools: stringSlicePtr()},
		"未知协议":  {MatchContains: "x", MatchProtocol: "grpc", NativeTools: stringSlicePtr("x_search")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateModelDefineRules([]ModelDefineRule{rule}); err == nil {
				t.Fatalf("该报错：%+v", rule)
			}
		})
	}
}

// 回归：解码进 DefaultConfig 的已有 slice 时，JSON 不会清零结构体字段。若不清空
// ModelDefineRules，gemini（旧配置第 4 条）会继承默认列表第 4 条 xai-grok-4-native
// 的 match_protocol / native_tools —— 2026-11-09 实测到的静默串值。
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
		switch rule.ID {
		case "gemini":
			if rule.MatchProtocol != "" || (rule.NativeTools != nil && len(*rule.NativeTools) != 0) {
				t.Fatalf("gemini 串到了默认 native 规则字段：%+v", rule)
			}
		case "xai-grok-4-native":
			if rule.NativeTools == nil || len(*rule.NativeTools) != 3 {
				t.Fatalf("新默认 native 规则没补上：%+v", rule)
			}
		}
	}
}
