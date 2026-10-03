package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// stubUpstream：一个假上游 —— 把给定的帧原样吐出来，顺便数一下被打了多少次
// （"不重试"这条只能靠计数证明，别只看有没有报错）。
type stubUpstream struct {
	server *httptest.Server
	hits   atomic.Int64
	last   atomic.Value // string：最近一次请求的 url / auth / ua
}

func newStubUpstream(t *testing.T, status int, contentType, body string) *stubUpstream {
	t.Helper()
	stub := &stubUpstream{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.hits.Add(1)
		raw, _ := json.Marshal(map[string]any{
			"url":  r.URL.String(),
			"auth": r.Header.Get("Authorization"),
			"ua":   r.Header.Get("User-Agent"),
		})
		stub.last.Store(string(raw))
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *stubUpstream) baseURL() string { return s.server.URL + "/v1" }

// 流式：`data:` 分帧交给成熟库（go-sse），这里只验"拼起来的整段对不对、增量顺序对不对"。
func TestChatStreamsAndSplitsReasoning(t *testing.T) {
	// 思考的字段名各家不同：三种都要认（OpenRouter 系 reasoning / DeepSeek 官方 reasoning_content / 第三种）
	frames := []string{
		`data: {"choices":[{"delta":{"reasoning":"先想"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"一下"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_text":"…"}}]}`,
		`: 心跳（注释帧，解析不了就跳过）`,
		`data: {"choices":[{"delta":{"content":"你好"}}]}`,
		`data: {"choices":[{"delta":{"content":"，世界"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":1654,"completion_tokens":62,"total_tokens":1716,` +
			`"prompt_tokens_details":{"cached_tokens":1408},"completion_tokens_details":{"reasoning_tokens":32}}}`,
		`data: [DONE]`,
	}
	stub := newStubUpstream(t, http.StatusOK, "text/event-stream", strings.Join(frames, "\n\n")+"\n\n")
	provider := Provider{ID: "stub", Vendor: VendorOpenAICompat, BaseURL: stub.baseURL(), Identity: IdentityBare}

	var deltas []Delta
	result, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "m", SessionID: "01a0e965-4156-741d-a8bd-5e16f28eb90d",
		Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "", func(delta Delta) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "你好，世界" {
		t.Fatalf("整段正文 = %q", result.Text)
	}
	if result.Reasoning != "先想一下…" {
		t.Fatalf("整段思考 = %q（三种字段名都该认）", result.Text)
	}
	want := []Delta{
		{Reasoning: "先想"}, {Reasoning: "一下"}, {Reasoning: "…"},
		{Text: "你好"}, {Text: "，世界"},
	}
	if len(deltas) != len(want) {
		t.Fatalf("增量 = %+v（想要 %+v）", deltas, want)
	}
	for index, delta := range deltas {
		if delta != want[index] {
			t.Fatalf("第 %d 个增量 = %+v（想要 %+v）", index, delta, want[index])
		}
	}
	// usage 归一化（缓存命中那几种字段名都认）
	var usage Usage
	if err := json.Unmarshal(result.Usage, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.PromptTokens != 1654 || usage.CompletionTokens != 62 || usage.TotalTokens != 1716 ||
		usage.CachedTokens != 1408 || usage.ReasoningTokens != 32 {
		t.Fatalf("归一化后的 usage = %+v", usage)
	}
	if len(usage.Raw) == 0 {
		t.Fatal("上游原样那份该留着备查")
	}
}

// 量只附在最后一段（`choices: []` + `usage`）也该认 —— 这就是"打完字才报数"的形状。
func TestTrailingUsageOnlyChunkIsAccepted(t *testing.T) {
	stub := newStubUpstream(t, 200, "text/event-stream",
		"data: {\"choices\":[{\"delta\":{\"content\":\"在\"}}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":10}}\n\n"+
			"data: [DONE]\n\n")
	provider := Provider{ID: "stub", Vendor: VendorDeepseek, BaseURL: stub.baseURL()}
	result, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "deepseek-chat", SessionID: "s", Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "在" {
		t.Fatalf("正文 = %q", result.Text)
	}
	var usage Usage
	if err := json.Unmarshal(result.Usage, &usage); err != nil {
		t.Fatalf("用量解不出来：%v（%s）", err, result.Usage)
	}
	if usage.PromptTokens != 8 || usage.TotalTokens != 10 {
		t.Fatalf("尾段的量该认：%+v", usage)
	}
}

