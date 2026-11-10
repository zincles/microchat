package tavern

import (
	"strings"

	"microchat/internal/macro"
	"microchat/internal/template"
)

// EffectiveEntry：求值后真正会留下的提示词条目（空条目不出现）。
type EffectiveEntry struct {
	Identifier   string `json:"identifier"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	Position     string `json:"position"`
	Depth        int    `json:"depth,omitempty"`
	Order        int    `json:"order,omitempty"`
	SystemPrompt bool   `json:"system_prompt"`
}

type Effective struct {
	Name      string            `json:"name"`
	Entries   []EffectiveEntry  `json:"entries"`
	Dropped   []Skipped         `json:"dropped"`
	Variables map[string]string `json:"variables"`
	Macros    []macro.Count     `json:"macros"`
	Warnings  []string          `json:"warnings,omitempty"`
}

// ExtractOptions：提取有效内容时的运行时值。
// LastUserMessage nil = 保留 {{lastUserMessage}}（提取预览）；非 nil = 替换成这次的值。
type ExtractOptions struct {
	Values          map[string]string
	LastUserMessage *string
}

// Extract：按 prompt_order 逐条执行 ST 控制宏，再丢掉空结果。
//
// 这是“提取有效部分”的核心：变量组/选项条目（setvar）会在这里被吃掉，getvar 会被
// 前面的 setvar 填上；只有真的留下正文的条目才会进入 Entries。它不写库、不建 Agent。
func Extract(data []byte, opts ExtractOptions) (Effective, error) {
	preview, err := Parse(data)
	if err != nil {
		return Effective{}, err
	}
	ctx := &template.Context{
		Values:          opts.Values,
		Variables:       map[string]string{},
		LastUserMessage: opts.LastUserMessage,
	}
	out := Effective{
		Name:      preview.Name,
		Entries:   []EffectiveEntry{},
		Dropped:   []Skipped{},
		Variables: map[string]string{},
		Macros:    []macro.Count{},
		Warnings:  append([]string{}, preview.Warnings...),
	}
	usage := map[string]int{}
	for _, entry := range preview.Entries {
		rendered := strings.TrimSpace(template.Render(entry.Content, ctx))
		if rendered == "" {
			out.Dropped = append(out.Dropped, Skipped{
				Identifier: entry.Identifier, Name: entry.Name, Reason: "求值后没有正文",
			})
			continue
		}
		out.Entries = append(out.Entries, EffectiveEntry{
			Identifier: entry.Identifier, Name: entry.Name, Role: entry.Role, Content: rendered,
			Position: entry.Position, Depth: entry.Depth, Order: entry.Order,
			SystemPrompt: entry.SystemPrompt,
		})
		usage = macro.Merge(usage, macro.Scan(rendered))
	}
	// 变量是提取的副产物；复制一份，别把内部 map 直接交出去。
	for name, value := range ctx.Variables {
		out.Variables[name] = value
	}
	out.Macros = macro.Counts(usage)
	return out, nil
}
