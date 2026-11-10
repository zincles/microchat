// Package template：ST 风格提示词宏的**顺序求值**（只服务导入/预设装配，不是世界状态）。
//
// 与 `internal/macro` 的分工：
//   - macro：只读身份宏（`{{user_name}}` / `{{char}}` / `{{model}}`），无副作用；
//   - template：ST 的变量与控制宏（`setvar` / `getvar` / `//` / `trim` / `lastUserMessage`），
//     **按文本出现顺序**执行，`setvar` 会改变后续 `getvar` 的结果。
//
// 没有递归展开：`setvar` 的值原样存，`getvar` 原样插入；最后再用 macro 替换身份宏。
// ST 的变量作用域（local/global）由调用方决定 —— 这里只拿一张 map，不在包内建全局状态。
package template

import (
	"regexp"
	"strings"

	"microchat/internal/macro"
)

// Context：一次求值用到的值。Variables 由调用方持有并跨条目复用（setvar 的副作用）。
type Context struct {
	Values map[string]string
	// Variables：local 变量（这份预设用 setvar/getvar，默认按会话/构建作用域）。
	Variables map[string]string
	// LastUserMessage：nil = 保留 `{{lastUserMessage}}` 原样（提取预览），
	// 非 nil = 替换成当前用户输入（即使空串）。
	LastUserMessage *string
}

var macroRe = regexp.MustCompile(`\{\{([\s\S]*?)\}\}`)

// Render：按出现顺序求值；未知宏原样保留，交给后面的 macro/变量引擎。
func Render(text string, ctx *Context) string {
	if ctx == nil {
		return text
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	trimAll := false
	var out strings.Builder
	cursor := 0
	for _, loc := range macroRe.FindAllStringSubmatchIndex(text, -1) {
		out.WriteString(text[cursor:loc[0]])
		inner := strings.TrimSpace(text[loc[2]:loc[3]])
		lower := strings.ToLower(inner)
		switch {
		case strings.HasPrefix(lower, "//"):
			// 注释：整条消失。
		case lower == "trim":
			trimAll = true
		case lower == "lastusermessage":
			if ctx.LastUserMessage == nil {
				out.WriteString(text[loc[0]:loc[1]])
			} else {
				out.WriteString(*ctx.LastUserMessage)
			}
		case strings.HasPrefix(inner, "setvar::"):
			rest := inner[len("setvar::"):]
			name, value, ok := strings.Cut(rest, "::")
			if ok {
				name = strings.TrimSpace(name)
				if name != "" {
					if value == "" {
						delete(ctx.Variables, name)
					} else {
						ctx.Variables[name] = value
					}
				}
			}
			// setvar 本身不产生正文。
		case strings.HasPrefix(inner, "getvar::"):
			name := strings.TrimSpace(inner[len("getvar::"):])
			if name != "" {
				out.WriteString(ctx.Variables[name])
			}
		default:
			// 身份宏 / 未知宏：原样保留，最后由 macro.Render 处理身份宏。
			out.WriteString(text[loc[0]:loc[1]])
		}
		cursor = loc[1]
	}
	out.WriteString(text[cursor:])
	result := out.String()
	if trimAll {
		result = strings.TrimSpace(result)
	}
	return macro.Render(result, ctx.Values)
}

// Scan：静态分析一段文本里的 ST 控制宏（不执行）。给导入预览/诊断用。
type Scan struct {
	SetVars  []string
	GetVars  []string
	Comments int
	Trim     int
	LastUser int
	Unknown  []string
}

func Analyze(text string) Scan {
	var out Scan
	for _, match := range macroRe.FindAllStringSubmatch(text, -1) {
		inner := strings.TrimSpace(match[1])
		lower := strings.ToLower(inner)
		switch {
		case strings.HasPrefix(lower, "//"):
			out.Comments++
		case lower == "trim":
			out.Trim++
		case lower == "lastusermessage":
			out.LastUser++
		case strings.HasPrefix(inner, "setvar::"):
			rest := inner[len("setvar::"):]
			if name, _, ok := strings.Cut(rest, "::"); ok {
				name = strings.TrimSpace(name)
				if name != "" {
					out.SetVars = append(out.SetVars, name)
				}
			}
		case strings.HasPrefix(inner, "getvar::"):
			name := strings.TrimSpace(inner[len("getvar::"):])
			if name != "" {
				out.GetVars = append(out.GetVars, name)
			}
		default:
			out.Unknown = append(out.Unknown, inner)
		}
	}
	return out
}
