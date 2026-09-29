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
	Models []store.Discovered `json:"models"`
}

// ModelsResponse：上游 `/models` 的响应（OpenAI 形状 **`{"data": [...]}`**；OpenRouter 同形多给几个字段）。
type ModelsResponse struct {
	Data []store.Discovered `json:"data"`
}

// Prettify：`deepseek-v4-flash` → `Deepseek V4 Flash`（照旧版逐字一致）。
// 取 `/` 后的尾巴，按 `-`/`_` 切词，每词首字母大写，空格相连。
func Prettify(upstreamID string) string {
	tail := upstreamID
	if index := strings.LastIndexByte(upstreamID, '/'); index >= 0 {
		tail = upstreamID[index+1:]
	}
	words := strings.FieldsFunc(tail, func(r rune) bool { return r == '-' || r == '_' })
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
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
