package store

// 撤销压缩（`UndoSummary`）的落库行为。
//
// 合法性（顶层 + 每层最新）在 `compact.PlanUndo` 里判，这里只钉**结构**那半：
// 三步的顺序、孩子与消息指针被放回来、以及"被盖住的摘要删不掉"这道结构闸。
//
// 例子照抄用户 2026-11-01 那个：
//
//	A-B-C-D-E-F-G-H-I
//	压缩为   AB - CD - EF - GH - I
//	再压缩为  ABCD - EF - GH - I

import (
	"errors"
	"testing"
)

// seedSummaryRow：落一行摘要，能指定 kind 与父子（仓库里 `seedSummary` 只造消息级）。
// 父摘要必须先存在（`parent_summary_id` 是自引用外键）。
func seedSummaryRow(t *testing.T, st *Store, id, sessionID string, parent *string, kind, begin, end string) {
	t.Helper()
	if _, err := st.db.Exec(
		`INSERT INTO summaries
		   (id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id,
		    text, blocks, tokens, source_ids, provider, model, prompt_version, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, '梗概', 1, 10, '[]', 'dummy', 'dummy', 1, 1)`,
		id, sessionID, parent, kind, begin, end); err != nil {
		t.Fatal(err)
	}
}

// pyramid：把用户那个例子摆进库里（8 条消息 + 4 个第 1 层 + 1 个第 2 层）。
func pyramid(t *testing.T, st *Store) {
	t.Helper()
	seedSession(t, st, "s1", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8")
	seedSummaryRow(t, st, "AB", "s1", nil, "message", "m1", "m2")
	seedSummaryRow(t, st, "CD", "s1", nil, "message", "m3", "m4")
	seedSummaryRow(t, st, "EF", "s1", nil, "message", "m5", "m6")
	seedSummaryRow(t, st, "GH", "s1", nil, "message", "m7", "m8")
	attach(t, st, "AB", "m1", "m2")
	attach(t, st, "CD", "m3", "m4")
	attach(t, st, "EF", "m5", "m6")
	attach(t, st, "GH", "m7", "m8")
	// 第 2 层：盖住 AB+CD（先插父，再把孩子的 parent 指过去 —— 外键要求父先存在）。
	seedSummaryRow(t, st, "ABCD", "s1", nil, "summary", "m1", "m4")
	if _, err := st.db.Exec(
		"UPDATE summaries SET parent_summary_id = 'ABCD' WHERE session_id = 's1' AND id IN ('AB','CD')"); err != nil {
		t.Fatal(err)
	}
}

// 撤销**合并级**（ABCD）：孩子回到顶层，消息**仍然**指着各自的孩子（文火不动）。
func TestUndoSummaryReleasesChildrenOfAMerge(t *testing.T) {
	st := openTemp(t)
	pyramid(t, st)

	result, err := st.UndoSummary("s1", "ABCD")
	if err != nil {
		t.Fatalf("撤销 ABCD 该成功：%v", err)
	}
	if result.Children != 2 || result.Messages != 0 {
		t.Fatalf("该放回 2 个子摘要、0 条消息，实际 children=%d messages=%d", result.Children, result.Messages)
	}

	// ABCD 没了
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM summaries WHERE id = 'ABCD'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("ABCD 该被删掉")
	}
	// 孩子重新变回顶层
	summaries, err := st.ListSummaries("s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range summaries {
		if s.ParentSummaryID != nil {
			t.Fatalf("%s 的 parent 该被置空，实际指向 %q", s.ID, *s.ParentSummaryID)
		}
	}
	// 消息指针**不动**（压缩只写 summary_id；撤销合并级不该碰消息）
	messages, err := st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.SummaryID == nil {
			t.Fatalf("消息 %s 的 summary_id 不该被清 —— 撤销合并级只放回子摘要", m.ID)
		}
		if *m.SummaryID == "ABCD" {
			t.Fatalf("消息 %s 不该指着 ABCD（消息只指直接盖它的那个）", m.ID)
		}
	}
}

// 撤销**消息级**（GH）：那两条消息回到原文（`summary_id` 置空）。
func TestUndoSummaryUnbindsMessages(t *testing.T) {
	st := openTemp(t)
	pyramid(t, st)

	result, err := st.UndoSummary("s1", "GH")
	if err != nil {
		t.Fatalf("撤销 GH 该成功：%v", err)
	}
	if result.Messages != 2 || result.Children != 0 {
		t.Fatalf("该放回 2 条消息、0 个子摘要，实际 messages=%d children=%d", result.Messages, result.Children)
	}
	// 这 2 条回到原文；别的仍然指着自己的摘要。
	messages, err := st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"m1": true, "m2": true, "m3": true, "m4": true, "m5": true, "m6": true, "m7": false, "m8": false}
	for _, m := range messages {
		if want[m.ID] {
			if m.SummaryID == nil {
				t.Fatalf("消息 %s 不该被解开（只有 GH 盖的那两条才解）", m.ID)
			}
		} else if m.SummaryID != nil {
			t.Fatalf("消息 %s 该回到原文，实际还指着 %q", m.ID, *m.SummaryID)
		}
	}
}

// **结构闸**：被上层盖住的摘要删不掉（不然上层那条的区间就破了个洞）。
//
// 这一条守的是"调用方绕过 `compact.PlanUndo` 直接调 store"那种情况 ——
// 策略可以被绕过，结构不行。
func TestUndoSummaryRefusesACoveredChild(t *testing.T) {
	st := openTemp(t)
	pyramid(t, st)

	if _, err := st.UndoSummary("s1", "AB"); !errors.Is(err, ErrSummaryNotTopLevel) {
		t.Fatalf("撤一条被盖住的摘要该报 ErrSummaryNotTopLevel，实际 %v", err)
	}
	// 报错之后**什么都没动**（事务回滚）：ABCD 与 AB 都还在，指针也没变。
	summaries, err := st.ListSummaries("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 5 {
		t.Fatalf("失败该整体回滚（仍有 5 条摘要），实际 %d 条", len(summaries))
	}
	messages, err := st.ListMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.SummaryID == nil {
			t.Fatalf("失败不该解开任何消息指针，%s 却被解了", m.ID)
		}
	}
}

// 不存在的摘要 ⇒ ErrNotFound（别静默成功）。
func TestUndoSummaryUnknownID(t *testing.T) {
	st := openTemp(t)
	pyramid(t, st)
	if _, err := st.UndoSummary("s1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的摘要该报 ErrNotFound，实际 %v", err)
	}
	// 别的会话里的同名摘要也不许被删（scope 到 session）。
	seedSession(t, st, "s2", "x1", "x2")
	seedSummaryRow(t, st, "OTHER", "s2", nil, "message", "x1", "x2")
	if _, err := st.UndoSummary("s1", "OTHER"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨会话撤销该报 ErrNotFound，实际 %v", err)
	}
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM summaries WHERE id = 'OTHER'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("别的会话那条摘要不该被删")
	}
}
