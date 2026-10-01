package store

import (
	"encoding/json"
	"errors"
	"testing"

	"microchat/internal/model"
)

// 压缩的落库口（`RecordSummary`）：**一个事务里**插摘要 + 把这一段的指针指过去。
//
// 三条要钉住的（都是"不许静默错"的那类）：
//  1. 正文一个字都不动（压缩只许改 `summary_id`）；
//  2. 区间里已经有人被盖过 ⇒ **报错**（不许覆盖已压缩的区间）；
//  3. 区间里有不存在的 id ⇒ 报错（那段被删过 ⇒ 区间不再成立），且**什么都不留**。
func TestRecordSummaryPointsTheSpanAndRefusesOverlap(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "s1", "m1", "m2", "m3", "m4")
	messages, err := st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	before := make([]string, len(messages))
	for index := range messages {
		before[index] = messages[index].Content
	}

	summary := model.Summary{
		ID: "sum1", SessionID: "s1", Type: model.TypeMessages,
		BeginMessageID: new("m1"), EndMessageID: new("m2"),
		Text: "前情提要", Blocks: 1, Tokens: 5, SourceIDs: []string{"m1", "m2"},
		Provider: "dummy", Model: "dummy", PromptVersion: 42, CreatedAt: 10,
	}
	if err := st.RecordSummary(summary, []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListSummaries("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "sum1" || rows[0].PromptVersion != 42 {
		t.Fatalf("摘要那一行 = %+v", rows)
	}
	messages, err = st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	for index, message := range messages {
		want := (index < 2)
		if (message.SummaryID != nil) != want {
			t.Fatalf("第 %d 条的指针 = %v，想要被盖 = %v", index, message.SummaryID, want)
		}
		if message.Content != before[index] {
			t.Fatalf("正文被动了：%q → %q", before[index], message.Content)
		}
	}

	// ② 同一段再来一次 ⇒ 报错，且**不留第二行**
	again := summary
	again.ID, again.Text = "sum2", "重复压"
	if err := st.RecordSummary(again, []string{"m1", "m2"}); err == nil {
		t.Fatal("覆盖已压缩的区间该报错")
	} else {
		var invalid InvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("该是 InvalidError（HTTP 400）：%v", err)
		}
	}
	if rows, _ := st.ListSummaries("s1"); len(rows) != 1 {
		t.Fatalf("失败不许留下东西：%+v", rows)
	}

	// ③ 区间里有不存在的 id ⇒ 报错，且一行都不插
	third := summary
	third.ID, third.Text = "sum3", "外来的"
	if err := st.RecordSummary(third, []string{"m3", "查无此条"}); err == nil {
		t.Fatal("区间不成立该报错")
	}
	if rows, _ := st.ListSummaries("s1"); len(rows) != 1 {
		t.Fatalf("失败不许留下东西：%+v", rows)
	}
	// 空区间同理（压缩要盖住至少一条消息）
	if err := st.RecordSummary(third, nil); err == nil {
		t.Fatal("空区间该报错")
	}
}

// 合并级落库（`source_kind = summary`）：孩子挂到父上、父自己无父，**消息指针一条都不动**。
func TestRecordSummaryMergesChildSummaries(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "s1", "m1", "m2", "m3", "m4")
	messageSummary := func(id, begin, end, text string) model.Summary {
		return model.Summary{
			ID: id, SessionID: "s1", Type: model.TypeMessages,
			BeginMessageID: new(begin), EndMessageID: new(end),
			Text: text, Blocks: 1, Tokens: 3, SourceIDs: []string{begin, end},
			Provider: "dummy", Model: "dummy", PromptVersion: 1, CreatedAt: 1,
		}
	}
	if err := st.RecordSummary(messageSummary("c1", "m1", "m2", "头一段"), []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSummary(messageSummary("c2", "m3", "m4", "后一段"), []string{"m3", "m4"}); err != nil {
		t.Fatal(err)
	}

	parent := model.Summary{
		ID: "p1", SessionID: "s1", Type: model.TypeSummaries,
		BeginMessageID: new("m1"), EndMessageID: new("m4"),
		Text: "并起来", Blocks: 2, Tokens: 6, SourceIDs: []string{"c1", "c2"},
		Provider: "dummy", Model: "dummy", PromptVersion: 1, CreatedAt: 2,
	}
	if err := st.RecordSummary(parent, []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}

	byID := summariesByID(t, st, "s1")
	if got := byID["p1"]; got.Type != model.TypeSummaries || got.ParentSummaryID != nil {
		t.Fatalf("父那一行该是顶层 summary：%+v", got)
	}
	for _, child := range []string{"c1", "c2"} {
		if got := byID[child]; got.ParentSummaryID == nil || *got.ParentSummaryID != "p1" {
			t.Fatalf("孩子 %s 该挂在 p1 上：%+v", child, got.ParentSummaryID)
		}
	}
	// **合并绝不改 messages**：指针保持指孩子（升格是装配侧的事）
	messages, err := st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].SummaryID == nil || *messages[0].SummaryID != "c1" ||
		messages[2].SummaryID == nil || *messages[2].SummaryID != "c2" {
		t.Fatal("合并级不许动 messages.summary_id")
	}
}

