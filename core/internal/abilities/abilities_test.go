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
		t.Fatal("没写 abilities 的 agent：在册的能力都该是开着的")
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
	// 退役的 judge（老配置里还写着它）：**照实解析**，不报错、没提到就是默认全开 ——
	// 只把它从"能力清单"里拿掉，不是把它变成一颗地雷。
	if setting := Resolve(agent, Judge); !setting.Enabled {
		t.Fatal("退役的 id 也得照实解析（老配置里还写着它）")
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
	if !Valid(Compact) || !Valid(Title) || Valid(Judge) || Valid("turn") {
		t.Fatal("在册能力就是 title / compact 两个（judge 已退役、turn 不是开关）")
	}
}

// `judge` 2026-10-28 退役（用户：「暂时砍掉 agent 的 judge 功能」）：**老配置不许因此读不进来**。
//
// 现场那份 `agents.json` 就有四个 agent 写着 `"judge": {…}` —— 把退役 id 当"写错"报，
// 会让整份人格读不进来、服务起不来。所以：不认它，但**放行**。
func TestRetiredJudgeIsToleratedNotRejected(t *testing.T) {
	agents := config.AgentsConfig{Agents: []config.Agent{
		{ID: "老文件", Abilities: map[string]config.AbilityToggle{
			Judge:   {Enabled: new(false)},
			Compact: {Enabled: new(true)},
		}},
	}}
	if err := Validate(agents); err != nil {
		t.Fatalf("退役的 id 不该让整份人格读不进来：%v", err)
	}
	// 但也不能假装它还生效：列出来的能力里没有它。
	for _, id := range IDs() {
		if id == Judge {
			t.Fatalf("退役的 judge 不该还在能力清单里：%v", IDs())
		}
	}
	// 真·写错的 id 仍然要报（退役不等于"什么都不查了"）。
	typo := config.AgentsConfig{Agents: []config.Agent{
		{ID: "写错的", Abilities: map[string]config.AbilityToggle{"judgee": {}}},
	}}
	if err := Validate(typo); err == nil || !strings.Contains(err.Error(), "judgee") {
		t.Fatalf("写错的 id 仍该报出来：%v", err)
	}
}

// 生效模板 = agent 覆盖 ?: 代码里的默认；**prompt_version 由生效模板算**（改一个字就变）。
//
// 这也是"能力卡带"那条口径的落点：默认模板只有一份（住这张表），覆盖只写在 agent 上。
func TestEffectiveTemplateAndPromptVersion(t *testing.T) {
	for _, id := range []ID{Title, Compact} {
		if DefaultTemplate(id) == "" {
			t.Fatalf("%s 该有默认模板（流程实现了就得有它那一段提示词）", id)
		}
	}
	if DefaultTemplate(Judge) != "" || DefaultTemplate("没有这个能力") != "" {
		t.Fatal("退役的 / 不认识的：没有模板")
	}
	// 没覆盖 ⇒ 默认那句；覆盖了 ⇒ 覆盖那句
	bare := Resolve(config.Agent{ID: "a"}, Compact)
	if Template(bare, Compact) != DefaultTemplate(Compact) {
		t.Fatal("没写 prompt 就该用默认模板")
	}
	agent := config.Agent{ID: "a", Abilities: map[string]config.AbilityToggle{
		Compact: {Prompt: new("只写叙事，别写流水账。")},
	}}
	overridden := Resolve(agent, Compact)
	if Template(overridden, Compact) != "只写叙事，别写流水账。" {
		t.Fatalf("覆盖了就该用覆盖的那句：%q", Template(overridden, Compact))
	}
	if PromptVersion(Template(overridden, Compact)) == PromptVersion(Template(bare, Compact)) {
		t.Fatal("换了模板，指纹必须跟着变")
	}
	// 未知 id 也照这套规则解析（能不能用是调用点的事）；模板为空 ⇒ 空串
	if Template(Resolve(config.Agent{ID: "a"}, "没有这个能力"), "没有这个能力") != "" {
		t.Fatal("不认识的 id 没有默认模板")
	}
}
