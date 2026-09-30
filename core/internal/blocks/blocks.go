// Package blocks：**对话块的切分** —— 压缩的粒度是块，不是条。
//
// 口径（见 `AGENTS.md` 的「摘要 / 压缩的设计」）：
//
//   - 块在 **`assistant` → `user` 的交界处**切（`U1 A1 | U2 U3 A3 | U4 A4 A5`）——
//     一次"你一句、我一段"就是一个块，连着说的几句用户话属于同一块；
//   - **块是推导的、不入库**：需要指一个块时用**两端消息 id**（所以这里只回下标，不回 id）；
//   - **块绝不被劈开**：压缩只吃整块（策略取整块、区间入口也要求对齐块首块尾）；
//   - **最后那个开着的块永不压** ✗：它还在长（对话还没接上下一轮），压它就是"把话说到一半的收起来"。
//
// 与 `state` / `store` 无关：这一包是纯函数（零依赖，只有 `model` 的 `Role`）。
package blocks

import "microchat/internal/model"

// Block：一个对话块在**这条会话的消息列表里**的下标区间（闭区间）。
//
// 只回下标、不回 id：块的形状是推导的，谁要用 id 自己去 `messages[Begin].ID` 取
// （避免这里再抄一份 id 数组，两处迟早对不上）。
type Block struct {
	Begin int
	End   int
}

// Split：按 `assistant → user` 的交界处切出全部块。
//
// 空会话 ⇒ 没有块。第一条消息不管是 `user` 还是 `assistant` 都从它起块
// （`user → user` **不切**：连着说的几句是同一个块 —— 切的是"我答完了，你又开口"那一刻）。
func Split(messages []model.Message) []Block {
	if len(messages) == 0 {
		return nil
	}
	blocks := []Block{}
	begin := 0
	for index := 1; index < len(messages); index++ {
		if messages[index].Role != model.RoleUser || messages[index-1].Role != model.RoleAssistant {
			continue
		}
		blocks = append(blocks, Block{Begin: begin, End: index - 1})
		begin = index
	}
	return append(blocks, Block{Begin: begin, End: len(messages) - 1})
}

// Closed：**已闭合**的块 —— 去掉最后那个开着的（压缩唯一能吃的就是这些）。
//
// 输入是 `Split` 的结果（块只推导一次，之后都按同一份走）。
// 只有一块（对话刚说了半轮）⇒ 一块都没有：**最后那个开着的块永不压**。
func Closed(blocks []Block) []Block {
	if len(blocks) <= 1 {
		return nil
	}
	return blocks[:len(blocks)-1]
}
