package statelang

import "strings"

// Format：把语句写回成**规范文本** —— 一行一条（赋值 `键 = 值`，删除 `delete(键)`），
// 并按语句的 `Table` 切成块：**命名表写 `<state 表名>`**，`global`（或不写表名）写 `<state>`。
//
// 为什么是这个形状：语言里**一块只装一张表**（表名住标签上），所以"表名"不是可有可无的一格 ——
// 丢掉它就等于把语句悄悄挪回 `global`（最恶心的那种错：不报错、结果不一样）。
// 断表名：表名不同的**相邻两段**各写一块；同一张表的连续语句合成一块（顺序原样保留，
// 因为"后写覆盖先写"是**按顺序**算的）。
//
// 契约（有测试钉住）：
//   - `Scan(Format(statements)).Statements` 与传入的语句**逐字段相同**（`Table` 那一格：
//     **空串与 `global` 是同一张表**，回来的是 `global` —— 不写表名的块本来就归 `global`）；
//   - 块长按 `StatementsMaxBlock` 切开，所以超过一块上限的长表也能原样往返；
//   - 再 `Format` 一次结果不变（幂等）。注释 / 空行 / `set` 前缀 / `del(键)` 这些等价写法都会被规范化掉；
//   - 空列表 ⇒ 空串（不写一个空块 —— 那不是"一个块"，是"没有块"）。
func Format(statements []Statement) string {
	var builder strings.Builder
	for index := 0; index < len(statements); {
		table := statements[index].Table
		end := index
		for end < len(statements) && statements[end].Table == table { // 同一张表连着的一段 = 一块
			end++
		}
		for chunk := index; chunk < end; chunk += StatementsMaxBlock {
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			writeBlock(&builder, table, statements[chunk:min(chunk+StatementsMaxBlock, end)])
		}
		index = end
	}
	return builder.String()
}

// writeBlock：写一块。**不写表名的只有 `global`** —— 不写就等于 `global`（`DefaultTable`），
// 两种写法落到同一张表 ⇒ 写哪一种是**可逆**的（这里选"不写"，因为它短）。
func writeBlock(builder *strings.Builder, table string, statements []Statement) {
	builder.WriteString("<" + BlockTag)
	if name := blockTableName(table); name != "" {
		builder.WriteString(" " + name)
	}
	builder.WriteString(">\n")
	for index, statement := range statements {
		if index > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(statementLine(statement))
	}
	builder.WriteString("\n</" + BlockTag + ">")
}

// blockTableName：标签里该写哪个名字 —— 空串与 `global` 融合（两者**是同一张表**，见 `tableName`：
// 不写表名 ⇒ `DefaultTable`），其余原样。
func blockTableName(table string) string {
	if table == "" || table == DefaultTable {
		return ""
	}
	return table
}

// statementLine：一条语句的规范形态（**不含标签**，那个归 `writeBlock`）。
func statementLine(statement Statement) string {
	if statement.Kind == KindDelete {
		return "delete(" + statement.Key + ")"
	}
	value := ""
	if statement.Value != nil {
		value = *statement.Value
	}
	return statement.Key + " = " + value
}
