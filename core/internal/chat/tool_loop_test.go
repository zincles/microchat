package chat

import (
	"encoding/json"
	"fmt"
	"microchat/internal/task"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
)

// 两个协议从普通发送入口走同一条工具循环。模型和搜索分别用自己的凭据和端点。
func TestSDKToolRoundTrip(t *testing.T) {
	for _, protocol := range []providers.Protocol{providers.ProtocolChatCompletion, providers.ProtocolOpenAIResponse} {
		t.Run(string(protocol), func(t *testing.T) {
			var executed atomic.Int32
			search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer search-key" || r.URL.Path != "/v1/tools/search" {
					t.Errorf("工具发错了渠道或端点 %s", r.URL.Path)
				}
				var args struct{ Query string }
				if err := json.NewDecoder(r.Body).Decode(&args); err != nil || args.Query == "" {
					t.Errorf("搜索参数 %+v, %v", args, err)
				}
				executed.Add(1)
				fmt.Fprint(w, `{"results":[{"url":"https://example.org","title":"找到资料"}]}`)
			}))
			defer search.Close()
			var rounds atomic.Int32
			requests := make(chan map[string]any, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("出站 JSON: %v", err)
					return
				}
				if r.Header.Get("Authorization") != "Bearer model-key" {
					t.Error("模型凭据不符")
				}
				wantPath := "/v1/chat/completions"
				if protocol == providers.ProtocolOpenAIResponse {
					wantPath = "/v1/responses"
				}
				if r.URL.Path != wantPath {
					t.Errorf("URL = %s", r.URL.Path)
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				round := rounds.Add(1)
				if protocol == providers.ProtocolOpenAIResponse {
					if round == 1 {
						for _, frame := range []string{
							`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-test","summary":[]}}`,
							`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"查一下。"}]}}`,
							`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","status":"in_progress","id":"fc_1","call_id":"call_1","name":"web_search","arguments":""}}`,
							`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"web_search","arguments":""}}`,
							`{"type":"response.function_call_arguments.delta","output_index":3,"item_id":"fc_2","delta":"{\"query\":"}`,
							`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"{\"query\":\"a\"}"}`,
							`{"type":"response.function_call_arguments.delta","output_index":3,"item_id":"fc_2","delta":"\"b\"}"}`,
							`{"type":"response.function_call_arguments.done","output_index":3,"item_id":"fc_2","arguments":"{\"query\":\"b\"}"}`,
							`{"type":"response.function_call_arguments.done","output_index":2,"item_id":"fc_1","arguments":"{\"query\":\"a\"}"}`,
							`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", frame)
						}
					} else {
						fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"拿到资料了。"}]}]}}`+"\n\n")
					}
				} else {
					if round == 1 {
						for _, frame := range []string{
							`{"id":"cmpl_1","choices":[{"index":0,"delta":{"content":"查一下。","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":"}},{"index":1,"id":"call_2","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"b\"}"}}]}}]}`,
							`{"id":"cmpl_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]},"finish_reason":"tool_calls"}]}`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", frame)
						}
					} else {
						fmt.Fprint(w, "data: "+`{"id":"cmpl_2","choices":[{"index":0,"delta":{"content":"拿到资料了。"},"finish_reason":"stop"}]}`+"\n\n")
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer upstream.Close()

			local := []string{"web_search"}
			channels := config.ProvidersConfig{Providers: []config.Provider{
				{ID: "model", Vendor: "openai", Protocol: string(protocol), BaseURL: upstream.URL + "/v1", APIKey: "model-key", LocalToolProvider: "search",
					ModelDefineRules: []config.ModelDefineRule{{ID: "tools", MatchContains: "fixture", LocalTools: &local}}},
				{ID: "search", Vendor: "deepseek", BaseURL: search.URL, APIKey: "search-key"},
			}}
			h := newHarness(t, channels, "model", "fixture")
			preview, err := h.service.PreviewWire(h.session, "查资料")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.Accept(h.session, "查资料"); err != nil {
				t.Fatal(err)
			}
			messages := h.settle(t)
			if len(messages) != 3 || rounds.Load() != 2 || executed.Load() != 2 {
				t.Fatalf("消息=%d 模型请求=%d 工具执行=%d turn=%+v", len(messages), rounds.Load(), executed.Load(), h.turns.Status(h.session.ID))
			}
			if messages[1].Kind != model.MessageKindToolRun || messages[2].Content != "拿到资料了。" ||
				messages[1].RunID != messages[2].RunID || len(messages[1].Tools) != 2 || messages[2].Idx != 3 {
				t.Fatalf("工具轮和最终消息没有正确归档 %+v", messages)
			}
			var previewBody map[string]any
			if err := json.Unmarshal(preview.Body, &previewBody); err != nil {
				t.Fatal(err)
			}
			first, second := <-requests, <-requests
			if !reflect.DeepEqual(first, previewBody) {
				t.Fatalf("预演和真发不同 %+v / %+v", previewBody, first)
			}
			tool := first["tools"].([]any)[0].(map[string]any)
			if tool["type"] != "function" {
				t.Fatalf("工具类型 %+v", tool)
			}
			if protocol == providers.ProtocolOpenAIResponse {
				if tool["name"] != "web_search" || tool["function"] != nil {
					t.Fatalf("Responses 工具声明 %+v", tool)
				}
				input := second["input"].([]any)
				last := input[len(input)-6:]
				if last[0].(map[string]any)["encrypted_content"] != "opaque-test" || last[1].(map[string]any)["phase"] != "commentary" {
					t.Fatalf("推理续传或消息阶段丢失 %+v", last)
				}
				for i := 0; i < 2; i++ {
					c, o := last[2+i].(map[string]any), last[4+i].(map[string]any)
					if c["call_id"] != fmt.Sprint("call_", i+1) || c["id"] != fmt.Sprint("fc_", i+1) || o["type"] != "function_call_output" || o["call_id"] != c["call_id"] || !strings.Contains(o["output"].(string), "找到资料") {
						t.Fatalf("回填错误 %+v %+v", c, o)
					}
				}
			} else {
				if tool["function"].(map[string]any)["name"] != "web_search" || tool["name"] != nil {
					t.Fatalf("Chat 工具声明 %+v", tool)
				}
				input := second["messages"].([]any)
				a := input[len(input)-3].(map[string]any)
				for i, c := range a["tool_calls"].([]any) {
					o := input[len(input)-2+i].(map[string]any)
					if o["role"] != "tool" || o["tool_call_id"] != c.(map[string]any)["id"] || !strings.Contains(o["content"].(string), "找到资料") {
						t.Fatalf("回填错误 %+v", o)
					}
				}
			}
			registered := 0
			for _, rec := range h.tasks.Board().Tasks {
				if rec.Kind == task.KindTurn {
					registered++
				}
			}
			if registered != 2 {
				t.Fatalf("两次上游请求的 Task 数=%d", registered)
			}
			copySession, err := h.store.CopySession(h.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			copyHistory, err := h.store.ListMessages(copySession.ID)
			if err != nil || !reflect.DeepEqual(copyHistory[1].Exchanges, messages[1].Exchanges) || copyHistory[1].Tools[0].TextOffset == nil {
				t.Fatalf("Copy 丢失调用元数据 %v", err)
			}
			next, err := h.service.PreviewWire(copySession, "继续")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(next.Body), "call_2") || protocol == providers.ProtocolOpenAIResponse && !strings.Contains(string(next.Body), "opaque-test") {
				t.Fatalf("重载后历史回放错误 %s", next.Body)
			}
		})
	}
}

