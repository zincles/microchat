package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/reroll"
	"microchat/internal/store"
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

func TestFilterFlowRoles(t *testing.T) {
	messages := []providers.ChatMessage{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
	}
	got := filterFlowRoles(messages, []string{"user"})
	if len(got) != 2 || got[0].Content != "u1" || got[1].Content != "u2" {
		t.Fatalf("只保留 user = %+v", got)
	}
	if got := filterFlowRoles(messages, nil); len(got) != len(messages) {
		t.Fatalf("空角色应全部保留：%+v", got)
	}
	trimmed := trimFlowMessages(filterFlowRoles(messages, []string{"user", "assistant"}), "tail", 2)
	if len(trimmed) != 2 || trimmed[0].Content != "a1" || trimmed[1].Content != "u2" {
		t.Fatalf("先过滤再取最近 2 条 = %+v", trimmed)
	}
}

func TestValidateTrimRejectsUnknownRole(t *testing.T) {
	graph := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "out", Type: "output"},
		{ID: "t", Type: "trim", Data: map[string]any{"roles": []any{"narrator"}}},
	}}
	if err := validateTestGraph(graph); err == nil {
		t.Fatal("未知角色应该被拒绝")
	}
}

func TestFlowMergeInputsKeepPortOrder(t *testing.T) {
	node := flowNode{Data: map[string]any{"inputs": []any{"in-3", "in-1"}}}
	got := flowMergeInputs(node)
	if len(got) != 2 || got[0] != "in-3" || got[1] != "in-1" {
		t.Fatalf("输入端口顺序 = %#v", got)
	}
	if got := flowMergeInputs(flowNode{}); len(got) != 2 || got[0] != "in-1" || got[1] != "in-2" {
		t.Fatalf("默认输入端口 = %#v", got)
	}
}

func TestFlowAgentAcceptRoundTrip(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"llm","type":"llm","position":{"x":1,"y":0},"data":{"prompts":[{"id":"p1","role":"system","content":"你是测试"}]}},
		{"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[{"id":"e1","source":"llm","sourceHandle":"text","target":"out","targetHandle":"in"}]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetSessionAgent(h.session.ID, "flow"); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	accepted, err := h.service.Accept(h.session, "你好")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Backend != "dummy" {
		t.Fatalf("FlowAgent 仍应走会话渠道：%+v", accepted)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("FlowAgent 一轮没落成 user+assistant：%+v", messages)
	}
	if messages[1].Content != dummyReply {
		t.Fatalf("dummy 回复 = %q", messages[1].Content)
	}
	trace, ok := h.service.FlowTrace(h.session.ID)
	if !ok || trace.Status != "done" || len(trace.Steps) != 3 {
		t.Fatalf("FlowAgent 执行追踪 = %+v (ok=%v)", trace, ok)
	}
	ctx, err := h.service.ContextUsage(h.session)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.FlowNodes) != 1 || ctx.FlowNodes[0].BudgetTokens <= 0 {
		t.Fatalf("FlowAgent 上下文节点 = %+v", ctx.FlowNodes)
	}
}

func TestFlowAgentDoesNotAutoAttachReasoning(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"正文","reasoning_content":"思考"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer upstream.Close()
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}, "stub", "stub")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"llm","type":"llm","position":{"x":1,"y":0},"data":{"prompts":[{"id":"p1","role":"system","content":"写正文"}]}},
		{"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[
		{"id":"e1","source":"llm","sourceHandle":"reasoning","target":"out","targetHandle":"text"}
	]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	if _, err := h.service.Accept(h.session, "你好"); err != nil {
		t.Fatal(err)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[1].Content != "思考" {
		t.Fatalf("FlowAgent 正文 = %+v", messages)
	}
	if messages[1].Reasoning != "" {
		t.Fatalf("没接 reasoning 端口时不该自动出现思考：%q", messages[1].Reasoning)
	}
}

