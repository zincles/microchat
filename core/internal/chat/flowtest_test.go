package chat

import "testing"

func TestJoinFlowInputsUsesConfiguredSeparator(t *testing.T) {
	if got := joinFlowInputs([]string{"A", "", "B"}, " | "); got != "A | B" {
		t.Fatalf("分隔符拼接 = %q", got)
	}
	if got := joinFlowInputs([]string{"A", "B"}, ""); got != "AB" {
		t.Fatalf("空分隔符应直接相连：%q", got)
	}
	if got := flowJoinSeparator(flowNode{Data: map[string]any{"separator": " / "}}); got != " / " {
		t.Fatalf("节点自定义分隔符 = %q", got)
	}
}
