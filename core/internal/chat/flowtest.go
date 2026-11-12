package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"microchat/internal/config"
	"microchat/internal/judge"
	"microchat/internal/providers"
	"microchat/internal/task"
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
	User      string          `json:"user,omitempty"`
	Status    string          `json:"status,omitempty"`
	Error     string          `json:"error,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms,omitempty"`
	// Payload：这一节点真会发出去的那一发（运行/测试都留档；密钥已打码）。
	Payload *providers.LastPayload `json:"payload,omitempty"`
}

type FlowTestResult struct {
	Output    string         `json:"output"`
	Reasoning string         `json:"reasoning,omitempty"`
	User      string         `json:"user,omitempty"`
	Steps     []FlowTestStep `json:"steps"`
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
	User      string
	Messages  []providers.ChatMessage
	// Bool / Number / Choice 是 judge 节点的类型化出口；Text 仍保留一份可读表示，
	// 旧节点不需要认识这些字段。
	Bool   *bool
	Number *float64
	Choice string
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
	// OnNode：执行器节点开始/结束的通知（运行时用它把 active node 写进 trace；测试可为 nil）。
	OnNode func(node flowNode, phase string)
}

type flowRunOutcome struct {
	Result      FlowTestResult
	Final       flowValue
	Usage       json.RawMessage
	UserMessage string
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

// executeFlowGraph：FlowAgent 的唯一调度器。节点行为在 flowNodeRunners，
// 这里只负责就绪、调用与传播；测试与聊天运行时共用。
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
	// 数据流执行：节点只在自己**已连接**的输入都收到值之后才可运行。
	// 拓扑顺序仍用来挑下一个可运行节点（保证确定性），但不再无条件把所有节点跑一遍。
	incoming := map[string][]flowEdge{}
	for _, edge := range graph.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}
	done := map[string]bool{}
	ready := func(node flowNode) bool {
		for _, edge := range incoming[node.ID] {
			if !done[edge.Source] {
				return false
			}
		}
		return true
	}
	for {
		var current *flowNode
		for index := range order {
			if done[order[index].ID] || !ready(order[index]) {
				continue
			}
			current = &order[index]
			break
		}
		if current == nil {
			if len(done) == len(order) {
				break
			}
			blocked := make([]string, 0)
			for _, node := range order {
				if !done[node.ID] {
					blocked = append(blocked, node.ID)
				}
			}
			err := fmt.Errorf("流程死锁：这些节点的输入永远等不到：%s", strings.Join(blocked, ", "))
			return flowRunOutcome{Result: FlowTestResult{Steps: steps}}, err
		}
		node := *current
		if opts.OnNode != nil {
			opts.OnNode(node, "start")
		}
		var nodeMessages []providers.ChatMessage
		inputs := []string{}
		for _, e := range graph.Edges {
			if e.Target == node.ID {
				inputs = append(inputs, sourceValue(values, e))
			}
		}
		step := FlowTestStep{ID: node.ID, Type: node.Type, Inputs: inputs}
		typed := flowValue{}
		if node.Type == "llm" && !flowNodeEnabled(node, graph.Edges, values) {
			step.Label = flowNodeLabel(node.Type)
			step.Status = "skipped"
			values[flowKey(node.ID, "out")] = flowValue{}
			values[flowKey(node.ID, "text")] = flowValue{}
			values[flowKey(node.ID, "reasoning")] = flowValue{}
			steps = append(steps, step)
			done[node.ID] = true
			if opts.OnNode != nil {
				opts.OnNode(node, "done")
			}
			continue
		}
		runner, ok := flowNodeRunners[node.Type]
		if !ok {
			err = fmt.Errorf("不认识的节点类型：%s", node.Type)
			step.Error = err.Error()
			steps = append(steps, step)
			return flowRunOutcome{Result: FlowTestResult{Steps: steps}}, err
		}
		run := &flowNodeRun{
			service: s, ctx: ctx, node: node, graph: graph, values: values, opts: opts, step: step,
		}
		if runErr := runner(run); runErr != nil {
			step = run.step
			step.Error = runErr.Error()
			steps = append(steps, step)
			return flowRunOutcome{Result: FlowTestResult{Steps: steps}}, runErr
		}
		step = run.step
		nodeMessages = run.messages
		typed = run.typed
		if run.lastSet {
			lastResult = run.last
		}
		values[flowKey(node.ID, "out")] = flowValue{Text: step.Output, Reasoning: step.Reasoning, User: step.User, Bool: typed.Bool, Number: typed.Number, Choice: typed.Choice}
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
		step.Status = "done"
		steps = append(steps, step)
		done[node.ID] = true
		if opts.OnNode != nil {
			opts.OnNode(node, "done")
		}
	}
	final := flowValue{}
	for _, node := range graph.Nodes {
		if node.Type == "output" {
			final = values[flowKey(node.ID, "out")]
		}
	}
	return flowRunOutcome{
		Result:      FlowTestResult{Output: final.Text, Reasoning: final.Reasoning, User: final.User, Steps: steps},
		UserMessage: final.User,
		Final:       final,
		Usage:       lastResult.Usage,
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

func flowLLMRetry(node flowNode) int {
	switch value := node.Data["max_retry"].(type) {
	case float64:
		if value >= 0 && value == float64(int(value)) {
			return int(value)
		}
	case int:
		if value >= 0 {
			return value
		}
	}
	return 3
}

// flowRetryable：取消/超时不再重试；配置类错误重试也不会变好，直接失败。
func flowRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstream *providers.UpstreamError
	if errors.As(err, &upstream) {
		return true
	}
	// 网络错误 / 空响应等由 providers 原样返回；统一按可重试处理。
	return true
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
	client := providers.NewClient(wire)
	maxRetry := flowLLMRetry(node)
	var result providers.Result
	for attempt := 0; ; attempt++ {
		result, err = client.Complete(ctx, wire, request, localReply(chosen))
		if err == nil {
			break
		}
		if attempt >= maxRetry || ctx.Err() != nil || !flowRetryable(err) {
			return providers.Result{}, payload, err
		}
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

func flowJoinInputs(node flowNode) []string {
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
		case "input", "text", "history", "trim", "merge", "llm", "join", "output", "judge":
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
			inputs := flowJoinInputs(node)
			seenInputs := map[string]bool{}
			for _, input := range inputs {
				input = strings.TrimSpace(input)
				if input == "" || seenInputs[input] {
					return fmt.Errorf("拼接节点 %s 有空的或重复的输入端口", node.ID)
				}
				seenInputs[input] = true
			}
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
		if rawRetry, ok := node.Data["max_retry"]; ok {
			switch value := rawRetry.(type) {
			case float64:
				if value < 0 || value != float64(int(value)) {
					return fmt.Errorf("LLM 节点 %s 的 max_retry 必须是非负整数", node.ID)
				}
			case int:
				if value < 0 {
					return fmt.Errorf("LLM 节点 %s 的 max_retry 必须是非负整数", node.ID)
				}
			default:
				return fmt.Errorf("LLM 节点 %s 的 max_retry 必须是非负整数", node.ID)
			}
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

// flowNodeFloat：节点里可选的数值配置（没有 / 非法就用默认）。
func flowNodeFloat(node flowNode, key string, fallback float64) float64 {
	raw, ok := node.Data[key]
	if !ok {
		return fallback
	}
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return parsed
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return parsed
		}
	}
	return fallback
}

// flowAnswerBool：把 JEV 的答案按 bool 解释。bool 直接认；数字按阈值；
// 常见的 {probability/confidence/score} 对象取其中的数。
func flowAnswerBool(value any, threshold float64) *bool {
	switch answer := value.(type) {
	case bool:
		return &answer
	case float64:
		result := answer >= threshold
		return &result
	case int:
		result := float64(answer) >= threshold
		return &result
	case map[string]any:
		for _, key := range []string{"probability", "confidence", "score", "value"} {
			if raw, ok := answer[key]; ok {
				if number := flowAnswerNumber(raw); number != nil {
					result := *number >= threshold
					return &result
				}
			}
		}
		if raw, ok := answer["bool"]; ok {
			if parsed, ok := raw.(bool); ok {
				return &parsed
			}
		}
	}
	if text, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true", "yes", "on", "1":
			result := true
			return &result
		case "false", "no", "off", "0":
			result := false
			return &result
		}
	}
	return nil
}

