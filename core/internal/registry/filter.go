package registry

import (
	"fmt"
	"strings"

	"microchat/internal/store"
)

// ── 模型筛选器（`providers.json` 的 `model_filter`）────────────────────────────
//
// 用户定（2026-10-22）："OpenRouter 有足足 400 多个模型，但实际上我们只需要其中的几个" ⇒
// **只有符合筛选器的模型会被导入**。筛选器 = **逗号分隔的多条子串规则**，命中任意一条即算中
// （2026-10-28 用户定：「deepseek,mimo 会吧 deepseek-v4-flash 和 mimo-v2.6-flash 都允许，
// 因为它们分别满足了一个规则」）。
//
// 为什么放在这个包里：`registry` 是"模型口径"的唯一权威（身份 `(provider, upstream_id)`、
// 显示名回退、发现与覆盖分列）。"发现到的模型里哪些算数"正是口径的一部分 ——
// 所以这里是**唯一实现**，`Probe` 调它、写入时的校验也调它，别在界面/服务层再写一遍
// （第二份必然在某处与这份不一致，而"筛选器两处语义不同"是**静默**的：列表少几个，没人会立刻发现）。

// 筛选器的三种病态写法 —— 报错要**说清该怎么写**，而不只是说"错了"。
const (
	// filterStarMessage：`*` 是用户最容易想当然的那个（"星号 = 全部"）。它在本项目里**没有**那个含义，
	// 而把它当字面字符去匹配的后果特别重：没有哪个模型 id 含 `*` ⇒ 筛完是空 ⇒ 刷新时"没见到的删行"
	// 会把整个渠道的模型表清空（连用户覆盖一起没）。所以**拒绝**，而不是"照字面匹配、你自己看着办"。
	filterStarMessage = "模型筛选器不支持通配符：`*` 在这里没有「全部」的含义" +
		"（要全部就把它留空）。写模型 id 里的公共子串即可，例如 gpt / claude / deepseek。"
)

// SplitModelFilter：把筛选器切成**一条条规则**（空 = 没有规则 ⇒ 不筛）。
//
// 分隔符只有**半角逗号**（2026-10-28 用户定：「我希望可以用逗号（半角）分割。例如 deepseek,mimo
// 会吧 deepseek-v4-flash 和 mimo-v2.6-flash 都允许，因为它们分别满足了一个规则」）。
//
// 为什么不收全角逗号 / 顿号：分隔符多一个，就多一个"模型 id 里正好有它"的可能性
// （模型 id 里 `,` 不是常见字符，而中文标点在 id 里更罕见 —— 但"更罕见"不是"不可能"，
// 一旦按它切，那个 id 就再也筛不出来了）。要收就只收一个，说清楚。
//
// 每条的**首尾空白吃掉**、空条丢掉：`deepseek, ,mimo,` ⇒ 两条规则（用户从别处粘一串很常见，
// 末尾多一个逗号不该变成一个"必须含空串"的怪规则 —— 空串子串什么都能命中，等于把筛选器静默作废）。
func SplitModelFilter(filter string) []string {
	parts := strings.Split(filter, ",")
	rules := make([]string, 0, len(parts))
	for _, part := range parts {
		if rule := strings.TrimSpace(part); rule != "" {
			rules = append(rules, rule)
		}
	}
	return rules
}

// ValidateModelFilter：筛选器能不能用（空 = 合法，表示不筛）。
//
// 两处调它，口径一致（同 abilities / segments 的"写入与启动读取都报错"）：
//   - 写入（`POST/PATCH /providers`）⇒ 400,交给用户改；
//   - 发现（`Probe`）⇒ 刷新报错、**一个模型都不动**（宁可这次不刷，也不能按一个坏筛选器把表清空）。
func ValidateModelFilter(filter string) error {
	rules := SplitModelFilter(filter)
	for _, rule := range rules {
		if strings.Contains(rule, "*") {
			return fmt.Errorf("规则 %q：%s", rule, filterStarMessage)
		}
	}
	// 全是空条（`","`、`" , ,"` 这类）：**按"没填"处理**，不报错 —— 它筛不出任何东西的意思
	// 和留空一样；报错反而要人去猜"我明明填了逗号"。真正危险的是"以为自己筛了、其实没筛"，
	// 而那种情况留空与全是逗号等价 —— 都不筛，行为一致，不是陷阱。
	return nil
}

// FilterModels：按筛选器切一遍发现到的模型，回「留下的」与「挡掉的个数」。
//
// 语义（用户原话的直译）：
//  1. **空 = 不筛**：空串、纯空白都算"没填" ⇒ 原样全要（连顺序都不动，调用方不必判空）；
//  2. **多规则、命中任意一条就算中**（2026-10-28 用户定）：`deepseek,mimo` ⇒
//     `deepseek-v4-flash`（命中第一条）与 `mimo-v2.6-flash`（命中第二条）都留下 ——
//     「因为它们分别满足了一个规则」。这是 **OR**，不是 AND（AND 的话那个例子一个都筛不出来）；
//  3. **每条规则都是子串包含**：拿 `upstream_id` 整串比（`openai/` 这种前缀**也参与**）⇒
//     `gpt` 命中 `openai/gpt-5`、`gpt-6`、`openai/gpt-5.6-sol`。**不是**通配、不是分词：
//     `gpt` 必须原样出现；
//  4. **不分大小写**：模型 id 的大小写是上游随手写的（`meta-llama/Llama-3.3-70B-Instruct`），
//     用户记的是片段（"llama"、"70b"），让大小写去决定筛不筛得出来只会白挨一次"怎么少了这么多"。
//
// 只比 `upstream_id`、**不比显示名**：显示名会变（上游改文案），id 才是身份（不变量 5）；
// 而且比两个字段会让"为什么这个被筛进来了"变得难以解释。
func FilterModels(filter string, models []store.Discovered) (kept []store.Discovered, dropped int) {
	// 规则先小写化一次：每条模型要跟每一条规则比，循环里反复 ToLower 是白费。
	rules := SplitModelFilter(filter)
	if len(rules) == 0 {
		return models, 0
	}
	needles := make([]string, 0, len(rules))
	for _, rule := range rules {
		needles = append(needles, strings.ToLower(rule))
	}
	kept = make([]store.Discovered, 0, len(models))
	for _, model := range models {
		id := strings.ToLower(model.UpstreamID)
		for _, needle := range needles {
			if strings.Contains(id, needle) {
				kept = append(kept, model)
				break // 命中任意一条即可；同一条模型不必再比下去
			}
		}
	}
	return kept, len(models) - len(kept)
}
