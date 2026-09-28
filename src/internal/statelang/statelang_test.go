package statelang

import (
	"encoding/json"
	"strings"
	"testing"
)

func set(key, value string) Statement {
	return Statement{Kind: KindSet, Key: key, Value: String(value)}
}

func remove(key string) Statement { return Statement{Kind: KindDelete, Key: key} }

func TestScanStripsBlocksAndKeepsProse(t *testing.T) {
	document := Scan("雨水顺着屋檐落下。\n\n<state>\nHP = 12\ndelete(火把)\n</state>\n")
	if document.Cleaned != "雨水顺着屋檐落下。" {
		t.Fatalf("Cleaned = %q", document.Cleaned)
	}
	if len(document.Statements) != 2 || !statementsEqual(document.Statements, []Statement{set("HP", "12"), remove("火把")}) {
		t.Fatalf("语句 = %+v", document.Statements)
	}
	if len(document.Diagnostics) != 0 || document.Blocks != 1 || document.Unterminated {
		t.Fatalf("诊断 = %+v（块数 %d）", document.Diagnostics, document.Blocks)
	}
}

// 用户口径（§36）：裸赋值、`del(...)`、`;` 当换行；`set`/`del` 照旧认。
func TestAcceptsBareAssignmentDelParensAndSemicolons(t *testing.T) {
	document := Scan("<state>\nAA = 123456\nB = 234\ndelete(AA)\nC = 3\ndelete(D)\nE=5; F=6\n</state>")
	want := []Statement{set("AA", "123456"), set("B", "234"), remove("AA"), set("C", "3"), remove("D"), set("E", "5"), set("F", "6")}
	if !statementsEqual(document.Statements, want) {
		t.Fatalf("语句 = %+v\n想要 = %+v", document.Statements, want)
	}
	if len(document.Diagnostics) != 0 {
		t.Fatalf("诊断 = %+v", document.Diagnostics)
	}
}

// 砍掉的写法要给**说得清**的报错（不是"键不能含空白"这种莫名其妙的话）。
func TestRetiredFormsExplainThemselves(t *testing.T) {
	for _, line := range []string{"set A = 1", "del A", "del(A)", "delete A"} {
		document := Scan("<state>\n" + line + "\n</state>")
		if len(document.Statements) != 0 {
			t.Fatalf("%q 不该收下：%+v", line, document.Statements)
		}
		if len(document.Diagnostics) != 1 {
			t.Fatalf("%q 诊断 = %+v", line, document.Diagnostics)
		}
		message := document.Diagnostics[0].Message
		if !strings.Contains(message, "砍掉") && !strings.Contains(message, "delete(键)") {
			t.Fatalf("%q 的报错说不清：%q", line, message)
		}
	}
	// 括号内外留空格仍然认（同一个形态，不是第二种写法）
	document := Scan("<state>\ndelete ( 想法 )\n</state>")
	if len(document.Statements) != 1 || document.Statements[0].Kind != KindDelete || document.Statements[0].Key != "想法" {
		t.Fatalf("宽容空白后应认下 delete ( 想法 )：%+v · %+v", document.Statements, document.Diagnostics)
	}
}

func TestBadLinesReportLineNumbers(t *testing.T) {
	// 第 2 行起是块体：`A=1 B=2` 在第 3 行（含 `<state>` 那行算第 2 行）
	document := Scan("正文\n<state>\nA=1 B=2\n乱写一行\n缺等号\n坏/键 = 1\n</state>")
	if len(document.Statements) != 0 {
		t.Fatalf("不该收下任何语句：%+v", document.Statements)
	}
	// 第 3、4 行都是"不认识的操作"：`乱写一行` 与 `缺等号`（后者不再有"缺少 `=`"这条诊断 ——
	// `set` 砍掉后，赋值路径必然含 `=`）
	wantWords := []string{"一行只能有一个变量", "不认识的操作", "不认识的操作", "键不能含"}
	if len(document.Diagnostics) != len(wantWords) {
		t.Fatalf("诊断 = %+v", document.Diagnostics)
	}
	for index, word := range wantWords {
		if !strings.Contains(document.Diagnostics[index].Message, word) {
			t.Fatalf("第 %d 条诊断 = %q，想要含 %q", index, document.Diagnostics[index].Message, word)
		}
	}
	if document.Diagnostics[0].Line != 3 {
		t.Fatalf("第一条诊断该指向第 3 行，得到 %d", document.Diagnostics[0].Line)
	}
	if !strings.Contains(document.Diagnostics[0].Text, "A=1 B=2") {
		t.Fatalf("诊断要原样带上那一行：%q", document.Diagnostics[0].Text)
	}
}

