package providers

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"microchat/internal/config"
)

// Build：opencode-go + response ⇒ URL 以 /responses 结尾、体有 input 无 messages。
func TestBuildResponse(t *testing.T) {
	provider := Provider{ID: "go", Vendor: VendorOpenCodeGo, Protocol: ProtocolOpenAIResponse, APIKey: "sk-x"}
	request, err := Build(provider, Request{
		Model: "muse-spark-1.3", SessionID: "s-1",
		Messages:  []ChatMessage{{Role: "user", Content: "在吗"}, {Role: "assistant", Content: "在"}},
		MaxTokens: 128,
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
	if _, ok := body["input"]; !ok {
		t.Fatalf("占位体该有 input：%s", raw)
	}
	if _, ok := body["messages"]; ok {
		t.Fatalf("占位体不该有 messages：%s", raw)
	}
	if body["max_output_tokens"] != float64(128) {
		t.Fatalf("max_output_tokens = %v", body["max_output_tokens"])
	}
	input, _ := body["input"].(string)
	if !strings.Contains(input, "user: 在吗") || !strings.Contains(input, "assistant: 在") {
		t.Fatalf("input 形状 = %q", input)
	}
	// custom 无 endpoints ⇒ base+/responses
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

// zenProtocolFor：表里 31 个全在 + 前缀兜底 + chat 系不命中。
func TestZenProtocolFor(t *testing.T) {
	if len(zenResponseModels) != 31 {
		t.Fatalf("写死表该 31 个，得到 %d", len(zenResponseModels))
	}
	for _, id := range []string{
		"gpt-5", "gpt-5.3-codex-spark", "gpt-6.1-sol",
		"grok-4.7", "grok-build-0.1",
		"muse-spark-1.2", "muse-spark-1.3", "muse-spark-1.3-contributor-free",
	} {
		protocol, ok := zenProtocolFor(id)
		if !ok || protocol != ProtocolOpenAIResponse {
			t.Fatalf("%q 该走 response：%q %v", id, protocol, ok)
		}
	}
	// 新模型兜底：前缀 gpt-/grok- 就算表里没有也走 response
	if _, ok := zenProtocolFor("gpt-9-future"); !ok {
		t.Fatal("gpt-9-future 该被前缀兜底")
	}
	// chat 系不命中
	for _, id := range []string{"deepseek-v4.1-flash", "kimi-k2.5", "qwen3.8-max", "glm-5", "big-pickle"} {
		if _, ok := zenProtocolFor(id); ok {
			t.Fatalf("%q 不该走 response", id)
		}
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
