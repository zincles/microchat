package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"microchat/internal/config"
	"microchat/internal/providers"
)

// FlowTest 是 FlowAgent 编辑器的「测试发送」：**只跑一次图，不落库、不建会话**。
//
// 这是运行时的最小验证器，不是完整 FlowAgent 运行时：
//   - DAG、单 input/output；LLM 节点内部是手写的 Prompt 列表；
//   - 每个 LLM 节点按 role/content + 对应输入孔组装 messages 后走 Complete；
//   - LLM 节点有两个出口：reasoning（思考）与 text（正文）；
//   - 输入节点吃测试消息，join/output 只拼上游文本；
//   - 中间结果只回给编辑器，不碰 messages / summaries / 世界状态。
type FlowTestStep struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Label     string   `json:"label"`
	Inputs    []string `json:"inputs,omitempty"`
	Output    string   `json:"output,omitempty"`
	Reasoning string   `json:"reasoning,omitempty"`
	Error     string   `json:"error,omitempty"`
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
	Role    string `json:"role"`
	Content string `json:"content"`
}

type flowValue struct {
	Text      string
	Reasoning string
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
	if err := validateTestGraph(graph); err != nil {
		return FlowTestResult{}, err
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
	order, err := topoFlow(graph)
	if err != nil {
		return FlowTestResult{}, err
	}
	values := map[string]flowValue{}
	steps := make([]FlowTestStep, 0, len(order))
	for _, node := range order {
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
			step.Output = message
		case "join":
			step.Label = "拼接"
			step.Output = joinFlowInputs(inputs)
		case "output":
			step.Label = "输出"
			step.Output = joinFlowInputs(inputs)
		case "llm":
			step.Label = "调用 LLM"
			text, reasoning, callErr := s.testLLMCall(ctx, node, graph.Edges, values, provider, modelID, providersConfig)
			if callErr != nil {
				step.Error = callErr.Error()
				steps = append(steps, step)
				return FlowTestResult{Steps: steps}, callErr
			}
			step.Output = text
			step.Reasoning = reasoning
		default:
			err = fmt.Errorf("不认识的节点类型：%s", node.Type)
			step.Error = err.Error()
			steps = append(steps, step)
			return FlowTestResult{Steps: steps}, err
		}
		values[flowKey(node.ID, "out")] = flowValue{Text: step.Output, Reasoning: step.Reasoning}
		if node.Type == "llm" {
			values[flowKey(node.ID, "text")] = flowValue{Text: step.Output}
			values[flowKey(node.ID, "reasoning")] = flowValue{Reasoning: step.Reasoning}
		}
		steps = append(steps, step)
	}
	final := ""
	for _, node := range graph.Nodes {
		if node.Type == "output" {
			final = values[flowKey(node.ID, "out")].Text
		}
	}
	return FlowTestResult{Output: final, Steps: steps}, nil
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

func joinFlowInputs(inputs []string) string {
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input) != "" {
			parts = append(parts, input)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// testLLMCall：把 LLM 节点里手写的 Prompt 条目按顺序变成 messages，再真正调用上游。
// 每个 Prompt 条目自己的左侧输入孔（targetHandle = prompt-<id>）只喂那一条；
// 手写内容和上游内容同时存在时，按“手写在前、上游在后”用空行隔开。
func (s *Service) testLLMCall(ctx context.Context, node flowNode, edges []flowEdge,
	values map[string]flowValue, provider, modelID string, providersConfig config.ProvidersConfig) (string, string, error) {
	raw, ok := node.Data["prompts"]
	if !ok {
		return "", "", errors.New("LLM 节点没有 Prompt 条目")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return "", "", err
	}
	var prompts []flowPrompt
	if err := json.Unmarshal(data, &prompts); err != nil {
		return "", "", fmt.Errorf("Prompt 条目解析失败：%w", err)
	}
	messages := make([]providers.ChatMessage, 0, len(prompts))
	for _, prompt := range prompts {
		role := strings.TrimSpace(prompt.Role)
		if role != "system" && role != "user" && role != "assistant" {
			return "", "", fmt.Errorf("Prompt %s 的 role 不合法：%s", prompt.ID, prompt.Role)
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
		return "", "", errors.New("LLM 节点没有任何非空 Prompt")
	}
	chosen := providers.SelectBackend(provider, modelID, providersConfig)
	wire := providers.FromConfig(chosen.Provider)
	result, err := providers.NewClient(wire).Complete(ctx, wire, providers.Request{
		Model: modelID, Messages: messages, SessionID: uuid.NewString(),
	}, localReply(chosen))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(result.Text), strings.TrimSpace(result.Reasoning), nil
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
	}
	if counts["input"] != 1 {
		return errors.New("流程图需要恰好一个输入节点")
	}
	if counts["output"] != 1 {
		return errors.New("流程图需要恰好一个输出节点")
	}
	for _, node := range graph.Nodes {
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
			if prompt.Role != "system" && prompt.Role != "user" && prompt.Role != "assistant" {
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
