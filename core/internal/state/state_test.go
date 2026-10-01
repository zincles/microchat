package state

import (
	"encoding/json"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/model"
)

func message(id, content string) model.Message {
	return model.Message{ID: id, Role: model.RoleUser, Content: content}
}

func has(tables Tables, table, key string) (string, bool) {
	value, ok := tables[table][key]
	return value, ok
}

// 底子来自 agent 的提示词 ⇒ 算**全局**；来自会话自己的提示词 ⇒ 算**本会话**（§3 的来源规则）。
func TestPromptSourceDecidesTheLayer(t *testing.T) {
	messages := []model.Message{message("m1", "走了。\n<state>\n地点 = 破庙\ndelete(HP)\n</state>")}

	// agent 的提示词（全局）：删掉的 HP 会从底子漏回来（无墓碑，§38）
	fromAgent := FromSources("c1", "<state>\nHP = 10\n</state>", PromptFromAgent, messages)
	if value, ok := has(fromAgent.Tables, "global", "HP"); !ok || value != "10" {
		t.Fatalf("本会话删不掉底子里的键（值该漏回来）：%+v", fromAgent.Tables)
	}
	if value, ok := has(fromAgent.Tables, "global", "地点"); !ok || value != "破庙" {
		t.Fatalf("消息里的赋值该生效：%+v", fromAgent.Tables)
	}
	// 底子那层只装提示词里的块；本会话那层装消息
	if len(fromAgent.Baseline) != 1 || fromAgent.Baseline[0].Key != "HP" || fromAgent.Baseline[0].Scope != ScopeGlobal {
		t.Fatalf("baseline = %+v", fromAgent.Baseline)
	}
	if len(fromAgent.Session) != 2 || fromAgent.Session[1].Kind != "delete" {
		t.Fatalf("session = %+v", fromAgent.Session)
	}
	// 每条操作都追得回"哪句话带来的"
	if fromAgent.Session[1].MessageID == nil || *fromAgent.Session[1].MessageID != "m1" {
		t.Fatalf("操作该带上 message_id：%+v", fromAgent.Session[1])
	}
	if fromAgent.Session[0].Seq != 0 || fromAgent.Session[1].Seq != 1 {
		t.Fatalf("seq 该按顺序重排：%+v", fromAgent.Session)
	}

	// 会话自己的提示词（本会话）：底子与消息同一层 ⇒ 删得掉
	fromSession := FromSources("c1", "<state>\nHP = 10\n</state>", PromptFromSession, messages)
	if _, ok := has(fromSession.Tables, "global", "HP"); ok {
		t.Fatalf("同一层里删除该真的删掉：%+v", fromSession.Tables)
	}
	if len(fromSession.Baseline) != 0 {
		t.Fatalf("会话自己的提示词不算底子（它算本会话那层）：%+v", fromSession.Baseline)
	}
}

// 不写表名与 `<state global>` 混在一条会话里 ⇒ **同一张表**，后写覆盖先写。
//
// （这是"未标表名默认视作 global"在现演那一层的落点；解析层的同一条在 `statelang` 里钉着。）
func TestUnnamedAndGlobalTableAreTheSame(t *testing.T) {
	view := FromSources("c1", "<state>HP = 10</state>", PromptFromSession, []model.Message{
		message("m1", "<state>HP = 12</state>"),
		message("m2", "<state global>HP = 15</state>"),
		message("m3", "<state>弹药 = 3</state>"),
	})
	if value, ok := has(view.Tables, "global", "HP"); !ok || value != "15" {
		t.Fatalf("两种写法该落到同一张表、后写覆盖先写：%+v", view.Tables)
	}
	if value, ok := has(view.Tables, "global", "弹药"); !ok || value != "3" {
		t.Fatalf("不写表名的键也在 global 里：%+v", view.Tables)
	}
	if len(view.Tables) != 1 {
		t.Fatalf("只该有 global 这一张表：%+v", view.Tables)
	}
}

