// Package statelang：世界状态那门小语言的**全部**规则 —— 解析、格式化、折叠。
//
// 它是什么：一块记事板。语句只有两种 —— **赋值**（`键 = 值`，值一律是字符串）
// 与**删除**（`del(键)`）。没有运算、没有变量作右值、没有条件、没有类型。
//
// 设计约束（四条，都是刻意的）：
//  1. **零依赖** —— 不 import 项目里任何包（连 model 都不）。语法规则只住在这里，
//     `world` 不许自己切行（有守卫测试盯着）。
//  2. **解析即验证** —— 没有单独的 Validate：`Scan` / `ParseStatements` 直接给诊断，
//     免得两套判断标准各说各话。
//  3. **容错解析** —— 一行坏 ⇒ 跳过那一行并**报出来**，整块仍然从正文里剔除：
//     标签泄漏进上下文，比丢掉一条操作严重得多。
//  4. **诊断带行号** —— 用户是在自己的提示词/消息里改这几行，得能直接定位。
//
// 语法（定稿与理由见 AGENTS.md 的「<state> 的语法」）：
//
//	<state>
//	AA = 123456        ← 赋值
//	B = 234; C = 落石   ← `;` 当换行（所以一行只能有一个变量）
//	del(AA)            ← 删除；`del AA` 也认
//	set B = 5          ← 兼容写法，与 `B = 5` 等价
//	</state>
//
// 词法一句话：块可以出现在任意位置；值里可以有 `=`（只在第一个处切）、**不能有空格**
// （⇒ `A=1 B=2` 不合法：报错，而不是静默删空格 —— 删空格会悄悄改掉值）。
package statelang

import "strings"

// 语法上限（就是语法的一部分，所以住在这里；world/UI 都从这里取）。
const (
	KeyMaxChars        = 64   // 键最长多少字符
	ValueMaxChars      = 2048 // 值最长多少字符
	StatementsMaxBlock = 32   // 一个块里最多几条语句
)

// BlockTag：块的标签名。**不可改** —— 它已经在存档与文档里了。
const BlockTag = "state"

// Kind：语句的种类。
type Kind string

const (
	KindSet    Kind = "set"
	KindDelete Kind = "delete"
)

// Statement：一条语句。
//
// JSON 形状（**稳定**，黄金测试按它写）：`{"kind":"set","table":"玩家状态","key":"AA","value":"123456"}`
// —— 删除语句没有 `value` 字段；未命名表的 `table` 是空串（省略不写）。
type Statement struct {
	Kind  Kind    `json:"kind"`
	Table string  `json:"table,omitempty"`
	Key   string  `json:"key"`
	Value *string `json:"value,omitempty"`
}

func String(value string) *string { return &value }

// Diagnostic：哪一行出了什么问题（行号是**文本里**的行号，1 起）。
type Diagnostic struct {
	Line    int    `json:"line"`
	Text    string `json:"text"` // 原样那一行 —— 报错时别再改字
	Message string `json:"message"`
}

// Document：`Scan` 的产出：剔干净之后的正文 + 解析出的语句 + 诊断。
//
// **注意这是"解析"不是"计算"**：`Statements` 是读出来的操作（原样、按出现顺序），
// 删除还没有生效、后写也没有覆盖前写 —— 要"算完的当前值"用 `Tables`。
type Document struct {
	// Cleaned：把 `<state>` 块**整块**删掉之后的正文。发给模型的永远是它。
	Cleaned string `json:"-"`
	// Statements：按出现顺序（折叠就按这个顺序累加）。
	Statements []Statement `json:"statements"`
	// Diagnostics：坏行与提醒。**不是**致命错误：解析继续。
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	// Blocks：认出几个块（含未闭合的那个）。
	Blocks int `json:"blocks"`
	// Unterminated：最后一个块没闭合（已按"到文本结尾"处理，仍然不会泄漏）。
	Unterminated bool `json:"unterminated,omitempty"`
}

// ContainsBlock：这段文本里**有没有** `<state>` 块（粗筛，给界面用：挂个"含状态"的小标记）。
//
// 注意它和"有没有**可解析的**语句"是两回事 —— 后者看 `Scan(...).Statements`。
func ContainsBlock(text string) bool {
	return findTag(text, 0, false) >= 0
}

// clears：这条语句是不是"把这个键清掉"。
//
// 两种写法效果相同（§37）：`delete(键)`，以及**赋成空值** `键 =`。
// 注意这属于**计算**：解析出来的语句仍然是 set（值是空串）—— 解析如实报告，计算才动手。
// 推论：这门语言里**存不下空串**（空值被理解成"清掉"，不是"设成空"）。
func clears(statement Statement) bool {
	return statement.Kind == KindDelete || statement.Value == nil || *statement.Value == ""
}

// Tables：**解析 + 计算** —— 读一段文本里的所有 `<state>` 块，按顺序折叠，得到
// `{表名: {键: 值}}`。删除过的键不出现；算完为空的表整张不出现；未命名表用空串作键。
//
// 这就是给外部工具用的那个形状（也是 `POST /api/v1/statelang` 的主体）。
// 想只要"发生了什么"（不计算）就用 `Scan(text).Statements`。
func Tables(text string) map[string]map[string]string {
	document := Scan(text)
	// 按**首次出现**的顺序建表，但 Go 的 map 走到 JSON 时按键排序 ⇒ 输出是稳定的
	tables := map[string]map[string]string{}
	for _, statement := range document.Statements {
		if _, ok := tables[statement.Table]; !ok {
			tables[statement.Table] = map[string]string{}
		}
		// 逐条就地折叠：与本表内的顺序一致（跨表的语句互不影响）
		if clears(statement) {
			delete(tables[statement.Table], statement.Key)
			continue
		}
		tables[statement.Table][statement.Key] = *statement.Value
	}
	for name, table := range tables {
		if len(table) == 0 {
			delete(tables, name) // 空的表自动消失
		}
	}
	return tables
}

// Fold：从空开始按顺序执行语句，得到**一层**的状态（键 → 值）。
//
// **没有墓碑**（2026-09-28 定稿）：`delete` 就是把这个键从本层拿掉，不留痕迹。
// 代价见 AGENTS.md：删**提示词底子**里的键 = 它会从下面漏回来
// （"删"只作用于自己那一层）⇒ 不想被删掉的键，别写进底子，写进第一条消息。
//
// 分层由调用方负责：本函数只管一层；跨层合并是 world 的活。
func Fold(statements []Statement) map[string]string {
	folded := map[string]string{}
	for _, statement := range statements {
		if clears(statement) {
			delete(folded, statement.Key)
			continue
		}
		folded[statement.Key] = *statement.Value
	}
	return folded
}

// 语法里用到的几个字符串判断，收在一处。
func isKeywordDelete(token string) bool { return token == "del" }

func trimSpace(text string) string { return strings.TrimSpace(text) }
