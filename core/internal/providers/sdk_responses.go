package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"microchat/internal/model"
)

func responseParams(p Provider, r Request) (responses.ResponseNewParams, error) {
	params := responses.ResponseNewParams{Model: r.Model}
	input := make(responses.ResponseInputParam, 0, len(r.Messages))
	endpointHash := responseEndpointHash(p)
	for _, m := range r.Messages {
		switch m.Role {
		case "assistant":
			input = append(input, responseAssistant(p.ID, r.Model, endpointHash, m)...)
		case "tool":
			item := responses.ResponseInputItemParamOfFunctionCallOutput(m.Content)
			item.OfFunctionCallOutput.CallID = openai.String(m.ToolCallID)
			input = append(input, item)
		case "user":
			content := responses.ResponseInputMessageContentListParam{responses.ResponseInputContentParamOfInputText(m.Content)}
			input = append(input, responses.ResponseInputItemParamOfMessage(content, "user"))
		case "system", "developer":
			input = append(input, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRole(m.Role)))
		default:
			return params, fmt.Errorf("providers: 未知消息角色 %q", m.Role)
		}
	}
	params.Input.OfInputItemList = input
	if key := cacheKey(p, r); key != "" {
		params.PromptCacheKey = openai.String(key)
	}
	if r.CacheRetention == CacheLong && presets[p.Vendor].longCache {
		params.PromptCacheRetention = "24h"
	}
	if r.MaxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(max(16, r.MaxTokens)))
	}
	if r.Temperature != nil {
		params.Temperature = openai.Float(*r.Temperature)
	}
	if level := strings.ToLower(strings.TrimSpace(r.ThinkingLevel)); level != "" {
		if effort, ok := thinkingEffortFor(p, r.Model, level); ok {
			params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(effort), Summary: "auto"}
		}
	}
	if p.Vendor != VendorCustom {
		params.Store = openai.Bool(false)
	}
	for _, t := range r.Tools {
		if !p.sendsTool(t) {
			continue
		}
		switch {
		case t.Function != nil:
			def, err := functionDefinition(t.Function)
			if err != nil {
				return params, err
			}
			// Responses 默认为严格模式；本地工具允许可选参数，按声明显式选择。
			strict := t.Function.Strict != nil && *t.Function.Strict
			if def.Parameters == nil {
				def.Parameters = shared.FunctionParameters{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
			}
			tool := responses.ToolParamOfFunction(def.Name, def.Parameters, strict)
			tool.OfFunction.Description = def.Description
			params.Tools = append(params.Tools, tool)
			if p.Vendor == VendorOpenAI && !slices.Contains(params.Include, responses.ResponseIncludableReasoningEncryptedContent) {
				params.Include = append(params.Include, responses.ResponseIncludableReasoningEncryptedContent)
			}
		case t.Type == "web_search":
			params.Tools = append(params.Tools, responses.ToolParamOfWebSearch("web_search"))
			if p.Vendor == VendorOpenAI {
				params.Include = append(params.Include, responses.ResponseIncludableWebSearchCallActionSources)
			}
		case p.Vendor != VendorOpenAI && t.Type != "function" && t.Type != "":
			// 例如 xAI 的 x_search 与 code_execution，仍由其上游执行。
			params.Tools = append(params.Tools, param.Override[responses.ToolUnionParam](t))
		default:
			return params, fmt.Errorf("providers: Responses 缺少 %q 的工具定义", t.Type)
		}
	}
	if len(params.Tools) != 0 && r.ToolChoice != "" {
		switch r.ToolChoice {
		case "auto", "required", "none":
			params.ToolChoice.OfToolChoiceMode = param.NewOpt(responses.ToolChoiceOptions(r.ToolChoice))
		default:
			params.ToolChoice.OfFunctionTool = &responses.ToolChoiceFunctionParam{Name: r.ToolChoice}
		}
	}
	params.SetExtraFields(extraFields(r))
	return params, nil
}