func flowAnswerNumber(value any) *float64 {
	switch answer := value.(type) {
	case float64:
		return &answer
	case int:
		number := float64(answer)
		return &number
	case map[string]any:
		for _, key := range []string{"score", "probability", "confidence", "value"} {
			if raw, ok := answer[key]; ok {
				if number := flowAnswerNumber(raw); number != nil {
					return number
				}
			}
		}
	}
	return nil
}

func flowAnswerChoice(value any) string {
	switch answer := value.(type) {
	case string:
		return answer
	case map[string]any:
		for _, key := range []string{"choice", "selected", "label"} {
			if raw, ok := answer[key]; ok {
				if text, ok := raw.(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

// flowNodeEnabled：LLM 节点的可选 `enabled` 控制端口。没接 = 默认启用；
// 接了且为 false ⇒ 跳过（不调模型、所有输出为空）。
func flowNodeEnabled(node flowNode, edges []flowEdge, values map[string]flowValue) bool {
	for _, edge := range edges {
		if edge.Target != node.ID || edge.TargetHandle != "enabled" {
			continue
		}
		value := values[flowKey(edge.Source, edge.SourceHandle)]
		if value.Bool != nil {
			return *value.Bool
		}
		text := strings.ToLower(strings.TrimSpace(value.Text))
		if text == "false" || text == "0" || text == "no" || text == "off" {
			return false
		}
	}
	return true
}

// flowJudgeQuestions：节点配置里的 questions 可以是 JSON 字符串（编辑器存的就是字符串），
// 也接受已经解码过的 object（手写测试）。非空且必须是 object。
func flowJudgeQuestions(node flowNode) (map[string]any, error) {
	raw, ok := node.Data["questions"]
	if !ok {
		return nil, errors.New("JEV 判断节点没有 questions")
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("JEV 判断节点的 questions 是空的")
		}
		var questions map[string]any
		if err := json.Unmarshal([]byte(value), &questions); err != nil {
			return nil, fmt.Errorf("JEV 判断节点的 questions 不是 JSON object：%w", err)
		}
		return questions, nil
	case map[string]any:
		if len(value) == 0 {
			return nil, errors.New("JEV 判断节点的 questions 是空的")
		}
		return value, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var questions map[string]any
		if err := json.Unmarshal(encoded, &questions); err != nil {
			return nil, fmt.Errorf("JEV 判断节点的 questions 不是 JSON object：%w", err)
		}
		return questions, nil
	}
}

// flowJudge：Flow 里的 JEV 那一发。裸调用 + Task 挂号（有会话时）。
//
// 它不走传统 Agent 的 judge 能力（那条已经删了）；provider/model 由节点自己给。
func (s *Service) flowJudge(ctx context.Context, sessionID string, node flowNode,
	stateText string, questions map[string]any, providersConfig config.ProvidersConfig) (judge.Answers, error) {
	providerID := flowNodeDataString(node, "provider")
	modelID := flowNodeDataString(node, "model")
	if providerID == "" || modelID == "" {
		return nil, errors.New("JEV 判断节点要填 provider 与 model（systemone 渠道与决策模型名）")
	}
	provider, ok := providersConfig.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("JEV 判断节点找不到渠道 %q", providerID)
	}
	var guard *task.Guard
	if sessionID != "" && s.Tasks != nil {
		guard = s.Tasks.Begin(task.KindJudgement, &sessionID, "JEV 判断 · 会话 "+shortID(sessionID))
		defer guard.Interrupted()
	}
	answers, err := judge.Evaluate(ctx, providers.FromConfig(provider), modelID, map[string]any{"text": stateText}, questions)
	if guard != nil {
		if err != nil {
			guard.Fail(err.Error())
		} else {
			guard.Succeed()
		}
	}
	return answers, err
}
