package compact

// undo.go —— 「撤销压缩」的**合法性判定**（唯一的权威；界面与接口都读这一份）。
//
// 2026-11-01 原始口径：只允许撤顶层 + 每层最近一次。
// 2026-11-05 用户扩展：允许撤更早的压缩，但会**连同同层级比它更晚的压缩一起撤销**。
// 例子：1–10、11–20、21–30、…、101–110；撤 11–20 ⇒ 11–20 之后同层全部一起消失。
//
// 于是规则变成：
//
//	① **只算顶层**（`parent_summary_id IS NULL`）。被上层盖住的摘要不能单独撤：
//	   上层那条的区间还盖着它，撤掉孩子会在上层的覆盖范围里留一个洞。
//	② **同层按位置形成一条链**：撤其中一条，等于把从它开始到该层末尾的整段历史一起撤掉。
//	   `Cascade` 就是这条链；界面先用它弹警告，接口再按它一次事务删。
//
// ⚠ 不碰库、不做 IO：纯函数（摘要进，两张表/链出），界面与接口共用同一份判断。

import (
	"sort"

	"microchat/internal/model"
)

// UndoPlan：一次算清的"层级 + 谁可以撤 + 撤它要连带谁"。
type UndoPlan struct {
	// Levels：摘要 id → 层级（1 = 直接盖消息，2 = 盖住那一层……）。
	Levels map[string]int
	// Undoable：顶层摘要都可撤（更早的也可，但会级联；被盖住的孩子不可）。
	Undoable map[string]bool
	// Cascade：撤这条时**实际要删的摘要 id**（含自己 + 同层更晚的），按位置升序。
	Cascade map[string][]string
	// Newest：层级 → 该层最新（按 created_at）的顶层摘要；仅保留给旧读口/说明。
	Newest map[int]string
}

// PlanUndo：按上面的规则算一遍。**不修改入参**。
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
		Cascade:  make(map[string][]string, len(summaries)),
		Newest:   make(map[int]string),
	}
	topByLevel := make(map[int][]model.Summary)
	for index := range summaries {
		summary := summaries[index]
		level := summaryLevel(summary.ID, childrenOf, memo)
		plan.Levels[summary.ID] = level
		if summary.ParentSummaryID != nil {
			continue
		}
		topByLevel[level] = append(topByLevel[level], summary)
		current, ok := byID[plan.Newest[level]]
		if !ok || newerThan(summary, current) {
			plan.Newest[level] = summary.ID
		}
	}
	for _, levelEntries := range topByLevel {
		sort.SliceStable(levelEntries, func(i, j int) bool {
			return earlierSummary(levelEntries[i], levelEntries[j])
		})
		ids := make([]string, len(levelEntries))
		for index := range levelEntries {
			ids[index] = levelEntries[index].ID
		}
		for index, summary := range levelEntries {
			plan.Undoable[summary.ID] = true
			plan.Cascade[summary.ID] = append([]string(nil), ids[index:]...)
		}
	}
	return plan
}

// earlierSummary：按**消息位置**排；缺 BeginIdx 的老数据再退到 begin id / created_at。
// 不能用 created_at 当主序：用户说“更晚”指的是对话里的位置，不是压缩动作发生的时间。
func earlierSummary(a, b model.Summary) bool {
	if a.BeginIdx != nil && b.BeginIdx != nil && *a.BeginIdx != *b.BeginIdx {
		return *a.BeginIdx < *b.BeginIdx
	}
	if a.BeginMessageID != nil && b.BeginMessageID != nil && *a.BeginMessageID != *b.BeginMessageID {
		return *a.BeginMessageID < *b.BeginMessageID
	}
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
}

// newerThan：a 比 b 新吗（给 Newest 用；不是撤销链的排序依据）。
func newerThan(a, b model.Summary) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID > b.ID
}

// UndoBlocker：为什么这一条**现在**不能撤？能撤就回空串。
//
// 顶层摘要一律可撤（更早的是级联撤）；不能撤只有"被上层盖住"和"不存在"两种。
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
	return ""
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
