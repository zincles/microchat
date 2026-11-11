package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"microchat/internal/config"
	"microchat/internal/providers"
)

// FlowTest 是 FlowAgent 编辑器的「测试发送」：**只跑一次图，不落库、不建会话**。
//
// 测试接口与聊天运行时共用 executeFlowGraph；测试这一侧只把 steps 回给编辑器，
// 不建会话、不落库。当前执行器的边界（第一版聊天也在用）：
//   - DAG、单 input/output；history 输出 messages[]，trim 按条数裁剪 messages[]；
//     LLM 节点内部是手写的 Prompt 列表；
//   - 每个 LLM 节点按 role/content + 对应输入孔组装 messages 后走 Complete（非流式、无工具）；
//   - LLM 节点有两个出口：reasoning（思考）与 text（正文）；
//   - 输入节点吃测试消息 / 本轮用户输入，join/output 只拼上游文本；
//   - 中间结果不落库；聊天运行时只落 output 的最终文本。
type FlowTestStep struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Label     string          `json:"label"`
	Inputs    []string        `json:"inputs,omitempty"`
	Output    string          `json:"output,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	Error     string          `json:"error,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms,omitempty"`
	// Payload：这一节点真会发出去的那一发（运行/测试都留档；密钥已打码）。
	Payload *providers.LastPayload `json:"payload,omitempty"`
}

type FlowTestResult struct {
	Output string         `json:"output"`
	Steps  []FlowTestStep `json:"steps"`
}

type flowGraph struct {
	Version int        `json:"version"`
	Nodes   []flowNode `json:"nodes"`
	Edges   []flowEdge `json:"edges"`
}
type flowNode struct {
	ID   string         `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}
type flowEdge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	SourceHandle string `json:"sourceHandle,omitempty"`
	Target       string `json:"target"`
	TargetHandle string `json:"targetHandle,omitempty"`
}

type flowPrompt struct {
	ID      string `json:"id"`
	Kind    string `json:"kind,omitempty"` // "" / prompt / history
	Role    string `json:"role"`
	Content string `json:"content"`
}

type flowValue struct {
	Text      string
	Reasoning string
	Messages  []providers.ChatMessage
}

const defaultJoinSeparator = "\n\n---\n\n"

// flowRunOptions：一次图执行的输入。测试接口传空 SessionID + 空 History；聊天运行时
// 传真实会话 id 与会话历史。
type flowRunOptions struct {
	SessionID       string
	Input           string
	History         []providers.ChatMessage
	DefaultProvider string
	DefaultModel    string
	ProvidersConfig config.ProvidersConfig
}

type flowRunOutcome struct {
	Result FlowTestResult
	Final  flowValue
	Usage  json.RawMessage
}

// TestFlow：执行一次编辑器测试。provider/model 空 ⇒ 读 config.json 的 defaults。
func (s *Service) TestFlow(ctx context.Context, flowAgent config.Agent, message, provider, modelID string) (FlowTestResult, error) {
	if flowAgent.Kind != "flow" {
		return FlowTestResult{}, errors.New("这个 Agent 不是 FlowAgent")
	}
	var graph flowGraph
	if err := json.Unmarshal(flowAgent.Flow, &graph); err != nil {
		return FlowTestResult{}, fmt.Errorf("流程 JSON 解析失败：%w", err)
	}
	cfg, err := config.LoadConfig(s.Paths)
	if err != nil {
		return FlowTestResult{}, err
	}
	_, _, providersConfig := s.files()
	if strings.TrimSpace(provider) == "" {
		provider = cfg.Defaults.Provider
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = cfg.Defaults.Model
	}
	outcome, err := s.executeFlowGraph(ctx, graph, flowRunOptions{
		Input:           message,
		DefaultProvider: provider,
		DefaultModel:    modelID,
		ProvidersConfig: providersConfig,
	})
	return outcome.Result, err
}

