package providers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	"microchat/internal/model"
)

func chatParams(p Provider, r Request) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{Model: r.Model, Messages: make([]openai.ChatCompletionMessageParamUnion, 0, len(r.Messages))}
	for _, m := range r.Messages {
		var msg openai.ChatCompletionMessageParamUnion
		switch m.Role {
		case "system":
			msg = openai.SystemMessage(m.Content)
		case "developer":
			msg = openai.DeveloperMessage(m.Content)
		case "user":
			msg = openai.UserMessage(m.Content)
		case "assistant":
			if m.Content == "" && len(m.ToolCalls) == 0 && m.Exchange != nil {
				continue
			}
			msg = openai.AssistantMessage(m.Content)
			assistant := msg.OfAssistant
			for _, call := range m.ToolCalls {
				assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{ID: call.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: call.Name, Arguments: call.Arguments}},
				})
			}
			field := reasoningFieldForModel(p, r.Model)
			requireThinking := p.Vendor == VendorDeepseek || (p.Vendor == VendorCustom && isDeepSeekFamily(r.Model))
			if field != "" && (m.Reasoning != "" || requireThinking) {
				assistant.SetExtraFields(map[string]any{field: m.Reasoning})
			}
		case "tool":
			msg = openai.ToolMessage(m.Content, m.ToolCallID)
			if toolResultNameFor(p) && m.Name != "" {
				msg.OfTool.SetExtraFields(map[string]any{"name": m.Name})
			}
		default:
			return params, fmt.Errorf("providers: 未知消息角色 %q", m.Role)
		}
		params.Messages = append(params.Messages, msg)
	}
	preset := presets[p.Vendor]
	if key := cacheKey(p, r); key != "" {
		params.PromptCacheKey = openai.String(key)
	}
	if r.CacheRetention == CacheLong && preset.longCache {
		params.PromptCacheRetention = "24h"
	}
	if r.Stream && preset.supportsUsage {
		params.StreamOptions.IncludeUsage = openai.Bool(true)
	}
	if preset.supportsStore {
		params.Store = openai.Bool(false)
	}
	if r.MaxTokens > 0 {
		if preset.maxTokensOld {
			params.MaxTokens = openai.Int(int64(r.MaxTokens))
		} else {
			params.MaxCompletionTokens = openai.Int(int64(r.MaxTokens))
		}
	}
	if r.Temperature != nil {
		params.Temperature = openai.Float(*r.Temperature)
	}
	extras := map[string]any{}
	level := strings.ToLower(strings.TrimSpace(r.ThinkingLevel))
	if level != "" {
		if p.Vendor == VendorDeepseek {
			mode := "enabled"
			if level == "off" {
				mode = "disabled"
			}
			extras["thinking"] = map[string]string{"type": mode}
		}
		if effort, ok := thinkingEffortFor(p, r.Model, level); ok {
			if p.Vendor == VendorOpenRouter {
				extras["reasoning"] = map[string]string{"effort": effort}
			} else {
				params.ReasoningEffort = shared.ReasoningEffort(effort)
			}
		}
	}
	for _, t := range r.Tools {
		if !p.sendsTool(t) {
			continue
		}
		if t.Function == nil {
			if p.Vendor == VendorOpenAI || t.Type == "function" || t.Type == "" {
				return params, fmt.Errorf("providers: Chat Completions 没有 %q 服务端工具声明", t.Type)
			}
			// 自定义网关可通过模型规则显式声明其扩展工具。
			params.Tools = append(params.Tools, param.Override[openai.ChatCompletionToolUnionParam](t))
			continue
		}
		f, err := functionDefinition(t.Function)
		if err != nil {
			return params, err
		}
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(f))
	}
	if len(params.Tools) != 0 && r.ToolChoice != "" {
		switch r.ToolChoice {
		case "auto", "required", "none":
			params.ToolChoice.OfAuto = openai.String(r.ToolChoice)
		default:
			params.ToolChoice = openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{Name: r.ToolChoice})
		}
	} else if len(params.Tools) == 0 && hasToolHistory(r.Messages) && p.Vendor != VendorOpenAI {
		// 部分兼容网关要求回放含工具的历史时显式带空数组。
		extras["tools"] = []openai.ChatCompletionToolUnionParam{}
	}
	for k, v := range extraFields(r) {
		extras[k] = v
	}
	params.SetExtraFields(extras)
	return params, nil
}

