package state

import (
	"strings"
	"testing"

	"microchat/internal/model"
)

func message(id, content string, parent *string) model.Message {
	return model.Message{ID: id, Role: model.RoleUser, Content: content, ParentID: parent}
}

func has(tables Tables, table, key string) (string, bool) {
	value, ok := tables[table][key]
	return value, ok
}

// 底子来自 agent 的提示词 ⇒ 算**全局**；来自会话自己的提示词 ⇒ 算**本会话**（§3 的来源规则）。
func TestPromptSourceDecidesTheLayer(t *testing.T) {
	messages := []model.Message{message("m1", "走了。\n<state>\n地点 = 破庙\ndelete(HP)\n</state>", nil)}

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
		message("m1", "<state 玩家状态>想法 = ;HP = 10</state>", nil),
		message("m2", "<state 世界情况>情况 = 待处理</state>", nil),
		message("m3", "<state 玩家状态>HP = </state>", nil),
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

// 行走算法：父摘要盖全 ⇒ 用父（能取粗的不取精）；盖不全 ⇒ 退回细的，不许漏内容。
func TestWalkPromotesOnlyWhenComplete(t *testing.T) {
	parentID, childA, childB := "p1", "s1", "s2"
	messages := []model.Message{
		{ID: "m1", Role: model.RoleUser, Content: "一", SummaryID: &childA},
		{ID: "m2", Role: model.RoleUser, Content: "二", SummaryID: &childA},
		{ID: "m3", Role: model.RoleUser, Content: "三", SummaryID: &childB},
		{ID: "m4", Role: model.RoleUser, Content: "四"},
	}
	full := []model.Summary{
		{ID: childA, ParentSummaryID: &parentID, Text: "细的 A", Blocks: 1},
		{ID: childB, ParentSummaryID: &parentID, Text: "细的 B", Blocks: 1},
		{ID: parentID, Text: "粗的", Blocks: 2},
	}
	parts := walk(messages, full)
	if len(parts) != 2 || parts[0].summary == nil || parts[0].summary.ID != parentID {
		t.Fatalf("父摘要盖全时该用父：%+v", parts)
	}
	if parts[1].message == nil || parts[1].message.ID != "m4" {
		t.Fatalf("没被覆盖的那条该发原文：%+v", parts[1])
	}

	// 真"盖不全"：这一串中间**隔着一条不属于父摘要孩子的**（run 到 2 就断，而眼前共有 3 条它的孩子）
	// ⇒ 必须退回细的 —— 拿只盖了一半的父摘要去顶，就会少发东西。
	other := "p9"
	gapped := []model.Message{
		messages[0], messages[1],
		{ID: "m9", Role: model.RoleUser, Content: "九", SummaryID: &other},
		messages[1],
	}
	summaries := []model.Summary{
		{ID: childA, ParentSummaryID: &parentID, Text: "细的 A", Blocks: 1},
		{ID: other, Text: "别的", Blocks: 1},
		{ID: parentID, Text: "粗的", Blocks: 2},
	}
	parts = walk(gapped, summaries)
	if len(parts) != 3 || parts[0].summary == nil || parts[0].summary.ID != childA {
		t.Fatalf("父摘要盖不全时该退回细的：%+v", parts)
	}
	if parts[2].summary == nil || parts[2].summary.ID != childA {
		t.Fatalf("断开之后再来的那串还是细摘要：%+v", parts[2])
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