// `global` 恒在：**一个变量都没有也回 `{"global": {}}`**；算完为空的**命名**表照旧消失。
func TestGlobalTableIsAlwaysPresent(t *testing.T) {
	empty := FromSources("c1", "", PromptFromAgent, []model.Message{message("m1", "走吧。")})
	if table, ok := empty.Tables["global"]; !ok || table == nil || len(table) != 0 {
		t.Fatalf("空状态该回 {\"global\": {}}：%+v", empty.Tables)
	}
	if empty.Effective == nil || len(empty.Effective) != 0 {
		t.Fatalf("`effective` 永不给 nil（它是默认表那份）：%+v", empty.Effective)
	}
	// 渲染那一层**不**因为"global 恒在"就多写一句空的"当前变量:"（出站内容的语义不许变）
	if rendered, ok := RenderTable(empty.Tables); ok || rendered != "" {
		t.Fatalf("空的那张 global 不该渲染出东西：%q", rendered)
	}

	// 命名表算完为空 ⇒ 整张消失（例外只给 global）
	named := FromSources("c1", "", PromptFromSession, []model.Message{
		message("m1", "<state 临时>X = 1</state>"),
		message("m2", "<state 临时>X = </state>"),
	})
	if _, ok := named.Tables["临时"]; ok {
		t.Fatalf("算完为空的命名表该消失：%+v", named.Tables)
	}
	if _, ok := named.Tables["global"]; !ok {
		t.Fatalf("global 该还在：%+v", named.Tables)
	}
}

// `at_idx` 那条链（`StateAt`）：**底子永远是当前的提示词**，正文只 fold 到第 N 条（含）。
//
// 三档：`at=2` 只看得到前两条带来的东西、`at=6`（= 全部）与不给参数一回事、`at=0` ⇒ 只有底子。
func TestStateAtFoldsOnlyUpToTheIndex(t *testing.T) {
	system := "<state 底子>种子 = 1</state>"
	messages := []model.Message{
		message("m1", "<state>第一 = 有</state>"),
		message("m2", "<state>第二 = 有</state>"),
		message("m3", "<state>第三 = 有</state>"),
		message("m4", "<state>第四 = 有</state>"),
		message("m5", "<state>第五 = 有</state>"),
		message("m6", "<state>第六 = 有</state>"),
	}
	at := func(index int) View { return StateAt("c1", system, PromptFromAgent, messages, index) }

	two := at(2)
	if two.Tables["global"]["第二"] != "有" {
		t.Fatalf("第 2 条带来的变量在 at=2 该看得见：%+v", two.Tables)
	}
	if _, ok := two.Tables["global"]["第三"]; ok {
		t.Fatalf("第 3 条带来的变量在 at=2 不该看得见：%+v", two.Tables)
	}
	if len(two.Session) != 2 {
		t.Fatalf("只该 fold 前两条的操作：%+v", two.Session)
	}
	// 底子与 at 无关：它是**当前**的生效提示词（不是历史快照）
	if len(two.Baseline) != 1 || len(at(0).Baseline) != 1 {
		t.Fatalf("底子该一直都在：%+v / %+v", two.Baseline, at(0).Baseline)
	}

	// at=0 ⇒ 只有底子
	zero := at(0)
	if len(zero.Session) != 0 || len(zero.Tables["global"]) != 0 {
		t.Fatalf("at=0 该只有底子：%+v", zero)
	}
	if zero.Tables["底子"]["种子"] != "1" {
		t.Fatalf("底子该算进来：%+v", zero.Tables)
	}

	// at=6 与"不给参数"（全量）一致；越界也当作"到最后一条"
	all := FromSources("c1", system, PromptFromAgent, messages)
	for _, view := range []View{at(6), at(99)} {
		if len(view.Session) != len(all.Session) || len(view.Tables["global"]) != len(all.Tables["global"]) {
			t.Fatalf("at=6/越界该等于全量：%+v vs %+v", view.Session, all.Session)
		}
	}

	// 底子**用当前的提示词**：换个提示词，同一个 at 的答案立刻跟着变（⇒ 它不是"那时候的快照"）
	changed := StateAt("c1", "<state 底子>种子 = 2</state>", PromptFromAgent, messages, 2)
	if changed.Tables["底子"]["种子"] != "2" {
		t.Fatalf("底子看的是**当前**的提示词：%+v", changed.Tables)
	}
}

