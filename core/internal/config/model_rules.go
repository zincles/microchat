// Package config：模型定义规则（model_define_rules）。
//
// 这是一组**纯数据 override**：按顺序匹配模型的 raw `upstream_id`（可选 provider），
// 命中的规则把 `brand` / `icon` / `thinking_levels` 覆盖到模型画像上；`is_final` 让它
// 可以退化成“首个命中即停止”。规则不执行任何代码，也不碰 wire 协议。
//
// 三条不能松的口径：
//  1. 模型行是发现数据，刷新会消失；规则是配置，必须能独立存活；
//  2. 匹配 raw id，不匹配用户改过的 display_name；
//  3. 未知思考档位 / 空规则 / 空匹配在读取与写入时都报错，不静默跳过。
package config

import (
	"fmt"
	"strings"

	"microchat/internal/model"
)

// ModelDefineRule：一条模型定义规则。字段名用 snake_case，与 config.json 其余部分一致。
//
// `ThinkingLevels` / `Brand` / `Icon` 都是指针：nil = 这条规则不碰该字段；
// 空数组（`[]`）与空串是明确的“覆写成空”，与“没写”不是一回事。
type ModelDefineRule struct {
	ID string `json:"id,omitempty"`
	// MatchContains / MatchExact：对 raw upstream_id 做大小写不敏感匹配。
	// 两者都写 = 必须同时满足（一般只用其中一个）。
	MatchContains string `json:"match_contains,omitempty"`
	MatchExact    string `json:"match_exact,omitempty"`
	// MatchProvider：可选，按 provider id 限定（例如只对 xai 渠道生效）。
	MatchProvider string `json:"match_provider,omitempty"`
	// IsFinal：命中并应用本规则后停止继续匹配。宽匹配 + final 会遮蔽后面的具体规则，
	// 所以内置规则一律不 final。
	IsFinal bool `json:"is_final,omitempty"`

	ThinkingLevels *[]string `json:"thinking_levels,omitempty"`
	Brand          *string   `json:"brand,omitempty"`
	Icon           *string   `json:"icon,omitempty"`
}

// ModelProfile：一个模型经过规则覆写后的展示/能力画像（派生结果，不落库）。
type ModelProfile struct {
	Brand          string   `json:"brand,omitempty"`
	Icon           string   `json:"icon,omitempty"`
	ThinkingLevels []string `json:"thinking_levels,omitempty"`
	// MatchedRules：命中的规则 id（没写 id 的用 `#1` 这种序号）。给界面解释“为什么”。
	MatchedRules []string `json:"matched_rules,omitempty"`
}

// ModelRulesConfig：HTTP 读写 model_define_rules 的外壳（与 config.json 的字段同名）。
type ModelRulesConfig struct {
	Rules []ModelDefineRule `json:"model_define_rules"`
}

// ModelIconIDs：允许写进规则里的图标 id（**封闭集合**，前端只出这些选项）。
// 只影响展示；新增品牌要同时在 ModelIcon.vue / modelIcons.ts 里加图形。
var ModelIconIDs = []string{"gpt", "gemini", "grok", "claude", "deepseek", "mimo", "glm", "kimi"}

func ValidModelIcon(icon string) bool {
	for _, id := range ModelIconIDs {
		if id == icon {
			return true
		}
	}
	return false
}

// normalizeLegacyModelIcons：旧版本默认规则写的是 openai/xai；读入时就地换成新 id。
// 只救存量配置，不把旧值当成合法写入值（PUT 仍然会 422）。
func normalizeLegacyModelIcons(rules []ModelDefineRule) {
	for index := range rules {
		if rules[index].Icon == nil {
			continue
		}
		switch *rules[index].Icon {
		case "openai":
			*rules[index].Icon = "gpt"
		case "xai":
			*rules[index].Icon = "grok"
		}
	}
}

// DefaultModelDefineRules：开箱的三条品牌规则（不含 tool use；grok-imagine 是图像模型，清空思考档）。
//
// 它们只影响显示，不改变请求；用户把整个数组写成 `[]` 就能关掉。
func DefaultModelDefineRules() []ModelDefineRule {
	noThinking := []string{}
	return []ModelDefineRule{
		{ID: "openai", MatchContains: "gpt", Brand: stringPtr("OpenAI"), Icon: stringPtr("gpt")},
		// 图像模型也带 “grok”，但它不是聊天模型：先把思考档位清空并停止，
		// 免得界面对它摆出一排它根本收不到的 off/minimal/...。
		{ID: "xai-image", MatchContains: "grok-imagine", IsFinal: true,
			Brand: stringPtr("xAI"), Icon: stringPtr("grok"), ThinkingLevels: &noThinking},
		{ID: "xai", MatchContains: "grok", Brand: stringPtr("xAI"), Icon: stringPtr("grok")},
	}
}

