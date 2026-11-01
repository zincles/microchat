package compact

// 撤销的合法性（用户 2026-11-01 定的口径）。
//
// 例子直接照抄用户原话：
//
//	A-B-C-D-E-F-G-H-I
//	压缩为   AB - CD - EF - GH - I
//	再压缩为  ABCD - EF - GH - I
//
//	「我们既可以撤销 ABCD（第二层的最近一次压缩）也可以撤销 GH（第一层的最近一次压缩）。
//	 但不能撤销 EF，因为它不是所在层（第一层）的最近一次压缩。」
//
// 这里只测**纯函数**（不碰库）：`PlanUndo` 算层级与可撤销集合，`UndoBlocker` 说清为什么不行。

import (
	"strings"
	"testing"

	"microchat/internal/model"
)

func summary(id string, parent *string, createdAt int64) model.Summary {
	return model.Summary{
		ID:              id,
		ParentSummaryID: parent,
		Type:            model.TypeMessages,
		CreatedAt:       createdAt,
	}
}

func ptr(s string) *string { return &s }

// cleanPyramid：用户那个例子 —— AB/CD/EF/GH 在第 1 层，ABCD 在第 2 层盖住 AB+CD。
//
//	创建顺序（created_at）：AB=1, CD=2, EF=3, GH=4, ABCD=5
//	父子：AB、CD 的 parent 是 ABCD（于是它俩不再是顶层）
func cleanPyramid() []model.Summary {
	return []model.Summary{
		summary("AB", ptr("ABCD"), 1),
		summary("CD", ptr("ABCD"), 2),
		summary("EF", nil, 3),
		summary("GH", nil, 4),
		summary("ABCD", nil, 5),
	}
}

func TestPlanUndoMatchesTheUsersExample(t *testing.T) {
	plan := PlanUndo(cleanPyramid())

	// ① 层级：盖消息的四个都是第 1 层，ABCD 是第 2 层。
	for _, id := range []string{"AB", "CD", "EF", "GH"} {
		if got := plan.Levels[id]; got != 1 {
			t.Fatalf("%s 的层级该是 1，实际 %d", id, got)
		}
	}
	if got := plan.Levels["ABCD"]; got != 2 {
		t.Fatalf("ABCD 的层级该是 2，实际 %d", got)
	}

	// ② 可撤销的**恰好**是 ABCD（第 2 层最新）与 GH（第 1 层最新）。
	want := map[string]bool{"ABCD": true, "GH": true}
	if len(plan.Undoable) != len(want) {
		t.Fatalf("可撤销的该有 %d 条，实际 %d 条：%v", len(want), len(plan.Undoable), plan.Undoable)
	}
	for id := range want {
		if !plan.Undoable[id] {
			t.Fatalf("%s 该可撤销（用户原话点名了它）", id)
		}
	}

	// ③ EF 不可撤（同层更早的一次压缩）。
	if plan.Undoable["EF"] {
		t.Fatal("EF 不该可撤销 —— 它不是第 1 层最近的一次压缩")
	}
	// ④ AB/CD 不可撤（被 ABCD 盖着）。
	for _, id := range []string{"AB", "CD"} {
		if plan.Undoable[id] {
			t.Fatalf("%s 不该可撤销 —— 它被上层摘要盖住了", id)
		}
	}

	// ⑤ 报错要说清是哪条闸拦的（否则人只会看到"撤销失败"然后反复点）。
	if reason := UndoBlocker(cleanPyramid(), "EF"); !strings.Contains(reason, "第 1 层") ||
		!strings.Contains(reason, "最近") {
		t.Fatalf("EF 的拒绝理由该点名'第 1 层最近一次'，实际：%q", reason)
	}
	if reason := UndoBlocker(cleanPyramid(), "AB"); !strings.Contains(reason, "上层摘要盖住") {
		t.Fatalf("AB 的拒绝理由该说'被上层盖住'，实际：%q", reason)
	}
	if reason := UndoBlocker(cleanPyramid(), "ABCD"); reason != "" {
		t.Fatalf("ABCD 该能撤，却给了理由：%q", reason)
	}
}

