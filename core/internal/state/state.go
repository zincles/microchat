// Package state：**世界状态**那一层 —— 分层现演、注入渲染、以及**唯一**的出站拼装路径。
//
// 语法（`<state>` 的解析与折叠）在 `statelang` 里；这一层负责"按会话顺序把各层折出来"
// （线性会话 ⇒ 顺序就是全部消息的先后，没有"当前路径"这回事）：
//
//	底子（生效的系统提示词里的 <state> 块）⇒ 全局 或 本会话（看提示词是谁写的）
//	消息正文里的块，按顺序 ⇒ 本会话
//
// **没有派生表**：文本一改，现演结果自然就变 —— 编辑/删除消息、改提示词都不存在"重算变量"。
package state

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"microchat/internal/model"
	"microchat/internal/statelang"
)

// Scope：一条操作算哪一层。
type Scope string

const (
	// ScopeGlobal：底子来自 agent 的提示词 ⇒ 用同一个 agent 的会话共享。
	ScopeGlobal Scope = "global"
	// ScopeSession：底子来自会话自己的提示词，或来自消息正文 ⇒ 只属于这条会话。
	ScopeSession Scope = "session"
)

// PromptSource：生效的那份系统提示词是从哪儿来的 —— 决定它的 `<state>` 块算哪一层。
type PromptSource string

const (
	// PromptFromAgent：会话没写自己的提示词（用的 agent 的）⇒ 底子算全局。
	PromptFromAgent PromptSource = "agent"
	// PromptFromSession：会话自己写了（覆盖了 agent 的）⇒ 底子算本会话。
	PromptFromSession PromptSource = "session"
)

// OpRow：一条"可现演的操作"—— 带上它来自哪条消息，于是"哪句话带来的状态"追得回来。
//
// JSON 形状与旧版（deprecated/）逐字一致：字段顺序即契约。
type OpRow struct {
	Seq       int64          `json:"seq"`
	Table     string         `json:"table"`
	Scope     Scope          `json:"scope"`
	Kind      statelang.Kind `json:"kind"`
	Key       string         `json:"key"`
	Value     *string        `json:"value"`
	MessageID *string        `json:"message_id"`
	SessionID *string        `json:"session_id"`
	CreatedAt int64          `json:"created_at"`
}

// View：面板与接口要的一份快照。
//
// `global_values` / `effective` 是**未命名表**那一层（老写法 `<state>…</state>` 的键住那儿），
// 留着是为了别把现存的界面弄坏；`tables` 才是全貌（表名 → 键 → 值）。
type View struct {
	Global       []OpRow                      `json:"global"`
	Session      []OpRow                      `json:"session"`
	GlobalValues map[string]string            `json:"global_values"`
	Effective    map[string]string            `json:"effective"`
	Tables       map[string]map[string]string `json:"tables"`
}

// Tables：表名 → 键 → 值。空串表名 = 未命名表（老写法 `<state>`）。
type Tables = map[string]map[string]string

