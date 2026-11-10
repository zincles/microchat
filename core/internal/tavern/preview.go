// Package tavern：SillyTavern 预设的**只读解析预览**。
//
// 第一刀只回答“这份预设按 ST 的规则会变成什么”：prompt_order 里哪些启用、哪些是
// relative、哪些是 absolute、哪些是 marker/空正文、哪些宏还没有解析。
// **不建 Agent、不写库、不执行 ST 的 setvar/getvar。**
package tavern

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"microchat/internal/macro"
)

// Entry：一条会进入提示词计划的条目（已启用且正文非空）。
type Entry struct {
	Identifier   string `json:"identifier"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	SystemPrompt bool   `json:"system_prompt"`
	// Position：relative = 按 prompt_order 排列；absolute = 按 depth 插入聊天历史。
	Position string `json:"position"`
	Depth    int    `json:"depth,omitempty"`
	Order    int    `json:"order,omitempty"`
}

// Skipped：一条没有进入计划的 order 条目（附原因，别静默丢）。
type Skipped struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
}

// Preview：解析结果。只读；`Entries` 保持 prompt_order 原顺序。
type Preview struct {
	Name             string        `json:"name"`
	PromptCount      int           `json:"prompt_count"`
	OrderGroups      int           `json:"order_groups"`
	OrderCharacterID string        `json:"order_character_id,omitempty"`
	OrderEntries     int           `json:"order_entries"`
	Enabled          int           `json:"enabled"`
	WithContent      int           `json:"with_content"`
	Relative         int           `json:"relative"`
	Absolute         int           `json:"absolute"`
	Entries          []Entry       `json:"entries"`
	Skipped          []Skipped     `json:"skipped"`
	Macros           []macro.Count `json:"macros"`
	Warnings         []string      `json:"warnings,omitempty"`
}

type preset struct {
	Name        string       `json:"name"`
	Prompts     []prompt     `json:"prompts"`
	PromptOrder []orderGroup `json:"prompt_order"`
}

type prompt struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Enabled    *bool  `json:"enabled"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	// system_prompt 是 Prompt Manager 的分类标记，**不是 role**。
	SystemPrompt      bool `json:"system_prompt"`
	Marker            bool `json:"marker"`
	InjectionPosition *int `json:"injection_position"`
	InjectionDepth    *int `json:"injection_depth"`
	InjectionOrder    *int `json:"injection_order"`
}

type orderGroup struct {
	CharacterID json.RawMessage `json:"character_id"`
	Order       []orderEntry    `json:"order"`
}

type orderEntry struct {
	Identifier string `json:"identifier"`
	Enabled    bool   `json:"enabled"`
}

// Parse：原始 JSON → 预览。坏 JSON 原样报错；结构问题进 warnings，不抛。
func Parse(data []byte) (Preview, error) {
	raw := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var p preset
	if err := json.Unmarshal(raw, &p); err != nil {
		return Preview{}, fmt.Errorf("预设不是合法 JSON：%w", err)
	}
	out := Preview{
		Name:        strings.TrimSpace(p.Name),
		PromptCount: len(p.Prompts),
		Entries:     []Entry{},
		Skipped:     []Skipped{},
		Macros:      []macro.Count{},
	}
	if out.Name == "" {
		out.Name = "未命名预设"
	}

	byID := make(map[string]prompt, len(p.Prompts))
	for _, item := range p.Prompts {
		id := strings.TrimSpace(item.Identifier)
		if id == "" {
			out.Warnings = append(out.Warnings, "有一条 prompt 没有 identifier，已跳过")
			continue
		}
		if _, exists := byID[id]; exists {
			out.Warnings = append(out.Warnings, "prompt identifier 重复："+id+"（后出现的覆盖前面的）")
		}
		byID[id] = item
	}

	order := make([]orderEntry, 0)
	if len(p.PromptOrder) > 0 {
		out.OrderGroups = len(p.PromptOrder)
		out.OrderCharacterID = rawString(p.PromptOrder[0].CharacterID)
		order = p.PromptOrder[0].Order
		if len(p.PromptOrder) > 1 {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("有 %d 组 prompt_order，预览只会使用第一组（character_id=%s）", len(p.PromptOrder), out.OrderCharacterID))
		}
	} else {
		// 老/畸形预设没有 prompt_order：退化成 prompts 数组里 enabled 的条目。
		out.Warnings = append(out.Warnings, "预设没有 prompt_order，已退化为按 prompts 数组顺序、只取 enabled 的条目")
		for _, item := range p.Prompts {
			enabled := item.Enabled == nil || *item.Enabled
			order = append(order, orderEntry{Identifier: item.Identifier, Enabled: enabled})
		}
	}
	out.OrderEntries = len(order)

	usage := map[string]int{}
	seen := map[string]bool{}
	for _, item := range order {
		identifier := strings.TrimSpace(item.Identifier)
		if !item.Enabled {
			name := ""
			if entry, ok := byID[identifier]; ok {
				name = entry.Name
			}
			out.Skipped = append(out.Skipped, Skipped{Identifier: identifier, Name: name, Reason: "未启用"})
			continue
		}
		out.Enabled++
		entry, ok := byID[identifier]
		if !ok {
			out.Skipped = append(out.Skipped, Skipped{Identifier: identifier, Reason: "prompt_order 指向不存在的 prompt"})
			continue
		}
		if entry.Marker {
			out.Skipped = append(out.Skipped, Skipped{Identifier: identifier, Name: entry.Name, Reason: "marker（占位，没有正文）"})
			continue
		}
		if strings.TrimSpace(entry.Content) == "" {
			out.Skipped = append(out.Skipped, Skipped{Identifier: identifier, Name: entry.Name, Reason: "正文为空"})
			continue
		}
		if seen[identifier] {
			out.Skipped = append(out.Skipped, Skipped{Identifier: identifier, Name: entry.Name, Reason: "在 prompt_order 里重复出现"})
			continue
		}
		seen[identifier] = true
		out.WithContent++

		role := strings.TrimSpace(entry.Role)
		if role == "" {
			role = "system"
			out.Warnings = append(out.Warnings, "条目 "+identifier+" 没有 role，按 system 预览")
		}
		position := "relative"
		depth, orderNo := 0, 0
		if entry.InjectionPosition != nil && *entry.InjectionPosition == 1 {
			position = "absolute"
			depth, orderNo = 4, 100
			if entry.InjectionDepth != nil {
				depth = *entry.InjectionDepth
			}
			if entry.InjectionOrder != nil {
				orderNo = *entry.InjectionOrder
			}
			if depth < 0 {
				out.Warnings = append(out.Warnings, "条目 "+identifier+" 的 injection_depth 为负，已按 0 预览")
				depth = 0
			}
		}
		if position == "absolute" {
			out.Absolute++
		} else {
			out.Relative++
		}
		out.Entries = append(out.Entries, Entry{
			Identifier: identifier, Name: entry.Name, Role: role, Content: entry.Content,
			SystemPrompt: entry.SystemPrompt, Position: position, Depth: depth, Order: orderNo,
		})
		usage = macro.Merge(usage, macro.Scan(entry.Content))
	}

	out.Macros = macro.Counts(usage)
	return out, nil
}

// rawString：character_id 可能是数字或字符串，预览只要一个可读值。
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return strings.TrimSpace(string(raw))
}
