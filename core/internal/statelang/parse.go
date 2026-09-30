package statelang

import (
	"fmt"
	"strings"
)

// Scan：在**一段文本**里找 `<state>` 块，产出「剔干净正文 + 语句 + 诊断」。
//
// 这是唯一能给"要发给模型的文本"过闸的入口：`Cleaned` 里绝不会再有标签。
func Scan(text string) Document {
	var document Document
	var cleaned strings.Builder
	cleaned.Grow(len(text))
	cursor := 0

	for {
		open := findTag(text, cursor, false)
		if open < 0 {
			break
		}
		openEnd := tagEnd(text, open)
		if openEnd < 0 {
			break // 认下的标签必然带 '>'；拿不到就当地没有标签
		}
		cleaned.WriteString(text[cursor:open])
		document.Blocks++

		close := findTag(text, openEnd, true)
		if close < 0 {
			// 没闭合：把剩下的都当块内容 —— **绝不能把它当正文发出去**
			document.Unterminated = true
			// 先报"没闭合"再报块体里的坏行 ⇒ 诊断整体**按行号升序**（与 Rust 版一致：界面从上往下读）
			document.Diagnostics = append(document.Diagnostics, Diagnostic{
				Line:    lineOf(text, open),
				Message: "状态块没有闭合，已按到文本结尾处理",
			})
			table := tableName(text, open, openEnd)
			baseLine := lineOf(text, openEnd)
			statements, diagnostics := parseStatements(text[openEnd:], baseLine)
			for index := range statements {
				statements[index].Table = table
			}
			document.Statements = append(document.Statements, statements...)
			document.Diagnostics = append(document.Diagnostics, diagnostics...)
			cursor = len(text)
			break
		}
		table := tableName(text, open, openEnd)
		if strings.ContainsAny(table, " \t\r\n/<>;=") {
			document.Diagnostics = append(document.Diagnostics, Diagnostic{
				Line:    lineOf(text, open),
				Message: "表名里不该出现空白或 `/` `<` `>` `;` `=`（照收，但多半是笔误）：" + table,
			})
		}
		baseLine := lineOf(text, openEnd)
		statements, diagnostics := parseStatements(text[openEnd:close], baseLine)
		for index := range statements {
			statements[index].Table = table
		}
		document.Statements = append(document.Statements, statements...)
		document.Diagnostics = append(document.Diagnostics, diagnostics...)
		end := tagEnd(text, close)
		if end < 0 {
			end = len(text) // 认下的闭合标签必然带 '>'；真拿不到就到结尾（安全侧）
		}
		cursor = end
	}

	cleaned.WriteString(text[cursor:])
	document.Cleaned = strings.TrimRight(cleaned.String(), " \t\r\n")
	return document
}

// ParseStatements：解析**块体**（标签之间的那段）。语言本体就是它，纯函数。
func ParseStatements(body string) ([]Statement, []Diagnostic) {
	return parseStatements(body, 1)
}