// executeFlowGraph：FlowAgent 的唯一执行器。编辑器测试与聊天运行时共用这一份 switch，
// 区别只在 opts：测试没有会话（history 空、每次 LLM 用随机 id），运行时骑真实会话。
func (s *Service) executeFlowGraph(ctx context.Context, graph flowGraph, opts flowRunOptions) (flowRunOutcome, error) {
	if err := validateTestGraph(graph); err != nil {
		return flowRunOutcome{}, err
	}
	order, err := topoFlow(graph)
	if err != nil {
		return flowRunOutcome{}, err
	}
	values := map[string]flowValue{}
	steps := make([]FlowTestStep, 0, len(order))
	var lastResult providers.Result
	for _, node := range order {
		var nodeMessages []providers.ChatMessage
		inputs := []string{}
		for _, e := range graph.Edges {
			if e.Target == node.ID {
				inputs = append(inputs, sourceValue(values, e))
			}
		}
		step := FlowTestStep{ID: node.ID, Type: node.Type, Inputs: inputs}
		switch node.Type {
		case "input":
			step.Label = "输入"
			step.Output = opts.Input
		case "history":
			step.Label = "历史数据"
			nodeMessages = append([]providers.ChatMessage{}, opts.History...)
			step.Output = fmt.Sprintf("messages[%d]", len(nodeMessages))
		case "trim":
			step.Label = "裁剪 messages"
			for _, edge := range graph.Edges {
				if edge.Target == node.ID {
					nodeMessages = append(nodeMessages, sourceMessages(values, edge)...)
				}
			}
			nodeMessages = trimFlowMessages(filterFlowRoles(nodeMessages, flowTrimRoles(node)), flowTrimMode(node), flowTrimCount(node))
			step.Output = fmt.Sprintf("messages[%d]", len(nodeMessages))
		case "merge":
			step.Label = "拼接 messages"
			for _, port := range flowMergeInputs(node) {
				for _, edge := range graph.Edges {
					if edge.Target == node.ID && (edge.TargetHandle == port || (port == "in-1" && edge.TargetHandle == "")) {
						nodeMessages = append(nodeMessages, sourceMessages(values, edge)...)
					}
				}
			}
			step.Output = fmt.Sprintf("messages[%d]", len(nodeMessages))
		case "join":
			step.Label = "拼接和复制"
			step.Output = joinFlowInputs(inputs, flowJoinSeparator(node))
		case "output":
			step.Label = "输出"
			step.Output = joinFlowInputs(inputs, defaultJoinSeparator)
		case "llm":
			step.Label = "调用 LLM"
			nodeProvider, nodeModel := opts.DefaultProvider, opts.DefaultModel
			if value, ok := node.Data["provider"].(string); ok && strings.TrimSpace(value) != "" {
				nodeProvider = strings.TrimSpace(value)
			}
			if value, ok := node.Data["model"].(string); ok && strings.TrimSpace(value) != "" {
				nodeModel = strings.TrimSpace(value)
			}
			callStarted := time.Now()
			result, payload, callErr := s.flowLLMCall(ctx, node, graph.Edges, values, nodeProvider, nodeModel, opts.SessionID, opts.ProvidersConfig)
			step.Provider = nodeProvider
			step.Model = nodeModel
			step.Usage = result.Usage
			step.ElapsedMS = time.Since(callStarted).Milliseconds()
			step.Payload = payload
			if callErr != nil {
				step.Error = callErr.Error()
				steps = append(steps, step)
				return flowRunOutcome{Result: FlowTestResult{Steps: steps}}, callErr
			}
			lastResult = result
			step.Output = result.Text
			step.Reasoning = result.Reasoning
		default:
			err = fmt.Errorf("不认识的节点类型：%s", node.Type)
			step.Error = err.Error()
			steps = append(steps, step)
			return flowRunOutcome{Result: FlowTestResult{Steps: steps}}, err
		}
		values[flowKey(node.ID, "out")] = flowValue{Text: step.Output, Reasoning: step.Reasoning}
		if node.Type == "history" || node.Type == "trim" || node.Type == "merge" {
			values[flowKey(node.ID, "out")] = flowValue{Text: step.Output, Messages: nodeMessages}
		}
		if node.Type == "join" {
			for _, port := range flowJoinOutputs(node) {
				values[flowKey(node.ID, port)] = flowValue{Text: step.Output}
			}
		}
		if node.Type == "llm" {
			values[flowKey(node.ID, "text")] = flowValue{Text: step.Output}
			values[flowKey(node.ID, "reasoning")] = flowValue{Reasoning: step.Reasoning}
		}
		steps = append(steps, step)
	}
	final := flowValue{}
	for _, node := range graph.Nodes {
		if node.Type == "output" {
			final = values[flowKey(node.ID, "out")]
		}
	}
	return flowRunOutcome{
		Result: FlowTestResult{Output: final.Text, Steps: steps},
		Final:  final,
		Usage:  lastResult.Usage,
	}, nil
}

