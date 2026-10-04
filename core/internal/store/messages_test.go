package store

import (
	"errors"
	"testing"
	"time"

	"microchat/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 线性会话的沙盒：一条会话 + 一串消息。
// id 用 UUIDv7 的形状（时间在前）⇒ 字符串序就是先后（顺序**就是** id 定的）。
func seedSession(t *testing.T, st *Store, sessionID string, messageIDs ...string) {
	t.Helper()
	if _, err := st.db.Exec(
		`INSERT INTO sessions (id, title, provider, model, agent_id, created_at, updated_at)
		 VALUES (?1, '', 'dummy', 'dummy', 'default', 1, 1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	for index, id := range messageIDs {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		if _, err := st.db.Exec(
			`INSERT INTO messages (id, session_id, role, content, created_at, updated_at) VALUES (?1, ?2, ?3, ?4, ?5, ?5)`,
			id, sessionID, role, "第 "+id+" 条", int64(index+1)); err != nil {
			t.Fatal(err)
		}
	}
}

// 直接落一行摘要（`insertSummary` 是给事务用的生产函数，这里要一个"塞进去就完了"的版本）。
// 父摘要必须先存在（`parent_summary_id` 是自引用外键）。
func seedSummary(t *testing.T, st *Store, id, sessionID string, parent *string, begin, end string) {
	t.Helper()
	if _, err := st.db.Exec(
		`INSERT INTO summaries
		   (id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id,
		    text, blocks, tokens, source_ids, provider, model, prompt_version, created_at)
		 VALUES (?1, ?2, ?3, 'message', ?4, ?5, '梗概', 1, 10, '[]', 'dummy', 'dummy', 1, 1)`,
		id, sessionID, parent, begin, end); err != nil {
		t.Fatal(err)
	}
}

// attach：把一段消息指向某份摘要（压缩只写这一格）。
func attach(t *testing.T, st *Store, summaryID string, messageIDs ...string) {
	t.Helper()
	for _, id := range messageIDs {
		if _, err := st.db.Exec("UPDATE messages SET summary_id = ?1 WHERE id = ?2", summaryID, id); err != nil {
			t.Fatal(err)
		}
	}
}

func messageIDsOf(messages []model.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// 线性会话：`ListMessages` = 这条会话的**全部**消息，按 id 升序（没有路径、没有分支）。
func TestListMessagesIsTheWholeSessionInIDOrder(t *testing.T) {
	st := openTemp(t)
	sessionID := "01a00000-0000-7000-8000-000000000000"
	// 故意**不按插入顺序**写 id（顺序只由 id 定，跟插入先后无关）
	seedSession(t, st, sessionID, "01a00000-0000-7000-8000-000000000003",
		"01a00000-0000-7000-8000-000000000001", "01a00000-0000-7000-8000-000000000002")
	messages, err := st.ListMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"01a00000-0000-7000-8000-000000000001",
		"01a00000-0000-7000-8000-000000000002",
		"01a00000-0000-7000-8000-000000000003",
	}
	if got := messageIDsOf(messages); !sameIDs(got, want) {
		t.Fatalf("顺序 = %v，想要 %v", got, want)
	}
	// 会话不存在 ⇒ 空列表（不是 404，口径与旧版一致）
	if messages, err := st.ListMessages("查无此会话"); err != nil || len(messages) != 0 {
		t.Fatalf("会话不存在该回空列表：%v %v", messages, err)
	}
}

// DEFINE.md 里走了一遍的那个例子：路径 A…G，摘要 BD(B,C,D)、EF(E,F)、BF=父(BD+EF)。
// 删 **F** ⇒ 消息 F、G；摘要 EF 与 BF；E 的 summary_id 置空；BD 升为顶层。
func TestDeletionPlanFollowsTheCascade(t *testing.T) {
	st := openTemp(t)
	sessionID := "01a00000-0000-7000-8000-000000000000"
	a, b, c, d, e, f, g := "01a00000-0000-7000-8000-000000000001",
		"01a00000-0000-7000-8000-000000000002", "01a00000-0000-7000-8000-000000000003",
		"01a00000-0000-7000-8000-000000000004", "01a00000-0000-7000-8000-000000000005",
		"01a00000-0000-7000-8000-000000000006", "01a00000-0000-7000-8000-000000000007"
	seedSession(t, st, sessionID, a, b, c, d, e, f, g)

	bd, ef, bf := "01b00000-0000-7000-8000-000000000001",
		"01b00000-0000-7000-8000-000000000002", "01b00000-0000-7000-8000-000000000003"
	// BF 是顶（盖 B..F），BD(B..D) 与 EF(E..F) 都是它的孩子（父必须先插）
	seedSummary(t, st, bf, sessionID, nil, b, f)
	seedSummary(t, st, bd, sessionID, &bf, b, d)
	seedSummary(t, st, ef, sessionID, &bf, e, f)
	attach(t, st, bd, b, c, d)
	attach(t, st, ef, e, f)

	plan, err := st.DeletionPlan(sessionID, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.DeletedMessageIDs; !sameIDs(got, []string{f, g}) {
		t.Fatalf("要删的消息 = %v，想要 [F G]", got)
	}
	if got := plan.DeletedSummaryIDs; !sameIDs(got, []string{ef, bf}) {
		t.Fatalf("要删的摘要 = %v，想要 [EF BF]", got)
	}
	if got := plan.UnlinkedMessageIDs; !sameIDs(got, []string{e}) {
		t.Fatalf("要被解链的消息 = %v，想要 [E]（它盖在 EF 上）", got)
	}
	if got := plan.UnlinkedSummaryIDs; !sameIDs(got, []string{bd}) {
		t.Fatalf("要被解链的摘要 = %v，想要 [BD]（它的父 BF 死了）", got)
	}
	if plan.LastDeletedMessageID != g {
		t.Fatalf("最后一条 = %q，想要 G", plan.LastDeletedMessageID)
	}

	// 执行 = 照同一份计划动手
	if err := st.ApplyDeletion(plan); err != nil {
		t.Fatal(err)
	}
	messages, err := st.ListMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDsOf(messages); !sameIDs(got, []string{a, b, c, d, e}) {
		t.Fatalf("剩下的消息 = %v，想要 [A B C D E]", got)
	}
	summaries, err := st.ListSummaries(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != bd {
		t.Fatalf("剩下的摘要 = %+v，想要只剩 BD", summaries)
	}
	if summaries[0].ParentSummaryID != nil {
		t.Fatalf("BD 该升为顶层：%v", *summaries[0].ParentSummaryID)
	}
	for _, message := range messages {
		if message.ID == e && message.SummaryID != nil {
			t.Fatalf("E 的指针该被置空：%v", *message.SummaryID)
		}
		if message.ID == a && message.SummaryID != nil {
			t.Fatalf("A 本来就没被覆盖，不该有指针：%v", *message.SummaryID)
		}
	}
}

// 边界：删**第一条** ⇒ 消息与摘要全清（会话变回空会话），没有任何"被解链"的幸存者。
func TestDeletionPlanFromTheFirstMessage(t *testing.T) {
	st := openTemp(t)
	sessionID := "01a00000-0000-7000-8000-000000000000"
	a, b, c := "01a00000-0000-7000-8000-000000000001",
		"01a00000-0000-7000-8000-000000000002", "01a00000-0000-7000-8000-000000000003"
	seedSession(t, st, sessionID, a, b, c)
	child := "01b00000-0000-7000-8000-000000000001"
	parent := "01b00000-0000-7000-8000-000000000002"
	seedSummary(t, st, parent, sessionID, nil, a, c)
	seedSummary(t, st, child, sessionID, &parent, a, b)
	attach(t, st, child, a, b)

	plan, err := st.DeletionPlan(sessionID, a)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(plan.DeletedMessageIDs, []string{a, b, c}) {
		t.Fatalf("要删的消息 = %v", plan.DeletedMessageIDs)
	}
	if !sameIDs(plan.DeletedSummaryIDs, []string{child, parent}) {
		t.Fatalf("要删的摘要 = %v（孩子与父一起，按 id 排）", plan.DeletedSummaryIDs)
	}
	if len(plan.UnlinkedMessageIDs) != 0 || len(plan.UnlinkedSummaryIDs) != 0 {
		t.Fatalf("全删了就没有幸存者要解链：%+v", plan)
	}
	if plan.LastDeletedMessageID != c {
		t.Fatalf("最后一条 = %q，想要 C", plan.LastDeletedMessageID)
	}
	if err := st.ApplyDeletion(plan); err != nil {
		t.Fatal(err)
	}
	if messages, _ := st.ListMessages(sessionID); len(messages) != 0 {
		t.Fatalf("该清空：%+v", messages)
	}
	if summaries, _ := st.ListSummaries(sessionID); len(summaries) != 0 {
		t.Fatalf("摘要该清空：%+v", summaries)
	}
}

// 区间缺失（老数据）的摘要：**不参与**级联判定（拿不出范围 ⇒ 说不出它盖了谁），但也不该把删除搞崩。
func TestDeletionPlanIgnoresIntervalLessSummaries(t *testing.T) {
	st := openTemp(t)
	sessionID := "01a00000-0000-7000-8000-000000000000"
	a, b := "01a00000-0000-7000-8000-000000000001", "01a00000-0000-7000-8000-000000000002"
	seedSession(t, st, sessionID, a, b)
	legacy := "01b00000-0000-7000-8000-000000000001"
	if _, err := st.db.Exec(
		`INSERT INTO summaries (id, session_id, parent_summary_id, source_kind, text, blocks, tokens,
		 source_ids, provider, model, prompt_version, created_at)
		 VALUES (?1, ?2, NULL, 'message', '老的', 1, 10, '[]', 'dummy', 'dummy', 1, 1)`,
		legacy, sessionID); err != nil {
		t.Fatal(err)
	}
	attach(t, st, legacy, a)

	plan, err := st.DeletionPlan(sessionID, b)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(plan.DeletedMessageIDs, []string{b}) {
		t.Fatalf("要删的消息 = %v", plan.DeletedMessageIDs)
	}
	if len(plan.DeletedSummaryIDs) != 0 {
		t.Fatalf("没有区间的摘要不参与判定：%v", plan.DeletedSummaryIDs)
	}
	if err := st.ApplyDeletion(plan); err != nil {
		t.Fatal(err)
	}
	if summaries, _ := st.ListSummaries(sessionID); len(summaries) != 1 {
		t.Fatalf("它该原样活着：%+v", summaries)
	}
}

// **父摘要的 id 排在自己的孩子前面**（父先铸、孩子后铸 —— 人工造的常见形状）：
// 逐条删摘要会"父先没了、孩子还指着它"⇒ 外键报错（真机上实测踩过，500）。
// 一批删必须过（自引用外键在**语句末尾**才核）。
func TestApplyDeletionSurvivesParentBeforeChildren(t *testing.T) {
	st := openTemp(t)
	sessionID := "01a00000-0000-7000-8000-000000000000"
	a, b := "01a00000-0000-7000-8000-000000000001", "01a00000-0000-7000-8000-000000000002"
	seedSession(t, st, sessionID, a, b)
	parent := "01b00000-0000-7000-8000-000000000001" // 比孩子小 ⇒ 排序在前
	child := "01b00000-0000-7000-8000-000000000002"
	seedSummary(t, st, parent, sessionID, nil, a, b)
	seedSummary(t, st, child, sessionID, &parent, a, b)

	plan, err := st.DeletionPlan(sessionID, a)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(plan.DeletedSummaryIDs, []string{parent, child}) {
		t.Fatalf("父与子都该在名单里（按 id 排 ⇒ 父在前）：%v", plan.DeletedSummaryIDs)
	}
	if err := st.ApplyDeletion(plan); err != nil {
		t.Fatalf("父子同批删不该被外键拦住：%v", err)
	}
	if summaries, _ := st.ListSummaries(sessionID); len(summaries) != 0 {
		t.Fatalf("该清空：%+v", summaries)
	}
}

// 计划只认这条会话里的消息：给一条别的会话的消息 ⇒ 404（不许跨界删）。
func TestDeletionPlanRejectsForeignMessage(t *testing.T) {
	st := openTemp(t)
	sessionID, otherID := "01a00000-0000-7000-8000-000000000000", "01a00000-0000-7000-8000-00000000000f"
	a := "01a00000-0000-7000-8000-000000000001"
	seedSession(t, st, sessionID, a)
	seedSession(t, st, otherID, "01a00000-0000-7000-8000-0000000000f1")
	if _, err := st.DeletionPlan(sessionID, "01a00000-0000-7000-8000-0000000000f1"); err != ErrNotFound {
		t.Fatalf("别的会话的消息该 404：%v", err)
	}
	if _, err := st.DeletionPlan(sessionID, "查无此消息"); err != ErrNotFound {
		t.Fatalf("不存在的消息该 404：%v", err)
	}
}

// Copy：新 session + 消息与摘要一并复制；摘要**铸新 id**，消息上的 `summary_id`、摘要的
// `parent_summary_id`、两端区间 `begin/end_message_id` 全部重映射到新的 id 上。
func TestCopySessionRemapsSummaryPointers(t *testing.T) {
	st := openTemp(t)
	sessionID := "01a00000-0000-7000-8000-000000000000"
	m := []string{
		"01a00000-0000-7000-8000-000000000001", "01a00000-0000-7000-8000-000000000002",
		"01a00000-0000-7000-8000-000000000003", "01a00000-0000-7000-8000-000000000004",
	}
	seedSession(t, st, sessionID, m...)
	if _, err := st.db.Exec("UPDATE sessions SET title = '有名字的会话' WHERE id = ?1", sessionID); err != nil {
		t.Fatal(err)
	}
	top, left, right := "01b00000-0000-7000-8000-000000000001",
		"01b00000-0000-7000-8000-000000000002", "01b00000-0000-7000-8000-000000000003"
	seedSummary(t, st, top, sessionID, nil, m[0], m[3])
	seedSummary(t, st, left, sessionID, &top, m[0], m[1])
	seedSummary(t, st, right, sessionID, &top, m[2], m[3])
	attach(t, st, left, m[0], m[1])
	attach(t, st, right, m[2], m[3])
	// 顶层吃的是两份摘要（source_kind = summary）⇒ 它的 source_ids 也得换
	if _, err := st.db.Exec(
		`UPDATE summaries SET source_kind = 'summary', source_ids = ?1 WHERE id = ?2`,
		`["`+left+`","`+right+`"]`, top); err != nil {
		t.Fatal(err)
	}

	copied, err := st.CopySession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == sessionID {
		t.Fatal("Copy 必须换新 session id")
	}
	if copied.Title != "有名字的会话" || copied.Provider != "dummy" || copied.AgentID != "default" {
		t.Fatalf("标题 / 渠道 / agent 该带着：%+v", copied)
	}
	messages, err := st.ListMessages(copied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != len(m) {
		t.Fatalf("消息条数 = %d，想要 %d", len(messages), len(m))
	}
	old := map[string]bool{}
	for _, id := range m {
		old[id] = true
	}
	for index, message := range messages {
		if old[message.ID] {
			t.Fatalf("消息该铸新 id（第 %d 条还是旧的 %s）", index, message.ID)
		}
		if message.SessionID != copied.ID {
			t.Fatalf("消息该挂在新会话上：%+v", message)
		}
		if message.Content != "第 "+m[index]+" 条" {
			t.Fatalf("顺序 / 正文该逐条对上：%+v", message)
		}
	}
	summaries, err := st.ListSummaries(copied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 {
		t.Fatalf("摘要条数 = %d，想要 3", len(summaries))
	}
	newIDOf := map[string]string{} // 旧摘要 id → 新摘要 id（按区间端点认）
	interval := func(summary model.Summary) string {
		return *summary.BeginMessageID + ".." + *summary.EndMessageID
	}
	for _, summary := range summaries {
		if summary.BeginMessageID == nil || summary.EndMessageID == nil {
			t.Fatalf("复制出来的摘要该带着（重映射过的）区间：%+v", summary)
		}
		switch interval(summary) {
		case messages[0].ID + ".." + messages[1].ID:
			newIDOf[left] = summary.ID
		case messages[2].ID + ".." + messages[3].ID:
			newIDOf[right] = summary.ID
		case messages[0].ID + ".." + messages[3].ID:
			newIDOf[top] = summary.ID
		default:
			t.Fatalf("区间指到了原会话的消息上：%+v", summary)
		}
	}
	if len(newIDOf) != 3 {
		t.Fatalf("三份摘要该各就各位：%v", newIDOf)
	}
	for _, summary := range summaries {
		switch summary.ID {
		case newIDOf[left], newIDOf[right]:
			if summary.ParentSummaryID == nil || *summary.ParentSummaryID != newIDOf[top] {
				t.Fatalf("父指针该指到新的顶层摘要上：%+v", summary)
			}
		case newIDOf[top]:
			if summary.ParentSummaryID != nil {
				t.Fatalf("顶层摘要在副本里还是顶层：%+v", summary)
			}
			if !sameIDs(summary.SourceIDs, []string{newIDOf[left], newIDOf[right]}) {
				t.Fatalf("source_ids 该换成新摘要 id：%v", summary.SourceIDs)
			}
		}
	}
	for index, want := range []string{left, left, right, right} {
		if messages[index].SummaryID == nil || *messages[index].SummaryID != newIDOf[want] {
			t.Fatalf("消息 %d 的 summary_id 该指到新摘要上：%+v", index, messages[index])
		}
	}
	// 原来的会话一个字节都没动
	if original, _ := st.ListSummaries(sessionID); len(original) != 3 {
		t.Fatalf("原会话的摘要该原样：%+v", original)
	}
	if source, _ := st.ListMessages(sessionID); !sameIDs(messageIDsOf(source), m) {
		t.Fatalf("原会话的消息该原样：%v", messageIDsOf(source))
	}
	// Copy 一条空会话也得能用（只是没有消息与摘要）
	emptyID := "01a00000-0000-7000-8000-00000000000e"
	seedSession(t, st, emptyID)
	empty, err := st.CopySession(emptyID)
	if err != nil {
		t.Fatal(err)
	}
	if empty.ID == emptyID {
		t.Fatal("空会话的 Copy 也要新 id")
	}
	if messages, _ := st.ListMessages(empty.ID); len(messages) != 0 {
		t.Fatalf("副本该是空的：%+v", messages)
	}
	// 不存在的会话 ⇒ ErrNotFound
	if _, err := st.CopySession("查无此会话"); err != ErrNotFound {
		t.Fatalf("该 404：%v", err)
	}
}

// 消息的 `updated_at`：新建时 = `created_at`；改正文后变大。
func TestMessageUpdatedAt(t *testing.T) {
	st := openTemp(t)
	const sessionID = "01a00000-0000-7000-8000-0000000000f1"
	seedSession(t, st, sessionID)

	inserted, err := st.InsertMessage(model.Message{
		ID: "01a00000-0000-7000-8000-0000000000f2", SessionID: sessionID, Role: model.RoleUser, Content: "原始",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted.UpdatedAt != inserted.CreatedAt {
		t.Fatalf("新建时 updated_at 该等于 created_at：%+v", inserted)
	}
	listed, err := st.ListMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].UpdatedAt != listed[0].CreatedAt {
		t.Fatalf("没改过的消息两者该相等：%+v", listed[0])
	}

	time.Sleep(3 * time.Millisecond) // 毫秒粒度：睡一会儿才能观察到变大
	edited, err := st.UpdateMessage(sessionID, inserted.ID, MessageEdit{Content: new("改过了")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.UpdatedAt <= edited.CreatedAt {
		t.Fatalf("改过之后 updated_at 该 > created_at：%+v", edited)
	}
	if edited.CreatedAt != inserted.CreatedAt {
		t.Fatalf("改正文不该动 created_at：%+v", edited)
	}
}

// 改消息的字段级语义：content / reasoning 各改各的（nil = 不动），两个都 nil ⇒ InvalidError。
func TestUpdateMessageEditsContentAndReasoningSeparately(t *testing.T) {
	st := openTemp(t)
	const sessionID = "s1"
	seedSession(t, st, sessionID, "m1", "m2")

	// 只改思考 ⇒ 正文不动、思考换了
	edited, err := st.UpdateMessage(sessionID, "m2", MessageEdit{Reasoning: new("想通了")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Reasoning != "想通了" {
		t.Fatalf("思考该换：%+v", edited)
	}
	listed, err := st.ListMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if listed[1].Content == "" || listed[1].Reasoning != "想通了" {
		t.Fatalf("只改思考不该动正文：%+v", listed[1])
	}

	// 只改正文 ⇒ 思考不动
	edited, err = st.UpdateMessage(sessionID, "m2", MessageEdit{Content: new("新正文")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Content != "新正文" || edited.Reasoning != "想通了" {
		t.Fatalf("只改正文不该动思考：%+v", edited)
	}

	// 清掉思考（"" = 清掉，不是"不动"）
	edited, err = st.UpdateMessage(sessionID, "m2", MessageEdit{Reasoning: new("")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Reasoning != "" {
		t.Fatalf("空串该清掉思考：%+v", edited)
	}

	// 两个都不给 ⇒ InvalidError（与 PATCH 的 422 对齐），且库里原样
	if _, err := st.UpdateMessage(sessionID, "m2", MessageEdit{}); err == nil {
		t.Fatal("两个都不给该报错")
	} else {
		var invalid InvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("该报 InvalidError：%v", err)
		}
	}

	// 不存在的消息 ⇒ NotFound
	if _, err := st.UpdateMessage(sessionID, "查无此条", MessageEdit{Content: new("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的消息该 NotFound：%v", err)
	}
}