// 标签泄漏进上下文比丢掉一条操作严重得多 —— 未闭合的块按"到文本结尾"处理。
func TestUnterminatedBlockNeverLeaks(t *testing.T) {
	document := Scan("正文\n<state>\nA = 1\n后面还有字")
	if strings.Contains(document.Cleaned, "<state>") || strings.Contains(document.Cleaned, "A = 1") {
		t.Fatalf("泄漏了：%q", document.Cleaned)
	}
	if document.Cleaned != "正文" {
		t.Fatalf("Cleaned = %q", document.Cleaned)
	}
	if !document.Unterminated || !statementsEqual(document.Statements, []Statement{set("A", "1")}) {
		t.Fatalf("语句 = %+v · 未闭合 = %v", document.Statements, document.Unterminated)
	}
}

// 正文里的裸 `<` 不能把真块挤掉（假标签的 `>` 可能站在真标签之后）。
func TestFalseTagBeforeRealBlock(t *testing.T) {
	document := Scan("伤害 < 10 而 <state>HP = 3</state> 之后")
	if !statementsEqual(document.Statements, []Statement{set("HP", "3")}) {
		t.Fatalf("语句 = %+v", document.Statements)
	}
	if strings.Contains(document.Cleaned, "HP") {
		t.Fatalf("块没剔干净：%q", document.Cleaned)
	}
}

// 两个块 ⇒ 语句按出现顺序拼接（折叠就靠这个顺序）。
func TestTwoBlocksConcatenateInOrder(t *testing.T) {
	document := Scan("<state>A = 1</state>中间<state>\nB = 2\n</state>")
	if !statementsEqual(document.Statements, []Statement{set("A", "1"), set("B", "2")}) {
		t.Fatalf("语句 = %+v", document.Statements)
	}
	if document.Cleaned != "中间" || document.Blocks != 2 {
		t.Fatalf("Cleaned = %q · 块数 = %d", document.Cleaned, document.Blocks)
	}
}

// 空值 = 清掉（§37）：**计算**动手，**解析**照实报告 —— 这条界线就是"解析 ≠ 计算"。
func TestEmptyValueClearsTheKey(t *testing.T) {
	document := Scan("<state 玩家状态>想法 = ;HP = 10</state>")
	// 解析：两条 set，值分别是空串与 10（没有变成 delete）
	if len(document.Statements) != 2 || document.Statements[0].Kind != KindSet {
		t.Fatalf("解析结果 = %+v", document.Statements)
	}
	if document.Statements[0].Value == nil || *document.Statements[0].Value != "" {
		t.Fatalf("解析该如实报告空串：%+v", document.Statements[0])
	}
	// 计算：空值那个键不在里面；空表整张消失
	tables := Tables("<state 玩家状态>想法 = ;HP = 10</state>")
	if _, exists := tables["玩家状态"]["想法"]; exists {
		t.Fatalf("空值该把键清掉：%+v", tables)
	}
	if tables["玩家状态"]["HP"] != "10" {
		t.Fatalf("tables = %+v", tables)
	}
	if len(Tables("<state>X = </state>")) != 0 {
		t.Fatalf("清空之后的表该消失：%+v", Tables("<state>X = </state>"))
	}
	// 折叠本身（单层）也守同一条规则
	if len(Fold(document.Statements)) != 1 {
		t.Fatalf("Fold = %+v", Fold(document.Statements))
	}
}