func flowKey(id, handle string) string {
	if strings.TrimSpace(handle) == "" {
		handle = "text"
	}
	return id + "\x00" + handle
}

// sourceValue：按 sourceHandle 取上游出口；旧图没有 sourceHandle 时回落到 out/text。
func sourceValue(values map[string]flowValue, edge flowEdge) string {
	if value, ok := values[flowKey(edge.Source, edge.SourceHandle)]; ok {
		if edge.SourceHandle == "reasoning" {
			return value.Reasoning
		}
		return value.Text
	}
	if value, ok := values[flowKey(edge.Source, "out")]; ok {
		return value.Text
	}
	return ""
}

func flowJoinSeparator(node flowNode) string {
	if separator, ok := node.Data["separator"].(string); ok {
		return separator
	}
	return defaultJoinSeparator
}

func sourceMessages(values map[string]flowValue, edge flowEdge) []providers.ChatMessage {
	value, ok := values[flowKey(edge.Source, edge.SourceHandle)]
	if !ok {
		value = values[flowKey(edge.Source, "out")]
	}
	return value.Messages
}

func flowTrimMode(node flowNode) string {
	if mode, ok := node.Data["mode"].(string); ok {
		switch mode {
		case "tail", "head", "drop-head", "drop-tail":
			return mode
		}
	}
	return "tail"
}

func flowTrimRoles(node flowNode) []string {
	raw, ok := node.Data["roles"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var roles []string
	if json.Unmarshal(data, &roles) != nil {
		return nil
	}
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		switch role {
		case "system", "user", "assistant":
			out = append(out, role)
		}
	}
	return out
}

// filterFlowRoles：roles 为空 = 全部角色；否则只留列出的角色。
func filterFlowRoles(messages []providers.ChatMessage, roles []string) []providers.ChatMessage {
	if len(roles) == 0 {
		return messages
	}
	allowed := map[string]bool{}
	for _, role := range roles {
		allowed[role] = true
	}
	out := make([]providers.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if allowed[message.Role] {
			out = append(out, message)
		}
	}
	return out
}

func flowTrimCount(node flowNode) int {
	switch value := node.Data["count"].(type) {
	case float64:
		if value >= 1 && value == float64(int(value)) {
			return int(value)
		}
	case int:
		if value >= 1 {
			return value
		}
	}
	return 10
}

// trimFlowMessages 是裁剪节点唯一的语义：count 永远按“条”算，不猜 token。
func trimFlowMessages(messages []providers.ChatMessage, mode string, count int) []providers.ChatMessage {
	if count < 1 || len(messages) == 0 {
		return messages
	}
	switch mode {
	case "head":
		if len(messages) > count {
			return messages[:count]
		}
	case "drop-head":
		if len(messages) > count {
			return messages[count:]
		}
		return nil
	case "drop-tail":
		if len(messages) > count {
			return messages[:len(messages)-count]
		}
		return nil
	default: // tail
		if len(messages) > count {
			return messages[len(messages)-count:]
		}
	}
	return messages
}

func joinFlowInputs(inputs []string, separator string) string {
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input) != "" {
			parts = append(parts, input)
		}
	}
	return strings.Join(parts, separator)
}

