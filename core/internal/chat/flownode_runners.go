package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"microchat/internal/providers"
)

// flowNodeRun 是一次节点执行的局部状态。executeFlowGraph 只负责调度，
// 每种节点怎么产出由 flowNodeRunners 里的函数决定。
type flowNodeRun struct {
	service  *Service
	ctx      context.Context
	node     flowNode
	graph    flowGraph
	values   map[string]flowValue
	opts     flowRunOptions
	step     FlowTestStep
	messages []providers.ChatMessage
	typed    flowValue
	last     providers.Result
	lastSet  bool
}

type flowNodeRunner func(*flowNodeRun) error

var flowNodeRunners = map[string]flowNodeRunner{
	"input":   runInputNode,
	"text":    runTextNode,
	"history": runHistoryNode,
	"trim":    runTrimNode,
	"merge":   runMergeNode,
	"join":    runJoinNode,
	"output":  runOutputNode,
	"judge":   runJudgeNode,
	"llm":     runLLMNode,
}

func runInputNode(r *flowNodeRun) error {
	r.step.Label = "输入"
	r.step.Output = r.opts.Input
	return nil
}

func runTextNode(r *flowNodeRun) error {
	r.step.Label = "纯文本"
	r.step.Output = flowNodeDataString(r.node, "text")
	return nil
}

func runHistoryNode(r *flowNodeRun) error {
	r.step.Label = "历史数据"
	r.messages = append([]providers.ChatMessage{}, r.opts.History...)
	r.step.Output = fmt.Sprintf("messages[%d]", len(r.messages))
	return nil
}

func runTrimNode(r *flowNodeRun) error {
	r.step.Label = "裁剪 messages"
	for _, edge := range r.graph.Edges {
		if edge.Target == r.node.ID {
			r.messages = append(r.messages, sourceMessages(r.values, edge)...)
		}
	}
	r.messages = trimFlowMessages(filterFlowRoles(r.messages, flowTrimRoles(r.node)), flowTrimMode(r.node), flowTrimCount(r.node))
	r.step.Output = fmt.Sprintf("messages[%d]", len(r.messages))
	return nil
}

func runMergeNode(r *flowNodeRun) error {
	r.step.Label = "拼接 messages"
	for _, port := range flowMergeInputs(r.node) {
		for _, edge := range r.graph.Edges {
			if edge.Target == r.node.ID && (edge.TargetHandle == port || (port == "in-1" && edge.TargetHandle == "")) {
				r.messages = append(r.messages, sourceMessages(r.values, edge)...)
			}
		}
	}
	r.step.Output = fmt.Sprintf("messages[%d]", len(r.messages))
	return nil
}

func runJoinNode(r *flowNodeRun) error {
	r.step.Label = "拼接和复制"
	ordered := make([]string, 0, len(r.step.Inputs))
	for _, port := range flowJoinInputs(r.node) {
		for _, edge := range r.graph.Edges {
			if edge.Target != r.node.ID {
				continue
			}
			handle := edge.TargetHandle
			if handle == "" && port == "in-1" {
				handle = "in-1"
			}
			if handle == port {
				ordered = append(ordered, sourceValue(r.values, edge))
			}
		}
	}
	r.step.Output = joinFlowInputs(ordered, flowJoinSeparator(r.node))
	return nil
}

func runOutputNode(r *flowNodeRun) error {
	r.step.Label = "输出"
	for _, edge := range r.graph.Edges {
		if edge.Target != r.node.ID {
			continue
		}
		switch edge.TargetHandle {
		case "text":
			r.step.Output = sourceValue(r.values, edge)
		case "reasoning":
			r.step.Reasoning = sourceValue(r.values, edge)
		case "user":
			r.step.User = sourceValue(r.values, edge)
		}
	}
	return nil
}

func runJudgeNode(r *flowNodeRun) error {
	r.step.Label = "JEV 判断"
	questions, err := flowJudgeQuestions(r.node)
	if err != nil {
		return err
	}
	answers, err := r.service.flowJudge(r.ctx, r.opts.SessionID, r.node,
		strings.Join(r.step.Inputs, "\n\n"), questions, r.opts.ProvidersConfig)
	if err != nil {
		return err
	}
	value := any(answers)
	if key := flowNodeDataString(r.node, "answer_key"); key != "" {
		var ok bool
		value, ok = answers[key]
		if !ok {
			return fmt.Errorf("JEV 判断节点 %s 的 answer_key %q 不在 answers 里", r.node.ID, key)
		}
	}
	encoded, _ := json.Marshal(value)
	r.step.Output = string(encoded)
	switch strings.TrimSpace(flowNodeDataString(r.node, "answer_type")) {
	case "bool":
		r.typed.Bool = flowAnswerBool(value, flowNodeFloat(r.node, "threshold", 0.5))
	case "number":
		r.typed.Number = flowAnswerNumber(value)
	case "choice":
		r.typed.Choice = flowAnswerChoice(value)
	}
	return nil
}

func runLLMNode(r *flowNodeRun) error {
	r.step.Label = "调用 LLM"
	providerID, modelID := r.opts.DefaultProvider, r.opts.DefaultModel
	if value, ok := r.node.Data["provider"].(string); ok && strings.TrimSpace(value) != "" {
		providerID = strings.TrimSpace(value)
	}
	if value, ok := r.node.Data["model"].(string); ok && strings.TrimSpace(value) != "" {
		modelID = strings.TrimSpace(value)
	}
	started := time.Now()
	result, payload, err := r.service.flowLLMCall(r.ctx, r.node, r.graph.Edges, r.values, providerID, modelID, r.opts.SessionID, r.opts.ProvidersConfig)
	r.step.Provider = providerID
	r.step.Model = modelID
	r.step.Usage = result.Usage
	r.step.ElapsedMS = time.Since(started).Milliseconds()
	r.step.Payload = payload
	if err != nil {
		return err
	}
	r.last = result
	r.lastSet = true
	r.step.Output = result.Text
	r.step.Reasoning = result.Reasoning
	return nil
}
