package providers

import (
	"encoding/json"
	"fmt"
)

// ServerTool：provider-native 服务端工具的线上形状（`{"type":"web_search"}`；chat / response 都可能）。
// 它没有 function 字段 —— 模型由上游自己执行，不回到客户端。
func ServerTool(id string) Tool { return Tool{Type: id} }

// ServerTools：把模型规则给出的 id 列表转成请求体里的工具声明（保持顺序、去空）。
func ServerTools(ids []string) []Tool {
	tools := make([]Tool, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		tools = append(tools, ServerTool(id))
	}
	return tools
}

// FunctionTool：客户端 function tool 的声明（由本地执行器提供）。
func FunctionTool(name, description string, parameters json.RawMessage) Tool {
	return Tool{Type: "function", Local: true, Function: &ToolFunction{
		Name: name, Description: description, Parameters: parameters,
	}}
}

// 逐个筛选，混合声明里的服务端工具与已注册本地函数各自生效。
func (p Provider) sendsTool(t Tool) bool {
	return t.Local || t.Function == nil || toolsAllowed(p)
}

// 即使调试覆盖添加了额外的函数声明，也只有调用方注册的函数可执行。
// 覆盖删除了某个函数时，模型意外返回它也要拒绝。
func allowsFunction(r Request, p Provider, name string) bool {
	for _, tool := range r.Tools {
		if !p.sendsTool(tool) || tool.Function == nil || tool.Function.Name != name {
			continue
		}
		raw, overridden := r.Override["tools"]
		if !overridden {
			return true
		}
		var sent []functionDeclaration
		if json.Unmarshal(raw, &sent) != nil {
			return false
		}
		for _, decl := range sent {
			if decl.Type == "function" && decl.name(p.EffectiveProtocol()) == name {
				return true
			}
		}
	}
	return false
}

type functionDeclaration struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Function *struct {
		Name string `json:"name"`
	} `json:"function"`
}

func (d functionDeclaration) name(protocol Protocol) string {
	if protocol == ProtocolOpenAIResponse {
		return d.Name
	}
	if d.Function != nil {
		return d.Function.Name
	}
	return ""
}

// 对覆盖只核对 function 的协议结构。其他工具和厂商扩展仍按配置原样发送。
func checkToolOverrides(protocol Protocol, overrides map[string]json.RawMessage) error {
	if raw, ok := overrides["tools"]; ok {
		var tools []functionDeclaration
		if err := json.Unmarshal(raw, &tools); err != nil {
			return fmt.Errorf("providers: tools 必须是数组: %w", err)
		}
		for i, decl := range tools {
			if decl.Type == "function" && (decl.name(protocol) == "" || (protocol == ProtocolOpenAIResponse && decl.Function != nil)) {
				return fmt.Errorf("providers: tools[%d] 不符合 %s 的 function 声明", i, protocol)
			}
		}
	}
	var choice functionDeclaration
	if raw := overrides["tool_choice"]; json.Unmarshal(raw, &choice) == nil && choice.Type == "function" &&
		(choice.name(protocol) == "" || (protocol == ProtocolOpenAIResponse && choice.Function != nil)) {
		return fmt.Errorf("providers: tool_choice 不符合 %s 的指定函数格式", protocol)
	}
	return nil
}
