// Package flow：FlowAgent 图的**唯一结构权威**。
//
// 图不是裸 JSON：版本、节点、边、每种节点的 data 字段都在这里定型。
// 保存、启动、FlowTest、聊天运行都先过 Validate；运行时不再靠 map 猜字段。
package flow

//go:generate go run ../../cmd/flowgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const Version = 1

// NodeTypes 是节点类型的唯一清单（运行时校验与前端类型生成共用）。
var NodeTypes = []string{"input", "text", "history", "trim", "merge", "assemble", "join", "llm", "judge", "debug", "output"}

type Graph struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// NodeData 是节点的**全部可持久化字段**；按节点类型只允许对应字段非零。
// Width 是旧 LLM 数据兼容字段，运行时不用；test/runState 只属于编辑器，永不落盘。
type NodeData struct {
	Text       string            `json:"text,omitempty"`
	Mode       string            `json:"mode,omitempty"`
	Count      *int              `json:"count,omitempty"`
	Roles      []string          `json:"roles,omitempty"`
	Inputs     []string          `json:"inputs,omitempty"`
	Outputs    []string          `json:"outputs,omitempty"`
	Separator  string            `json:"separator,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	MaxRetry   *int              `json:"max_retry,omitempty"`
	Width      *float64          `json:"width,omitempty"`
	Height     *float64          `json:"height,omitempty"`
	Prompts    []Prompt          `json:"prompts,omitempty"`
	Questions  map[string]string `json:"questions,omitempty"`
	AnswerType string            `json:"answer_type,omitempty"`
	AnswerKey  string            `json:"answer_key,omitempty"`
	Threshold  *float64          `json:"threshold,omitempty"`
}

type Node struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Position Position `json:"position"`
	Data     NodeData `json:"data"`
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

// Parse：严格解码（图、节点、边、NodeData 都不允许未知字段）。
func Parse(raw []byte) (Graph, error) {
	var graph Graph
	if err := json.Unmarshal(raw, &graph); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

// UnmarshalJSON：任何从这里进入的图（agents.json / HTTP / 测试）都走同一套
// DisallowUnknownFields + Validate，不允许调用方绕过 schema。
func (g *Graph) UnmarshalJSON(raw []byte) error {
	type graphWire Graph
	var wire graphWire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("Flow 图 JSON 不合法：%w", err)
	}
	if dec.More() {
		return errors.New("Flow 图 JSON 后面还有内容")
	}
	graph := Graph(wire)
	if graph.Version != Version {
		return fmt.Errorf("Flow 图版本必须是 %d，得到 %d", Version, graph.Version)
	}
	if err := Validate(graph); err != nil {
		return err
	}
	*g = graph
	return nil
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
	return validateDAG(graph)
}

func validateNodeData(node Node) error {
	data := node.Data
	switch node.Type {
	case "input", "history", "output":
		if !emptyData(data) {
			return fmt.Errorf("节点 %s（%s）不应该有 data 字段", node.ID, node.Type)
		}
	case "text":
		if !onlyFields(data, "text") {
			return fmt.Errorf("文本节点 %s 含不属于它的字段", node.ID)
		}
	case "trim":
		if !onlyFields(data, "mode", "count", "roles") {
			return fmt.Errorf("裁剪节点 %s 含不属于它的字段", node.ID)
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
	case "merge":
		if !onlyFields(data, "inputs") {
			return fmt.Errorf("拼接 messages 节点 %s 含不属于它的字段", node.ID)
		}
		if err := validatePorts(node.ID, "输入", data.Inputs); err != nil {
			return err
		}
	case "join":
		if !onlyFields(data, "inputs", "outputs", "separator") {
			return fmt.Errorf("拼接节点 %s 含不属于它的字段", node.ID)
		}
		if err := validatePorts(node.ID, "输入", data.Inputs); err != nil {
			return err
		}
		if err := validatePorts(node.ID, "输出", data.Outputs); err != nil {
			return err
		}
	case "llm":
		if !onlyFields(data, "provider", "model", "max_retry", "width", "height", "prompts") {
			return fmt.Errorf("LLM 节点 %s 含不属于它的字段", node.ID)
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
		if err := validatePromptEntries(node, "LLM"); err != nil {
			return err
		}
	case "assemble":
		if !onlyFields(data, "prompts") {
			return fmt.Errorf("拼装 messages 节点 %s 含不属于它的字段", node.ID)
		}
		if err := validatePromptEntries(node, "拼装 messages"); err != nil {
			return err
		}
	case "debug":
		if !emptyData(data) {
			return fmt.Errorf("调试节点 %s 不应该有 data 字段", node.ID)
		}
	case "judge":
		if !onlyFields(data, "provider", "model", "questions", "answer_type", "answer_key", "threshold") {
			return fmt.Errorf("JEV 节点 %s 含不属于它的字段", node.ID)
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
		if len(data.Questions) == 0 {
			return fmt.Errorf("JEV 节点 %s 没有 questions", node.ID)
		}
		for key, question := range data.Questions {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(question) == "" {
				return fmt.Errorf("JEV 节点 %s 的 questions 有空键或空值", node.ID)
			}
		}
	default:
		return fmt.Errorf("不认识的节点类型：%s", node.Type)
	}
	return nil
}

// validatePromptEntries：LLM 与 assemble 共用的 Prompt 列表规则。
// kind 空 / prompt / history；history 不要求 role，prompt 必须是 system/user/assistant。
func validatePromptEntries(node Node, label string) error {
	if len(node.Data.Prompts) == 0 {
		return fmt.Errorf("%s 节点 %s 没有 Prompt 条目", label, node.ID)
	}
	ids := map[string]bool{}
	for _, prompt := range node.Data.Prompts {
		if strings.TrimSpace(prompt.ID) == "" || ids[prompt.ID] {
			return fmt.Errorf("%s 节点 %s 有空的或重复的 Prompt id", label, node.ID)
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
}

func emptyData(data NodeData) bool {
	return data.Text == "" && data.Mode == "" && data.Count == nil && len(data.Roles) == 0 &&
		len(data.Inputs) == 0 && len(data.Outputs) == 0 && data.Separator == "" &&
		data.Provider == "" && data.Model == "" && data.MaxRetry == nil && data.Width == nil &&
		data.Height == nil && len(data.Prompts) == 0 && len(data.Questions) == 0 &&
		data.AnswerType == "" && data.AnswerKey == "" && data.Threshold == nil
}

func onlyFields(data NodeData, allowed ...string) bool {
	allowedSet := map[string]bool{}
	for _, name := range allowed {
		allowedSet[name] = true
	}
	// 零值字段忽略；非零字段必须在该节点类型的白名单内。
	if data.Text != "" && !allowedSet["text"] {
		return false
	}
	if data.Mode != "" && !allowedSet["mode"] {
		return false
	}
	if data.Count != nil && !allowedSet["count"] {
		return false
	}
	if len(data.Roles) > 0 && !allowedSet["roles"] {
		return false
	}
	if len(data.Inputs) > 0 && !allowedSet["inputs"] {
		return false
	}
	if len(data.Outputs) > 0 && !allowedSet["outputs"] {
		return false
	}
	if data.Separator != "" && !allowedSet["separator"] {
		return false
	}
	if data.Provider != "" && !allowedSet["provider"] {
		return false
	}
	if data.Model != "" && !allowedSet["model"] {
		return false
	}
	if data.MaxRetry != nil && !allowedSet["max_retry"] {
		return false
	}
	if data.Width != nil && !allowedSet["width"] {
		return false
	}
	if data.Height != nil && !allowedSet["height"] {
		return false
	}
	if len(data.Prompts) > 0 && !allowedSet["prompts"] {
		return false
	}
	if len(data.Questions) > 0 && !allowedSet["questions"] {
		return false
	}
	if data.AnswerType != "" && !allowedSet["answer_type"] {
		return false
	}
	if data.AnswerKey != "" && !allowedSet["answer_key"] {
		return false
	}
	if data.Threshold != nil && !allowedSet["threshold"] {
		return false
	}
	return true
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

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
