package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/model"
)

func TestBufferedFunctionCalls(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolChatCompletion, ProtocolOpenAIResponse} {
		t.Run(string(protocol), func(t *testing.T) {
			reply := `{"id":"c1","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`
			if protocol == ProtocolOpenAIResponse {
				reply = `{"id":"r1","status":"completed","output":[{"type":"function_call","status":"completed","id":"item1","call_id":"call1","name":"lookup","arguments":"{}"}]}`
			}
			stub := newStubUpstream(t, 200, "application/json", reply)
			p := Provider{ID: "model", Vendor: VendorOpenAI, Protocol: protocol, BaseURL: stub.baseURL()}
			r := Request{Model: "test", SessionID: "s", Tools: []Tool{FunctionTool("lookup", "", json.RawMessage(`{"type":"object"}`))}, ToolChoice: "lookup"}
			req, err := Build(p, r)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				ToolChoice functionDeclaration `json:"tool_choice"`
			}
			if err = json.NewDecoder(req.Body).Decode(&body); err != nil || body.ToolChoice.name(protocol) != "lookup" {
				t.Fatalf("指定函数 %+v, %v", body, err)
			}
			result, err := NewClient(p).Complete(context.Background(), p, r, "")
			if err != nil || result.Text != "" || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call1" || result.Exchange.ResponseID == "" {
				t.Fatalf("非流式纯函数调用 %+v, %v", result, err)
			}
			r.Tools = nil
			if _, err = NewClient(p).Complete(context.Background(), p, r, ""); err == nil {
				t.Fatal("辅助调用没有注册函数，不能静默接受纯工具输出")
			}
		})
	}
}

func TestResponseRejectsPartialCalls(t *testing.T) {
	added := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"lookup","arguments":""}}` + "\n\n"
	delta := `data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc1","delta":"{}"}` + "\n\n"
	for _, tc := range []struct{ name, tail string }{
		{"断流", ""},
		{"参数尚未结束", `data: {"type":"response.completed","response":{}}`},
		{"其他 item 的参数", `data: {"type":"response.function_call_arguments.done","item_id":"fc2","output_index":0,"arguments":"{}"}`},
		{"截断", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`},
		{"失败", `data: {"type":"response.failed","response":{"error":{"message":"fixture error"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := readResponseStream(200, strings.NewReader(added+delta+tc.tail+"\n\n"), nil)
			if err == nil || len(result.ToolCalls) > 0 {
				t.Fatalf("半截工具请求被接受 %+v, %v", result, err)
			}
		})
	}
	for _, raw := range []string{
		`{"status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"截断"}]}]}`,
		`{"output":[{"type":"function_call","call_id":"x","name":"lookup","arguments":"{"}]}`,
		`{"output":[{"type":"function_call","call_id":"x","name":"lookup","arguments":"{}"},{"type":"function_call","call_id":"x","name":"lookup","arguments":"{}"}]}`,
	} {
		if _, err := completeResponse(200, []byte(raw)); err == nil {
			t.Fatalf("错误非流式结果被接受 %s", raw)
		}
	}
}

func TestReplayUsesAssembledTextAndMatchingProvider(t *testing.T) {
	p := Provider{ID: "old", Vendor: VendorOpenAI, Protocol: ProtocolOpenAIResponse}
	exchange := &model.Exchange{Provider: p.ID, EndpointHash: responseEndpointHash(ApplyPreset(p)), Protocol: string(p.Protocol), Model: "m", Items: []model.OutputItem{
		{Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","id":"rs1","encrypted_content":"opaque","summary":[]}`)},
		{Type: "message", Text: "旧正文 <state>HP=1</state>", Phase: "commentary"},
		{Type: "function_call", ID: "fc1", CallID: "c1"},
	}}
	r := Request{SessionID: "session", Model: "m", Messages: []ChatMessage{
		{Role: "assistant", Content: "现行正文", Exchange: exchange, ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
	}}
	for _, channel := range []string{"old", "new"} {
		p.ID = channel
		req, err := Build(p, r)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if strings.Contains(body, "旧正文") || strings.Contains(body, "HP=1") || !strings.Contains(body, "现行正文") || !strings.Contains(body, "function_call_output") {
			t.Fatalf("绕过装配的正文或丢了结果 %s", body)
		}
		if strings.Contains(body, "opaque") != (channel == "old") || strings.Contains(body, "fc1") != (channel == "old") {
			t.Fatalf("专属回放数据发错渠道 %s", body)
		}
	}
	archive := exchange.ForStorage(false)
	if len(archive.Items) != 2 || len(exchange.Items) != 3 {
		t.Fatal("关闭思考留档时改坏了本轮内存续传项")
	}
}

func TestSDKRejectsOrphanResultBeforeSending(t *testing.T) {
	p := Provider{ID: "p", Vendor: VendorOpenAI, Protocol: ProtocolOpenAIResponse}
	_, err := Build(p, Request{SessionID: "s", Messages: []ChatMessage{{Role: "tool", ToolCallID: "missing", Content: "result"}}})
	if err == nil {
		t.Fatal("孤立结果应在发送前报错")
	}
	stub := newStubUpstream(t, http.StatusBadRequest, "text/plain", "tools[0].name is required")
	p.BaseURL = stub.baseURL()
	_, err = NewClient(p).Chat(context.Background(), p, Request{SessionID: "s", Model: "m"}, "", nil)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != 400 || !strings.Contains(upstream.Body, "tools[0].name") || stub.hits.Load() != 1 {
		t.Fatalf("400 信息丢失或重试 %v", err)
	}
}

func TestWrongProtocolFunctionOverride(t *testing.T) {
	for _, tc := range []struct {
		protocol   Protocol
		key, wrong string
	}{
		{ProtocolOpenAIResponse, "tools", `[{"type":"function","function":{"name":"lookup"}}]`},
		{ProtocolChatCompletion, "tools", `[{"type":"function","name":"lookup"}]`},
		{ProtocolOpenAIResponse, "tool_choice", `{"type":"function","function":{"name":"lookup"}}`},
	} {
		p := Provider{ID: "p", Vendor: VendorOpenAI, Protocol: tc.protocol}
		_, err := Build(p, Request{SessionID: "s", Model: "m", Override: map[string]json.RawMessage{tc.key: json.RawMessage(tc.wrong)}})
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Fatalf("应指出字段 %s: %v", tc.key, err)
		}
	}
}
