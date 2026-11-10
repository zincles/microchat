// Package macro：**只读替换宏** —— 存储永远是 `{{...}}` 原文，只在出站/显示时替换。
//
// 和 `<state>` 是两件事：
//   - state 是历史现演出的世界事实；宏只是名字/型号这类**展示或提示词插值**。
//   - 宏没有 setter、没有作用域、没有副作用；同一段文本永远替换成同一份值。
//
// 未知宏**原样保留**：预设里将来会有 `{{getvar::...}}` 之类的 ST 宏，
// 那些归模板变量引擎，这一层不猜、也不吞。
package macro

import (
	"regexp"
	"sort"
	"strings"
)

// Definition：一个可用宏（别名也在这里声明；别名与主名同值）。
type Definition struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description"`
}

// View：带当前值的可用宏（`GET /sessions/{id}/macros` 与前端面板用）。
type View struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description"`
	Value       string   `json:"value"`
}

// 默认展示值：没有用户名称/人格名时也要能替换，不能让 `{{user_name}}` 变成空。
const (
	DefaultUserName  = "你"
	DefaultAgentName = "助手"
)

var definitions = []Definition{
	{Name: "user_name", Aliases: []string{"user"}, Description: "当前用户名称"},
	{Name: "char_name", Aliases: []string{"char", "agent_name"}, Description: "当前 Agent 名称"},
	{Name: "model", Description: "当前会话模型"},
}

// Definitions：可用宏清单（顺序 = 面板展示顺序）。返回副本，调用方不能改包内那张表。
func Definitions() []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, d := range definitions {
		copy := d
		copy.Aliases = append([]string{}, d.Aliases...)
		out = append(out, copy)
	}
	return out
}

// Values：把当前会话的值摊平成「宏名 → 值」，别名也放进去（渲染只查这一张表）。
func Values(userName, agentName, model string) map[string]string {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		userName = DefaultUserName
	}
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		agentName = DefaultAgentName
	}
	values := map[string]string{
		"user_name":  userName,
		"user":       userName,
		"char_name":  agentName,
		"char":       agentName,
		"agent_name": agentName,
		"model":      strings.TrimSpace(model),
	}
	return values
}

// Registered：可用宏 + 当前值；值缺失时也给出空串（面板要能看见这个宏存在）。
func Registered(values map[string]string) []View {
	out := make([]View, 0, len(definitions))
	for _, d := range definitions {
		out = append(out, View{
			Name: d.Name, Aliases: append([]string{}, d.Aliases...),
			Description: d.Description, Value: values[d.Name],
		})
	}
	return out
}

// IsKnown：这个宏名是否已注册（含别名）。用于面板区分“已注册 / 未知（例如 ST getvar）”。
func IsKnown(name string) bool {
	name = strings.TrimSpace(name)
	for _, d := range definitions {
		if d.Name == name {
			return true
		}
		for _, alias := range d.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// simpleMacro：只认不带参数的简单宏名；`{{getvar::x}}` 这类不碰（留给变量引擎）。
var simpleMacro = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}`)

// anyMacro：扫描用（认任意 `{{...}}`，用于统计“全文里用了哪些宏”）。
var anyMacro = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// Render：只替换 values 里有的简单宏；未知宏保持原样。**不递归**（值里再有宏也不展开）。
func Render(text string, values map[string]string) string {
	if text == "" || len(values) == 0 {
		return text
	}
	return simpleMacro.ReplaceAllStringFunc(text, func(match string) string {
		sub := simpleMacro.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		value, ok := values[strings.TrimSpace(sub[1])]
		if !ok {
			return match
		}
		return value
	})
}

// Scan：统计一段文本里每个 `{{...}}` 出现的次数（返回副本；未知宏也统计）。
// 名字保留原文内部空白之外的内容：`{{getvar::x}}` 记作 `getvar::x`。
func Scan(text string) map[string]int {
	out := map[string]int{}
	for _, match := range anyMacro.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(match[1])
		if name == "" {
			continue
		}
		out[name]++
	}
	return out
}

// Merge：把 b 的计数并进 a（扫描“系统提示词 + 全部消息”用）。
func Merge(a, b map[string]int) map[string]int {
	if a == nil {
		a = map[string]int{}
	}
	for name, count := range b {
		a[name] += count
	}
	return a
}

// Counts：按次数降序、同次数按名字升序 —— 面板输出稳定。
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Known bool   `json:"known"`
}

func Counts(usage map[string]int) []Count {
	out := make([]Count, 0, len(usage))
	for name, count := range usage {
		out = append(out, Count{Name: name, Count: count, Known: IsKnown(name)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
