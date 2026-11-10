package state

import (
	"testing"

	"microchat/internal/config"
	"microchat/internal/model"
)

func TestBuildOutgoingWithPromptEntries(t *testing.T) {
	messages := []model.Message{
		{ID: "m1", Role: model.RoleUser, Content: "第一句"},
		{ID: "m2", Role: model.RoleAssistant, Content: "回复"},
		{ID: "m3", Role: model.RoleUser, Content: "第二句"},
	}
	entries := []config.PromptEntry{
		{ID: "rel-sys", Role: "system", Position: "relative", Content: "相对 system"},
		{ID: "rel-user", Role: "user", Position: "relative", Content: "相对 user"},
		{ID: "abs-1", Role: "system", Position: "absolute", Depth: 1, Order: 100, Content: "倒数第二前"},
		{ID: "abs-0", Role: "assistant", Position: "absolute", Depth: 0, Order: 50, Content: "最后之后"},
	}
	out := BuildOutgoingWithPromptEntries("base", messages, nil, Tables{}, false, entries)
	want := []struct {
		role OutgoingRole
		text string
		kind string
	}{
		{RoleSystem, "base", "system"},
		{RoleSystem, "相对 system", "prompt"},
		{RoleUser, "相对 user", "prompt"},
		{RoleUser, "第一句", "message"},
		{RoleAssistant, "回复", "message"},
		{RoleSystem, "倒数第二前", "injection"},
		{RoleUser, "第二句", "message"},
		{RoleAssistant, "最后之后", "injection"},
	}
	if len(out) != len(want) {
		t.Fatalf("出站项数 = %d，想要 %d：%+v", len(out), len(want), out)
	}
	for i, item := range want {
		if out[i].Role != item.role || out[i].Content != item.text || out[i].Type != item.kind {
			t.Fatalf("第 %d 项 = %+v，想要 %+v", i, out[i], item)
		}
	}
}

func TestInjectionDepthClampsAndPrependStateHitsRealUser(t *testing.T) {
	messages := []model.Message{{ID: "u", Role: model.RoleUser, Content: "真实用户句"}}
	entries := []config.PromptEntry{
		{ID: "abs", Role: "assistant", Position: "absolute", Depth: 9, Content: "注入"},
	}
	out := BuildOutgoingWithPromptEntries("", messages, nil, Tables{"global": {"HP": "12"}}, true, entries)
	if len(out) != 3 {
		t.Fatalf("出站 = %+v", out)
	}
	if out[0].Type != "system" {
		t.Fatalf("状态表该先合成 system：%+v", out[0])
	}
	if out[1].Role != RoleAssistant || out[1].Type != "injection" || out[1].Content != "注入" {
		t.Fatalf("越界 depth 应夹到历史开头：%+v", out[1])
	}
	if out[2].Role != RoleUser || out[2].Type != "message" || out[2].Content == "真实用户句" {
		t.Fatalf("prepend_state 该贴真实用户句：%+v", out[2])
	}
}
