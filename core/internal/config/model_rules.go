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
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"microchat/internal/model"
)

// ModelDefineRule：一条模型定义规则。字段名用 snake_case，与 config.json 其余部分一致。
//
// `ThinkingLevels` / `Brand` / `Icon` 都是指针：nil = 这条规则不碰该字段；
// 空数组（`[]`）与空串是明确的“覆写成空”，与“没写”不是一回事。
//
// `ServerTools` 是**追加**语义：命中的规则把列出的 provider-native tool 并进画像，
// 不是替换。多个规则命中按顺序去重追加；下游发请求时再按 provider/协议能力展开/校验。
type ModelDefineRule struct {
	ID string `json:"id,omitempty"`
	// MatchContains / MatchExact：对 raw upstream_id 做大小写不敏感匹配。
	// 两者都写 = 必须同时满足（一般只用其中一个）。
	MatchContains string `json:"match_contains,omitempty"`
	MatchExact    string `json:"match_exact,omitempty"`
	// MatchProvider：可选，按 provider id 限定（例如只对 xai 渠道生效）。
	MatchProvider string `json:"match_provider,omitempty"`
	// MatchProtocol：可选，限定渠道当前协议（openai-chat-completion / openai-response）；
	// 空 = 不限制。`chat` / `response` 是写入时可接受的简写。
	MatchProtocol string `json:"match_protocol,omitempty"`
	// IsFinal：命中并应用本规则后停止继续匹配。宽匹配 + final 会遮蔽后面的具体规则，
	// 所以内置规则一律不 final。
	IsFinal bool `json:"is_final,omitempty"`

	ThinkingLevels *[]string `json:"thinking_levels,omitempty"`
	Brand          *string   `json:"brand,omitempty"`
	Icon           *string   `json:"icon,omitempty"`
	// ServerTools：provider 侧执行的 tool id（如 x_search / web_search / code_execution）。
	// **追加**到 profile.server_tools，不替换；实际请求是否支持由 provider 层校验。
	ServerTools *[]string `json:"server_tools,omitempty"`
	// LocalTools：客户端本地执行的 tool id。与 ServerTools 分开存；具体工具定义在后续工具注册表。
	LocalTools *[]string `json:"local_tools,omitempty"`
}

// ModelProfile：一个模型经过规则覆写后的展示/能力画像（派生结果，不落库）。
type ModelProfile struct {
	Brand          string   `json:"brand,omitempty"`
	Icon           string   `json:"icon,omitempty"`
	ThinkingLevels []string `json:"thinking_levels,omitempty"`
	// ServerTools / LocalTools：规则追加的工具 id（按规则顺序去重）。
	ServerTools []string `json:"server_tools,omitempty"`
	LocalTools  []string `json:"local_tools,omitempty"`
	// MatchedRules：命中的规则 id（没写 id 的用 `#1` 这种序号）。给界面解释“为什么”。
	MatchedRules []string `json:"matched_rules,omitempty"`
}

// ModelRulesConfig：HTTP 写 model_define_rules 的外壳（与 config.json 的字段同名）。
// DisabledBuiltinModelRules 省略（nil）= 不改停用列表；显式 [] = 全部启用。
type ModelRulesConfig struct {
	Rules                     []ModelDefineRule `json:"model_define_rules"`
	DisabledBuiltinModelRules []string          `json:"disabled_builtin_model_rules,omitempty"`
}

// BuiltinModelRuleView：系统内建全局规则 + 只读/停用标记（GET /config/model-rules）。
type BuiltinModelRuleView struct {
	ModelDefineRule
	Builtin  bool `json:"builtin"`
	Disabled bool `json:"disabled"`
}

// ModelRulesView：全局规则视图 = 用户规则 + 系统内置规则（含停用状态）。
type ModelRulesView struct {
	ModelDefineRules          []ModelDefineRule      `json:"model_define_rules"`
	BuiltinModelRules         []BuiltinModelRuleView `json:"builtin_model_rules"`
	DisabledBuiltinModelRules []string               `json:"disabled_builtin_model_rules"`
}

