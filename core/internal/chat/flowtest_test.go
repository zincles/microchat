package chat

import (
	"testing"

	"microchat/internal/providers"
)

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

func TestTrimFlowMessages(t *testing.T) {
	messages := []providers.ChatMessage{
		{Role: "user", Content: "1"},
		{Role: "assistant", Content: "2"},
		{Role: "user", Content: "3"},
		{Role: "assistant", Content: "4"},
	}
	cases := []struct {
		mode  string
		count int
		want  string
	}{
		{"tail", 2, "3,4"},
		{"head", 2, "1,2"},
		{"drop-head", 2, "3,4"},
		{"drop-tail", 2, "1,2"},
		{"tail", 10, "1,2,3,4"},
	}
	for _, tc := range cases {
		got := trimFlowMessages(messages, tc.mode, tc.count)
		joined := ""
		for i, message := range got {
			if i > 0 {
				joined += ","
			}
			joined += message.Content
		}
		if joined != tc.want {
			t.Fatalf("%s/%d = %q，想要 %q", tc.mode, tc.count, joined, tc.want)
		}
	}
	if got := trimFlowMessages(messages, "drop-head", 9); got != nil {
		t.Fatalf("去掉开头超过条数时应为空：%+v", got)
	}
}

func TestValidateTrimNode(t *testing.T) {
	base := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "out", Type: "output"},
	}}
	bad := flowGraph{Nodes: append(append([]flowNode{}, base.Nodes...), flowNode{ID: "t", Type: "trim", Data: map[string]any{"count": 0.5}})}
	if err := validateTestGraph(bad); err == nil {
		t.Fatal("半条消息应该被拒绝")
	}
	good := flowGraph{Nodes: append(append([]flowNode{}, base.Nodes...), flowNode{ID: "t", Type: "trim", Data: map[string]any{"mode": "drop-tail", "count": float64(2)}})}
	if err := validateTestGraph(good); err != nil {
		t.Fatalf("合法裁剪节点被拒：%v", err)
	}
}
