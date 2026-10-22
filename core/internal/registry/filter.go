package registry

import (
	"fmt"
	"strings"

	"microchat/internal/store"
)

// ── 模型筛选器（`providers.json` 的 `model_filter`）────────────────────────────
//
// 用户定（2026-10-22）："OpenRouter 有足足 400 多个模型，但实际上我们只需要其中的几个" ⇒
// **只有符合筛选器的模型会被导入**。目前只有一种筛选器：**是否包含完整字符串**（子串包含）。
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

// ValidateModelFilter：筛选器能不能用（空 = 合法，表示不筛）。
//
// 两处调它，口径一致（同 abilities / segments 的"写入与启动读取都报错"）：
//   - 写入（`POST/PATCH /providers`）⇒ 400,交给用户改；
//   - 发现（`Probe`）⇒ 刷新报错、**一个模型都不动**（宁可这次不刷，也不能按一个坏筛选器把表清空）。
func ValidateModelFilter(filter string) error {
	trimmed := strings.TrimSpace(filter)
	if strings.Contains(trimmed, "*") {
		return fmt.Errorf("%s", filterStarMessage)
	}
	return nil
}

// FilterModels：按筛选器切一遍发现到的模型，回「留下的」与「挡掉的个数」。
//
// 语义（三句话，用户原话的直译）：
//  1. **空 = 不筛**：空串、纯空白都算"没填" ⇒ 原样全要（连顺序都不动，调用方不必判空）；
//  2. **子串包含**：拿 `upstream_id` 整串比（`openai/` 这种前缀**也参与**）⇒ `gpt` 命中
//     `openai/gpt-5`、`gpt-6`、`openai/gpt-5.6-sol`。**不是**通配、不是分词：`gpt` 必须原样出现；
//  3. **不分大小写**：模型 id 的大小写是上游随手写的（`meta-llama/Llama-3.3-70B-Instruct`），
//     用户记的是片段（"llama"、"70b"），让大小写去决定筛不筛得出来只会白挨一次"怎么少了这么多"。
//
// 只比 `upstream_id`、**不比显示名**：显示名会变（上游改文案），id 才是身份（不变量 5）；
// 而且比两个字段会让"为什么这个被筛进来了"变得难以解释。
func FilterModels(filter string, models []store.Discovered) (kept []store.Discovered, dropped int) {
	needle := strings.ToLower(strings.TrimSpace(filter))
	if needle == "" {
		return models, 0
	}
	kept = make([]store.Discovered, 0, len(models))
	for _, model := range models {
		if strings.Contains(strings.ToLower(model.UpstreamID), needle) {
			kept = append(kept, model)
		}
	}
	return kept, len(models) - len(kept)
}
