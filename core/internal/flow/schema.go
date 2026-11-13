// Package flow：FlowAgent 图的**唯一结构权威**。
//
// 为什么单独成包：agents.json 里存的是原始 JSON，但“能存”不等于“能跑”。
// 这里把版本、节点、边的形状，以及每种节点 data 的字段一次说清；
// 保存与启动都过它，运行时不再靠 map[string]any 猜字段。
package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const Version = 1

type Graph struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Node struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Position Position        `json:"position"`
	Data     json.RawMessage `json:"data"`
}

type Edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	SourceHandle string `json:"sourceHandle"`
	Target       string `json:"target"`
	TargetHandle string `json:"targetHandle"`
}

type Prompt struct {
	ID      string `json:"id"`
	Kind    string `json:"kind,omitempty"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type textData struct {
	Text string `json:"text"`
}

type trimData struct {
	Mode  string   `json:"mode"`
	Count *int     `json:"count"`
	Roles []string `json:"roles"`
}

type mergeData struct {
	Inputs []string `json:"inputs"`
}

type joinData struct {
	Inputs    []string `json:"inputs"`
	Outputs   []string `json:"outputs"`
	Separator string   `json:"separator"`
}

type llmData struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	MaxRetry *int     `json:"max_retry"`
	Width    *float64 `json:"width"`
	Height   *float64 `json:"height"`
	Prompts  []Prompt `json:"prompts"`
}

type judgeData struct {
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Questions  json.RawMessage `json:"questions"`
	AnswerType string          `json:"answer_type"`
	AnswerKey  string          `json:"answer_key"`
	Threshold  *float64        `json:"threshold"`
}

// Parse：严格解码（图、节点、边、节点 data 都不允许未知字段）。
func Parse(raw []byte) (Graph, error) {
	var graph Graph
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&graph); err != nil {
		return Graph{}, fmt.Errorf("Flow 图 JSON 不合法：%w", err)
	}
	if graph.Version != Version {
		return Graph{}, fmt.Errorf("Flow 图版本必须是 %d，得到 %d", Version, graph.Version)
	}
	if err := Validate(graph); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

// ValidateRaw：保存 / 启动时的入口。
func ValidateRaw(raw []byte) error {
	_, err := Parse(raw)
	return err
}

func Validate(graph Graph) error {
	if len(graph.Nodes) == 0 {
		return errors.New("Flow 图没有节点")
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, node := range graph.Nodes {
		if strings.TrimSpace(node.ID) == "" || seen[node.ID] {
			return errors.New("节点 id 为空或重复")
		}
		seen[node.ID] = true
		counts[node.Type]++
		if !finite(node.Position.X) || !finite(node.Position.Y) {
			return fmt.Errorf("节点 %s 的坐标不是有限数", node.ID)
		}
		if err := validateNodeData(node); err != nil {
			return err
		}
	}
	if counts["input"] != 1 {
		return errors.New("Flow 图需要恰好一个输入节点")
	}
	if counts["output"] != 1 {
		return errors.New("Flow 图需要恰好一个输出节点")
	}
	edgeIDs := map[string]bool{}
	sourcePorts := map[string]bool{}
	targetPorts := map[string]bool{}
	for _, edge := range graph.Edges {
		if strings.TrimSpace(edge.ID) == "" || edgeIDs[edge.ID] {
			return errors.New("连线 id 为空或重复")
		}
		edgeIDs[edge.ID] = true
		if !seen[edge.Source] || !seen[edge.Target] {
			return errors.New("连线指向不存在的节点")
		}
		if strings.TrimSpace(edge.SourceHandle) == "" || strings.TrimSpace(edge.TargetHandle) == "" {
			return fmt.Errorf("连线 %s 的端口为空", edge.ID)
		}
		sp := edge.Source + "\x00" + edge.SourceHandle
		tp := edge.Target + "\x00" + edge.TargetHandle
		if sourcePorts[sp] {
			return fmt.Errorf("节点 %s 的输出端口 %s 接了多条线", edge.Source, edge.SourceHandle)
		}
		if targetPorts[tp] {
			return fmt.Errorf("节点 %s 的输入端口 %s 接了多条线", edge.Target, edge.TargetHandle)
		}
		sourcePorts[sp] = true
		targetPorts[tp] = true
	}
	if err := validateDAG(graph); err != nil {
		return err
	}
	return nil
}

func validateNodeData(node Node) error {
	switch node.Type {
	case "input", "history", "output":
		return decodeData(node, &struct{}{})
	case "text":
		var data textData
		return decodeData(node, &data)
	case "trim":
		var data trimData
		if err := decodeData(node, &data); err != nil {
			return err
		}
		if data.Mode != "" && data.Mode != "tail" && data.Mode != "head" && data.Mode != "drop-head" && data.Mode != "drop-tail" {
			return fmt.Errorf("裁剪节点 %s 的 mode 不合法", node.ID)
		}
		if data.Count != nil && *data.Count < 1 {
			return fmt.Errorf("裁剪节点 %s 的 count 必须是正整数", node.ID)
		}
		for _, role := range data.Roles {
			if role != "system" && role != "user" && role != "assistant" {
				return fmt.Errorf("裁剪节点 %s 的角色不合法：%s", node.ID, role)
			}
		}
		return nil
	case "merge":
		var data mergeData
		if err := decodeData(node, &data); err != nil {
			return err
		}
		return validatePorts(node.ID, "输入", data.Inputs)
	case "join":
		var data joinData
		if err := decodeData(node, &data); err != nil {
			return err
		}
		if err := validatePorts(node.ID, "输入", data.Inputs); err != nil {
			return err
		}
		return validatePorts(node.ID, "输出", data.Outputs)
	case "llm":
		var data llmData
		if err := decodeData(node, &data); err != nil {
			return err
		}
		if data.MaxRetry != nil && *data.MaxRetry < 0 {
			return fmt.Errorf("LLM 节点 %s 的 max_retry 必须是非负整数", node.ID)
		}
		if data.Width != nil && (!finite(*data.Width) || *data.Width <= 0) {
			return fmt.Errorf("LLM 节点 %s 的 width 不合法", node.ID)
		}
		if data.Height != nil && (!finite(*data.Height) || *data.Height <= 0) {
			return fmt.Errorf("LLM 节点 %s 的 height 不合法", node.ID)
		}
		if len(data.Prompts) == 0 {
			return fmt.Errorf("LLM 节点 %s 没有 Prompt 条目", node.ID)
		}
		ids := map[string]bool{}
		for _, prompt := range data.Prompts {
			if strings.TrimSpace(prompt.ID) == "" || ids[prompt.ID] {
				return fmt.Errorf("LLM 节点 %s 有空的或重复的 Prompt id", node.ID)
			}
			ids[prompt.ID] = true
			if prompt.Kind != "" && prompt.Kind != "prompt" && prompt.Kind != "history" {
				return fmt.Errorf("Prompt %s 的 kind 不合法：%s", prompt.ID, prompt.Kind)
			}
			if prompt.Kind != "history" && prompt.Role != "system" && prompt.Role != "user" && prompt.Role != "assistant" {
				return fmt.Errorf("Prompt %s 的 role 只能是 system / user / assistant", prompt.ID)
			}
		}
		return nil
	case "judge":
		var data judgeData
		if err := decodeData(node, &data); err != nil {
			return err
		}
		if strings.TrimSpace(data.Provider) == "" || strings.TrimSpace(data.Model) == "" {
			return fmt.Errorf("JEV 节点 %s 必须填渠道和模型", node.ID)
		}
		switch data.AnswerType {
		case "string", "bool", "number", "choice":
		default:
			return fmt.Errorf("JEV 节点 %s 的 answer_type 不合法", node.ID)
		}
		if data.Threshold != nil && (*data.Threshold < 0 || *data.Threshold > 1) {
			return fmt.Errorf("JEV 节点 %s 的 threshold 必须在 0..1", node.ID)
		}
		if len(data.Questions) == 0 || !json.Valid(data.Questions) {
			return fmt.Errorf("JEV 节点 %s 的 questions 不是合法 JSON", node.ID)
		}
		return nil
	default:
		return fmt.Errorf("不认识的节点类型：%s", node.Type)
	}
}

func decodeData(node Node, into any) error {
	raw := bytes.TrimSpace(node.Data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("节点 %s（%s）的 data 不合法：%w", node.ID, node.Type, err)
	}
	return nil
}

func validatePorts(nodeID, side string, ports []string) error {
	seen := map[string]bool{}
	for _, port := range ports {
		port = strings.TrimSpace(port)
		if port == "" || seen[port] {
			return fmt.Errorf("节点 %s 有空的或重复的%s端口", nodeID, side)
		}
		seen[port] = true
	}
	return nil
}

func validateDAG(graph Graph) error {
	indegree := map[string]int{}
	for _, node := range graph.Nodes {
		indegree[node.ID] = 0
	}
	for _, edge := range graph.Edges {
		indegree[edge.Target]++
	}
	queue := make([]string, 0, len(graph.Nodes))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	seen := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		seen++
		for _, edge := range graph.Edges {
			if edge.Source != id {
				continue
			}
			indegree[edge.Target]--
			if indegree[edge.Target] == 0 {
				queue = append(queue, edge.Target)
			}
		}
	}
	if seen != len(graph.Nodes) {
		return errors.New("Flow 图里有环")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
