package state

import (
	"testing"

	"microchat/internal/model"
)

func TestPrependStateSticksToLastUser(t *testing.T) {
	messages := []model.Message{
		{ID: "m1", Idx: 1, Role: model.RoleUser, Content: "第一句"},
		{ID: "m2", Idx: 2, Role: model.RoleAssistant, Content: "收到"},
		{ID: "m3", Idx: 3, Role: model.RoleUser, Content: "第二句"},
	}
	tables := Tables{"global": {"位置": "家中"}}
	out := BuildOutgoing("", messages, nil, tables, true)
	if len(out) != 4 {
		t.Fatalf("出站 = %+v", out)
	}
	last := out[3]
	if last.Content != "<current_state>\n位置 = 家中\n</current_state>\n\n第二句" {
		t.Fatalf("只贴最后一条 user：%q", last.Content)
	}
	if out[1].Content != "第一句" {
		t.Fatalf("历史不动：%q", out[1].Content)
	}
	// 关 ⇒ 老样子
	off := BuildOutgoing("", messages, nil, tables, false)
	if off[3].Content != "第二句" {
		t.Fatalf("关时不贴：%q", off[2].Content)
	}
	// 空表 ⇒ 不贴
	empty := BuildOutgoing("", messages, nil, Tables{"global": {}}, true)
	if empty[2].Content != "第二句" {
		t.Fatalf("没状态不贴：%q", empty[2].Content)
	}
}
