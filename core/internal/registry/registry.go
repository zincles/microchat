// Package registry：**模型口径** —— 身份 `(provider, upstream_id)`、显示名三级回退、上下文优先顺序。
//
// 两条规矩：
//  1. **发现列与用户列分列**：刷新只碰发现所得（`upstream_*` / `context_length` / `max_output`），
//     **永不覆盖用户列**（`display_name` / `params` / `context_override` / `tokenizer`）；
//  2. 显示名与上下文**只在这里解析一次**（显示名三级回退：用户覆盖 → 上游名 → prettify）。
package registry

import (
	"strings"

	"microchat/internal/store"
)

// ProbeResult：试拉一次模型列表的结果（刷新模型走它；**不落库**）。
type ProbeResult struct {
	// Models：**筛选后**、真正要导入的那些（筛选器空 = 上游给多少就是多少）。
	Models []store.Discovered `json:"models"`
	// Filtered：被模型筛选器挡掉的个数（0 = 没筛）。留着它只为把话说清
	// （"470 个里筛剩 12 个"），落库逻辑一个字都不看它。
	Filtered int `json:"filtered"`
}

// ModelsResponse：上游 `/models` 的响应（OpenAI 形状 **`{"data": [...]}`**；OpenRouter 同形多给几个字段）。
type ModelsResponse struct {
	Data []store.Discovered `json:"data"`
}

// Prettify：`deepseek-v4-flash` → `Deepseek V4 Flash`（照旧版逐字一致）。
// 取 `/` 后的尾巴，按 `-`/`_` 切词，每词首字母大写，空格相连。
//
// 版本号：两个**单数字**相邻时合成点号（`claude-opus-5-5` → `Claude Opus 5.5`）。
// 日期这种多位数段不动（`...-4-5-20251101` → `4.5 20251101`），避免把 2025-11-01 拼成
// 一个看不懂的 2025.11.01。
func Prettify(upstreamID string) string {
	tail := upstreamID
	if index := strings.LastIndexByte(upstreamID, '/'); index >= 0 {
		tail = upstreamID[index+1:]
	}
	words := strings.FieldsFunc(tail, func(r rune) bool { return r == '-' || r == '_' })
	merged := make([]string, 0, len(words))
	for i := 0; i < len(words); i++ {
		if i+1 < len(words) && singleDigit(words[i]) && singleDigit(words[i+1]) {
			merged = append(merged, words[i]+"."+words[i+1])
			i++
			continue
		}
		merged = append(merged, words[i])
	}
	words = merged
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func singleDigit(word string) bool {
	return len(word) == 1 && word[0] >= '0' && word[0] <= '9'
}

// DisplayName：**三级回退**（用户覆盖 → 上游名 → prettify）。只在这里做。
func DisplayName(user *string, upstream *string, upstreamID string) string {
	if user != nil && strings.TrimSpace(*user) != "" {
		return *user
	}
	if upstream != nil && strings.TrimSpace(*upstream) != "" {
		return *upstream
	}
	return Prettify(upstreamID)
}

// ContextLen：上下文口径 —— **覆盖 > 上游发现 > 配置兜底**。只在这里做。
func ContextLen(override *int64, discovered *int64, fallback int) int {
	if override != nil && *override > 0 {
		return int(*override)
	}
	if discovered != nil && *discovered > 0 {
		return int(*discovered)
	}
	return fallback
}
