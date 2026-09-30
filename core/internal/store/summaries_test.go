package store

import (
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
		ID: "sum1", SessionID: "s1", SourceKind: model.SourceMessages,
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