// 回放以装配后的正文及成对的本地调用为准。只有原渠道、模型与协议相同时才使用原生项。
// 正文经过剔标签或宏展开时，用当前正文替换原文本段，不能从协议附件把存档原文发回去。
func responseAssistant(provider, modelID, endpointHash string, m ChatMessage) responses.ResponseInputParam {
	var input responses.ResponseInputParam
	callItem := func(c ToolCall, itemID string) responses.ResponseInputItemUnionParam {
		item := responses.ResponseInputItemParamOfFunctionCall(c.Arguments, c.ID, c.Name)
		if itemID != "" {
			item.OfFunctionCall.ID = openai.String(itemID)
		}
		return item
	}
	record := m.Exchange
	if record == nil || record.Protocol != string(ProtocolOpenAIResponse) || record.Provider != provider || record.Model != modelID || record.EndpointHash != endpointHash {
		for _, call := range m.ToolCalls {
			input = append(input, callItem(call, ""))
		}
		if m.Content != "" {
			input = append(input, responses.ResponseInputItemParamOfMessage(m.Content, "assistant"))
		}
		return input
	}
	var original strings.Builder
	for _, part := range record.Items {
		if part.Type == "message" {
			original.WriteString(part.Text)
		}
	}
	sameText := original.String() == m.Content
	wroteText := false
	calls := make(map[string]ToolCall, len(m.ToolCalls))
	for _, call := range m.ToolCalls {
		calls[call.ID] = call
	}
	for _, part := range record.Items {
		switch part.Type {
		case "message":
			text := part.Text
			if !sameText {
				if wroteText {
					continue
				}
				text = m.Content
			}
			if text != "" {
				msg := responses.ResponseInputItemParamOfMessage(text, "assistant")
				msg.OfMessage.Phase = responses.EasyInputMessagePhase(part.Phase)
				input = append(input, msg)
				wroteText = true
			}
		case "function_call":
			if c, ok := calls[part.CallID]; ok {
				input = append(input, callItem(c, part.ID))
				delete(calls, part.CallID)
			}
		case "reasoning", "web_search_call", "file_search_call", "x_search_call", "code_interpreter_call":
			var item responses.ResponseOutputItemUnion
			if len(part.Payload) == 0 || json.Unmarshal(part.Payload, &item) != nil || item.Type != part.Type {
				continue
			}
			if item.Type == "reasoning" && item.EncryptedContent == "" {
				continue
			}
			input = append(input, param.Override[responses.ResponseInputItemUnionParam](part.Payload))
		}
	}
	if !wroteText && m.Content != "" {
		input = append(input, responses.ResponseInputItemParamOfMessage(m.Content, "assistant"))
	}
	for _, c := range m.ToolCalls {
		if _, ok := calls[c.ID]; ok {
			input = append(input, callItem(c, ""))
		}
	}
	return input
}

func responseResult(reply *responses.Response) (Result, error) {
	reason := string(reply.Status)
	if reason == "" {
		reason = "completed"
	}
	if reply.Error.Message != "" || reason != "completed" {
		return Result{}, &UpstreamError{Status: http.StatusOK, Body: snippet(fmt.Sprintf("response.%s %s %s %s", reason, reply.Error.Code, reply.Error.Message, reply.IncompleteDetails.Reason))}
	}
	result := newResult(reply.ID, reason)
	result.Usage = NormalizeUsage([]byte(reply.Usage.RawJSON()))
	for _, item := range reply.Output {
		part := model.OutputItem{Type: item.Type, ID: item.ID}
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "refusal":
					return Result{}, &UpstreamError{Status: http.StatusOK, Body: snippet("response.refusal: " + c.Refusal)}
				case "output_text":
					part.Text += c.Text
				}
			}
			part.Phase = string(item.Phase)
			result.Text += part.Text
		case "function_call":
			if item.Status != "" && item.Status != "completed" {
				return Result{}, fmt.Errorf("providers: 调用 %q 仍处于 %s", item.CallID, item.Status)
			}
			part.CallID = item.CallID
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments.OfString})
		case "computer_call", "custom_tool_call", "local_shell_call", "apply_patch_call", "mcp_approval_request":
			return Result{}, fmt.Errorf("providers: 上游请求的 %q 需要专用执行器", item.Type)
		case "reasoning":
			for _, summary := range item.Summary {
				if result.Reasoning != "" {
					result.Reasoning += "\n\n"
				}
				result.Reasoning += summary.Text
			}
			part.Payload = json.RawMessage(item.RawJSON())
		default:
			// 保留未知输出项，当前适配器只执行已注册的本地 function。
			part.Payload = json.RawMessage(item.RawJSON())
			if event := hostedItem(item); event != nil {
				result.ServerTools = append(result.ServerTools, *event)
			}
		}
		result.Exchange.Items = append(result.Exchange.Items, part)
	}
	return result.checked()
}

func isHostedType(kind string) bool {
	switch kind {
	case "web_search_call", "x_search_call", "file_search_call", "code_interpreter_call", "code_execution_call", "image_generation_call", "mcp_call":
		return true
	}
	return false
}