// usage 归一化：**只有这一处**认识各家字段名；认不出来 ⇒ nil（宁可不显示，也别显示一排 0）。
func TestNormalizeUsageRecognizesFieldVariants(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // 空 = 该回 nil
	}{
		{"OpenAI 系（prompt_tokens_details.cached_tokens）",
			`{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":80}}`,
			`{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"cached_tokens":80,"reasoning_tokens":0,` +
				`"raw":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":80}}}`},
		{"DeepSeek 系（prompt_cache_hit_tokens）",
			`{"prompt_tokens":100,"completion_tokens":1,"prompt_cache_hit_tokens":64}`,
			`{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"cached_tokens":64,"reasoning_tokens":0,` +
				`"raw":{"prompt_tokens":100,"completion_tokens":1,"prompt_cache_hit_tokens":64}}`},
		{"Anthropic 系（cache_read_input_tokens）",
			`{"prompt_tokens":7,"cache_read_input_tokens":3}`,
			`{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7,"cached_tokens":3,"reasoning_tokens":0,` +
				`"raw":{"prompt_tokens":7,"cache_read_input_tokens":3}}`},
		{"思考 tokens 在顶层（reasoning_tokens）",
			`{"prompt_tokens":1,"completion_tokens":9,"reasoning_tokens":8}`,
			`{"prompt_tokens":1,"completion_tokens":9,"total_tokens":10,"cached_tokens":0,"reasoning_tokens":8,` +
				`"raw":{"prompt_tokens":1,"completion_tokens":9,"reasoning_tokens":8}}`},
		{"连 prompt_tokens 都没有 ⇒ 不认识", `{"foo":"bar"}`, ""},
		{"不是 JSON ⇒ 不认识", `{oops`, ""},
		{"null ⇒ 没有", `null`, ""},
		{"空 ⇒ 没有", ``, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := NormalizeUsage(json.RawMessage(testCase.raw))
			if testCase.want == "" {
				if len(got) != 0 {
					t.Fatalf("该回 nil，得到 %s", got)
				}
				return
			}
			if string(got) != testCase.want {
				t.Fatalf("得到 %s\n想要 %s", got, testCase.want)
			}
		})
	}
}

// 上游报错**原样返回**：不重试（打一次就是一次）、不吞错（状态码与正文都在）、不改写文案。
func TestUpstreamErrorIsReturnedAsIsAndNotRetried(t *testing.T) {
	stub := newStubUpstream(t, http.StatusTooManyRequests, "application/json",
		`{"error":{"message":"rate limited","type":"too_many_requests"}}`)
	provider := Provider{ID: "stub", Vendor: VendorOpenAICompat, BaseURL: stub.baseURL(), APIKey: "sk-real", Identity: IdentityBare}
	_, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "m", SessionID: "s1", Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "", nil)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("该把上游的错原样带回来：%v", err)
	}
	if upstream.Status != http.StatusTooManyRequests || !strings.Contains(upstream.Body, "rate limited") {
		t.Fatalf("上游报错 = %+v", upstream)
	}
	if hits := stub.hits.Load(); hits != 1 {
		t.Fatalf("不许重试：打了 %d 次", hits)
	}
	if snapshot, _ := stub.last.Load().(string); !strings.Contains(snapshot, `"auth":"Bearer sk-real"`) {
		t.Fatalf("密钥该带上（那是上游要的）：%s", snapshot)
	}
}

// 流里报的错（200 开着流再报）也算上游错 —— 半条正文**不能**当成功。
func TestErrorInsideTheStreamFailsTheTurn(t *testing.T) {
	frames := []string{
		`data: {"choices":[{"delta":{"content":"说到一半"}}]}`,
		`data: {"error":{"message":"上游崩了"}}`,
	}
	stub := newStubUpstream(t, http.StatusOK, "text/event-stream", strings.Join(frames, "\n\n")+"\n\n")
	provider := Provider{ID: "stub", Vendor: VendorOpenAICompat, BaseURL: stub.baseURL(), Identity: IdentityBare}
	result, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "m", SessionID: "s1", Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "", nil)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("该报上游错：%v（正文 %q）", err, result.Text)
	}
	if !strings.Contains(upstream.Body, "上游崩了") {
		t.Fatalf("错帧要原样带回来：%+v", upstream)
	}
}

