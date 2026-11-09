package providers

import "encoding/json"

// ServerTool：provider-native 服务端工具的线上形状（Responses API：`{"type":"web_search"}`）。
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

// FunctionTool：客户端 function tool 的声明（目前由后续工具注册表调用）。
func FunctionTool(name, description string, parameters json.RawMessage) Tool {
	return Tool{Type: "function", Function: &ToolFunction{
		Name: name, Description: description, Parameters: parameters,
	}}
}

// allServerTools：这批工具是不是**全部**为服务端工具（没有 function 字段）。
// 用来把“已知的 Responses 服务端能力”和“默认关闭的任意 function tool”区分开。
func allServerTools(tools []Tool) bool {
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		if tool.Function != nil || tool.Type == "" || tool.Type == "function" {
			return false
		}
	}
	return true
}
