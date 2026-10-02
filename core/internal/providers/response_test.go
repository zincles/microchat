package providers

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"microchat/internal/config"
)

// Build：opencode-go + response ⇒ URL 以 /responses 结尾、体是数组 input、无 messages。
func TestBuildResponse(t *testing.T) {
	provider := Provider{ID: "go", Vendor: VendorOpenCodeGo, Protocol: ProtocolOpenAIResponse, APIKey: "sk-x"}
	temperature := 0.7
	effort, summary := "high", "auto"
	request, err := Build(provider, Request{
		Model: "muse-spark-1.3", SessionID: "s-1",
		Messages:  []ChatMessage{{Role: "user", Content: "在吗"}, {Role: "assistant", Content: "在"}},
		MaxTokens: 128, Temperature: &temperature,
		ReasoningEffort: &effort, ReasoningSummary: &summary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(request.URL.String(), "/responses") {
		t.Fatalf("URL = %q", request.URL.String())
	}
	raw, _ := io.ReadAll(request.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["messages"]; ok {
		t.Fatalf("response 体不该有 messages：%s", raw)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input 该是 2 项数组：%s", raw)
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("首项 role = %v", first["role"])
	}
	content, ok := first["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content 该是 1 项数组：%v", first["content"])
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "在吗" {
		t.Fatalf("input_text 形状 = %v", content[0])
	}
	if body["max_output_tokens"] != float64(128) {
		t.Fatalf("max_output_tokens = %v", body["max_output_tokens"])
	}
	if body["store"] != false {
		t.Fatalf("opencode-go 该发 store:false：%s", raw)
	}
	if body["temperature"] != 0.7 {
		t.Fatalf("temperature = %v", body["temperature"])
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning = %v", body["reasoning"])
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("opencode 系本来就没有 longCache ⇒ 自然不发 prompt_cache_key：%s", raw)
	}
	// custom 无 endpoints ⇒ base+/responses，且 custom 不发 store（用户自备端点，别乱塞字段）
	custom, err := Build(
		Provider{ID: "c", Vendor: VendorCustom, Protocol: ProtocolOpenAIResponse, BaseURL: "https://x.invalid/v1"},
		Request{Model: "m", SessionID: "s"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if custom.URL.String() != "https://x.invalid/v1/responses" {
		t.Fatalf("URL = %q", custom.URL.String())
	}
	customRaw, _ := io.ReadAll(custom.Body)
	var customBody map[string]any
	if err := json.Unmarshal(customRaw, &customBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := customBody["store"]; ok {
		t.Fatalf("custom 不该发 store：%s", customRaw)
	}
	if _, ok := customBody["reasoning"]; ok {
		t.Fatalf("没给 effort/summary 就不该发 reasoning：%s", customRaw)
	}
}

// Validate 矩阵：response ← opencode-go/opencode/openrouter/custom；deepseek+response 报错。
func TestValidateResponseMatrix(t *testing.T) {
	for _, vendor := range []Vendor{VendorOpenCodeGo, VendorOpenCode, VendorOpenRouter, VendorCustom} {
		provider := Provider{ID: "p", Vendor: vendor, Protocol: ProtocolOpenAIResponse, BaseURL: "https://x.invalid/v1"}
		if err := provider.Validate(); err != nil {
			t.Fatalf("vendor %q × response 该过：%v", vendor, err)
		}
	}
	// custom 无 base 无 endpoints[response] ⇒ 报错
	if err := (Provider{ID: "c", Vendor: VendorCustom, Protocol: ProtocolOpenAIResponse}).Validate(); err == nil {
		t.Fatal("custom 没给 base_url 该报错")
	}
	// custom 有 endpoints[response] 没 base ⇒ 过（端点全靠这张表给）
	endpointed := Provider{ID: "c", Vendor: VendorCustom, Protocol: ProtocolOpenAIResponse,
		Endpoints: map[Protocol]string{ProtocolOpenAIResponse: "https://x.invalid/r"}}
	if err := endpointed.Validate(); err != nil {
		t.Fatalf("custom 有 endpoints[response] 该过：%v", err)
	}
	// Build 原样用 verbatim 端点
	built, err := Build(endpointed, Request{Model: "m", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if built.URL.String() != "https://x.invalid/r" {
		t.Fatalf("URL = %q", built.URL.String())
	}
	// deepseek + response ⇒ 错配
	if err := (Provider{ID: "d", Vendor: VendorDeepseek, Protocol: ProtocolOpenAIResponse,
		BaseURL: "https://x.invalid"}).Validate(); err == nil {
		t.Fatal("deepseek + response 该错配报错")
	}
}

// ResolveProtocol：有行且 api=="openai-responses" ⇒ response；无行/别的 api 值 ⇒ chat 缺省。
// 路由唯一真相 = model_route 表（Zen 写死表已删，见 model_route）。
func TestResolveProtocol(t *testing.T) {
	responses := func(provider, upstreamID string) (string, bool) {
		if provider == "oc" && upstreamID == "gpt-5" {
			return "openai-responses", true
		}
		if provider == "oc" && upstreamID == "deepseek-v4.1-flash" {
			return "openai-completions", true
		}
		return "", false
	}
	if got := ResolveProtocol(responses, "oc", "gpt-5"); got != ProtocolOpenAIResponse {
		t.Fatalf("有行 responses 该走 response：%q", got)
	}
	if got := ResolveProtocol(responses, "oc", "deepseek-v4.1-flash"); got != ProtocolChatCompletion {
		t.Fatalf("别的 api 值该回 chat：%q", got)
	}
	if got := ResolveProtocol(responses, "oc", "kimi-k2.5"); got != ProtocolChatCompletion {
		t.Fatalf("无行该回 chat：%q", got)
	}
	if got := ResolveProtocol(nil, "oc", "gpt-5"); got != ProtocolChatCompletion {
		t.Fatalf("lookup 为空该回 chat：%q", got)
	}
	// 查表键 = 渠道 id + 模型 id（model_route 按渠道刷新：同 vendor 两条渠道各有各的表）
	seen := ""
	lookup := func(provider, upstreamID string) (string, bool) {
		seen = provider + "/" + upstreamID
		return "", false
	}
	ResolveProtocol(lookup, "ocgo-2", "m")
	if seen != "ocgo-2/m" {
		t.Fatalf("查表键 = %q", seen)
	}
}

// FromConfig 带 endpoints ⇒ 忽略 + 不炸：Build 仍走预设拼出来的。
func TestFromConfigIgnoresEndpoints(t *testing.T) {
	configured := config.Provider{
		ID: "go", Vendor: "opencode-go", APIKey: "sk-x",
		Endpoints: map[string]string{"openai-response": "https://evil.invalid/r"},
	}
	wire := FromConfig(configured)
	if len(wire.Endpoints) != 0 {
		t.Fatalf("配置里的 endpoints 该被忽略：%v", wire.Endpoints)
	}
	built, err := Build(wire, Request{Model: "m", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if built.URL.String() != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("URL = %q", built.URL.String())
	}
	// response 路也一样：走预设拼出来的 /responses，而不是配置里那张表
	wire.Protocol = ProtocolOpenAIResponse
	resp, err := Build(wire, Request{Model: "m", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.URL.String() != "https://opencode.ai/zen/go/v1/responses" {
		t.Fatalf("URL = %q", resp.URL.String())
	}
}
