package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/reroll"
	"microchat/internal/store"
)

func TestFlowJudgeNodeCallsSystemOne(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"involves":{"choice":"yes","probability":0.82}}}`))
	}))
	t.Cleanup(upstream.Close)
	providersConfig := config.ProvidersConfig{Providers: []config.Provider{{
		ID: "jev", Vendor: "custom", Protocol: "systemone", BaseURL: upstream.URL,
	}}}
	graph := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "judge", Type: "judge", Data: map[string]any{
			"provider": "jev", "model": "jev-latest", "answer_key": "involves",
			"questions": `{"involves":"是否涉及这个角色？"}`,
		}},
		{ID: "out", Type: "output"},
	}, Edges: []flowEdge{
		{ID: "e1", Source: "in", SourceHandle: "out", Target: "judge", TargetHandle: "state"},
		{ID: "e2", Source: "judge", SourceHandle: "out", Target: "out", TargetHandle: "text"},
	}}
	out, err := (&Service{}).executeFlowGraph(context.Background(), graph, flowRunOptions{
		Input: "你推门进来", ProvidersConfig: providersConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != "jev-latest" || body["state"] == nil {
		t.Fatalf("JEV 请求体不对：%v", body)
	}
	if !strings.Contains(out.Final.Text, `"choice":"yes"`) {
		t.Fatalf("answer_key 输出 = %q", out.Final.Text)
	}
}

func TestFlowTextNodeOutputsConstant(t *testing.T) {
	graph := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "text", Type: "text", Data: map[string]any{"text": "写死的常量"}},
		{ID: "out", Type: "output"},
	}, Edges: []flowEdge{
		{ID: "e1", Source: "text", SourceHandle: "out", Target: "out", TargetHandle: "text"},
	}}
	out, err := (&Service{}).executeFlowGraph(context.Background(), graph, flowRunOptions{Input: "原话"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Final.Text != "写死的常量" {
		t.Fatalf("纯文本节点输出 = %q", out.Final.Text)
	}
	if out.Final.User != "" {
		t.Fatalf("纯文本节点不该碰用户消息：%+v", out)
	}
}

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
	stubConfig := config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}
	h := newHarness(t, stubConfig, "stub", "stub")
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
	stubConfig := config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}
	h := newHarness(t, stubConfig, "stub", "stub")
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
	stubConfig := config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}
	h := newHarness(t, stubConfig, "stub", "stub")
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

func TestFlowOutputUserPortOverridesHistory(t *testing.T) {
	agent := config.Agent{Flow: json.RawMessage(`{"version":1,"nodes":[{"id":"in","type":"input"},{"id":"out","type":"output"}],"edges":[{"source":"in","sourceHandle":"out","target":"out","targetHandle":"user"}]}`)}
	graph := flowGraph{Nodes: []flowNode{{ID: "in", Type: "input"}, {ID: "out", Type: "output"}}, Edges: []flowEdge{{Source: "in", SourceHandle: "out", Target: "out", TargetHandle: "user"}}}
	out, err := (&Service{}).executeFlowGraph(context.Background(), graph, flowRunOptions{Input: "原问题"})
	if err != nil {
		t.Fatal(err)
	}
	if out.UserMessage != "原问题" || out.Result.User != "原问题" {
		t.Fatalf("user 端口 = %+v", out)
	}
	_ = agent
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
		OnNode: func(node flowNode, phase string) { events = append(events, node.ID+":"+phase) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || !strings.Contains(strings.Join(events, ","), "llm:start") {
		t.Fatalf("节点执行事件 = %#v", events)
	}
}

func TestFlowAgentStoresOutputUserPort(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","data":{}},
		{"id":"llm","type":"llm","data":{"prompts":[{"id":"p1","role":"system","content":"写"}]}},
		{"id":"out","type":"output","data":{}}
	],"edges":[{"id":"e0","source":"in","target":"out","targetHandle":"user"},{"id":"e1","source":"llm","sourceHandle":"text","target":"out","targetHandle":"text"}]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	if _, err := h.service.Accept(h.session, "原话"); err != nil {
		t.Fatal(err)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[0].Content != "原话" {
		t.Fatalf("FlowAgent 用户消息应写入 output.user：%+v", messages)
	}
}

func TestFlowOutputUserPortOverridesStoredUser(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","data":{}},
		{"id":"llm","type":"llm","data":{"prompts":[{"id":"p1","role":"system","content":"改"}]}},
		{"id":"out","type":"output","data":{}}
	],"edges":[{"id":"e0","source":"in","sourceHandle":"out","target":"llm","targetHandle":"prompt-p1"},{"id":"e1","source":"llm","sourceHandle":"text","target":"out","targetHandle":"user"}]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	if _, err := h.service.Accept(h.session, "原话"); err != nil {
		t.Fatal(err)
	}
	messages := h.settle(t)
	if len(messages) != 2 || messages[0].Content != dummyReply {
		t.Fatalf("output.user 应覆盖入库用户消息：%+v", messages)
	}
}

func TestFlowResendStoresOutputUserPort(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	flow := json.RawMessage(`{"version":1,"nodes":[
		{"id":"in","type":"input","data":{}},
		{"id":"llm","type":"llm","data":{"prompts":[{"id":"p1","role":"system","content":"改"}]}},
		{"id":"out","type":"output","data":{}}
	],"edges":[{"id":"e0","source":"in","sourceHandle":"out","target":"llm","targetHandle":"prompt-p1"},{"id":"e1","source":"llm","sourceHandle":"text","target":"out","targetHandle":"user"}]}`)
	if err := config.SaveJSON(h.configDir+"/agents.json", config.AgentsConfig{Agents: []config.Agent{{
		ID: "flow", Kind: "flow", Flow: flow,
	}}}); err != nil {
		t.Fatal(err)
	}
	h.session.AgentID = "flow"
	ids, err := store.MintOrderedIDs(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.InsertMessage(model.Message{
		ID: ids[0], SessionID: h.session.ID, Role: model.RoleUser, Content: "原话",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Resend(h.session); err != nil {
		t.Fatal(err)
	}
	messages := h.settle(t)
	var user *model.Message
	for index := range messages {
		if messages[index].Role == model.RoleUser {
			user = &messages[index]
		}
	}
	if user == nil || user.Content != dummyReply {
		t.Fatalf("Resend 也要用 output.user 覆盖被回答的用户消息：%+v", messages)
	}
}

func TestFlowLLMRetriesThenSucceeds(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 3 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"成功"}}]}`))
	}))
	defer upstream.Close()
	stubConfig := config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}
	h := newHarness(t, stubConfig, "stub", "stub")
	graph := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "llm", Type: "llm", Data: map[string]any{"max_retry": float64(3), "prompts": []any{map[string]any{"id": "p1", "role": "system", "content": "写"}}}},
		{ID: "out", Type: "output"},
	}, Edges: []flowEdge{{ID: "e", Source: "llm", SourceHandle: "text", Target: "out", TargetHandle: "text"}}}
	out, err := h.service.executeFlowGraph(context.Background(), graph, flowRunOptions{
		DefaultProvider: "stub", DefaultModel: "stub", ProvidersConfig: stubConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || out.Result.Output != "成功" {
		t.Fatalf("重试次数 = %d, output = %q", calls, out.Result.Output)
	}
}

func TestFlowLLMFailureFailsWholeFlow(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	stubConfig := config.ProvidersConfig{Providers: []config.Provider{{
		ID: "stub", Kind: "openai-compat", BaseURL: upstream.URL, Identity: "bare",
	}}}
	h := newHarness(t, stubConfig, "stub", "stub")
	graph := flowGraph{Nodes: []flowNode{
		{ID: "in", Type: "input"},
		{ID: "llm", Type: "llm", Data: map[string]any{"max_retry": float64(2), "prompts": []any{map[string]any{"id": "p1", "role": "system", "content": "写"}}}},
		{ID: "out", Type: "output"},
	}, Edges: []flowEdge{{ID: "e", Source: "llm", SourceHandle: "text", Target: "out", TargetHandle: "text"}}}
	_, err := h.service.executeFlowGraph(context.Background(), graph, flowRunOptions{
		DefaultProvider: "stub", DefaultModel: "stub", ProvidersConfig: stubConfig,
	})
	if err == nil {
		t.Fatal("节点重试耗尽后应让整个 Flow 失败")
	}
	if calls != 3 {
		t.Fatalf("max_retry=2 应总共尝试 3 次，实际 %d", calls)
	}
}