func hostedItem(item responses.ResponseOutputItemUnion) *ToolEvent {
	if !isHostedType(item.Type) {
		return nil
	}
	e := &ToolEvent{ItemID: item.ID, Kind: strings.TrimSuffix(item.Type, "_call"), State: item.Status,
		Query: item.Action.Query, Queries: item.Action.Queries}
	if e.Query != "" {
		e.Queries = append([]string{e.Query}, e.Queries...)
	}
	for _, source := range item.Action.Sources {
		if strings.HasPrefix(source.URL, "https://") || strings.HasPrefix(source.URL, "http://") {
			var title string
			_ = json.Unmarshal([]byte(source.JSON.ExtraFields["title"].Raw()), &title)
			e.Sources = append(e.Sources, ToolSource{Type: string(source.Type), URL: source.URL, Title: title})
		}
	}
	return e
}

type responseSlot struct {
	item      responses.ResponseOutputItemUnion
	arguments strings.Builder
	text      map[int64]*responseTextPart
	textID    string
	done      bool
}

type responseTextPart struct {
	deltas []string
	final  string
	done   bool
}

func getResponseSlot(slots map[int64]*responseSlot, index int64) *responseSlot {
	slot := slots[index]
	if slot == nil {
		slot = &responseSlot{}
		slots[index] = slot
	}
	return slot
}

// completeText 只用有序的、可归属到消息项的文本补齐缺失的 item.done。
func (s *responseSlot) completeText() {
	item := &s.item
	if len(s.text) == 0 || (item.Type != "" && item.Type != "message") || (s.done && len(item.Content) != 0) {
		return
	}
	item.Type = "message"
	if item.ID == "" {
		item.ID = s.textID
	}
	item.Content = make([]responses.ResponseOutputMessageContentUnion, 0, len(s.text))
	for _, index := range slices.Sorted(maps.Keys(s.text)) {
		part := s.text[index]
		var text string
		if part.done {
			text = part.final
		} else {
			text = strings.Join(part.deltas, "")
		}
		item.Content = append(item.Content, responses.ResponseOutputMessageContentUnion{Type: "output_text", Text: text})
	}
}