// testLLMCall：把 LLM 节点里手写的 Prompt 条目按顺序变成 messages，再真正调用上游。
// 每个 Prompt 条目自己的左侧输入孔（targetHandle = prompt-<id>）只喂那一条；
// 手写内容和上游内容同时存在时，按“手写在前、上游在后”用空行隔开。
func (s *Service) flowLLMCall(ctx context.Context, node flowNode, edges []flowEdge,
	values map[string]flowValue, provider, modelID, sessionID string, providersConfig config.ProvidersConfig) (providers.Result, *providers.LastPayload, error) {
	raw, ok := node.Data["prompts"]
	if !ok {
		return providers.Result{}, nil, errors.New("LLM 节点没有 Prompt 条目")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return providers.Result{}, nil, err
	}
	var prompts []flowPrompt
	if err := json.Unmarshal(data, &prompts); err != nil {
		return providers.Result{}, nil, fmt.Errorf("Prompt 条目解析失败：%w", err)
	}
	messages := make([]providers.ChatMessage, 0, len(prompts))
	for _, prompt := range prompts {
		if prompt.Kind == "history" {
			handle := "history-" + prompt.ID
			for _, edge := range edges {
				if edge.Target == node.ID && edge.TargetHandle == handle {
					messages = append(messages, sourceMessages(values, edge)...)
				}
			}
			continue
		}
		role := strings.TrimSpace(prompt.Role)
		if role != "system" && role != "user" && role != "assistant" {
			return providers.Result{}, nil, fmt.Errorf("Prompt %s 的 role 不合法：%s", prompt.ID, prompt.Role)
		}
		parts := []string{}
		if strings.TrimSpace(prompt.Content) != "" {
			parts = append(parts, strings.TrimSpace(prompt.Content))
		}
		handle := "prompt-" + prompt.ID
		for _, edge := range edges {
			if edge.Target == node.ID && edge.TargetHandle == handle {
				if text := strings.TrimSpace(sourceValue(values, edge)); text != "" {
					parts = append(parts, text)
				}
			}
		}
		content := strings.Join(parts, "\n\n")
		if content == "" {
			continue
		}
		messages = append(messages, providers.ChatMessage{Role: role, Content: content})
	}
	if len(messages) == 0 {
		return providers.Result{}, nil, errors.New("LLM 节点没有任何非空 Prompt")
	}
	chosen := providers.SelectBackend(provider, modelID, providersConfig)
	if chosen.Name != providers.BackendFallback && chosen.Name != providers.BackendDummy {
		chosen.Provider.Protocol = string(providers.ResolveProtocolFor(s.Store.ModelRoute, chosen.Provider.ID, modelID, providers.Protocol(chosen.Provider.Protocol)))
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = uuid.NewString()
	}
	wire := providers.FromConfig(chosen.Provider)
	request := providers.Request{Model: modelID, Messages: messages, SessionID: sessionID}
	var payload *providers.LastPayload
	if built, buildErr := providers.Build(wire, request); buildErr == nil {
		if snapshot, snapErr := providers.Snapshot(built, time.Now()); snapErr == nil {
			payload = &snapshot
		}
	}
	result, err := providers.NewClient(wire).Complete(ctx, wire, request, localReply(chosen))
	if err != nil {
		return providers.Result{}, payload, err
	}
	result.Text = strings.TrimSpace(result.Text)
	result.Reasoning = strings.TrimSpace(result.Reasoning)
	return result, payload, nil
}

func flowMergeInputs(node flowNode) []string {
	raw, ok := node.Data["inputs"]
	if !ok {
		return []string{"in-1", "in-2"}
	}
	data, _ := json.Marshal(raw)
	var inputs []string
	if json.Unmarshal(data, &inputs) != nil || len(inputs) == 0 {
		return []string{"in-1", "in-2"}
	}
	return inputs
}

func flowJoinOutputs(node flowNode) []string {
	raw, ok := node.Data["outputs"]
	if !ok {
		return []string{"out-1"}
	}
	data, _ := json.Marshal(raw)
	var outputs []string
	if json.Unmarshal(data, &outputs) != nil || len(outputs) == 0 {
		return []string{"out-1"}
	}
	return outputs
}