// FromSources：**现演一份快照**（顺序就是 fold 的顺序）。
func FromSources(sessionID, systemPrompt string, source PromptSource, messages []model.Message) View {
	promptScope := ScopeGlobal
	if source == PromptFromSession {
		promptScope = ScopeSession
	}

	globalRows := []OpRow{}
	sessionRows := []OpRow{}

	// 1) 底子：系统提示词里的块（没有块就是空底子，很正常）。
	for _, statement := range statelang.Scan(systemPrompt).Statements {
		row := OpRow{
			Table:     statement.Table,
			Scope:     promptScope,
			Kind:      statement.Kind,
			Key:       statement.Key,
			Value:     statement.Value,
			SessionID: &sessionID,
		}
		if promptScope == ScopeGlobal {
			globalRows = append(globalRows, row)
		} else {
			sessionRows = append(sessionRows, row)
		}
	}

	// 2) 然后才是消息，按顺序追加。
	for index := range messages {
		message := messages[index]
		messageID := message.ID
		createdAt := message.CreatedAt
		for _, statement := range statelang.Scan(message.Content).Statements {
			sessionRows = append(sessionRows, OpRow{
				Table:     statement.Table,
				Scope:     ScopeSession,
				Kind:      statement.Kind,
				Key:       statement.Key,
				Value:     statement.Value,
				MessageID: &messageID,
				SessionID: &sessionID,
				CreatedAt: createdAt,
			})
		}
	}

	for index := range globalRows {
		globalRows[index].Seq = int64(index)
	}
	for index := range sessionRows {
		sessionRows[index].Seq = int64(index)
	}

	global := fold(globalRows, ScopeGlobal)
	session := fold(sessionRows, ScopeSession)
	merged := merge(global, session)
	return View{
		Global:       globalRows,
		Session:      sessionRows,
		GlobalValues: unnamedTable(global),
		Effective:    unnamedTable(merged),
		Tables:       merged,
	}
}

// Fold：从空开始按出现顺序折叠，得到**一层**的 `{表: {键: 值}}`。
//
// 删除（`delete(键)` 或赋空值）就是把这个键**直接删掉**，不留痕迹（§38）；
// 算完为空的表自动消失（§37）。
func Fold(statements []statelang.Statement) Tables {
	rows := make([]statelang.Statement, 0, len(statements))
	rows = append(rows, statements...)
	return foldStatements(rows)
}

func fold(rows []OpRow, scope Scope) Tables {
	statements := make([]statelang.Statement, 0, len(rows))
	for index := range rows {
		if rows[index].Scope != scope {
			continue
		}
		statements = append(statements, statelang.Statement{
			Kind: rows[index].Kind, Table: rows[index].Table,
			Key: rows[index].Key, Value: rows[index].Value,
		})
	}
	return foldStatements(statements)
}

func foldStatements(statements []statelang.Statement) Tables {
	tables := Tables{}
	for _, statement := range statements {
		if tables[statement.Table] == nil {
			tables[statement.Table] = map[string]string{}
		}
		if clears(statement) {
			delete(tables[statement.Table], statement.Key)
			continue
		}
		tables[statement.Table][statement.Key] = *statement.Value
	}
	for name, table := range tables {
		if len(table) == 0 {
			delete(tables, name)
		}
	}
	return tables
}

// clears：这条语句是不是"把这个键清掉" —— `delete(键)`，或**赋成空值** `键 =`（§37）。
func clears(statement statelang.Statement) bool {
	return statement.Kind == statelang.KindDelete || statement.Value == nil || *statement.Value == ""
}

// merge：全局打底、本会话覆写，**按表各自合并**。
// 本会话删掉的键会从全局漏回来 —— 这就是"无墓碑"的语义（§38）。
func merge(global, session Tables) Tables {
	merged := Tables{}
	for name, table := range global {
		copied := make(map[string]string, len(table))
		for key, value := range table {
			copied[key] = value
		}
		merged[name] = copied
	}
	for name, table := range session {
		if merged[name] == nil {
			merged[name] = map[string]string{}
		}
		for key, value := range table {
			merged[name][key] = value
		}
	}
	for name, table := range merged {
		if len(table) == 0 {
			delete(merged, name)
		}
	}
	return merged
}

// unnamedTable：未命名表（老写法 `<state>…</state>` 的键都住这儿）。
func unnamedTable(tables Tables) map[string]string {
	table := tables[""]
	if table == nil {
		return map[string]string{}
	}
	return table
}

