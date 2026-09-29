package state

import (
	"strings"
	"testing"

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
	if value, ok := has(fromAgent.Tables, "", "HP"); !ok || value != "10" {
		t.Fatalf("本会话删不掉底子里的键（值该漏回来）：%+v", fromAgent.Tables)
	}
	if value, ok := has(fromAgent.Tables, "", "地点"); !ok || value != "破庙" {
		t.Fatalf("消息里的赋值该生效：%+v", fromAgent.Tables)
	}
	// 全局那层只装底子；本会话那层装消息
	if len(fromAgent.Global) != 1 || fromAgent.Global[0].Key != "HP" || fromAgent.Global[0].Scope != ScopeGlobal {
		t.Fatalf("global = %+v", fromAgent.Global)
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
	if _, ok := has(fromSession.Tables, "", "HP"); ok {
		t.Fatalf("同一层里删除该真的删掉：%+v", fromSession.Tables)
	}
	if len(fromSession.Global) != 0 {
		t.Fatalf("会话自己的提示词不算全局：%+v", fromSession.Global)
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

// 注入渲染：按表分组；未命名表不写表头；标签永远不出现。
func TestRenderTableGroupsByTable(t *testing.T) {
	rendered, ok := RenderTable(Tables{
		"":     {"HP": "10"},
		"玩家状态": {"心情": "疲惫"},
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
		t.Fatalf("未命名表不该有表头，标签永远不许出现：\n%s", rendered)
	}
	if _, ok := RenderTable(Tables{}); ok {
		t.Fatal("空表不该渲染出东西")
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
	outgoing := BuildOutgoing(system, messages, nil, Tables{"": {"HP": "12", "季节": "初冬"}})
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

// span：一条"盖住某段"的摘要（线性会话里区间就是覆盖范围本身）。
func span(id string, parent *string, begin, end, text string, blocks int64) model.Summary {
	return model.Summary{
		ID: id, ParentSummaryID: parent,
		BeginMessageID: new(begin), EndMessageID: new(end),
		Text: text, Blocks: blocks,
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
