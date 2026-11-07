// Package model：思考档位的**规范清单**。
//
// 七档是跨厂商的 UI 词汇（off … max）；各家真正接受的字段与降级映射仍在
// `providers.thinkingEffortFor`。这里只放“哪些值是合法的”这一件事，
// 免得 config / providers / 前端各抄一份。
package model

// ThinkingLevelOrder：统一七档，顺序 = UI 从低到高。
var ThinkingLevelOrder = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidThinkingLevel：这个值是不是七档之一。
func ValidThinkingLevel(level string) bool {
	for _, candidate := range ThinkingLevelOrder {
		if candidate == level {
			return true
		}
	}
	return false
}