// RenderTable：注入给模型的变量表 —— **按表分组**渲染；未命名表不写表头；空 ⇒ false。
//
// 这是"告诉模型现在是什么值"的唯一渲染处；标签（`<state>`）**永远不出现**在里面。
func RenderTable(tables Tables) (string, bool) {
	if len(tables) == 0 {
		return "", false
	}
	var builder strings.Builder
	builder.WriteString("当前变量:")
	names := sortedKeys(tables)
	for _, name := range names {
		table := tables[name]
		if len(table) == 0 {
			continue
		}
		if name == "" {
			for _, key := range sortedKeys(table) {
				builder.WriteString("\n  " + key + " = " + table[key])
			}
			continue
		}
		builder.WriteString("\n  〔" + name + "〕")
		for _, key := range sortedKeys(table) {
			builder.WriteString("\n    " + key + " = " + table[key])
		}
	}
	return builder.String(), true
}

// OutgoingRole：发往上游时 `messages[].role` 的取值。
type OutgoingRole string

const (
	RoleSystem    OutgoingRole = "system"
	RoleUser      OutgoingRole = "user"
	RoleAssistant OutgoingRole = "assistant"
)

// Outgoing：发给模型的一条消息。角色含 system —— 系统提示词不落库，但必须出现在请求里。
//
// `Source` 那一组是**给自己看的出处**（不发给上游）：检查"压缩后到底发了什么"只能靠它，
// 否则界面只能靠正文里的抬头去猜（猜法一改就散 ✗）。
type Outgoing struct {
	Role    OutgoingRole `json:"role"`
	Content string       `json:"content"`

	Source    string  `json:"source"`               // system | message | summary
	MessageID *string `json:"message_id,omitempty"` // source=message
	SummaryID *string `json:"summary_id,omitempty"` // source=summary
	Blocks    *int64  `json:"blocks,omitempty"`     // source=summary：它覆盖几个块
}

// BuildOutgoing：组装**真正要发出去的东西** —— 系统提示词（+ 当前变量表）+ 历史
// （标签已剔除、有摘要就不发明文）。
//
// 这是"剔除标签、只发当前状态、按摘要收拢"的**唯一**实现处；谁都不许再写第二条拼装路径。
// 摘要在**装配**里取代原文（Mask），存放处一个字节都不动。
func BuildOutgoing(systemPrompt string, messages []model.Message, summaries []model.Summary, tables Tables) []Outgoing {
	// 提示词里的 `<state>` 块和消息里一个待遇：**原样留在存档里，发出去时剔除**。
	system := strings.TrimSpace(statelang.Scan(systemPrompt).Cleaned)
	if table, ok := RenderTable(tables); ok {
		if system != "" {
			system += "\n\n"
		}
		system += table
	}

	outgoing := []Outgoing{}
	if system != "" {
		outgoing = append(outgoing, Outgoing{Role: RoleSystem, Content: system, Source: "system"})
	}
	for _, part := range walk(messages, summaries) {
		if part.message != nil {
			// 正文为空的不发出去：生成中的占位消息就在历史里躺着，而"空一条、再说一句"
			// 对上游是纯噪音；整句都是 `<state>` 块的消息同理（剔除后为空）。
			content := statelang.Scan(part.message.Content).Cleaned
			if strings.TrimSpace(content) == "" {
				continue
			}
			role := RoleAssistant
			if part.message.Role == model.RoleUser {
				role = RoleUser
			}
			id := part.message.ID
			outgoing = append(outgoing, Outgoing{
				Role: role, Content: content, Source: "message", MessageID: &id,
			})
			continue
		}
		// 摘要：不是谁说的话，是**程序摆给模型的前情** ⇒ 用 user 角色 + 明写的抬头，
		// 免得模型把它当成"用户刚说的一句"。
		text := strings.TrimSpace(part.summary.Text)
		if text == "" {
			continue
		}
		blocks := part.summary.Blocks
		if blocks < 1 {
			blocks = 1
		}
		id, count := part.summary.ID, blocks
		outgoing = append(outgoing, Outgoing{
			Role:      RoleUser,
			Content:   "【前情提要·" + strconv.FormatInt(blocks, 10) + " 块】\n" + text,
			Source:    "summary",
			SummaryID: &id,
			Blocks:    &count,
		})
	}
	return outgoing
}