// parseStatements：`baseLine` 是块体第一行在**整段文本**里的行号（好让诊断指向用户看得见的那一行）。
func parseStatements(body string, baseLine int) ([]Statement, []Diagnostic) {
	statements := []Statement{}
	diagnostics := []Diagnostic{}

	// `;` 当作换行（口径：一行一个变量，`A=1; B=2` 与分两行等价）。
	// 因此**值里不能出现 `;`** —— 与"值里不能有空格"同一类规矩：宁可报出来，也不猜。
	lines := strings.Split(strings.ReplaceAll(body, ";", "\n"), "\n")
	for index, raw := range lines {
		lineNumber := baseLine + index
		line := trimSpace(raw)
		if line == "" {
			continue
		}
		if len(statements) >= StatementsMaxBlock {
			diagnostics = append(diagnostics, Diagnostic{
				Line: lineNumber, Text: line,
				Message: fmt.Sprintf("操作数超过上限 %d，其余已忽略", StatementsMaxBlock),
			})
			return statements, diagnostics
		}
		bad := func(message string) {
			diagnostics = append(diagnostics, Diagnostic{Line: lineNumber, Text: line, Message: message})
		}

		// **删除**：`delete(键)` —— 唯一的形态（调用形态，和 `键 = 值` 并列，最像样板代码）。
		// 括号内外多几个空格无妨；但 `del 键` / `del(键)` / `delete 键` 这些老形态一律**报错**，
		// 不做兼容 —— 与 `set` 同一条纪律：语言只有一个规范形态。
		if key, ok := deleteCall(line); ok {
			if message := checkKey(key); message != "" {
				bad(message)
				continue
			}
			statements = append(statements, Statement{Kind: KindDelete, Key: key})
			continue
		}

		// 赋值**只有一种形态**：`键 = 值`（`set` 已砍）。其余行首关键字各给一条说得清的报错 ——
		// 不这么做的话 `set A = 1` 会被当成"键叫 `set A`"，报出来的是"键不能含空白"，莫名其妙。
		// 行首的"词"：到空格或 `(` 为止 —— `del(A)` 这种没有空格的写法也得认出是 `del`
		head := line
		if index := strings.IndexAny(line, " \t("); index >= 0 {
			head = line[:index]
		}
		switch {
		case strings.EqualFold(head, "set"):
			bad("`set` 写法已砍掉，直接写 `键 = 值`")
			continue
		case strings.EqualFold(head, "del"), strings.EqualFold(head, "delete"):
			bad("删除要写成 `delete(键)`（`del 键` / `delete 键` 都不认了）")
			continue
		case strings.EqualFold(head, "setglobal"), strings.EqualFold(head, "delglobal"):
			bad("`" + head + "` 不从消息里写全局变量了，请挪到 config/ 的全局状态里")
			continue
		}
		if !strings.Contains(line, "=") {
			bad("不认识的操作 `" + head + "`，已忽略")
			continue
		}

		// 只在**第一个** `=` 处切 —— 值里可以有 `=`
		before, after, _ := strings.Cut(line, "=")
		key := trimSpace(before)
		trimmed := trimSpace(after)
		value := &trimmed

		if message := checkKey(key); message != "" {
			bad(message)
			continue
		}
		// **一行只能有一个变量**：值里出现空白 ⇒ 多半是把两条写在一行了（`A=1 B=2`）。
		// 这里刻意**报错而不是静默删空格** —— 删空格会把 `A = 你好 世界` 悄悄改成 `你好世界`。
		if strings.ContainsAny(*value, " \t\r\n") {
			bad("值里不能有空格（一行只能有一个变量，多个变量请用 `;` 或换行分开）")
			continue
		}
		if chars := len([]rune(*value)); chars > ValueMaxChars {
			bad(fmt.Sprintf("值超过 %d 字符（%d），已忽略：%s", ValueMaxChars, chars, key))
			continue
		}

		statements = append(statements, Statement{Kind: KindSet, Key: key, Value: value})
	}
	return statements, diagnostics
}

// tableName：标签里的表名 —— `<state 玩家状态>` ⇒ `玩家状态`；**不写表名 ⇒ `global`**（`DefaultTable`）。
//
// 于是 `<state>…</state>` 与 `<state global>…</state>` 落到**同一张表**（后写覆盖先写）——
// 这正是"未标表名的默认视作 global"那句话的落点。
//
// 名字**照收**（trim 之后是什么就是什么），只对明显写坏的名字报一条诊断：
// 名字里出现空白或 `/ < > = ;` 时，多半是笔误 —— 但**不静默丢弃**，也不改它的归属。
func tableName(text string, open, openEnd int) string {
	inner := trimSpace(text[open+1 : openEnd-1]) // `<state 玩家状态>` → `state 玩家状态`
	if len(inner) < len(BlockTag) {
		return DefaultTable
	}
	name := trimSpace(inner[len(BlockTag):]) // 去掉 `state` 前缀（大小写不敏感）
	if name == "" {
		return DefaultTable
	}
	if strings.ContainsAny(name, " \t\r\n/<>;=") {
		return name // 归属不改；调用方另出诊断（Scan 里）
	}
	return name
}