// 空流 ⇒ 明确的错（不往库里塞一条空消息）。
func TestEmptyStreamIsAnError(t *testing.T) {
	stub := newStubUpstream(t, http.StatusOK, "text/event-stream", "data: [DONE]\n\n")
	provider := Provider{ID: "stub", Vendor: VendorOpenAICompat, BaseURL: stub.baseURL(), Identity: IdentityBare}
	_, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "m", SessionID: "s1", Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "", nil)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("空流该报 ErrEmpty：%v", err)
	}
}

// 配了 `stream: false` 的渠道：一轮生成**明说**发不出去，别静默按流式发
// （那会让人对着"时好时坏"猜）。"整段拿结果"的辅助调用走的是 `Complete`（那一发本来就不流式）。
func TestNonStreamingIsRefusedExplicitly(t *testing.T) {
	off := false
	provider := Provider{ID: "stub", Vendor: VendorOpenAICompat, BaseURL: "http://127.0.0.1:1/v1", Stream: &off}
	_, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "m", SessionID: "s1",
	}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "stream:false") {
		t.Fatalf("该明说这家渠道不能用来聊天：%v", err)
	}
}

// readResponseStream：脚本化事件流 —— 文本分片拼正文、reasoning 系吞了记 log、usage 归一化。
func TestReadResponseStreamTextAndUsage(t *testing.T) {
	frames := []string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"你好"}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"想一下"}`,
		`data: {"type":"response.reasoning_summary_part.done","output_index":0}`,
		`data: {"type":"response.reasoning_text.delta","output_index":0,"delta":"还是想"}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"，世界"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}}`,
		`data: [DONE]`,
	}
	var deltas []Delta
	result, err := readResponseStream(200, strings.NewReader(strings.Join(frames, "\n\n")+"\n\n"),
		func(delta Delta) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "你好，世界" {
		t.Fatalf("正文 = %q", result.Text)
	}
	if result.Reasoning != "" {
		t.Fatalf("reasoning 系该吞了别入库：%q", result.Reasoning)
	}
	if len(deltas) != 2 || deltas[0].Text != "你好" || deltas[1].Text != "，世界" {
		t.Fatalf("增量 = %+v", deltas)
	}
	var usage Usage
	if err := json.Unmarshal(result.Usage, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}

// refusal ⇒ 整轮失败（文案带事件名）；空正文 ⇒ ErrEmpty（与 chat 同一条脾气）。
func TestReadResponseStreamRefusalAndEmpty(t *testing.T) {
	refusal := `data: {"type":"response.refusal.delta","delta":"不说"}` + "\n\n"
	if _, err := readResponseStream(200, strings.NewReader(refusal), nil); err == nil {
		t.Fatal("refusal 该失败")
	} else if upstream, ok := err.(*UpstreamError); !ok || !strings.Contains(upstream.Body, "refusal") {
		t.Fatalf("文案该带事件名：%v", err)
	}
	failed := `data: {"type":"response.failed","response":{"status":"failed"}}` + "\n\n"
	if _, err := readResponseStream(200, strings.NewReader(failed), nil); err == nil {
		t.Fatal("failed 该失败")
	}
	onlyDone := "data: [DONE]\n\n"
	if _, err := readResponseStream(200, strings.NewReader(onlyDone), nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("空正文该报 ErrEmpty：%v", err)
	}
}

// completeResponse：非流式 output_text 数组拼正文；reasoning 摘要吞了；refusal 失败。
func TestCompleteResponseBody(t *testing.T) {
	body := `{"output":[
		{"type":"reasoning","summary":[{"text":"想过了"}]},
		{"type":"message","content":[{"type":"output_text","text":"答案"},{"type":"output_text","text":"是 42"}]}
	],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
	result, err := completeResponse(200, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "答案是 42" {
		t.Fatalf("正文 = %q", result.Text)
	}
	refused := `{"output":[{"type":"message","content":[{"type":"refusal","text":"不说"}]}]}`
	if _, err := completeResponse(200, []byte(refused)); err == nil {
		t.Fatal("refusal 该失败")
	}
	if _, err := completeResponse(200, []byte(`{"output":[]}`)); !errors.Is(err, ErrEmpty) {
		t.Fatalf("空正文该报 ErrEmpty：%v", err)
	}
}