type part struct {
	message *model.Message
	summary *model.Summary
}

// walk：装配时的行走算法 —— **能取粗的不取精**。
//
// 线性会话 + 摘要记区间（`begin_message_id` / `end_message_id`）⇒ 一次查表就跳一段，
// O(1)（不再"数过去"）。**也不需要"父的孩子一个不缺"那道闸**：区间就是覆盖范围本身 ——
// 父盖的必然是一段连着的消息（块不被劈开、压缩只吃连续的段），两端还在就说明这一段没被动过
// （删消息的级联会把"区间被碰过"的摘要连同父链一起作废，见 `store.DeletionPlan`）。
//
// 两次"宁可用细的也不许漏内容"的退让：指针悬空、区间缺失（老数据）或区间与眼前这条对不上
// ⇒ 这一条照原文发，往前一步。
func walk(messages []model.Message, summaries []model.Summary) []part {
	byID := make(map[string]*model.Summary, len(summaries))
	for index := range summaries {
		byID[summaries[index].ID] = &summaries[index]
	}
	// 区间两端是**消息 id** ⇒ 先换算成这条会话里的下标，之后每跳一次都是 O(1)
	indexOf := make(map[string]int, len(messages))
	for index := range messages {
		indexOf[messages[index].ID] = index
	}
	// span：这条摘要盖住的下标区间（闭区间）。两端缺一、或指不着这条会话里的消息 ⇒ false。
	span := func(summary *model.Summary) (int, int, bool) {
		if summary == nil || summary.BeginMessageID == nil || summary.EndMessageID == nil {
			return 0, 0, false
		}
		begin, beginOK := indexOf[*summary.BeginMessageID]
		end, endOK := indexOf[*summary.EndMessageID]
		if !beginOK || !endOK || end < begin {
			return 0, 0, false
		}
		return begin, end, true
	}

	parts := []part{}
	index := 0
	for index < len(messages) {
		message := &messages[index]

		summary := (*model.Summary)(nil)
		if message.SummaryID != nil {
			summary = byID[*message.SummaryID]
		}
		// 摘要**只在它区间的左端**被用上：区间不从这条起（指针悬空 / 区间缺失 / 数据对不上）
		// 就照原文发这一条，往前一步 —— 宁可用细的，也不许漏内容。
		begin, end, ok := span(summary)
		if !ok || begin != index {
			parts = append(parts, part{message: message})
			index++
			continue
		}

		// 能再粗一层吗：祖先与自己的**左端同起点**（金字塔左端对齐）⇒ 用最粗的那个。
		// 左端不同就不许升 —— 那说明父盖的前半截已经发过了（同一批孩子里的老二）。
		coarsest := summary
		for coarsest.ParentSummaryID != nil {
			parent := byID[*coarsest.ParentSummaryID]
			parentBegin, parentEnd, ok := span(parent)
			if !ok || parentBegin != begin || parentEnd < end {
				break
			}
			coarsest = parent
		}
		if _, coarsestEnd, ok := span(coarsest); ok {
			end = coarsestEnd
		}
		parts = append(parts, part{summary: coarsest})
		index = end + 1
	}
	return parts
}

// DefaultTokenizerRatio：`models.tokenizer` 里 ratio 的缺省值。
const DefaultTokenizerRatio = 1.3

// EstimateTokens：**唯一**的 token 估算处：`tokens = ceil(字符数 / ratio)`。
//
// 只用于排预算与显示占用 —— 绝不参与计费、也绝不参与任何正确性判断。别处一律调它。
func EstimateTokens(text string, ratio float64) int {
	if text == "" {
		return 0
	}
	if !(ratio > 0) || math.IsInf(ratio, 0) || ratio > 100 {
		ratio = DefaultTokenizerRatio
	}
	count := len([]rune(text))
	estimated := int(math.Ceil(float64(count) / ratio))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
