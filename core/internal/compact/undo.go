package compact

// undo.go —— 「撤销压缩」的**合法性判定**（唯一的权威；界面与接口都读这一份）。
//
// 用户 2026-11-01 定的口径（原话带例子）：
//
//	A-B-C-D-E-F-G-H-I
//	压缩为   AB - CD - EF - GH - I
//	再压缩为  ABCD - EF - GH - I
//
//	「我们既可以撤销 ABCD（第二层的最近一次压缩）也可以撤销 GH（第一层的最近一次压缩）。
//	 但不能撤销 EF，因为它不是所在层（第一层）的最近一次压缩。」
//
// 于是有两条闸，缺一条就会把金字塔弄成一个说不清的状态：
//
//	① **只算顶层**（`parent_summary_id IS NULL`）。被上层盖住的摘要不能单独撤：
//	   上层那条的区间还盖着它，撤掉孩子就会在**上层的覆盖范围里留一个洞** ——
//	   装配按区间 O(1) 跳，跳过去之后那一截既不是原文也不是摘要。
//	   （想撤被盖住的，先把上层撤了 —— 顺序自然就对了。）
//	② **每层只允许撤最新的那一个**。撤销是"后悔上一步"，而压缩是一条**栈**：
//	   每层各自一条。跳过中间那个去撤更早的，剩下的是"一串不连续的历史" ——
//	   数得出做过几次压缩，却推不出"上一次压的是什么"。
//
// ⚠ 这里**不碰库**、不做 IO：纯函数（`[]model.Summary` 进，两张表出）。于是它能在
// 界面（一次拉全部摘要算一遍）与接口（撤销前再算一遍）**共用同一份判断**，
// 不会出现"按钮亮着、点下去却被拒"那种两套逻辑打架的事。

import (
	"strconv"

	"microchat/internal/model"
)

// UndoPlan：一次算清的"层级 + 谁可以撤"。
type UndoPlan struct {
	// Levels：摘要 id → 层级（**1 = 直接盖消息**，2 = 盖住那一层……）。
	// 与装配的直觉一致：层号越大离原文越远。
	Levels map[string]int
	// Undoable：摘要 id → 现在能不能撤销（上面那两条闸）。
	Undoable map[string]bool
	// Newest：层级 → 该层最新的那条**顶层**摘要（给"为什么不能撤"的报错指路用）。
	Newest map[int]string
}

// PlanUndo：按上面那两条闸算一遍。**不修改入参**。
func PlanUndo(summaries []model.Summary) UndoPlan {
	byID := make(map[string]model.Summary, len(summaries))
	childrenOf := make(map[string][]string, len(summaries))
	for index := range summaries {
		summary := summaries[index]
		byID[summary.ID] = summary
		if parent := summary.ParentSummaryID; parent != nil {
			childrenOf[*parent] = append(childrenOf[*parent], summary.ID)
		}
	}
	memo := make(map[string]int, len(summaries))
	plan := UndoPlan{
		Levels:   make(map[string]int, len(summaries)),
		Undoable: make(map[string]bool),
		Newest:   make(map[int]string),
	}
	for index := range summaries {
		summary := summaries[index]
		level := summaryLevel(summary.ID, childrenOf, memo)
		plan.Levels[summary.ID] = level
		// 有爹的那些本来就撤不了，**不参与"最新"的评比** ——
		// 让它们参与只会把真正能撤的那条挤掉（那是错的：一条被盖住的新摘要，
		// 不该让同层唯一可撤的那条变成不可撤）。
		if summary.ParentSummaryID != nil {
			continue
		}
		current, ok := byID[plan.Newest[level]]
		if !ok || newerThan(summary, current) {
			plan.Newest[level] = summary.ID
		}
	}
	for _, id := range plan.Newest {
		plan.Undoable[id] = true
	}
	return plan
}

// newerThan：a 比 b 新吗。
//
// 先比 `created_at`（毫秒），相等再比 id —— 摘要 id 是 **UUIDv7**（时间有序），
// 所以同毫秒的那一批也排得出先后。两步都不能省：只比时间会把同毫秒的几条判成一样新，
// 于是"每层只有一条能撤"这条规则就同时放行好几条。
func newerThan(a, b model.Summary) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID > b.ID
}

// UndoBlocker：为什么这一条**现在**不能撤？能撤就回空串。
//
// 专门给"按钮亮着却被拒"那种局面用 —— 拒绝的时候必须说清是哪条闸拦的，
// 否则人只会看到"撤销失败"，然后反复点。
func UndoBlocker(summaries []model.Summary, summaryID string) string {
	var target model.Summary
	found := false
	for index := range summaries {
		if summaries[index].ID == summaryID {
			target, found = summaries[index], true
			break
		}
	}
	if !found {
		return "这条会话里没有这条摘要"
	}
	if target.ParentSummaryID != nil {
		return "它已经被上层摘要盖住了：先撤上面那层，被盖住的孩子才轮得到"
	}
	plan := PlanUndo(summaries)
	if plan.Undoable[summaryID] {
		return ""
	}
	level := plan.Levels[summaryID]
	if newestID, ok := plan.Newest[level]; ok && newestID != summaryID {
		return "它所在的第 " + strconv.Itoa(level) + " 层，最近一次压缩不是它：只能撤最近的那一次"
	}
	return "这一条不能撤销"
}

// UndoSteps：撤销时要做的三步，**顺序就是语义**（别重排）。
//
// 为什么"顺序"值得写成具名常量：自引用外键在**语句末尾**才核（`ApplyDeletion` 上记着同一条），
// 所以"先把孩子的 parent 置空、再删这一条"不是风格问题 —— 反了就是
// FOREIGN KEY constraint failed。store 照着这个顺序做，测试也钉住它。
var UndoSteps = []string{
	"release-children", // ① 孩子的 parent_summary_id 置空（它们重新变回顶层）
	"release-messages", // ② 直接指向它的消息的 summary_id 置空（只对消息级摘要有效）
	"delete-summary",   // ③ 最后删这一条摘要
}