// HTTP SSE 的标准终态包含 response.output。部分网关只在 output_item.done 给最终项，
// 还有些只给 arguments.done。按 output_index 归并，到 completed 才准交给执行器。
func collectResponse(stream *ssestream.Stream[responses.ResponseStreamEventUnion], emit func(Delta)) (Result, error) {
	defer stream.Close()
	slots := map[int64]*responseSlot{}
	var text []string
	var reasoning strings.Builder
	var unorderedText bool
	var responseID string
	for stream.Next() {
		e := stream.Current()
		index := e.OutputIndex
		if !e.JSON.OutputIndex.Valid() || index < 0 {
			index = -1
		}
		switch e.Type {
		case "response.created":
			responseID = e.Response.ID
		case "response.output_text.delta", "response.output_text.done":
			if e.Type == "response.output_text.delta" && e.Delta != "" {
				text = append(text, e.Delta)
				if emit != nil {
					emit(Delta{Text: e.Delta})
				}
			}
			if index < 0 {
				unorderedText = true
				continue
			}
			slot := getResponseSlot(slots, index)
			if e.ItemID != "" {
				if slot.textID != "" && slot.textID != e.ItemID {
					unorderedText = true
				}
				slot.textID = e.ItemID
			}
			if slot.text == nil {
				slot.text = make(map[int64]*responseTextPart)
			}
			part := slot.text[e.ContentIndex]
			if part == nil {
				part = &responseTextPart{}
				slot.text[e.ContentIndex] = part
			}
			if e.Type == "response.output_text.done" && e.JSON.Text.Valid() {
				part.final, part.done = e.Text, true
			} else if e.Delta != "" {
				part.deltas = append(part.deltas, e.Delta)
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_summary_part.done":
			delta := e.Delta
			if e.Type == "response.reasoning_summary_part.done" && reasoning.Len() != 0 {
				delta = "\n\n"
			}
			reasoning.WriteString(delta)
			if emit != nil && delta != "" {
				emit(Delta{Reasoning: delta})
			}
		case "response.output_item.added", "response.output_item.done":
			slot := getResponseSlot(slots, index)
			slot.item = e.Item
			slot.done = e.Type == "response.output_item.done"
			slot.arguments.Reset()
			slot.arguments.WriteString(e.Item.Arguments.OfString)
			if emit != nil {
				if tool := hostedItem(e.Item); tool != nil {
					emit(Delta{Tool: tool})
				}
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			slot := slots[index]
			if slot == nil || slot.item.Type != "function_call" || e.ItemID != "" && slot.item.ID != e.ItemID {
				return Result{}, fmt.Errorf("providers: 工具参数没有对应的 output item %q", e.ItemID)
			}
			if e.Type == "response.function_call_arguments.done" {
				if e.JSON.Arguments.Valid() {
					slot.arguments.Reset()
					slot.arguments.WriteString(e.Arguments)
				}
				slot.item.Arguments.OfString = slot.arguments.String()
				slot.item.Status = "completed"
				slot.done = true
			} else {
				slot.arguments.WriteString(e.Delta)
			}
		case "response.failed", "response.incomplete", "response.error", "error", "response.refusal", "response.refusal.delta", "response.refusal.done":
			return Result{}, &UpstreamError{Status: http.StatusOK, Body: snippet(e.Type + " " + e.Response.Error.Message + " " + e.Response.IncompleteDetails.Reason + " " + e.Message + " " + e.Delta + " " + e.Refusal)}
		case "response.completed":
			if !e.JSON.Response.Valid() {
				return Result{}, errors.New("providers: response.completed 缺少 response 对象")
			}
			reply := e.Response
			if reply.ID == "" {
				reply.ID = responseID
			}
			var unorderedItem, unorderedBody bool
			if !reply.JSON.Output.Valid() {
				hasText := false
				// 复用同一 output_index 作为文本和工具的网关没有可证明的输出顺序。
				for _, slot := range slots {
					if len(slot.text) != 0 && (slot.item.Type != "" && slot.item.Type != "message" ||
						slot.textID != "" && slot.item.ID != "" && slot.textID != slot.item.ID) {
						unorderedText = true
					}
				}
				for _, i := range slices.Sorted(maps.Keys(slots)) {
					slot := slots[i]
					if slot.item.Type == "function_call" && !slot.done {
						return Result{}, fmt.Errorf("providers: 工具参数尚未完成 %q", slot.item.CallID)
					}
					if !unorderedText {
						slot.completeText()
					}
					item := &slot.item
					if slot.done || item.Type == "message" && len(item.Content) != 0 || isHostedType(item.Type) {
						reply.Output = append(reply.Output, *item)
						unorderedItem = unorderedItem || i < 0 && strings.HasSuffix(item.Type, "_call")
						hasText = hasText || item.Type == "message" && slices.ContainsFunc(item.Content, func(c responses.ResponseOutputMessageContentUnion) bool {
							return c.Type == "output_text" && c.Text != ""
						})
					}
				}
				// 兼容只流文本、终帧只带用量的网关；无序正文不充作工具位置的证据。
				if unorderedText && !hasText && len(text) != 0 {
					unorderedBody = len(reply.Output) != 0
					reply.Output = append(reply.Output, responses.ResponseOutputItemUnion{Type: "message",
						Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: strings.Join(text, "")}}})
				}
			}
			result, err := responseResult(&reply)
			if err != nil {
				return Result{}, err
			}
			if unorderedItem {
				// 保存调用事实，但缺失 output_index 的工具不能作为有序记录续传或定位。
				result.Exchange.Items = result.Exchange.Items[1:]
			}
			if unorderedBody {
				result.Exchange.Items = result.Exchange.Items[:len(result.Exchange.Items)-1]
			}
			if result.Reasoning == "" {
				result.Reasoning = reasoning.String()
			}
			if emit != nil && len(text) == 0 && result.Text != "" {
				emit(Delta{Text: result.Text})
			}
			return result, nil
		default:
			if !strings.HasPrefix(e.Type, "response.") {
				continue
			}
			name, state, found := strings.Cut(strings.TrimPrefix(e.Type, "response."), ".")
			if found && strings.HasSuffix(name, "_call") && e.ItemID != "" {
				if emit != nil {
					emit(Delta{Tool: &ToolEvent{ItemID: e.ItemID, Kind: strings.TrimSuffix(name, "_call"), State: state}})
				}
				if index >= 0 && isHostedType(name) {
					slot := getResponseSlot(slots, index)
					if slot.item.Type == "" {
						slot.item = responses.ResponseOutputItemUnion{Type: name, ID: e.ItemID}
					}
					if !slot.done && slot.item.Type == name && slot.item.ID == e.ItemID {
						slot.item.Status = state
					}
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Result{}, err
	}
	if len(text) == 0 && len(slots) == 0 {
		return Result{}, ErrEmpty
	}
	return Result{}, errors.New("providers: Responses 流缺少终态事件")
}
