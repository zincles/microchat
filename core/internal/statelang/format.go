package statelang

import "strings"

// Format：把语句写成**规范形式**（一行一条）：赋值 `键 = 值`，删除 `del(键)`。
//
// 契约（有测试钉住）：解析器认得的语句，`Format` 出来的文本一定能被解析回**同一批语句**；
// 再 `Format` 一次结果不变。注释/空行/`set` 前缀这些等价写法都会被规范化掉。
func Format(statements []Statement) string {
	var builder strings.Builder
	for index, statement := range statements {
		if index > 0 {
			builder.WriteByte('\n')
		}
		switch statement.Kind {
		case KindDelete:
			builder.WriteString("delete(" + statement.Key + ")")
		default:
			value := ""
			if statement.Value != nil {
				value = *statement.Value
			}
			builder.WriteString(statement.Key + " = " + value)
		}
	}
	return builder.String()
}

// FormatBlock：连着 `<state>` 标签一起写 —— 要往提示词/消息里落库时用这个。
func FormatBlock(statements []Statement) string {
	if len(statements) == 0 {
		return ""
	}
	return "<" + BlockTag + ">\n" + Format(statements) + "\n</" + BlockTag + ">"
}