// ModelRulesView：按配置生成只读/停用标记；内置规则永远全部列出。
func (c Config) ModelRulesView() ModelRulesView {
	user := c.ModelDefineRules
	if user == nil {
		user = []ModelDefineRule{}
	}
	disabled := make(map[string]bool, len(c.DisabledBuiltinModelRules))
	for _, id := range c.DisabledBuiltinModelRules {
		disabled[id] = true
	}
	builtins := make([]BuiltinModelRuleView, 0, len(builtinModelRules))
	for _, rule := range builtinModelRules {
		builtins = append(builtins, BuiltinModelRuleView{ModelDefineRule: rule, Builtin: true, Disabled: disabled[rule.ID]})
	}
	return ModelRulesView{
		ModelDefineRules:          user,
		BuiltinModelRules:         builtins,
		DisabledBuiltinModelRules: append([]string{}, c.DisabledBuiltinModelRules...),
	}
}

// ModelIconIDs：允许写进规则里的图标 id（**封闭集合**，前端只出这些选项）。
// 只影响展示；新增品牌要同时在 ModelIcon.vue / modelIcons.ts 里加图形。
var ModelIconIDs = []string{"gpt", "gemini", "grok", "claude", "deepseek", "mimo", "glm", "kimi",
	"longcat", "mistral", "qwen", "minimax", "tencent", "meta", "stepfun", "bytedance"}

// ServerToolIDs：规则里允许追加的 provider-native tool id（封闭集合）。
// 真正可发不可发还要看 provider/协议/模型；这里只保证规则文件不写出错字。
var ServerToolIDs = []string{"x_search", "web_search", "code_execution"}

// ModelRuleProtocols：MatchProtocol 可写的协议 id（空串 = 不限制）。
var ModelRuleProtocols = []string{"openai-chat-completion", "openai-response"}

func NormalizeModelRuleProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "any":
		return ""
	case "chat", "openai-chat-completion":
		return "openai-chat-completion"
	case "response", "openai-response":
		return "openai-response"
	default:
		return strings.TrimSpace(value)
	}
}

func validModelRuleProtocol(value string) bool {
	value = NormalizeModelRuleProtocol(value)
	if value == "" {
		return true
	}
	for _, allowed := range ModelRuleProtocols {
		if strings.EqualFold(value, allowed) {
			return true
		}
	}
	return false
}

func validServerToolID(value string) bool {
	for _, id := range ServerToolIDs {
		if value == id {
			return true
		}
	}
	return false
}

func ValidModelIcon(icon string) bool {
	for _, id := range ModelIconIDs {
		if id == icon {
			return true
		}
	}
	return false
}

//go:embed builtin_model_rules.json
var builtinModelRulesJSON []byte

// BuiltinModelDefineRules：系统内建全局规则库（只读，可停用）。
//
// 数据住 builtin_model_rules.json（随二进制发布），与用户规则分开：
// 生效顺序 = 内置（未停用）在前，用户规则在后；用户规则可以覆盖内置字段。
// 用户不能编辑/删除内置规则，只能通过 config.json 的 disabled_builtin_model_rules 停用。
var builtinModelRules = mustParseBuiltinModelRules()

func mustParseBuiltinModelRules() []ModelDefineRule {
	var rules []ModelDefineRule
	if err := json.Unmarshal(builtinModelRulesJSON, &rules); err != nil {
		panic(fmt.Sprintf("builtin_model_rules.json: %v", err))
	}
	if err := ValidateModelDefineRules(rules); err != nil {
		panic(fmt.Sprintf("builtin_model_rules.json: %v", err))
	}
	for index, rule := range rules {
		if strings.TrimSpace(rule.ID) == "" {
			panic(fmt.Sprintf("builtin_model_rules.json[%d]: id 不能为空（内置规则必须可停用）", index))
		}
	}
	return rules
}