// 空值 = 清掉；删除只作用于自己那张表。
func TestEmptyValueAndTableNamespacing(t *testing.T) {
	view := FromSources("c1", "", PromptFromSession, []model.Message{
		message("m1", "<state 玩家状态>想法 = ;HP = 10</state>"),
		message("m2", "<state 世界情况>情况 = 待处理</state>"),
		message("m3", "<state 玩家状态>HP = </state>"),
	})
	if _, ok := has(view.Tables, "玩家状态", "想法"); ok {
		t.Fatalf("空值该把键清掉：%+v", view.Tables)
	}
	if _, ok := has(view.Tables, "玩家状态", "HP"); ok {
		t.Fatalf("后一条消息的空值该清掉它：%+v", view.Tables)
	}
	if value, ok := has(view.Tables, "世界情况", "情况"); !ok || value != "待处理" {
		t.Fatalf("别的表不该受影响：%+v", view.Tables)
	}
}

// 注入渲染：按表分组；`global` 那张不写表头；标签永远不出现；空 ⇒ false（含"global 恒在"的情形）。
func TestRenderTableGroupsByTable(t *testing.T) {
	rendered, ok := RenderTable(Tables{
		"global": {"HP": "10"},
		"玩家状态":   {"心情": "疲惫"},
	})
	if !ok {
		t.Fatal("非空表该渲染出东西")
	}
	for _, want := range []string{"当前变量:", "\n  HP = 10", "\n  〔玩家状态〕", "\n    心情 = 疲惫"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("渲染缺 %q：\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "〔〕") || strings.Contains(rendered, "<state>") {
		t.Fatalf("默认表不该有表头，标签永远不许出现：\n%s", rendered)
	}
	if _, ok := RenderTable(Tables{}); ok {
		t.Fatal("空表不该渲染出东西")
	}
	// `global` 恒在 ⇒ 只有一张空 global 时也必须"什么都不渲染"（否则提示词会多一句空的"当前变量:"）
	if rendered, ok := RenderTable(Tables{"global": {}}); ok || rendered != "" {
		t.Fatalf("空 global 不该渲染出东西：%q", rendered)
	}
	// 别的表空、global 有值 ⇒ 只渲染 global 那份
	rendered, ok = RenderTable(Tables{"global": {"HP": "10"}, "临时": {}})
	if !ok || strings.Contains(rendered, "临时") {
		t.Fatalf("空表照旧跳过：%q", rendered)
	}
}

// `StateView` 的层名是 `baseline`（**不是** `global`）—— 2026-09-30 的改名，靠这条钉住。
//
// 为什么必须改：`global` 现在是**表名**（不写表名的块落到它）⇒ 同一份 JSON 里不许两义。
// 这条测试红了，多半是有人把层名写回了 `global`（那会和 `tables` 里那张表撞车）。
func TestStateViewLayerNamesAreStable(t *testing.T) {
	view := FromSources("c1", "<state>HP = 1</state>", PromptFromSession, []model.Message{message("m1", "<state>HP = 2</state>")})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"baseline", "session", "baseline_values", "effective", "tables"}
	if len(fields) != len(want) {
		t.Fatalf("StateView 的字段该正好这几个：%s", raw)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("缺字段 %q：%s", name, raw)
		}
	}
	if _, ok := fields["global"]; ok {
		t.Fatalf("`global` 只许是**表名**（住在 tables 里），不许再当层名：%s", raw)
	}
	if _, ok := fields["global_values"]; ok {
		t.Fatalf("`global_values` 已改名成 `baseline_values`：%s", raw)
	}
	// 顺带钉住：`tables` 里那张表就叫 `global`
	if _, ok := view.Tables["global"]; !ok {
		t.Fatalf("默认表该叫 global：%+v", view.Tables)
	}
}