// 合并级的三条闸：孩子被抢 / 指不着 / 跨会话 ⇒ 报错且**整笔回滚**（不留父行、不动孩子）。
func TestRecordSummaryMergeRefusesPoachedOrStrayChildren(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "s1", "m1", "m2", "m3", "m4")
	seedSession(t, st, "s2", "m5", "m6")
	messageSummary := func(sessionID, id, begin, end string) model.Summary {
		return model.Summary{
			ID: id, SessionID: sessionID, Type: model.TypeMessages,
			BeginMessageID: new(begin), EndMessageID: new(end),
			Text: id, Blocks: 1, SourceIDs: []string{begin, end}, CreatedAt: 1,
		}
	}
	if err := st.RecordSummary(messageSummary("s1", "c1", "m1", "m2"), []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSummary(messageSummary("s1", "c2", "m3", "m4"), []string{"m3", "m4"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSummary(messageSummary("s2", "c3", "m5", "m6"), []string{"m5", "m6"}); err != nil {
		t.Fatal(err)
	}
	parent := func(id string, children ...string) model.Summary {
		return model.Summary{
			ID: id, SessionID: "s1", Type: model.TypeSummaries,
			BeginMessageID: new("m1"), EndMessageID: new("m4"),
			Text: id, Blocks: 2, SourceIDs: children, CreatedAt: 2,
		}
	}

	// 半路被抢：c1 先挂上了一个父（先并一次）⇒ 再并 [c1, c2] 该整笔回滚
	if err := st.RecordSummary(parent("pA", "c1"), []string{"c1"}); err != nil {
		t.Fatal(err)
	}
	assertMergeRefused(t, st, "s1", parent("pB", "c1", "c2"), []string{"c1", "c2"}, "pB")

	// 指不着：有一个 id 不在这条会话里 ⇒ 同样报错、不留父行
	assertMergeRefused(t, st, "s1", parent("pC", "c2", "查无此条"), []string{"c2", "查无此条"}, "pC")

	// 跨会话：c3 属于 s2 ⇒ 在 s1 里并 [c2, c3] 该报错
	assertMergeRefused(t, st, "s1", parent("pD", "c2", "c3"), []string{"c2", "c3"}, "pD")

	// 三次失败之后：先到的父还在、孩子们一个都没被多挂
	byID := summariesByID(t, st, "s1")
	if got := byID["c1"]; got.ParentSummaryID == nil || *got.ParentSummaryID != "pA" {
		t.Fatalf("c1 该仍挂在先到的 pA 上：%+v", got.ParentSummaryID)
	}
	if byID["c2"].ParentSummaryID != nil {
		t.Fatal("c2 该仍无父（那几次都没成）")
	}
	if got := summariesByID(t, st, "s2")["c3"]; got.ParentSummaryID != nil {
		t.Fatal("跨会话那次不许给 c3 挂爹")
	}
	for _, id := range []string{"pB", "pC", "pD"} {
		if _, ok := byID[id]; ok {
			t.Fatalf("失败不许留下父行 %s", id)
		}
	}
}

// assertMergeRefused：这次合并该报 InvalidError，且**父行不存在**（整笔回滚）。
func assertMergeRefused(t *testing.T, st *Store, sessionID string, parent model.Summary, children []string, parentID string) {
	t.Helper()
	err := st.RecordSummary(parent, children)
	var invalid InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("%s：该报 InvalidError：%v", parentID, err)
	}
	if _, ok := summariesByID(t, st, sessionID)[parentID]; ok {
		t.Fatalf("%s：失败不许留下父行", parentID)
	}
}

// summariesByID：这条会话的摘要按 id 索引（断言用）。
func summariesByID(t *testing.T, st *Store, sessionID string) map[string]model.Summary {
	t.Helper()
	rows, err := st.ListSummaries(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]model.Summary, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID
}

// 摘要重摇的 apply（`ReplaceSummaryText`）：**就地**换正文与随行数据，
// 地址 / 区间 / 成员 / 父指针 / 创建时间一概不动，也不碰 messages。
func TestReplaceSummaryTextSwapsTextAndKeepsTheFrame(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "s1", "m1", "m2", "m3", "m4")
	original := model.Summary{
		ID: "sum1", SessionID: "s1", Type: model.TypeMessages,
		BeginMessageID: new("m1"), EndMessageID: new("m2"),
		Text: "旧正文", Blocks: 1, Tokens: 5, SourceIDs: []string{"m1", "m2"},
		Provider: "dummy", Model: "dummy", PromptVersion: 42, CreatedAt: 10,
	}
	if err := st.RecordSummary(original, []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}

	updated := model.Summary{
		ID: "sum1", SessionID: "s1",
		// 下面这几格是"不许动"的，故意都给成别的值来验守卫
		Type: model.TypeSummaries, Blocks: 77, SourceIDs: []string{"别的"},
		BeginMessageID: new("m3"), EndMessageID: new("m4"), CreatedAt: 999,
		// 这几格才是该换的
		Text: "新正文", Tokens: 999, Provider: "other", Model: "other-model",
		PromptVersion: 7, Usage: json.RawMessage(`{"total_tokens":11}`), Dirty: true,
	}
	if err := st.ReplaceSummaryText(updated); err != nil {
		t.Fatal(err)
	}

	got := summariesByID(t, st, "s1")["sum1"]
	if got.Text != "新正文" || got.Tokens != 999 || got.Provider != "other" ||
		got.Model != "other-model" || got.PromptVersion != 7 || !got.Dirty {
		t.Fatalf("该换的那几格没换全：%+v", got)
	}
	if string(got.Usage) != `{"total_tokens":11}` {
		t.Fatalf("usage 该跟着换：%s", got.Usage)
	}
	// 不该动的（含故意给错的值）
	if got.ID != "sum1" || got.SessionID != "s1" || got.Type != model.TypeMessages {
		t.Fatalf("地址 / 成员种类被动了：%+v", got)
	}
	if got.Blocks != 1 || got.CreatedAt != 10 {
		t.Fatalf("blocks / created_at 被动了：%+v", got)
	}
	if got.BeginMessageID == nil || *got.BeginMessageID != "m1" ||
		got.EndMessageID == nil || *got.EndMessageID != "m2" {
		t.Fatalf("覆盖区间被动了：%v–%v", got.BeginMessageID, got.EndMessageID)
	}
	if got.ParentSummaryID != nil {
		t.Fatalf("顶层摘要不该被挂爹：%v", *got.ParentSummaryID)
	}
	if len(got.SourceIDs) != 2 || got.SourceIDs[0] != "m1" || got.SourceIDs[1] != "m2" {
		t.Fatalf("成员指针被动了：%v", got.SourceIDs)
	}
	// 也不碰 messages：指针原样
	messages, err := st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		if messages[index].SummaryID == nil || *messages[index].SummaryID != "sum1" {
			t.Fatalf("messages 被动了：第 %d 条指针 = %v", index, messages[index].SummaryID)
		}
	}
}