// deleteCall：认 `delete(键)`（括号内外多几个空格无妨）。认不出返回 false。
func deleteCall(line string) (string, bool) {
	open := strings.IndexByte(line, '(')
	if open < 0 {
		return "", false
	}
	if !strings.EqualFold(trimSpace(line[:open]), "delete") {
		return "", false
	}
	inner, ok := strings.CutSuffix(trimSpace(line[open+1:]), ")")
	if !ok {
		return "", false
	}
	return trimSpace(inner), true
}

func checkKey(key string) string {
	if key == "" {
		return "键为空"
	}
	if len([]rune(key)) > KeyMaxChars {
		return fmt.Sprintf("键超过 %d 字符", KeyMaxChars)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "键不能含空白"
	}
	if strings.ContainsAny(key, "/<>=") {
		return "键不能含 `/` `<` `>` `=`"
	}
	return ""
}

// splitFirstSpace：在第一处空白切开（`set A = 1` → `set` + `A = 1`）。
func splitFirstSpace(line string) (head, tail string) {
	for index, r := range line {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return line[:index], line[index+len(string(r)):]
		}
	}
	return line, ""
}

// findTag：找下一个 `<state>`（closing=false）或 `</state>`（closing=true）的起始下标；没有返回 -1。
// 名字大小写不敏感、标签内允许多余空白（`<State >` 也认）。
func findTag(text string, from int, closing bool) int {
	cursor := from
	for cursor < len(text) {
		offset := strings.IndexByte(text[cursor:], '<')
		if offset < 0 {
			return -1
		}
		start := cursor + offset
		end := tagEnd(text, start)
		if end < 0 {
			// 没有 '>' ⇒ 这不是标签（正文里的 `<` 多了去了），后面也不会再有完整标签
			return -1
		}
		if isBlockTag(text[start+1:end-1], closing) {
			return start
		}
		// 不是标签 ⇒ **只往前挪一个字符**，别跳到这个 `>` 后面：
		// `a < b <state>…</state>` 里那个假标签的 `>` 可能站在真标签之后，一步跳过去就会漏掉真块。
		cursor = start + 1
	}
	return -1
}

// isBlockTag：标签名是不是我们的块标签 —— `state` 或 `state 表名`（大小写不敏感、允许多余空白）。
//
// 闭合标签里写不写表名都认（`</state>` 与 `</state 玩家状态>` 等价）：模型爱写对称，
// 不认的话整块会被当成"没闭合"，把后半段正文一起吞掉。
func isBlockTag(raw string, closing bool) bool {
	name := trimSpace(raw)
	if closing {
		rest, ok := strings.CutPrefix(name, "/")
		if !ok {
			return false
		}
		name = trimSpace(rest)
	}
	if len(name) < len(BlockTag) || !strings.EqualFold(name[:len(BlockTag)], BlockTag) {
		return false
	}
	rest := name[len(BlockTag):]
	// 必须是 `state` 本身，或 `state 表名`（`stateX` 不算）
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

// tagEnd：标签 `<…>` 的结束下标（**含** `>`）；没有 `>` 返回 -1 —— 那不是标签。
func tagEnd(text string, start int) int {
	if start >= len(text) || text[start] != '<' {
		return -1
	}
	offset := strings.IndexByte(text[start:], '>')
	if offset < 0 {
		return -1
	}
	return start + offset + 1
}

// lineOf：`offset` 处在第几行（1 起）。
func lineOf(text string, offset int) int {
	if offset > len(text) {
		offset = len(text)
	}
	return 1 + strings.Count(text[:offset], "\n")
}
