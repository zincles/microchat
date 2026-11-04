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

	// ② 顶层都可撤；被 ABCD 盖住的 AB/CD 不可撤。
	for _, id := range []string{"ABCD", "EF", "GH"} {
		if !plan.Undoable[id] {
			t.Fatalf("%s 是顶层，该可撤销", id)
		}
	}
	for _, id := range []string{"AB", "CD"} {
		if plan.Undoable[id] {
			t.Fatalf("%s 被上层摘要盖住了，不该可撤销", id)
		}
	}

	// ③ 更早的 EF 撤销时，连同同层更晚的 GH 一起撤。
	if got := plan.Cascade["EF"]; len(got) != 2 || got[0] != "EF" || got[1] != "GH" {
		t.Fatalf("EF 的级联链该是 EF→GH，实际 %v", got)
	}
	if got := plan.Cascade["GH"]; len(got) != 1 || got[0] != "GH" {
		t.Fatalf("GH 是第 1 层最新，级联只该有它自己：%v", got)
	}
	if got := plan.Cascade["ABCD"]; len(got) != 1 || got[0] != "ABCD" {
		t.Fatalf("ABCD 是第 2 层唯一顶层，级联只该有它自己：%v", got)
	}

	// ④ 报错只拦"被上层盖住"。
	if reason := UndoBlocker(cleanPyramid(), "EF"); reason != "" {
		t.Fatalf("EF 现在可撤（会级联 GH），却给了理由：%q", reason)
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
	for _, id := range []string{"AB", "CD", "EF", "GH"} {
		if !plan.Undoable[id] {
			t.Fatalf("撤掉 ABCD 后 %s 是顶层，该可撤", id)
		}
	}
	if got := plan.Cascade["CD"]; len(got) != 3 || got[0] != "CD" || got[2] != "GH" {
		t.Fatalf("CD 的级联链该是 CD→EF→GH，实际 %v", got)
	}

	// 再把 GH 撤了 ⇒ CD 的级联链缩短到 CD→EF。
	after2 := after[:3]
	plan2 := PlanUndo(after2)
	if got := plan2.Cascade["CD"]; len(got) != 2 || got[0] != "CD" || got[1] != "EF" {
		t.Fatalf("撤掉 GH 之后 CD 的链该是 CD→EF，实际 %v", got)
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
	if !plan.Undoable["m00000000-0000-7000-8000-000000000aaa"] ||
		!plan.Undoable["m00000000-0000-7000-8000-000000000bbb"] {
		t.Fatal("两条顶层都可撤；更早的那条会级联撤更晚的")
	}
	if got := plan.Cascade["m00000000-0000-7000-8000-000000000aaa"]; len(got) != 2 {
		t.Fatalf("同层两格的级联链该有 2 条，实际 %v", got)
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