func validateTestGraph(graph flowGraph) error {
	if len(graph.Nodes) == 0 {
		return errors.New("流程图是空的")
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, node := range graph.Nodes {
		if strings.TrimSpace(node.ID) == "" || seen[node.ID] {
			return errors.New("节点 id 为空或重复")
		}
		seen[node.ID] = true
		counts[node.Type]++
		switch node.Type {
		case "input", "history", "trim", "merge", "llm", "join", "output":
		default:
			return fmt.Errorf("不认识的节点类型：%s", node.Type)
		}
	}
	if counts["input"] != 1 {
		return errors.New("流程图需要恰好一个输入节点")
	}
	if counts["output"] != 1 {
		return errors.New("流程图需要恰好一个输出节点")
	}
	for _, node := range graph.Nodes {
		if node.Type == "trim" {
			rawCount, ok := node.Data["count"]
			if ok {
				switch value := rawCount.(type) {
				case float64:
					if value < 1 || value != float64(int(value)) {
						return fmt.Errorf("裁剪节点 %s 的条数必须是正整数", node.ID)
					}
				case int:
					if value < 1 {
						return fmt.Errorf("裁剪节点 %s 的条数必须是正整数", node.ID)
					}
				default:
					return fmt.Errorf("裁剪节点 %s 的条数必须是正整数", node.ID)
				}
			}
			if mode, ok := node.Data["mode"]; ok && mode != nil {
				text, _ := mode.(string)
				switch text {
				case "tail", "head", "drop-head", "drop-tail":
				default:
					return fmt.Errorf("裁剪节点 %s 的裁剪方式不合法", node.ID)
				}
			}
			if rawRoles, ok := node.Data["roles"]; ok && rawRoles != nil {
				data, _ := json.Marshal(rawRoles)
				var roles []string
				if json.Unmarshal(data, &roles) != nil {
					return fmt.Errorf("裁剪节点 %s 的角色不合法", node.ID)
				}
				for _, role := range roles {
					if role != "system" && role != "user" && role != "assistant" {
						return fmt.Errorf("裁剪节点 %s 的角色不合法", node.ID)
					}
				}
			}
		}
		if node.Type == "merge" {
			inputs := flowMergeInputs(node)
			seenInputs := map[string]bool{}
			for _, input := range inputs {
				input = strings.TrimSpace(input)
				if input == "" || seenInputs[input] {
					return fmt.Errorf("拼接 messages 节点 %s 有空的或重复的输入端口", node.ID)
				}
				seenInputs[input] = true
			}
		}
		if node.Type == "join" {
			outputs := flowJoinOutputs(node)
			seenPorts := map[string]bool{}
			for _, port := range outputs {
				port = strings.TrimSpace(port)
				if port == "" || seenPorts[port] {
					return fmt.Errorf("拼接节点 %s 有空的或重复的输出端口", node.ID)
				}
				seenPorts[port] = true
			}
		}
		if node.Type != "llm" {
			continue
		}
		raw, ok := node.Data["prompts"]
		if !ok {
			return fmt.Errorf("LLM 节点 %s 没有 Prompt 条目", node.ID)
		}
		data, _ := json.Marshal(raw)
		var prompts []flowPrompt
		if err := json.Unmarshal(data, &prompts); err != nil || len(prompts) == 0 {
			return fmt.Errorf("LLM 节点 %s 的 Prompt 条目为空或格式不对", node.ID)
		}
		seenPrompts := map[string]bool{}
		for _, prompt := range prompts {
			if strings.TrimSpace(prompt.ID) == "" || seenPrompts[prompt.ID] {
				return fmt.Errorf("LLM 节点 %s 有空的或重复的 Prompt id", node.ID)
			}
			seenPrompts[prompt.ID] = true
			if prompt.Kind != "" && prompt.Kind != "prompt" && prompt.Kind != "history" {
				return fmt.Errorf("Prompt %s 的 kind 不合法：%s", prompt.ID, prompt.Kind)
			}
			if prompt.Kind != "history" && prompt.Role != "system" && prompt.Role != "user" && prompt.Role != "assistant" {
				return fmt.Errorf("Prompt %s 的 role 只能是 system / user / assistant", prompt.ID)
			}
		}
	}
	for _, e := range graph.Edges {
		if !seen[e.Source] || !seen[e.Target] {
			return errors.New("连线指向不存在的节点")
		}
	}
	return nil
}

func topoFlow(graph flowGraph) ([]flowNode, error) {
	byID := map[string]flowNode{}
	indegree := map[string]int{}
	for _, n := range graph.Nodes {
		byID[n.ID] = n
		indegree[n.ID] = 0
	}
	for _, e := range graph.Edges {
		indegree[e.Target]++
	}
	queue := []string{}
	for id, d := range indegree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	out := make([]flowNode, 0, len(graph.Nodes))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, byID[id])
		for _, e := range graph.Edges {
			if e.Source != id {
				continue
			}
			indegree[e.Target]--
			if indegree[e.Target] == 0 {
				queue = append(queue, e.Target)
			}
		}
	}
	if len(out) != len(graph.Nodes) {
		return nil, errors.New("流程图里有环")
	}
	return out, nil
}
