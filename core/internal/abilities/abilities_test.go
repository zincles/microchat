package abilities

import (
	"strings"
	"testing"

	"microchat/internal/config"
)

// 归真：`enabled` 缺字段 = **默认全开**（现有 agents.json 一个字都不用改）。
func TestMissingToggleMeansEverythingOn(t *testing.T) {
	setting := Resolve(config.Agent{ID: "a"}, Compact)
	if !setting.Enabled {
		t.Fatal("没写 abilities 的 agent：三个能力都该是开着的")
	}
	if setting.Provider != "" || setting.Model != "" || setting.Prompt != "" {
		t.Fatalf("没写就没有覆盖：%+v", setting)
	}
}

// 只写了一半（比如只填了 model）**不许**把能力变成关掉 —— `Enabled` 是 `*bool` 的理由就在这儿。
func TestPartialToggleStaysEnabled(t *testing.T) {
	agent := config.Agent{ID: "a", Abilities: map[string]config.AbilityToggle{
		Compact: {Model: new("便宜模型")},
	}}
	setting := Resolve(agent, Compact)
	if !setting.Enabled {
		t.Fatal("没写 enabled 就是开着")
	}
	if setting.Model != "便宜模型" || setting.Provider != "" {
		t.Fatalf("覆盖 = %+v", setting)
	}
}

// 明确关掉 / 明确换渠道换模板。
func TestToggleOverrides(t *testing.T) {
	agent := config.Agent{ID: "a", Abilities: map[string]config.AbilityToggle{
		Compact: {Enabled: new(false), Provider: new("dummy"), Prompt: new("只写叙事。")},
		Title:   {Enabled: new(false)},
	}}
	if setting := Resolve(agent, Compact); setting.Enabled || setting.Provider != "dummy" || setting.Prompt != "只写叙事。" {
		t.Fatalf("compact = %+v", setting)
	}
	if setting := Resolve(agent, Title); setting.Enabled {
		t.Fatal("title 也被关掉了")
	}
	if setting := Resolve(agent, Judge); !setting.Enabled {
		t.Fatal("没提到的能力保持默认全开")
	}
}

// 空白不算覆盖（`"  "` 等于没写）。
func TestBlankOverrideIsNoOverride(t *testing.T) {
	agent := config.Agent{ID: "a", Abilities: map[string]config.AbilityToggle{
		Compact: {Provider: new("   "), Model: new("   "), Prompt: new("   ")},
	}}
	setting := Resolve(agent, Compact)
	if setting.Provider != "" || setting.Model != "" || setting.Prompt != "" {
		t.Fatalf("空白该等于没写：%+v", setting)
	}
}

// 未知的能力 id：**说得清的错**（写配置时拒；不静默忽略 —— 配了不生效最难查）。
func TestValidateRejectsUnknownIDs(t *testing.T) {
	agents := config.AgentsConfig{Agents: []config.Agent{
		{ID: "只写题的", Abilities: map[string]config.AbilityToggle{"compactt": {}}},
	}}
	err := Validate(agents)
	if err == nil || !strings.Contains(err.Error(), "compactt") || !strings.Contains(err.Error(), "只写题的") {
		t.Fatalf("该报出是哪个 agent 上的哪个 id：%v", err)
	}
	if err := Validate(config.AgentsConfig{Agents: []config.Agent{{ID: "好的"}}}); err != nil {
		t.Fatalf("没写 abilities 的 agent 不该报错：%v", err)
	}
	if !Valid(Compact) || !Valid(Title) || !Valid(Judge) || Valid("turn") {
		t.Fatal("能力 id 就是 title / compact / judge 三个（turn 不是开关）")
	}
}