// 唯一的出站拼装：标签剔除、当前表注入、空的正文不发、没有提示词就不发 system。
func TestBuildOutgoing(t *testing.T) {
	system := "你是主持人。\n<state>\n季节 = 初冬\n</state>"
	messages := []model.Message{
		{ID: "m1", Role: model.RoleUser, Content: "开场。<state>HP = 10</state>"},
		{ID: "m2", Role: model.RoleAssistant, Content: "<state>HP = 12</state>"}, // 整句都是块 ⇒ 剔除后为空
		{ID: "m3", Role: model.RoleAssistant, Content: "你把火把点着了。"},
	}
	outgoing := BuildOutgoing(system, messages, nil, Tables{"global": {"HP": "12", "季节": "初冬"}})
	if len(outgoing) != 3 {
		t.Fatalf("出站 = %+v", outgoing)
	}
	if outgoing[0].Role != RoleSystem || !strings.Contains(outgoing[0].Content, "你是主持人。") {
		t.Fatalf("system = %+v", outgoing[0])
	}
	if strings.Contains(outgoing[0].Content, "<state>") {
		t.Fatalf("标签泄漏进上下文：%s", outgoing[0].Content)
	}
	if !strings.Contains(outgoing[0].Content, "HP = 12") || !strings.Contains(outgoing[0].Content, "季节 = 初冬") {
		t.Fatalf("该注入当前状态表：%s", outgoing[0].Content)
	}
	if outgoing[1].Content != "开场。" || outgoing[2].Content != "你把火把点着了。" {
		t.Fatalf("正文该只留散文：%+v", outgoing[1:])
	}
	// 没有系统提示词、也没有变量 ⇒ 不发明 system
	if got := BuildOutgoing("", []model.Message{{ID: "m1", Role: model.RoleUser, Content: "在吗"}}, nil, Tables{}); len(got) != 1 || got[0].Role != RoleUser {
		t.Fatalf("空提示词不该发 system：%+v", got)
	}
}

// 出站每一项的序号：`system` ⇒ 0（合成项）、`message` ⇒ 自己那条、`summary` ⇒ 它替代的**范围**。
//
// 序号**只有一处算**（`store.IndexMessages`：按 id 排序后的位置）⇒ 这里只是把编好的号带出来，不重算；
// 手搭出来、没编过号的消息**不带**这一格（0 是留给系统提示词的，不能拿它冒充"第 0 条消息"）。
func TestBuildOutgoingCarriesIndexes(t *testing.T) {
	messages := []model.Message{
		{ID: "m1", Idx: 1, Role: model.RoleUser, Content: "一", SummaryID: new("s1")},
		{ID: "m2", Idx: 2, Role: model.RoleAssistant, Content: "二", SummaryID: new("s1")},
		{ID: "m3", Idx: 3, Role: model.RoleUser, Content: "三"},
	}
	summaries := []model.Summary{span("s1", nil, "m1", "m2", "前情", 1)}
	outgoing := BuildOutgoing("你是主持人", messages, summaries, Tables{})
	if len(outgoing) != 3 {
		t.Fatalf("出站 = %+v", outgoing)
	}
	if outgoing[0].Idx == nil || *outgoing[0].Idx != 0 {
		t.Fatalf("system 该是 idx 0（合成的，不是消息）：%+v", outgoing[0])
	}
	item := outgoing[1]
	if item.Type != "summary" || item.FromIdx == nil || item.ToIdx == nil || *item.FromIdx != 1 || *item.ToIdx != 2 {
		t.Fatalf("摘要该报它替代的范围（1..2）：%+v", item)
	}
	if item.Idx != nil {
		t.Fatalf("摘要不该有自己的 idx：%+v", item)
	}
	if item.Role != RoleAssistant {
		t.Fatalf("摘要是 AI 生成的前情 ⇒ 该标 assistant（保住交替）：%+v", item)
	}
	last := outgoing[2]
	if last.Type != "message" || last.Idx == nil || *last.Idx != 3 || last.MessageID == nil || *last.MessageID != "m3" {
		t.Fatalf("消息项该带它自己的序号：%+v", last)
	}

	// 手搭的（没编过号的）消息 ⇒ 不带这一格
	raw := BuildOutgoing("", []model.Message{{ID: "x", Role: model.RoleUser, Content: "在吗"}}, nil, Tables{})
	if len(raw) != 1 || raw[0].Idx != nil {
		t.Fatalf("没编过号的消息不该硬编一个：%+v", raw)
	}
}