func stringPtr(value string) *string {
	copy := value
	return &copy
}

// ValidateModelDefineRules：规则集合的校验（写入与 LoadConfig 同一口径）。
func ValidateModelDefineRules(rules []ModelDefineRule) error {
	seen := map[string]bool{}
	for index, rule := range rules {
		if err := rule.validate(); err != nil {
			return fmt.Errorf("model_define_rules[%d]：%w", index, err)
		}
		if rule.ID != "" {
			if seen[rule.ID] {
				return fmt.Errorf("model_define_rules[%d]：id %q 重复", index, rule.ID)
			}
			seen[rule.ID] = true
		}
	}
	return nil
}

func (r ModelDefineRule) validate() error {
	if strings.TrimSpace(r.MatchContains) == "" && strings.TrimSpace(r.MatchExact) == "" &&
		strings.TrimSpace(r.MatchProvider) == "" {
		return fmt.Errorf("规则至少需要一个匹配条件（match_contains / match_exact / match_provider）")
	}
	if r.ThinkingLevels == nil && r.Brand == nil && r.Icon == nil {
		return fmt.Errorf("规则没有任何要覆写的字段（thinking_levels / brand / icon）")
	}
	if r.Icon != nil && strings.TrimSpace(*r.Icon) != "" && !ValidModelIcon(*r.Icon) {
		return fmt.Errorf("未知图标 %q（只能是 %s）", *r.Icon, strings.Join(ModelIconIDs, " / "))
	}
	if r.ThinkingLevels != nil {
		seen := map[string]bool{}
		for _, level := range *r.ThinkingLevels {
			if !model.ValidThinkingLevel(level) {
				return fmt.Errorf("未知思考档位 %q（只能是 %s）", level, strings.Join(model.ThinkingLevelOrder, " / "))
			}
			if seen[level] {
				return fmt.Errorf("思考档位 %q 重复", level)
			}
			seen[level] = true
		}
	}
	return nil
}

// ModelProfile：按顺序把命中的**全局**规则覆盖到 fallback 画像上。
//
// fallback.ThinkingLevels 是代码里的默认能力（providers.ThinkingLevels）；
// 规则没有写 thinking_levels 时保留它。Brand/Icon 留空 = 界面自己回退。
func (c Config) ModelProfile(providerID, upstreamID string, fallback ModelProfile) ModelProfile {
	return c.ModelProfileWithRules(providerID, upstreamID, fallback, nil)
}

// ModelProfileWithRules：全局规则先、渠道规则后（渠道永远优先）。
//
// `is_final` 只在各自那一层里停止：全局规则 final 不会挡住渠道自定义规则，
// 否则“渠道覆盖全局”就不成立了。matched_rules 带 `global:` / `provider:` 前缀，
// 方便界面解释这条画像到底是谁给的。
func (c Config) ModelProfileWithRules(providerID, upstreamID string, fallback ModelProfile, providerRules []ModelDefineRule) ModelProfile {
	profile := ModelProfile{
		Brand:          fallback.Brand,
		Icon:           fallback.Icon,
		ThinkingLevels: append([]string{}, fallback.ThinkingLevels...),
	}
	profile = applyModelRules(profile, c.ModelDefineRules, providerID, upstreamID, "global")
	profile = applyModelRules(profile, providerRules, providerID, upstreamID, "provider")
	return profile
}

func applyModelRules(profile ModelProfile, rules []ModelDefineRule, providerID, upstreamID, scope string) ModelProfile {
	for index, rule := range rules {
		if !rule.matches(providerID, upstreamID) {
			continue
		}
		if rule.ThinkingLevels != nil {
			profile.ThinkingLevels = append([]string{}, (*rule.ThinkingLevels)...)
		}
		if rule.Brand != nil {
			profile.Brand = *rule.Brand
		}
		if rule.Icon != nil {
			profile.Icon = *rule.Icon
		}
		label := rule.ID
		if label == "" {
			label = fmt.Sprintf("#%d", index+1)
		}
		profile.MatchedRules = append(profile.MatchedRules, scope+":"+label)
		if rule.IsFinal {
			break
		}
	}
	return profile
}

func (r ModelDefineRule) matches(providerID, upstreamID string) bool {
	if value := strings.TrimSpace(r.MatchProvider); value != "" && providerID != value {
		return false
	}
	if value := strings.TrimSpace(r.MatchExact); value != "" && !strings.EqualFold(upstreamID, value) {
		return false
	}
	if value := strings.TrimSpace(r.MatchContains); value != "" &&
		!strings.Contains(strings.ToLower(upstreamID), strings.ToLower(value)) {
		return false
	}
	return true
}