func TestFlowAgentMapsReasoningPort(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"正文","reasoning_content":"思考"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer upstream.Close()
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}, "stub", "stub")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"llm","type":"llm","position":{"x":1,"y":0},"data":{"prompts":[{"id":"p1","role":"system","content":"写"}]}},
		{"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[
		{"id":"e1","source":"llm","sourceHandle":"text","target":"out","targetHandle":"text"},
		{"id":"e2","source":"llm","sourceHandle":"reasoning","target":"out","targetHandle":"reasoning"}
	]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	if _, err := h.service.Accept(h.session, "你好"); err != nil {
		t.Fatal(err)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[1].Content != "正文" || messages[1].Reasoning != "思考" {
		t.Fatalf("正文/思考端口映射 = %+v", messages)
	}
}

func TestFlowAgentLegacyReasoningInputStaysReasoning(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"正文","reasoning_content":"思考"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer upstream.Close()
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}, "stub", "stub")
	// 老图：单个 in，源是 reasoning。迁移/执行都必须落到 reasoning，不能变成正文。
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"llm","type":"llm","position":{"x":1,"y":0},"data":{"prompts":[{"id":"p1","role":"system","content":"写"}]}},
		{"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[
		{"id":"e1","source":"llm","sourceHandle":"reasoning","target":"out","targetHandle":"in"}
	]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	if _, err := h.service.Accept(h.session, "你好"); err != nil {
		t.Fatal(err)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[1].Content != "" || messages[1].Reasoning != "思考" {
		t.Fatalf("老 reasoning→in 迁移 = %+v", messages)
	}
}

func TestFlowAgentRerollExecutesGraph(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"llm","type":"llm","position":{"x":1,"y":0},"data":{"prompts":[{"id":"p1","role":"system","content":"写"}]}},
		{"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[{"id":"e1","source":"llm","sourceHandle":"text","target":"out","targetHandle":"text"}]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	ids, err := store.MintOrderedIDs(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []model.Message{
		{ID: ids[0], SessionID: h.session.ID, Role: model.RoleUser, Content: "原问题"},
		{ID: ids[1], SessionID: h.session.ID, Role: model.RoleAssistant, Content: "旧回复"},
	} {
		if _, err := h.store.InsertMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	rerolls := reroll.New(h.store, h.service.Paths, h.turns, h.tasks, h.service)
	rerolls.FlowReroll = h.service
	h.service.Candidates = rerolls
	if _, err := rerolls.EnterSync(context.Background(), h.session); err != nil {
		t.Fatal(err)
	}
	state := rerolls.State(h.session.ID, reroll.TargetMessage)
	if state.Count != 2 || state.Items[1].Preview == "" {
		t.Fatalf("FlowAgent 重摇没有候选：%+v", state)
	}
	if _, err := rerolls.Switch(h.session.ID, reroll.TargetMessage, 2); err != nil {
		t.Fatal(err)
	}
	messages, err := h.store.ListMessages(h.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tail *model.Message
	if len(messages) > 0 {
		tail = &messages[len(messages)-1]
	}
	if tail == nil || tail.Content != dummyReply {
		t.Fatalf("FlowAgent 重摇候选 = %+v（想要 %q）", tail, dummyReply)
	}
}

func TestRewriteFlowInput(t *testing.T) {
	if got := rewriteFlowInput([]string{"原问题"}, "前缀 {{input}} 后缀"); got != "前缀 原问题 后缀" {
		t.Fatalf("模板改写 = %q", got)
	}
	if got := rewriteFlowInput([]string{"原问题"}, ""); got != "原问题" {
		t.Fatalf("空模板应放行 = %q", got)
	}
	if got := rewriteFlowInput(nil, "固定输入"); got != "固定输入" {
		t.Fatalf("无输入也可覆写 = %q", got)
	}
}

func TestExecuteFlowReportsActiveNode(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	graph := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "llm", Type: "llm", Data: map[string]any{"prompts": []any{map[string]any{"id": "p1", "role": "system", "content": "写"}}}},
		{ID: "out", Type: "output"},
	}, Edges: []flowEdge{{ID: "e", Source: "llm", SourceHandle: "text", Target: "out", TargetHandle: "text"}}}
	var events []string
	if _, err := h.service.executeFlowGraph(context.Background(), graph, flowRunOptions{
		DefaultProvider: "dummy", DefaultModel: "dummy", ProvidersConfig: dummyProviders(),
		OnNode: func(id, _ string, phase string) { events = append(events, id+":"+phase) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || !strings.Contains(strings.Join(events, ","), "llm:start") {
		t.Fatalf("节点执行事件 = %#v", events)
	}
}