// span：一条"盖住某段"的摘要（线性会话里区间就是覆盖范围本身）。
func span(id string, parent *string, begin, end, text string, blocks int64) model.Summary {
	return model.Summary{
		ID: id, ParentSummaryID: parent,
		BeginMessageID: new(begin), EndMessageID: new(end),
		Text: text, Blocks: blocks,
	}
}

// ExpandChildren：一父两子（消息+摘要）+ 叶子 + pending 过。
func TestExpandChildren(t *testing.T) {
	messages := []model.Message{
		{ID: "m1", Idx: 1, Role: model.RoleUser, Content: "一"},
		{ID: "m2", Idx: 2, Role: model.RoleAssistant, Content: "二"},
		{ID: "m3", Idx: 3, Role: model.RoleUser, Content: "三"},
	}
	child := model.Summary{ID: "c1", BeginMessageID: new("m1"), EndMessageID: new("m2"), Text: "子", Blocks: 1, SourceIDs: []string{"m1", "m2"}}
	parent := model.Summary{ID: "p1", BeginMessageID: new("m1"), EndMessageID: new("m3"), Text: "父", Blocks: 2, SourceIDs: []string{"c1", "m3"}}
	leaf := model.Summary{ID: "leaf", BeginMessageID: new("m3"), EndMessageID: new("m3"), Text: "叶", Blocks: 1}
	summaries := []model.Summary{child, parent, leaf}
	pendingID := "pending-x"
	next := 4
	items := []Outgoing{
		{Role: RoleAssistant, Content: "父正文", Type: "summary", SummaryID: new("p1")},
		{Role: RoleAssistant, Content: "叶正文", Type: "summary", SummaryID: new("leaf")},
		{Role: RoleUser, Content: "待发", Type: "message", MessageID: &pendingID, Idx: &next, Pending: true},
	}
	out := ExpandChildren(items, messages, summaries)
	if len(out) != 3 {
		t.Fatalf("条数该不变：%+v", out)
	}
	kids := out[0].Children
	if len(kids) != 2 {
		t.Fatalf("一父两子：%+v", out[0])
	}
	if kids[0].Type != "summary" || kids[0].SummaryID == nil || *kids[0].SummaryID != "c1" {
		t.Fatalf("第一个孩子该是摘要 c1：%+v", kids[0])
	}
	if kids[0].FromIdx == nil || *kids[0].FromIdx != 1 || kids[0].ToIdx == nil || *kids[0].ToIdx != 2 {
		t.Fatalf("摘要孩子该带范围 1..2：%+v", kids[0])
	}
	if len(kids[0].Children) != 0 {
		t.Fatalf("只展一层，孙辈不展：%+v", kids[0])
	}
	if kids[0].Content != "" {
		t.Fatalf("孩子只给 id+idx，不给正文：%+v", kids[0])
	}
	if kids[1].Type != "message" || kids[1].MessageID == nil || *kids[1].MessageID != "m3" || kids[1].Idx == nil || *kids[1].Idx != 3 {
		t.Fatalf("第二个孩子该是消息 m3：%+v", kids[1])
	}
	if kids[1].Content != "" {
		t.Fatalf("消息孩子不给正文：%+v", kids[1])
	}
	if out[1].Children == nil || len(out[1].Children) != 0 {
		t.Fatalf("叶子该是空数组：%+v", out[1])
	}
	if len(out[2].Children) != 0 || out[2].Type != "message" || !out[2].Pending {
		t.Fatalf("pending 项原样过：%+v", out[2])
	}
}