// 撤掉顶层之后，被盖住的孩子要**重新变成顶层**并重新参与"最新"的评比 —— 否则
// 撤销一次就再也撤不了任何东西（那正是"栈"该有的行为，但得测出来）。
func TestPlanUndoAfterUndoingTheTop(t *testing.T) {
	// 撤掉 ABCD 之后的状态：AB/CD 的 parent 回到 NULL，ABCD 那一行没了。
	after := []model.Summary{
		summary("AB", nil, 1),
		summary("CD", nil, 2),
		summary("EF", nil, 3),
		summary("GH", nil, 4),
	}
	plan := PlanUndo(after)
	if len(plan.Undoable) != 1 || !plan.Undoable["GH"] {
		t.Fatalf("撤掉 ABCD 之后，只剩 GH（第 1 层最新）该可撤，实际：%v", plan.Undoable)
	}
	if plan.Undoable["CD"] {
		t.Fatal("CD 不该可撤 —— GH 比它新")
	}

	// 再把 GH 撤了 ⇒ 第 1 层最新变成 EF。
	after2 := after[:3]
	plan2 := PlanUndo(after2)
	if len(plan2.Undoable) != 1 || !plan2.Undoable["EF"] {
		t.Fatalf("撤掉 GH 之后该轮到 EF，实际：%v", plan2.Undoable)
	}
}

// 同毫秒的两条要排得出先后（靠 UUIDv7 的 id 序）—— 不然"每层只放一条"会同时放行两条。
func TestPlanUndoTieBreaksOnIDWhenCreatedAtIsEqual(t *testing.T) {
	// 两个都 created_at = 7，id 里 "…b" > "…a" ⇒ b 更新。
	summaries := []model.Summary{
		summary("m00000000-0000-7000-8000-000000000aaa", nil, 7),
		summary("m00000000-0000-7000-8000-000000000bbb", nil, 7),
	}
	plan := PlanUndo(summaries)
	if len(plan.Undoable) != 1 {
		t.Fatalf("同毫秒的两条也只该放行一条，实际 %d 条：%v", len(plan.Undoable), plan.Undoable)
	}
	if !plan.Undoable["m00000000-0000-7000-8000-000000000bbb"] {
		t.Fatal("id 大的那条才该是最新（UUIDv7 时间有序）")
	}
}

// 一条摘要都没有 / 只有一条 —— 别炸，也别给出可撤销。
func TestPlanUndoOnEmptyAndSingle(t *testing.T) {
	if plan := PlanUndo(nil); len(plan.Undoable) != 0 {
		t.Fatalf("空列表不该有可撤销项：%v", plan.Undoable)
	}
	plan := PlanUndo([]model.Summary{summary("only", nil, 1)})
	if !plan.Undoable["only"] {
		t.Fatal("只有一条顶层摘要时，它就是该层最新的 —— 该可撤")
	}
	if plan.Levels["only"] != 1 {
		t.Fatalf("没有孩子的摘要层级该是 1，实际 %d", plan.Levels["only"])
	}
}

// UndoSteps 的顺序是**语义**（自引用外键在语句末尾才核）：release 必须在 delete 之前。
// 这条钉住"谁把顺序调了"——调了就是 FOREIGN KEY constraint failed。
func TestUndoStepsReleasesBeforeDeleting(t *testing.T) {
	index := func(step string) int {
		for i, s := range UndoSteps {
			if s == step {
				return i
			}
		}
		t.Fatalf("UndoSteps 里没有 %q：%v", step, UndoSteps)
		return -1
	}
	if index("release-children") >= index("delete-summary") {
		t.Fatalf("孩子的 parent 必须先置空再删摘要（自引用外键在语句末尾才核）：%v", UndoSteps)
	}
	if index("release-messages") >= index("delete-summary") {
		t.Fatalf("消息指针必须先清再删摘要：%v", UndoSteps)
	}
}