// 格式器契约：解析器认得的语句 ⇒ Format 出来的文本解析回**同一批语句**；再格式化不变。
func TestFormatRoundtripAndIdempotent(t *testing.T) {
	origin := []Statement{set("AA", "123456"), set("状态", "血量=50/100=危险"), remove("AA"), set("C", "3")}
	text := Format(origin)
	parsed, diagnostics := ParseStatements(text)
	if len(diagnostics) != 0 {
		t.Fatalf("格式化出的文本解析有诊断：%+v", diagnostics)
	}
	if !statementsEqual(parsed, origin) {
		t.Fatalf("往返不一致：\n得到 %+v\n想要 %+v", parsed, origin)
	}
	if second := Format(parsed); second != text {
		t.Fatalf("不幂等：\n%q\n%q", text, second)
	}
	if block := FormatBlock(origin); !strings.HasPrefix(block, "<state>") || !strings.HasSuffix(block, "</state>") {
		t.Fatalf("整块形状不对：%q", block)
	}
	if statements, _ := ParseStatements(FormatBlock(origin)); !statementsEqual(statements, origin) {
		t.Fatalf("带标签往返也不一致：%+v", statements)
	}
}

// 折叠：**没有墓碑** —— 删就是直接删掉，键连"删过"的痕迹都不留。
func TestFoldRemovesWithoutTrace(t *testing.T) {
	folded := Fold([]Statement{set("A", "1"), set("B", "2"), remove("A"), set("B", "3")})
	if _, exists := folded["A"]; exists {
		t.Fatalf("A 该被直接删掉，得到 %q", folded["A"])
	}
	if folded["B"] != "3" {
		t.Fatalf("B = %q", folded["B"])
	}
	if _, exists := folded["C"]; exists {
		t.Fatalf("没设置过的键不该出现")
	}
	if len(folded) != 1 {
		t.Fatalf("折叠 = %+v", folded)
	}
}

// JSON 形状是**稳定契约**（黄金测试 / 跨语言向量都按它写）。
func TestGoldenJSONShape(t *testing.T) {
	document := Scan("<state>\nAA = 123456\ndelete(AA)\nA=1 B=2\n</state>")
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"statements":[{"kind":"set","key":"AA","value":"123456"},{"kind":"delete","key":"AA"}],` +
		`"diagnostics":[{"line":4,"text":"A=1 B=2","message":"值里不能有空格（一行只能有一个变量，多个变量请用 ` + "`;`" + ` 或换行分开）"}],` +
		`"blocks":1}`
	if string(raw) != want {
		t.Fatalf("JSON 形状变了：\n得到 %s\n想要 %s", raw, want)
	}
}

func TestContainsBlock(t *testing.T) {
	for text, want := range map[string]bool{
		"<state>A = 1</state>":  true,
		"<State >A = 1</State>": true,
		"<状态>A = 1</状态>":        false,
		"伤害 < 10":               false,
		"没有块":                   false,
	} {
		if got := ContainsBlock(text); got != want {
			t.Fatalf("ContainsBlock(%q) = %v，想要 %v", text, got, want)
		}
	}
}

func statementsEqual(a, b []Statement) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index].Kind != b[index].Kind || a[index].Key != b[index].Key {
			return false
		}
		if (a[index].Value == nil) != (b[index].Value == nil) {
			return false
		}
		if a[index].Value != nil && *a[index].Value != *b[index].Value {
			return false
		}
	}
	return true
}

// 解析器**永远不许 panic**，而且**永远不许把标签留在 Cleaned 里**（这是安全性质，不是风格）。
func FuzzScan(f *testing.F) {
	for _, seed := range []string{
		"", "<state>", "</state>", "<state></state>", "<state>\nA = 1\n</state>",
		"a < b <state>X = 1</state>", "<state>A=1 B=2", "中文<state>键 = 值</state>后文",
		"<state>" + strings.Repeat("x = 1\n", 40) + "</state>",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		document := Scan(text)
		if ContainsBlock(document.Cleaned) {
			t.Fatalf("标签泄漏进正文：%q → %q", text, document.Cleaned)
		}
		for _, statement := range document.Statements {
			if statement.Key == "" {
				t.Fatalf("空键不该进语句：%q", text)
			}
		}
		Fold(document.Statements) // 折叠不许 panic
	})
}