// 覆盖：一条被摘要盖住的消息。
func covered(id, content, summaryID string) model.Message {
	return model.Message{ID: id, Role: model.RoleUser, Content: content, SummaryID: new(summaryID)}
}

// 行走算法（**按区间跳**）：两层金字塔里孩子与父左端对齐 ⇒ 一次用最粗的那份、跳过整段；
// 段外的消息照原文发。
func TestWalkJumpsBySummarySpan(t *testing.T) {
	left, right, top := "s1", "s2", "p1"
	messages := []model.Message{
		covered("m1", "一", left),
		covered("m2", "二", left),
		covered("m3", "三", right),
		covered("m4", "四", right),
		message("m5", "五"),
	}
	summaries := []model.Summary{
		span(left, new(top), "m1", "m2", "细的 A", 1),
		span(right, new(top), "m3", "m4", "细的 B", 1),
		span(top, nil, "m1", "m4", "粗的", 2),
	}
	parts := walk(messages, summaries)
	if len(parts) != 2 || parts[0].summary == nil || parts[0].summary.ID != top {
		t.Fatalf("能取粗的就该用父（一次跳 4 条）：%+v", parts)
	}
	if parts[1].message == nil || parts[1].message.ID != "m5" {
		t.Fatalf("没被覆盖的那条该发原文：%+v", parts[1])
	}

	// 父的区间**盖不住自己的孩子**（这两种数据不该出现）：不许升，老实用细的 —— 少发内容才是真错。
	partial := []model.Summary{
		span(left, new(top), "m1", "m2", "细的 A", 1),
		span(right, new(top), "m3", "m4", "细的 B", 1),
		span(top, nil, "m1", "m1", "半截的粗的", 2),
	}
	parts = walk(messages, partial)
	if len(parts) != 3 || parts[0].summary == nil || parts[0].summary.ID != left {
		t.Fatalf("父盖不全时该用细的：%+v", parts)
	}
	if parts[1].summary == nil || parts[1].summary.ID != right {
		t.Fatalf("第二段还是它自己的细摘要：%+v", parts[1])
	}
	if parts[2].message == nil || parts[2].message.ID != "m5" {
		t.Fatalf("段外照原文：%+v", parts[2])
	}
}

// 摘要在它区间的**左端**才用得上：指针悬空 / 区间缺失（老数据）/ 区间不从这条起
// ⇒ 这条照原文发（宁可细，不许漏）。
func TestWalkFallsBackWhenSpanDoesNotFit(t *testing.T) {
	messages := []model.Message{
		covered("m1", "一", "missing"),       // 指针悬空
		covered("m2", "二", "no-span"),       // 区间缺失
		covered("m3", "三", "starts-before"), // 区间从 m1 起（对不上这条）
		message("m4", "四"),
	}
	summaries := []model.Summary{
		{ID: "no-span", Text: "老数据", Blocks: 1},
		span("starts-before", nil, "m1", "m2", "别处的", 1),
	}
	parts := walk(messages, summaries)
	if len(parts) != 4 {
		t.Fatalf("认不出区间的那些该逐条发原文：%+v", parts)
	}
	for index, want := range []string{"m1", "m2", "m3", "m4"} {
		if parts[index].message == nil || parts[index].message.ID != want {
			t.Fatalf("第 %d 条该是原文 %s：%+v", index, want, parts[index])
		}
	}
}

