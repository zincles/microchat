package blocks

import (
	"testing"

	"microchat/internal/model"
)

// conversation：按 `U1 A1 U2 U3 A3 U4 A4 A5` 的形状造消息（压缩的粒度就是它）。
func conversation(roles ...model.Role) []model.Message {
	messages := make([]model.Message, 0, len(roles))
	for index, role := range roles {
		messages = append(messages, model.Message{ID: string(rune('a' + index)), Role: role, Content: "x"})
	}
	return messages
}

// 块在 **assistant → user 的交界处**切：连着说的几句用户话属于同一块。
func TestSplitCutsAtAssistantToUser(t *testing.T) {
	messages := conversation(
		model.RoleUser, model.RoleAssistant, // 块 1
		model.RoleUser, model.RoleUser, model.RoleAssistant, // 块 2（U2 U3 连着说）
		model.RoleUser, model.RoleAssistant, model.RoleAssistant, // 块 3
	)
	got := Split(messages)
	want := []Block{{0, 1}, {2, 4}, {5, 7}}
	if len(got) != len(want) {
		t.Fatalf("块数 = %d，想要 %d：%+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("第 %d 块 = %+v，想要 %+v", index, got[index], want[index])
		}
	}
}

// **最后那个开着的块永不压**：只有一块（刚聊了半轮）⇒ 一块可压的都没有。
func TestClosedDropsTheOpenBlock(t *testing.T) {
	if closed := Closed(Split(conversation(model.RoleUser, model.RoleAssistant))); len(closed) != 0 {
		t.Fatalf("只有一块时不该有已闭合的块：%+v", closed)
	}
	closed := Closed(Split(conversation(
		model.RoleUser, model.RoleAssistant,
		model.RoleUser, model.RoleAssistant,
		model.RoleUser, model.RoleAssistant,
	)))
	if len(closed) != 2 {
		t.Fatalf("三块里已闭合的该有两块：%+v", closed)
	}
	if empty := Split(nil); len(empty) != 0 {
		t.Fatalf("空会话没有块：%+v", empty)
	}
}

// 第一条消息是 assistant（编辑过 / 老数据）也照样从它起块 —— 别丢内容。
func TestSplitKeepsLeadingAssistant(t *testing.T) {
	got := Split(conversation(model.RoleAssistant, model.RoleUser, model.RoleAssistant))
	want := []Block{{0, 0}, {1, 2}}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("第 %d 块 = %+v，想要 %+v", index, got[index], want[index])
		}
	}
}