func functionDefinition(f *ToolFunction) (shared.FunctionDefinitionParam, error) {
	def := shared.FunctionDefinitionParam{Name: f.Name}
	if f.Name == "" {
		return def, fmt.Errorf("providers: function 工具缺少 name")
	}
	if len(f.Parameters) != 0 {
		// SDK 把 map 里的 json.Number 编成字符串。保留每个属性的原始 JSON，
		// 也保住超过 float64 精度的整数以及嵌套数组里的值。
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(f.Parameters, &fields); err != nil || fields == nil {
			return def, fmt.Errorf("providers: 工具 %q 的 parameters 必须为对象", f.Name)
		}
		def.Parameters = make(shared.FunctionParameters, len(fields))
		for key, value := range fields {
			def.Parameters[key] = value
		}
	}
	if f.Description != "" {
		def.Description = openai.String(f.Description)
	}
	if f.Strict != nil {
		def.Strict = openai.Bool(*f.Strict)
	}
	return def, nil
}

// OpenAI 兼容渠道的三个思考扩展字段由 SDK 保存在 ExtraFields 中。
func chatReasoning(fields map[string]respjson.Field) string {
	for _, key := range []string{"reasoning", "reasoning_content", "reasoning_text"} {
		var text string
		if raw := fields[key].Raw(); raw != "" && json.Unmarshal([]byte(raw), &text) == nil && text != "" {
			return text
		}
	}
	return ""
}

func collectChat(stream *ssestream.Stream[openai.ChatCompletionChunk], emit func(Delta)) (Result, error) {
	defer stream.Close()
	var acc openai.ChatCompletionAccumulator
	var reasoning strings.Builder
	var usage json.RawMessage
	for stream.Next() {
		chunk := stream.Current()
		if !acc.AddChunk(chunk) {
			return Result{}, fmt.Errorf("providers: 无法合并 chat 分片（响应 id 或 index 不一致）")
		}
		if chunk.JSON.Usage.Valid() {
			usage = NormalizeUsage([]byte(chunk.Usage.RawJSON()))
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			delta := Delta{Text: choice.Delta.Content, Reasoning: chatReasoning(choice.Delta.JSON.ExtraFields)}
			reasoning.WriteString(delta.Reasoning)
			if emit != nil && (delta.Text != "" || delta.Reasoning != "") {
				emit(delta)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Result{}, err
	}
	result, err := completionResult(&acc.ChatCompletion)
	result.Reasoning = reasoning.String()
	result.Usage = usage
	return result, err
}

func completionResult(reply *openai.ChatCompletion) (Result, error) {
	if field := reply.JSON.ExtraFields["error"]; field.Valid() {
		return Result{}, &UpstreamError{Status: 200, Body: snippet(field.Raw())}
	}
	if len(reply.Choices) == 0 {
		return Result{}, ErrEmpty
	}
	choice := reply.Choices[0]
	msg := choice.Message
	result := newResult(reply.ID, choice.FinishReason)
	result.Usage = NormalizeUsage([]byte(reply.Usage.RawJSON()))
	result.Reasoning = chatReasoning(msg.JSON.ExtraFields)
	// Refusal is a model outcome, not an API failure. Surface it as reply text so
	// callers and archives never turn a refusal into a blank response.
	result.Text = msg.Content
	if msg.Refusal != "" {
		if result.Text != "" {
			result.Text += "\n\n"
		}
		result.Text += msg.Refusal
	}
	if result.Text != "" {
		result.Exchange.Items = append(result.Exchange.Items, model.OutputItem{Type: "message", Text: result.Text})
	}
	toolFinish := choice.FinishReason == "tool_calls" && msg.Refusal == ""
	if toolFinish {
		for _, t := range msg.ToolCalls {
			if t.Type != "" && t.Type != "function" {
				return Result{}, fmt.Errorf("providers: 尚未注册 %q 工具执行器", t.Type)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: t.ID, Name: t.Function.Name, Arguments: t.Function.Arguments})
			result.Exchange.Items = append(result.Exchange.Items, model.OutputItem{Type: "function_call", CallID: t.ID})
		}
	}
	switch {
	case toolFinish:
		if len(result.ToolCalls) == 0 {
			return Result{}, fmt.Errorf("providers: 上游以 %q 停止，但没有完整的 tool call", choice.FinishReason)
		}
	case len(msg.ToolCalls) != 0 && choice.FinishReason != "length" && choice.FinishReason != "content_filter" && msg.Refusal == "":
		return Result{}, fmt.Errorf("providers: 工具调用未完成 (finish_reason=%q)", choice.FinishReason)
	}
	return result.checked()
}