func TestCancelToolFollowupKeepsOnlyThisStep(t *testing.T) {
	var round atomic.Int32
	release := make(chan struct{})
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tools/search" {
			http.Error(w, "fixture error", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if round.Add(1) == 1 {
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"已查询。","tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"资料\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			t.Errorf("工具回填请求 %v", err)
			return
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "tool" || !strings.Contains(last.Content, "fixture error") {
			t.Errorf("失败结果没有回填 %#v", last)
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"当前半句"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer downstream.Close()
	defer close(release)
	h := newHarness(t, config.ProvidersConfig{Providers: []config.Provider{{ID: "ds", Vendor: "deepseek", BaseURL: downstream.URL}}}, "ds", "deepseek-test")
	if _, err := h.service.Accept(h.session, "查资料"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := h.turns.Status(h.session.ID)
		if state.Step == 2 && state.Chars > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有进入第二步 %+v", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !h.service.Stop(h.session.ID) {
		t.Fatal("取消失败")
	}
	for {
		done := false
		for _, r := range h.tasks.Board().Tasks {
			if r.Kind == task.KindTurn && r.Outcome == task.StateCanceled {
				done = true
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("取消没有收尾")
		}
		time.Sleep(5 * time.Millisecond)
	}
	messages := h.settle(t)
	if len(messages) != 3 || messages[1].Content != "已查询。" || len(messages[1].Tools) != 1 || messages[1].Tools[0].Status != "failed" || messages[2].Status != model.MessageStatusCanceled || messages[2].Content != "当前半句" {
		t.Fatalf("取消后的消息 %+v", messages)
	}
}