// 半路闯进一段区间里**不许升到父**：父盖的前半截已经发出去了（同一批孩子里的老二）。
func TestWalkDoesNotPromoteMidway(t *testing.T) {
	left, right, top, solo := "s1", "s2", "p1", "s9"
	messages := []model.Message{
		covered("m1", "一", solo), // 这一段不属于父摘要
		covered("m2", "二", left),
		covered("m3", "三", right),
		covered("m4", "四", right),
	}
	summaries := []model.Summary{
		span(solo, nil, "m1", "m1", "单独一段", 1),
		span(left, new(top), "m1", "m2", "细的 A", 1),
		span(right, new(top), "m3", "m4", "细的 B", 1),
		span(top, nil, "m1", "m4", "粗的", 2),
	}
	parts := walk(messages, summaries)
	if len(parts) != 3 || parts[0].summary == nil || parts[0].summary.ID != solo {
		t.Fatalf("该先用自己那一段：%+v", parts)
	}
	if parts[1].message == nil || parts[1].message.ID != "m2" {
		t.Fatalf("区间对不上的那条该发原文：%+v", parts[1])
	}
	if parts[2].summary == nil || parts[2].summary.ID != right {
		t.Fatalf("左端不同就不许升到父：%+v", parts[2])
	}
}

// 完全没有摘要（或一条都没有）：逐条发原文 —— 装配得像没压缩一样。
func TestWalkWithoutSummaries(t *testing.T) {
	messages := []model.Message{message("m1", "一"), message("m2", "二")}
	parts := walk(messages, nil)
	if len(parts) != 2 || parts[0].message == nil || parts[1].message == nil {
		t.Fatalf("没有摘要就该逐条发：%+v", parts)
	}
}

// token 估算是**唯一**一处：ceil(字符数 / ratio)，ratio 乱给就用缺省值。
func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("", 1.3); got != 0 {
		t.Fatalf("空串 = %d", got)
	}
	if got := EstimateTokens("abcd", 1.0); got != 4 {
		t.Fatalf("4 字 / 1.0 = %d", got)
	}
	if got := EstimateTokens("中文三个字", 1.3); got != 4 { // ceil(5 / 1.3) = 4
		t.Fatalf("ceil(5/1.3) = %d", got)
	}
	if got := EstimateTokens("x", 0); got != 1 {
		t.Fatalf("ratio 乱给该用缺省值：%d", got)
	}
}

// 生效提示词的**唯一一处**解析：会话覆盖 → agent 的 → 内置默认；落空就往上弹，**永不返回空串**。
//
// 悬空 agent（软引用找不到）与"只写了空白"都当"没这一级"—— 底子曾因此发成空串。
func TestResolveSystemPromptNeverEmpty(t *testing.T) {
	agents := config.AgentsConfig{Agents: []config.Agent{
		{ID: "跑团", Name: "跑团主持人", SystemPrompt: "你是跑团主持人。"},
		{ID: "沉默", Name: "沉默", SystemPrompt: "   \n  "},
	}}
	builtin := config.BuiltinDefaultAgent().SystemPrompt

	cases := []struct {
		name       string
		session    model.Session
		wantText   string
		wantSource PromptSource
	}{
		{"会话覆盖优先", model.Session{SystemPrompt: "你是临时改的。", AgentID: "跑团"}, "你是临时改的。", PromptFromSession},
		{"回落到 agent", model.Session{AgentID: "跑团"}, "你是跑团主持人。", PromptFromAgent},
		{"agent 悬空 ⇒ 内置默认", model.Session{AgentID: "早就删掉的 agent"}, builtin, PromptFromBuiltin},
		{"agent 只写了空白 ⇒ 内置默认", model.Session{AgentID: "沉默"}, builtin, PromptFromBuiltin},
		{"会话只写了空白 ⇒ 不算写，回落 agent", model.Session{SystemPrompt: "  \n ", AgentID: "跑团"}, "你是跑团主持人。", PromptFromAgent},
		{"内置 default（文件里没有这条）⇒ builtin", model.Session{AgentID: "default"}, builtin, PromptFromBuiltin},
		{"什么都没有 ⇒ builtin", model.Session{}, builtin, PromptFromBuiltin},
	}
	for _, c := range cases {
		text, source := ResolveSystemPrompt(c.session, agents)
		if text != c.wantText || source != c.wantSource {
			t.Fatalf("%s：得到 (%q, %q)，要 (%q, %q)", c.name, text, source, c.wantText, c.wantSource)
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("%s：生效提示词**永不给空**", c.name)
		}
	}
}