// BuiltinModelDefineRules：返回内置规则（只读；调用方不得修改）。
func BuiltinModelDefineRules() []ModelDefineRule { return builtinModelRules }

// BuiltinModelRuleIDs：内置规则 id 集合（用于禁止用户规则复用 id）。
func BuiltinModelRuleIDs() map[string]bool {
	out := make(map[string]bool, len(builtinModelRules))
	for _, rule := range builtinModelRules {
		out[rule.ID] = true
	}
	return out
}

func stringPtr(value string) *string {
	copy := value
	return &copy
}

func stringSlicePtr(values ...string) *[]string {
	copy := append([]string{}, values...)
	return &copy
}

// dropBuiltinRuleIDs：旧配置把内置规则也存进用户数组时，丢掉同 id 的副本。
func dropBuiltinRuleIDs(rules []ModelDefineRule) []ModelDefineRule {
	if len(rules) == 0 {
		return rules
	}
	builtin := BuiltinModelRuleIDs()
	out := rules[:0]
	for _, rule := range rules {
		if rule.ID != "" && builtin[rule.ID] {
			continue
		}
		out = append(out, rule)
	}
	return out
}

// ValidateUserModelDefineRules：用户规则校验（额外禁止复用内置 id）。
func ValidateUserModelDefineRules(rules []ModelDefineRule) error {
	if err := ValidateModelDefineRules(rules); err != nil {
		return err
	}
	builtin := BuiltinModelRuleIDs()
	for index, rule := range rules {
		if rule.ID != "" && builtin[rule.ID] {
			return fmt.Errorf("model_define_rules[%d]：id %q 是系统内置规则，用户规则不能复用", index, rule.ID)
		}
	}
	return nil
}

