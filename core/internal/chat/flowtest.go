package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/state"
)

// FlowTest 是 FlowAgent 编辑器的「测试发送」：**只跑一次图，不落库、不建会话**。
//
// 这是运行时的最小验证器，不是完整 FlowAgent 运行时：
//   - DAG、单 input/output、agent 节点只能指向传统 Agent；
//   - 每个 agent 节点用一次性临时会话走现有提示词拼装 + Complete；
//   - 输入节点吃测试消息，join/output 只拼上游文本；
//   - 中间结果只回给编辑器，不碰 messages / summaries / 世界状态。
type FlowTestStep struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Label  string   `json:"label"`
	Inputs []string `json:"inputs,omitempty"`
	Output string   `json:"output,omitempty"`
	Error  string   `json:"error,omitempty"`
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
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
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
	_, agents, providersConfig := s.files()
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
	values := map[string]string{}
	steps := make([]FlowTestStep, 0, len(order))
	for _, node := range order {
		inputs := []string{}
		for _, e := range graph.Edges {
			if e.Target == node.ID {
				inputs = append(inputs, values[e.Source])
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
		case "agent":
			agentID := strings.TrimSpace(fmt.Sprint(node.Data["agent_id"]))
			agent, ok := agents.Resolve(agentID)
			if !ok {
				err = fmt.Errorf("节点 %s 指向的智能体不存在：%s", node.ID, agentID)
				step.Error = err.Error()
				steps = append(steps, step)
				return FlowTestResult{Steps: steps}, err
			}
			step.Label = "调用 " + agent.Name
			if agent.Kind == "flow" {
				err = errors.New("FlowAgent 不能调用另一个 FlowAgent")
				step.Error = err.Error()
				steps = append(steps, step)
				return FlowTestResult{Steps: steps}, err
			}
			text, callErr := s.testAgentCall(ctx, agent, joinFlowInputs(inputs), provider, modelID, providersConfig)
			if callErr != nil {
				step.Error = callErr.Error()
				steps = append(steps, step)
				return FlowTestResult{Steps: steps}, callErr
			}
			step.Output = text
		default:
			err = fmt.Errorf("不认识的节点类型：%s", node.Type)
			step.Error = err.Error()
			steps = append(steps, step)
			return FlowTestResult{Steps: steps}, err
		}
		values[node.ID] = step.Output
		steps = append(steps, step)
	}
	final := ""
	for _, node := range graph.Nodes {
		if node.Type == "output" {
			final = values[node.ID]
		}
	}
	return FlowTestResult{Output: final, Steps: steps}, nil
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

// testAgentCall：用一次性临时会话走一遍真正的提示词拼装 + 上游 Complete。
func (s *Service) testAgentCall(ctx context.Context, agent config.Agent, input, provider, modelID string, providersConfig config.ProvidersConfig) (string, error) {
	sid := uuid.NewString()
	session := model.Session{ID: sid, Provider: provider, Model: modelID, AgentID: agent.ID}
	messages := []model.Message{{ID: uuid.NewString(), SessionID: sid, Role: model.RoleUser, Content: input}}
	prompt, _ := s.EffectiveSystemPrompt(session)
	entries, presetActive := s.promptEntries(session, messages)
	if presetActive {
		prompt = strings.TrimSpace(session.SystemPrompt)
	}
	outgoing := state.BuildOutgoingWithPromptEntries(prompt, messages, nil, nil, false, entries)
	chosen := providers.SelectBackend(provider, modelID, providersConfig)
	wire := providers.FromConfig(chosen.Provider)
	result, err := providers.NewClient(wire).Complete(ctx, wire, providers.Request{
		Model: modelID, Messages: wireMessages(outgoing, nil), SessionID: sid,
	}, localReply(chosen))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Text), nil
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