// 非顶层（已被并走）/ 不存在 / 跨会话 ⇒ 报错（`ErrNotFound`，与改正文一致），且那一行原样。
func TestReplaceSummaryTextRefusesNonTopLevelAndStray(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "s1", "m1", "m2", "m3", "m4")
	seedSession(t, st, "s2", "m5", "m6")
	messageSummary := func(sessionID, id, begin, end string) model.Summary {
		return model.Summary{
			ID: id, SessionID: sessionID, Type: model.TypeMessages,
			BeginMessageID: new(begin), EndMessageID: new(end),
			Text: id, Blocks: 1, Tokens: 3, SourceIDs: []string{begin, end},
			Provider: "dummy", Model: "dummy", PromptVersion: 1, CreatedAt: 1,
		}
	}
	if err := st.RecordSummary(messageSummary("s1", "c1", "m1", "m2"), []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSummary(messageSummary("s1", "c2", "m3", "m4"), []string{"m3", "m4"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSummary(messageSummary("s2", "c3", "m5", "m6"), []string{"m5", "m6"}); err != nil {
		t.Fatal(err)
	}
	// c1 被并走 ⇒ 不再是顶层
	parent := model.Summary{
		ID: "p1", SessionID: "s1", Type: model.TypeSummaries,
		BeginMessageID: new("m1"), EndMessageID: new("m4"),
		Text: "并起来", Blocks: 2, Tokens: 6, SourceIDs: []string{"c1", "c2"}, CreatedAt: 2,
	}
	if err := st.RecordSummary(parent, []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}

	before := summariesByID(t, st, "s1")
	assertReplacedRefused := func(id, sessionID string) {
		t.Helper()
		err := st.ReplaceSummaryText(model.Summary{
			ID: id, SessionID: sessionID, Text: "想改", Tokens: 999, Dirty: true,
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s/%s：该报 ErrNotFound：%v", sessionID, id, err)
		}
	}
	// 目标有父
	assertReplacedRefused("c1", "s1")
	// 不存在
	assertReplacedRefused("查无此条", "s1")
	// 跨会话（c3 属于 s2）
	assertReplacedRefused("c3", "s1")
	// 换一对会话也一样
	assertReplacedRefused("c1", "s2")

	// 失败之后那一行原样（含 c1 的正文 / tokens / 父指针）
	after := summariesByID(t, st, "s1")
	if got := after["c1"]; got.Text != before["c1"].Text || got.Tokens != before["c1"].Tokens ||
		got.Dirty || got.ParentSummaryID == nil || *got.ParentSummaryID != "p1" {
		t.Fatalf("被拒之后 c1 该原样：%+v", got)
	}
	// s2 里的 c3 也一个字没动
	if got := summariesByID(t, st, "s2")["c3"]; got.Text != "c3" || got.Tokens != 3 || got.Dirty {
		t.Fatalf("跨会话那次不许动 c3：%+v", got)
	}
}