// normalizeDisabledBuiltinModelRules：启动时只保留仍存在且不重复的内置 id。
// 内置规则会随版本增删；旧配置里的已删除 id 不应该把服务卡在启动。
func normalizeDisabledBuiltinModelRules(ids []string) []string {
	known := BuiltinModelRuleIDs()
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !known[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ValidateDisabledBuiltinModelRules：停用列表只能引用真实存在的内置规则，且不能重复。
func ValidateDisabledBuiltinModelRules(ids []string) error {
	known := BuiltinModelRuleIDs()
	seen := map[string]bool{}
	for index, id := range ids {
		if !known[id] {
			return fmt.Errorf("disabled_builtin_model_rules[%d]：未知的内置规则 id %q", index, id)
		}
		if seen[id] {
			return fmt.Errorf("disabled_builtin_model_rules[%d]：id %q 重复", index, id)
		}
		seen[id] = true
	}
	return nil
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
	if r.ThinkingLevels == nil && r.Brand == nil && r.Icon == nil &&
		(r.ServerTools == nil || len(*r.ServerTools) == 0) &&
		(r.LocalTools == nil || len(*r.LocalTools) == 0) {
		return fmt.Errorf("规则没有任何要覆写的字段（thinking_levels / brand / icon / server_tools / local_tools）")
	}
	if r.MatchProtocol != "" && !validModelRuleProtocol(r.MatchProtocol) {
		return fmt.Errorf("未知匹配协议 %q（只能是空 / openai-chat-completion / openai-response）", r.MatchProtocol)
	}
	if (r.ServerTools != nil || r.LocalTools != nil) &&
		(r.Brand != nil || r.Icon != nil || r.ThinkingLevels != nil) {
		return fmt.Errorf("追加工具与品牌/图标/思考不能写在同一条规则里（拆成两条）")
	}
	if r.ServerTools != nil {
		if len(*r.ServerTools) == 0 {
			return fmt.Errorf("server_tools 不能是空数组（不想追加就整格省略）")
		}
		seenTools := map[string]bool{}
		for _, id := range *r.ServerTools {
			if !validServerToolID(id) {
				return fmt.Errorf("未知 native tool %q（只能是 %s）", id, strings.Join(ServerToolIDs, " / "))
			}
			if seenTools[id] {
				return fmt.Errorf("native tool %q 重复", id)
			}
			seenTools[id] = true
		}
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
	return c.ModelProfileFor(providerID, upstreamID, "", fallback)
}

// ModelProfileFor：带渠道协议（openai-chat-completion / openai-response）的画像。
// 带 `match_protocol` 的规则只在这里生效；老调用方 ModelProfile 传空协议 = 不匹配这类规则。
func (c Config) ModelProfileFor(providerID, upstreamID, protocol string, fallback ModelProfile) ModelProfile {
	return c.ModelProfileWithRulesFor(providerID, upstreamID, protocol, fallback, nil)
}

// ModelProfileWithRules：全局规则先、渠道规则后（渠道永远优先）。
//
// `is_final` 只在各自那一层里停止：全局规则 final 不会挡住渠道自定义规则，
// 否则“渠道覆盖全局”就不成立了。matched_rules 带 `global:` / `provider:` 前缀，
// 方便界面解释这条画像到底是谁给的。
func (c Config) ModelProfileWithRules(providerID, upstreamID string, fallback ModelProfile, providerRules []ModelDefineRule) ModelProfile {
	return c.ModelProfileWithRulesFor(providerID, upstreamID, "", fallback, providerRules)
}

// EffectiveModelDefineRules：系统内置（未停用）+ 用户规则，按此顺序生效。
func (c Config) EffectiveModelDefineRules() []ModelDefineRule {
	disabled := make(map[string]bool, len(c.DisabledBuiltinModelRules))
	for _, id := range c.DisabledBuiltinModelRules {
		disabled[id] = true
	}
	out := make([]ModelDefineRule, 0, len(builtinModelRules)+len(c.ModelDefineRules))
	for _, rule := range builtinModelRules {
		if !disabled[rule.ID] {
			out = append(out, rule)
		}
	}
	out = append(out, c.ModelDefineRules...)
	return out
}

func (c Config) ModelProfileWithRulesFor(providerID, upstreamID, protocol string, fallback ModelProfile, providerRules []ModelDefineRule) ModelProfile {
	profile := ModelProfile{
		Brand:          fallback.Brand,
		Icon:           fallback.Icon,
		ThinkingLevels: append([]string{}, fallback.ThinkingLevels...),
		ServerTools:    append([]string{}, fallback.ServerTools...),
		LocalTools:     append([]string{}, fallback.LocalTools...),
	}
	profile = applyModelRules(profile, c.EffectiveModelDefineRules(), providerID, upstreamID, protocol, "global")
	profile = applyModelRules(profile, providerRules, providerID, upstreamID, protocol, "provider")
	return profile
}

func applyModelRules(profile ModelProfile, rules []ModelDefineRule, providerID, upstreamID, protocol, scope string) ModelProfile {
	for index, rule := range rules {
		if !rule.matches(providerID, upstreamID, protocol) {
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
		if rule.ServerTools != nil {
			seen := make(map[string]bool, len(profile.ServerTools)+len(*rule.ServerTools))
			for _, id := range profile.ServerTools {
				seen[id] = true
			}
			for _, id := range *rule.ServerTools {
				if !seen[id] {
					profile.ServerTools = append(profile.ServerTools, id)
					seen[id] = true
				}
			}
		}
		if rule.LocalTools != nil {
			seen := make(map[string]bool, len(profile.LocalTools)+len(*rule.LocalTools))
			for _, id := range profile.LocalTools {
				seen[id] = true
			}
			for _, id := range *rule.LocalTools {
				id = strings.TrimSpace(id)
				if id != "" && !seen[id] {
					profile.LocalTools = append(profile.LocalTools, id)
					seen[id] = true
				}
			}
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

func (r ModelDefineRule) matches(providerID, upstreamID, protocol string) bool {
	if value := strings.TrimSpace(r.MatchProvider); value != "" && providerID != value {
		return false
	}
	if value := NormalizeModelRuleProtocol(r.MatchProtocol); value != "" &&
		!strings.EqualFold(value, NormalizeModelRuleProtocol(protocol)) {
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
