// Package providers：**只有它碰网络** —— 上游请求的唯一构造处。
//
// 形状**照抄 Pi**（`badlogic/pi-mono` 的 `packages/ai`），因为 OpenCode GO 那类网关按客户端身份放行：
//
//   - 头：`kind` 决定必需头（opencode 系要 `x-opencode-session`）；`identity` 决定自报身份；
//     用户 `headers` **最后合并 ⇒ 永远能覆盖**。
//   - 体：与 Pi 的 `openai-completions` 逐项对齐（stream ✓ stream_options.include_usage ✓
//     prompt_cache_key / prompt_cache_retention 的**条件** ✓ store:false ✓ max_completion_tokens ✓）。
//
// 两条硬规矩：
//  1. **`SessionID` 必填** —— 让"忘了传会话 id"在**构造期**就报，而不是静默少一个头（上游看来那像"时好时坏"）；
//  2. 会话 id 是**路由键**，缓存是**体里的参数** —— 两者分开（`cacheRetention: "none"` 只影响后者）。
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"microchat/internal/config"
)

// Vendor：渠道背后是**找谁**（哪家上游）。它决定 base_url、必需头、以及几家特有的兼容开关。
type Vendor string

const (
	// VendorOpenAICompat：**旧命名**（Rust 时代）—— 端点全靠配置给，仍按 OpenAI 兼容对待。
	// 留它是为了**不静默改变老配置的行为**（少了它，老配置会掉进"未知 vendor" ⇒ 不发 stream_options）。
	VendorOpenAICompat Vendor = "openai-compat"
	VendorDummy        Vendor = "dummy"       // 不联网：请求照拼（调试页看得到），但不发
	VendorOpenAI       Vendor = "openai"      // 官方 OpenAI（api.openai.com）
	VendorOpenRouter   Vendor = "openrouter"  // OpenRouter
	VendorDeepseek     Vendor = "deepseek"    // DeepSeek 官方（api.deepseek.com，OpenAI 兼容）
	VendorOpenCodeGo   Vendor = "opencode-go" // OpenCode Go（网关按客户端身份放行）
	VendorOpenCode     Vendor = "opencode"    // OpenCode（同上，非 Go 套餐）
	VendorOllama       Vendor = "ollama"      // 本地
	VendorLMStudio     Vendor = "lmstudio"    // 本地
	VendorTypesafe     Vendor = "typesafe"    // Typesafe（SystemOne 协议）
	VendorCustom       Vendor = "custom"      // 自定义端点：端点全靠配置给，按 OpenAI 兼容对待
)

// Kind：deprecated —— `Vendor` 的读入别名（值与 Vendor 常量逐字相同）。新代码一律用 Vendor。
type Kind string

const (
	// KindOpenAICompat：唯一还活着的别名 —— 存量 `kind: openai-compat` 读入仍走它
	// （`EffectiveVendor` 的 openai-compat→custom 分支）。其余 Kind* 常量已删：
	// 生产代码零引用，测试改用 Vendor 字面量。
	KindOpenAICompat Kind = "openai-compat"
)

// Protocol：渠道**说什么话**（线协议）。与 Vendor 正交，由 Validate 做兼容矩阵检查。
type Protocol string

const (
	// ProtocolChatCompletion：OpenAI Chat Completion（`/chat/completions`）—— 缺省。
	ProtocolChatCompletion Protocol = "openai-chat-completion"
	// ProtocolOpenAIResponse：OpenAI Response（`/responses`）—— opencode 系/openrouter/custom 可走。
	ProtocolOpenAIResponse Protocol = "openai-response"
	// ProtocolAnthropicMessages：占位 —— 暂不支持。
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	// ProtocolGeminiGenerateContent：占位 —— 暂不支持。
	ProtocolGeminiGenerateContent Protocol = "gemini-generate-content"
	// ProtocolSystemOne：JEV SystemOne（BaseURL verbatim 即整条 URL）。
	ProtocolSystemOne Protocol = "systemone"
)

// Identity：以什么身份自报家门。OpenCode 官方要求"用自己的标识，别用库名"（`node-fetch` 就是被拒那类）。
type Identity string

const (
	// IdentityMicrochat：默认。`user-agent: microchat/<版本>`（官方明确允许自报家门）。
	IdentityMicrochat Identity = "microchat"
	// IdentityPi：**逐字照 Pi** —— 有些网关只认已验证的客户端时用这个。
	IdentityPi Identity = "pi"
	// IdentityBare：什么都不装（Go 原生那套）。对 OpenCode 系会失败 —— 那是它的定义。
	IdentityBare Identity = "bare"
)

// CacheRetention：提示词缓存的档位（照 Pi 的 `"none" | "short" | "long"`）。
type CacheRetention string

const (
	// CacheAuto：不指定，按 provider 自动决定（Pi 的默认行为）。
	CacheAuto CacheRetention = "auto"
	// CacheNone：**子调用**（摘要/标题这类 one-off）用它 —— 不写缓存。
	CacheNone CacheRetention = "none"
	// CacheLong：长缓存（能支持才发）。
	CacheLong CacheRetention = "long"
)

// Version：自报版本用。
const Version = "0.1.0"

// Provider：一个渠道（`config/providers.json` 里的一条）。
//
// `Vendor`（找谁）× `Protocol`（说什么话）：新代码只填这两个；`Kind` 是废弃的读入别名
// （老配置/旧字面量照常编译，`EffectiveVendor` 收敛；`Vendor` 非空时 `Kind` 被忽略）。
type Provider struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Vendor   Vendor            `json:"vendor"`
	Kind     Kind              `json:"kind,omitempty"` // deprecated：只读入；Vendor 非空时忽略
	Protocol Protocol          `json:"protocol"`
	BaseURL  string            `json:"base_url"`
	Headers  map[string]string `json:"headers"`
	APIKey   string            `json:"api_key"`
	// Identity：空 = 跟随服务器默认（服务器也没写 ⇒ **pi**）。以什么身份自报家门。
	Identity Identity `json:"identity"`
	// ClientUAOverride：**直接覆写 UA**（默认空）。优先级**高于**身份与服务器默认。
	ClientUAOverride string `json:"client_ua_override"`
	// SessionHeader：会话 id 透传成哪个头。nil = 按 kind 默认（opencode 系 = `x-opencode-session`）；
	// "" = 明确不发；其它 = 用这个名字。
	SessionHeader *string `json:"session_header"`
	// ReasoningField：**回传思考**用哪个字段名。nil = 按 kind 默认；"" = 不回传。
	ReasoningField *string `json:"reasoning_field"`
	// AllowTools：**允许工具透传**（默认关）。开了才把 `tools` / `tool_calls` 原样发给上游 ——
	// 不认识的字段有些上游会直接 400，所以默认不发。
	AllowTools *bool `json:"allow_tools"`
	// ToolResultName：工具结果消息里带不带函数名（Pi 的 `requiresToolResultName`）。
	ToolResultName *bool     `json:"tool_result_name"`
	Timeouts       *Timeouts `json:"timeouts"`
	Stream         *bool     `json:"stream"`
	StoreReasoning *bool     `json:"store_reasoning"`
	// Endpoints：protocol → 整条 URL（verbatim，原样 POST）。**预设专用**：配置里写了忽略+log；
	// 缺省 = 老路（base_url + 后缀）。聚合站（opencode 系/openrouter）才带这张表。
	Endpoints map[Protocol]string `json:"endpoints,omitempty"`
}

type Timeouts struct {
	ConnectSeconds float64 `json:"connect_seconds"`
	TotalSeconds   float64 `json:"total_seconds"`
}

// ChatMessage：发给上游的一条消息（OpenAI 形状）。
//
// `Reasoning` **不出现在 JSON 里**（各自字段名不同，由 provider 决定）：
// 推理型模型的思考要**回传上游**才有连续的多轮推理（Pi 的原话：`reasoning_details` 是 replay metadata）。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// Reasoning：这条 assistant 消息当时的思考；只在该 kind 支持回传时才发。
	Reasoning string `json:"-"`

	// ── 工具回程（**原样透传**，形状与上游给的完全一致）──
	// ToolCalls：assistant 消息里的 `tool_calls` 原始 JSON（`[{id,type,function:{name,arguments}}]`）。
	ToolCalls json.RawMessage `json:"-"`
	// ToolCallID：`role:"tool"` 的结果消息指回哪一次调用。
	ToolCallID string `json:"-"`
	// Name：有些上游要求工具结果里带函数名（Pi 的 `requiresToolResultName`）。
	Name string `json:"-"`
}

// Tool：一个工具声明 —— **只进请求体顶层的 `tools` 数组**（不是提示词文本）。
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// Strict：**只在工具自己声明了才发**（有些上游见到不认识的字段直接 400）。
	Strict *bool `json:"strict,omitempty"`
}

// Request：一次上游调用。**SessionID 必填**（见包注释）。
type Request struct {
	Model          string
	Messages       []ChatMessage
	SessionID      string
	CacheRetention CacheRetention
	MaxTokens      int
	Temperature    *float64
	// ReasoningEffort：推理强度（"low"/"medium"/"high"/"max"）—— 各家的取值集合不同，
	// 所以**原样透传**，不做映射；没给就不发。
	ReasoningEffort *string
	// ReasoningSummary：思考摘要的档位（Pi 的 `reasoningSummary`）—— **原样透传**，没给就不发。
	// 只进 Response 协议的 `reasoning.summary`；chat 体不发它。
	ReasoningSummary *string
	// Tools / ToolChoice：**工具透传**（原样进请求体；空 = 不发）。
	Tools      []Tool
	ToolChoice json.RawMessage
	Stream     bool
}

// presets：vendor 的内建定义。**只放端点和兼容开关，不搬模型目录**（模型是发现来的）。
type preset struct {
	baseURL       string
	apiKeyEnvs    []string
	supportsUsage bool // stream_options.include_usage
	supportsStore bool // store: false
	longCache     bool // prompt_cache_retention: "24h"
	maxTokensOld  bool // 用 max_tokens 而不是 max_completion_tokens
	// sessionHeader：会话头（opencode 系必需）
	sessionHeader string
	// reasoningField：**回传思考**时用哪个字段名（"" = 不回传）。
	// 认三种是照 Pi：`reasoning` / `reasoning_content` / `reasoning_text`（各家不同）。
	reasoningField string
	// responsesPath：Response 协议相对 base 的路径（opencode-go/opencode 是 "/responses"；其余空）。
	// ApplyPreset 只在它非空时才填 Endpoints[openai-response]。
	responsesPath string
}

var presets = map[Vendor]preset{
	// 旧名：端点由配置供给（可能是任何 OpenAI 兼容服务），但语义与 custom 一致的那部分照给
	VendorOpenAICompat: {supportsUsage: true, supportsStore: true},
	VendorCustom:       {supportsUsage: true, supportsStore: true},
	VendorOpenAI:       {baseURL: "https://api.openai.com/v1", apiKeyEnvs: []string{"OPENAI_API_KEY"}, supportsUsage: true, supportsStore: true, longCache: true},
	VendorOpenRouter:   {baseURL: "https://openrouter.ai/api/v1", apiKeyEnvs: []string{"OPENROUTER_API_KEY"}, supportsUsage: true},
	// DeepSeek 官方：OpenAI 兼容（`/chat/completions` + `/models` 同形）；思考字段用官方的 `reasoning_content`
	//（接收侧三种全认，发送侧用它 —— 也是 `wireMessages` 里"deepseek vendor 字段必须在"那条规则的落点）。
	VendorDeepseek:   {baseURL: "https://api.deepseek.com", apiKeyEnvs: []string{"DEEPSEEK_API_KEY"}, supportsUsage: true, reasoningField: "reasoning_content"},
	VendorOpenCodeGo: {baseURL: "https://opencode.ai/zen/go/v1", apiKeyEnvs: []string{"OPENCODE_API_KEY"}, supportsUsage: true, maxTokensOld: true, sessionHeader: "x-opencode-session", reasoningField: "reasoning_content", responsesPath: "/responses"},
	VendorOpenCode:   {baseURL: "https://opencode.ai/zen/v1", apiKeyEnvs: []string{"OPENCODE_API_KEY"}, supportsUsage: true, maxTokensOld: true, sessionHeader: "x-opencode-session", reasoningField: "reasoning_content", responsesPath: "/responses"},
	VendorOllama:     {baseURL: "http://127.0.0.1:11434/v1", supportsUsage: false},
	VendorLMStudio:   {baseURL: "http://127.0.0.1:1234/v1", supportsUsage: false},
	VendorTypesafe:   {baseURL: "https://api.typesafe.ai/v1/systemone", apiKeyEnvs: []string{"TYPESAFE_API_KEY", "JEV_API_KEY"}},
	VendorDummy:      {},
}

// ResolveProtocol：发之前定这次说什么话 —— 查 `model_route`（store 已落地），没有 ⇒ chat。
// store 句柄由调用方给（别在 providers 里开库）。
//
// 规则：api=="openai-responses" ⇒ openai-response；其余（含没有行）⇒ chat 缺省。
// 查表键是**渠道 id**（`model_route` 按渠道刷新：同一个 vendor 配两条渠道时各有各的表，
// 拿 vendor 查会串台）。Zen 写死表已删（路由唯一真相 = model_route 表，见 registry 的快照链路）。
func ResolveProtocol(lookup func(providerID, upstreamID string) (string, bool), providerID, model string) Protocol {
	// store 里存的是 models.dev 口径（"openai-responses"，复数，见 registry.NpmToAPI）；
	// providers.Protocol 是单数（"openai-response"）—— 这里认复数那一侧。
	if lookup != nil {
		if api, ok := lookup(providerID, model); ok && api == "openai-responses" {
			return ProtocolOpenAIResponse
		}
	}
	return ProtocolChatCompletion
}

// EffectiveVendor：Vendor 非空用它；否则 Kind 映射（openai-compat→custom，其余逐字）；都空→custom。
func (p Provider) EffectiveVendor() Vendor {
	if p.Vendor != "" {
		return p.Vendor
	}
	if p.Kind != "" {
		if p.Kind == KindOpenAICompat {
			return VendorCustom
		}
		return Vendor(p.Kind)
	}
	return VendorCustom
}

// EffectiveProtocol：空→openai-chat-completion（缺省）。
func (p Provider) EffectiveProtocol() Protocol {
	if p.Protocol != "" {
		return p.Protocol
	}
	return ProtocolChatCompletion
}

// Normalize：把 Kind 别名与空 Protocol 收敛成 Vendor×Protocol（Vendor 非空时 Kind 被忽略）。
func (p Provider) Normalize() Provider {
	p.Vendor = p.EffectiveVendor()
	p.Protocol = p.EffectiveProtocol()
	return p
}

// Validate：vendor×protocol 兼容矩阵（错配 loud error）。
//
//   - chat-completion ← openai-compat、dummy、openai、openrouter、deepseek、opencode-go、
//     opencode、ollama、lmstudio、custom；custom 必须有 base_url（端点全靠配置给）。
//   - openai-response ← opencode-go、opencode、openrouter、custom；custom 必须有 base_url
//     或 Endpoints[openai-response]（端点全靠配置给）；其余 vendor 走它直接错配。
//   - systemone ← typesafe、openrouter、custom；base_url 非空（verbatim 整条 URL）。
//   - 其余 protocol（anthropic-messages、gemini-generate-content）是占位，永远报错"暂不支持"。
func (p Provider) Validate() error {
	vendor := p.EffectiveVendor()
	protocol := p.EffectiveProtocol()
	switch protocol {
	case ProtocolAnthropicMessages, ProtocolGeminiGenerateContent:
		return fmt.Errorf("providers: protocol %q 暂不支持", protocol)
	case ProtocolOpenAIResponse:
		switch vendor {
		case VendorOpenCodeGo, VendorOpenCode, VendorOpenRouter, VendorCustom:
		default:
			return fmt.Errorf("providers: vendor %q 不支持 protocol %q", vendor, protocol)
		}
		if vendor == VendorCustom && strings.TrimSpace(p.BaseURL) == "" &&
			strings.TrimSpace(p.Endpoints[ProtocolOpenAIResponse]) == "" {
			return fmt.Errorf("providers: custom 缺少 base_url（端点全靠配置给，或配 endpoints[openai-response]）")
		}
		return nil
	case ProtocolSystemOne:
		switch vendor {
		case VendorTypesafe, VendorOpenRouter, VendorCustom:
		default:
			return fmt.Errorf("providers: vendor %q 不支持 protocol %q", vendor, protocol)
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("providers: systemone 缺少 base_url（verbatim 整条 URL）")
		}
		return nil
	case ProtocolChatCompletion:
		switch vendor {
		case VendorOpenAICompat, VendorDummy, VendorOpenAI, VendorOpenRouter, VendorDeepseek,
			VendorOpenCodeGo, VendorOpenCode, VendorOllama, VendorLMStudio, VendorCustom:
		default:
			return fmt.Errorf("providers: vendor %q 不支持 protocol %q", vendor, protocol)
		}
		if vendor == VendorCustom && strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("providers: custom 缺少 base_url（端点全靠配置给）")
		}
		return nil
	default:
		return fmt.Errorf("providers: 未知 protocol %q", protocol)
	}
}

// ApplyPreset：把 vendor 的默认值补进 provider（用户显式填了的不动）。
func ApplyPreset(p Provider) Provider {
	preset := presets[p.EffectiveVendor()]
	if p.BaseURL == "" {
		p.BaseURL = preset.baseURL
	}
	if p.APIKey == "" {
		for _, env := range preset.apiKeyEnvs {
			if value := os.Getenv(env); value != "" {
				p.APIKey = value
				break
			}
		}
	}
	// Endpoints：预设专用 —— 聚合站（opencode 系/openrouter）才带这张表（配置里写了 FromConfig 直接忽略+log，
	// 不断老配置）；用户显式填了的不动。
	switch p.EffectiveVendor() {
	case VendorOpenCodeGo, VendorOpenCode, VendorOpenRouter:
		if p.Endpoints == nil {
			p.Endpoints = map[Protocol]string{}
		}
		if _, ok := p.Endpoints[ProtocolChatCompletion]; !ok {
			if url := urlForEndpoint(p.BaseURL, "/chat/completions"); url != "" {
				p.Endpoints[ProtocolChatCompletion] = url
			}
		}
		if _, ok := p.Endpoints[ProtocolOpenAIResponse]; !ok && preset.responsesPath != "" {
			if url := urlForEndpoint(p.BaseURL, preset.responsesPath); url != "" {
				p.Endpoints[ProtocolOpenAIResponse] = url
			}
		}
	}
	// identity 不在这里兜底：它可能来自"服务器级默认特征"（见 config 的 server.identity），
	// 所以空值要**留着**给上层填；`userAgent` 最后兜到 **pi**。
	return p
}

// userAgent：**默认就是 Pi 的形状**（用户 2026-09-29 定）；`microchat`/`bare` 也可选。
//
// 覆写优先级：`client_ua_override`（逐字照用）> 本渠道 identity > 服务器默认 > **pi**。
func userAgent(p Provider) string {
	if p.ClientUAOverride != "" {
		return p.ClientUAOverride
	}
	if p.Identity == "" || p.Identity == IdentityPi { // 空 = 默认 = Pi
		// 照 Pi：`pi (<platform> <release>; <arch>)`
		return fmt.Sprintf("pi (%s %s; %s)", runtime.GOOS, kernelRelease(), goArchToNode(runtime.GOARCH))
	}
	if p.Identity == IdentityBare {
		return "" // Go 自己会补 Go-http-client/1.1（那是"什么都不装"的定义）
	}
	return "microchat/" + Version
}

func kernelRelease() string {
	body, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return runtime.GOOS
	}
	return strings.TrimSpace(string(body))
}

func goArchToNode(arch string) string {
	switch arch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		return arch
	}
}

// sessionHeaderFor：这个渠道发不发会话头、发成什么名字（nil = 按 vendor 默认；"" = 不发）。
func sessionHeaderFor(p Provider) string {
	if p.SessionHeader != nil {
		return *p.SessionHeader
	}
	return presets[p.EffectiveVendor()].sessionHeader
}

// reasoningFieldFor：这个渠道回传思考用哪个字段名（nil = 按 vendor 默认；"" = 不回传）。
func reasoningFieldFor(p Provider) string {
	if p.ReasoningField != nil {
		return *p.ReasoningField
	}
	return presets[p.EffectiveVendor()].reasoningField
}

// reasoningFieldForModel：同上，但 custom/openai-compat 保留旧的模型名启发式 ——
// 模型名含 `deepseek` 时按 DeepSeek 官方字段回传（否则那一路上启发式永远落空）。
func reasoningFieldForModel(p Provider, model string) string {
	if p.ReasoningField != nil {
		return *p.ReasoningField
	}
	vendor := p.EffectiveVendor()
	if (vendor == VendorCustom || vendor == VendorOpenAICompat) && isDeepSeekFamily(model) {
		return "reasoning_content"
	}
	return presets[vendor].reasoningField
}

// toolsAllowed：这个渠道允许工具透传吗（默认**关** —— 宁可少发，也别让上游因为不认识的字段 400）。
func toolsAllowed(p Provider) bool { return p.AllowTools != nil && *p.AllowTools }

// toolResultNameFor：工具结果消息带不带函数名（默认不带）。
func toolResultNameFor(p Provider) bool { return p.ToolResultName != nil && *p.ToolResultName }

// headersFor：**头的唯一拼装处** —— vendor 预设 → identity → 会话头 → 用户 headers（最后，永远能覆盖）。
func headersFor(p Provider, r Request) map[string]string {
	vendor := p.EffectiveVendor()
	sessionHeader := sessionHeaderFor(p)
	headers := map[string]string{}

	// vendor 的内建头
	switch vendor {
	case VendorOpenAI, VendorOpenRouter:
		// 会话亲和（Pi：openai 发三条，openrouter 只发 x-session-id）
		if sessionHeader == "" && r.SessionID != "" {
			if vendor == VendorOpenRouter {
				headers["x-session-id"] = r.SessionID
			} else {
				headers["session_id"] = r.SessionID
				headers["x-client-request-id"] = r.SessionID
				headers["x-session-affinity"] = r.SessionID
			}
		}
	}
	if vendor == VendorOpenRouter {
		// 归属（Pi 那三样；用户 headers 仍可覆盖）
		headers["HTTP-Referer"] = "https://github.com/zincles/microchat"
		headers["X-OpenRouter-Title"] = "microchat"
		headers["X-OpenRouter-Categories"] = "cli-agent"
	}

	// **必需**的会话头（OpenCode 系；09/05 起缺了就报错）—— 名字可覆写，也可明确关掉
	if sessionHeader != "" {
		headers[sessionHeader] = r.SessionID
	}

	// identity：空 = **pi**（默认；服务器级默认在上层填，这层兜最后一道）
	identity := p.Identity
	if identity == "" {
		identity = IdentityPi
	}
	if ua := userAgent(p); ua != "" {
		headers["User-Agent"] = ua
	}
	if vendor == VendorOpenCodeGo || vendor == VendorOpenCode {
		if identity == IdentityPi {
			headers["x-opencode-client"] = "pi"
		} else if identity == IdentityMicrochat {
			headers["x-opencode-client"] = "microchat"
		}
	}
	// Accept：**流式要 text/event-stream**（SSE 的正确语义；omp 那份日志就是这么发的）。
	// Pi 发的是 application/json（它不讲究这个，网关也不查），但我们要发得像回事。
	if r.Stream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "application/json"
	}

	// 用户显式配置最后合并 ⇒ 永远是最后一句话
	for name, value := range p.Headers {
		headers[name] = value
	}
	return headers
}

// bodyFor：**体的唯一拼装处**（照 Pi 的 `openai-completions` 逐项对齐）。
func bodyFor(p Provider, r Request) map[string]any {
	preset := presets[p.EffectiveVendor()]
	reasoningField := reasoningFieldForModel(p, r.Model)
	body := map[string]any{
		"model":    r.Model,
		"messages": wireMessages(r.Messages, reasoningField, p.EffectiveVendor(), r.Model, toolResultNameFor(p)),
		"stream":   r.Stream,
	}
	retention := r.CacheRetention
	if retention == "" {
		retention = CacheAuto
	}
	// prompt_cache_key：只在 OpenAI 官方（且没关缓存）或长缓存时才给
	if (isOpenAIOfficial(p) && retention != CacheNone) || (retention == CacheLong && preset.longCache) {
		if r.SessionID != "" {
			body["prompt_cache_key"] = clampPromptCacheKey(r.SessionID)
		}
	}
	if retention == CacheLong && preset.longCache {
		body["prompt_cache_retention"] = "24h"
	}
	// **只在流式时发**：探针实测 —— 非流式请求带上它，网关直接 400
	//（"stream_options should be set along with stream = true"）。
	if preset.supportsUsage && r.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if preset.supportsStore {
		body["store"] = false
	}
	if r.MaxTokens > 0 {
		if preset.maxTokensOld {
			body["max_tokens"] = r.MaxTokens
		} else {
			body["max_completion_tokens"] = r.MaxTokens
		}
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	// reasoning_effort：推理强度（omp 发 `max`、Pi 发 `high`）—— 只在我们显式给了时才发
	if r.ReasoningEffort != nil && *r.ReasoningEffort != "" {
		body["reasoning_effort"] = *r.ReasoningEffort
	}
	// 工具定义：**只在渠道允许透传时才发**（不认识的字段有些上游会 400）。
	if toolsAllowed(p) && len(r.Tools) > 0 {
		body["tools"] = r.Tools
		if len(r.ToolChoice) > 0 {
			body["tool_choice"] = r.ToolChoice
		}
	} else if hasToolHistory(r.Messages) {
		// **与开关无关**：历史里出现过工具调用 ⇒ 这个参数**必须在**（哪怕空数组）。
		// 否则 `role:"tool"` 的结果消息没有对应的调用，上游会直接拒 —— 这正是 Pi 那条
		// `tools: []` 要防的情况。（回放历史本身也照旧：它是存档的一部分。）
		body["tools"] = []Tool{}
	}
	return body
}

// wireMessages：把消息拼成要发出去的形状。
//
// 两件特殊处理（都照 Pi）：
//  1. assistant 消息带着**当时的思考** ⇒ 补回同一个消息里（字段名随 provider：`reasoning_content` 等）；
//  2. 思考字段**必须存在**的规则按 vendor 走（Pi 的 `openai-completions.ts:1379` 那条）：
//     vendor==deepseek ⇒ 没有思考也补空字符串；vendor 为 custom/openai-compat ⇒ 保留旧的
//     模型名启发式（含 `deepseek` 才补）；其余 vendor 不补。想整块关掉就把 `reasoning_field` 设成 ""。
func wireMessages(messages []ChatMessage, reasoningField string, vendor Vendor, model string, toolResultName bool) []map[string]any {
	requirePresence := false
	if reasoningField != "" {
		switch vendor {
		case VendorDeepseek:
			requirePresence = true
		case VendorCustom, VendorOpenAICompat:
			requirePresence = isDeepSeekFamily(model)
		}
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		item := map[string]any{"role": message.Role, "content": message.Content}
		if reasoningField != "" && message.Role == "assistant" {
			if strings.TrimSpace(message.Reasoning) != "" {
				item[reasoningField] = message.Reasoning
			} else if requirePresence {
				item[reasoningField] = "" // 字段必须在 ⇒ 空着也得在（Pi 的解法）
			}
		}
		// 工具回程：assistant 的 tool_calls 原样带回；`role:"tool"` 的结果指回调用 id
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			item["tool_calls"] = message.ToolCalls
		}
		if message.Role == "tool" {
			item["tool_call_id"] = message.ToolCallID
			if toolResultName && message.Name != "" {
				item["name"] = message.Name // 只有要求的上游才带
			}
		}
		out = append(out, item)
	}
	return out
}

// hasToolHistory：这段历史里出现过工具调用或结果吗。
//
// Pi 的用法：有历史但这次没给 tools ⇒ 仍然发一个**空的 `tools: []`**
// （对话里出现过 tool_calls/tool_results 时，Anthropic 经 LiteLLM 转发要求这个参数必须在）。
func hasToolHistory(messages []ChatMessage) bool {
	for _, message := range messages {
		if len(message.ToolCalls) > 0 || message.Role == "tool" {
			return true
		}
	}
	return false
}

// isDeepSeekFamily：模型名里含 `deepseek` 就算（Pi 的 `isDeepSeek` 是同一套启发式）。
func isDeepSeekFamily(model string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek")
}

func isOpenAIOfficial(p Provider) bool { return strings.Contains(p.BaseURL, "api.openai.com") }

// clampPromptCacheKey：OpenAI 对 prompt_cache_key 有长度限制，超了会被拒。
func clampPromptCacheKey(key string) string {
	const limit = 64
	if len(key) <= limit {
		return key
	}
	return key[:limit]
}

// Build：拼出要发的那一发（**纯函数**，好测）。**不发**（dummy 也走它 —— 调试页看到的就是它）。
//
// 按 protocol 分两路拼 URL（都先 Normalize＋Validate）：chat 走 `bodyFor`（Pi 的 openai-completions 形状），
// openai-response 走 `buildResponse`（Pi 的 openai-responses 形状：数组 input + reasoning/max_output_tokens/store）；
// systemone 走 BuildSystemOne，其余占位已在 Validate 拦下。
func Build(p Provider, r Request) (*http.Request, error) {
	p = ApplyPreset(p.Normalize())
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.SessionID) == "" {
		// 会话 id 是**路由键**：本会话的每一轮、每个辅助调用都必须带上同一个。
		// 忘了传 ⇒ 在这里就报，别让它变成"上游看来时好时坏"的静默 bug。
		return nil, errors.New("providers: SessionID 必填（每会话一个稳定 id —— 路由与提示词缓存都要它）")
	}
	if p.EffectiveProtocol() == ProtocolOpenAIResponse {
		return buildResponse(p, r)
	}
	if p.EffectiveProtocol() != ProtocolChatCompletion {
		// systemone 走 BuildSystemOne，其余占位已在 Validate 拦下。
		return nil, fmt.Errorf("providers: protocol %q 不走 Build（chat 请用空 protocol，systemone 走 BuildSystemOne）", p.Protocol)
	}
	if p.EffectiveVendor() != VendorDummy && p.BaseURL == "" {
		return nil, fmt.Errorf("providers: %s 缺少 base_url", p.ID)
	}
	body, err := json.Marshal(bodyFor(p, r))
	if err != nil {
		return nil, err
	}
	url := endpointURL(p, ProtocolChatCompletion, "/chat/completions")
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, value := range headersFor(p, r) {
		request.Header.Set(name, value)
	}
	request.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	return request, nil
}

// buildResponse：Response 协议那一发（照 Pi `openai-responses.ts:328-378`）。
//
// URL：Endpoints[response] 非空用它（预设按 base 拼的），否则老路 base+`/responses`；
// 体：`{"model","input": [{role,content:[{type:"input_text",text}]}],"stream": 照 r.Stream,
// "reasoning":{effort?,summary?}（给了才发）、"max_output_tokens"（>0 才发）、
// "store":false（opencode 系照 Pi 发；custom 不发）、"temperature"（有就发）}`。
// prompt_cache_key 条件照 chat（opencode 系本来就没有 longCache ⇒ 自然不发，别特意关）。
// history 里没有工具，tool_calls 回放撞上再说（YAGNI）。
func buildResponse(p Provider, r Request) (*http.Request, error) {
	if p.EffectiveVendor() != VendorDummy && p.BaseURL == "" &&
		strings.TrimSpace(p.Endpoints[ProtocolOpenAIResponse]) == "" {
		return nil, fmt.Errorf("providers: %s 缺少 base_url", p.ID)
	}
	input := make([]map[string]any, 0, len(r.Messages))
	for _, message := range r.Messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		input = append(input, map[string]any{
			"role":    role,
			"content": []map[string]any{{"type": "input_text", "text": message.Content}},
		})
	}
	preset := presets[p.EffectiveVendor()]
	body := map[string]any{
		"model":  r.Model,
		"input":  input,
		"stream": r.Stream,
	}
	if r.ReasoningEffort != nil && *r.ReasoningEffort != "" || r.ReasoningSummary != nil && *r.ReasoningSummary != "" {
		reasoning := map[string]any{}
		if r.ReasoningEffort != nil && *r.ReasoningEffort != "" {
			reasoning["effort"] = *r.ReasoningEffort
		}
		if r.ReasoningSummary != nil && *r.ReasoningSummary != "" {
			reasoning["summary"] = *r.ReasoningSummary
		}
		body["reasoning"] = reasoning
	}
	if r.MaxTokens > 0 {
		body["max_output_tokens"] = r.MaxTokens
	}
	// store:false：opencode 系照 Pi 发；custom 不发（用户自备端点，不认识的字段可能 400）。
	// openrouter 走 chat 那条的老口径（预设里没开 supportsStore ⇒ 不发）。
	if vendor := p.EffectiveVendor(); vendor == VendorOpenCodeGo || vendor == VendorOpenCode {
		body["store"] = false
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	retention := r.CacheRetention
	if retention == "" {
		retention = CacheAuto
	}
	if (isOpenAIOfficial(p) && retention != CacheNone) || (retention == CacheLong && preset.longCache) {
		if r.SessionID != "" {
			body["prompt_cache_key"] = clampPromptCacheKey(r.SessionID)
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := endpointURL(p, ProtocolOpenAIResponse, "/responses")
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for name, value := range headersFor(p, r) {
		request.Header.Set(name, value)
	}
	request.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	return request, nil
}

// endpointURL：按 protocol 拼 URL —— Endpoints[protocol] 非空用它（verbatim，原样 POST），
// 否则老路 base_url + 后缀。都先 Normalize＋Validate（调用方 Build 已做）。
func endpointURL(p Provider, protocol Protocol, suffix string) string {
	if url := strings.TrimSpace(p.Endpoints[protocol]); url != "" {
		return url
	}
	return strings.TrimRight(p.BaseURL, "/") + suffix
}

// urlForEndpoint：预设拼端点表用 —— baseURL 去尾 `/` + path；baseURL 为空 ⇒ 空。
func urlForEndpoint(baseURL, path string) string {
	if strings.TrimSpace(baseURL) == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + path
}

// BuildSystemOne：拼出打 SystemOne（JEV）协议的那一发（**纯函数**，好测）。**不发**。
//
// 与 Build 的区别：URL = BaseURL **原样**（不拼路径 —— 它本身就是整条 URL）；
// 头只有 Content-Type + Accept: application/json + Authorization（有 key 才发），用户 headers 最后覆盖；
// 体只有 `{"model","state","questions"}`；SessionID 不进头（state 自带上下文）。
func BuildSystemOne(p Provider, model string, state any, questions map[string]any) (*http.Request, error) {
	p = ApplyPreset(p.Normalize())
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.EffectiveProtocol() != ProtocolSystemOne {
		return nil, fmt.Errorf("providers: protocol %q 不走 BuildSystemOne（systemone 才走这里）", p.Protocol)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return nil, fmt.Errorf("providers: %s 缺少 base_url（verbatim 整条 URL）", p.ID)
	}
	body, err := json.Marshal(map[string]any{
		"model":     model,
		"state":     state,
		"questions": questions,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if p.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	// 用户显式配置最后合并 ⇒ 永远是最后一句话
	for name, value := range p.Headers {
		request.Header.Set(name, value)
	}
	return request, nil
}

// LastPayload：最近一发（**含请求头**）—— `/debug/last-payload` 用它。
//
// 没有它，"为什么 403"只能靠猜（网关拒的往往是头，不是体）。
type LastPayload struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	At      int64             `json:"at"`
}

// Snapshot：把一发请求记成可回显的形状（密钥打码）。
func Snapshot(request *http.Request, at time.Time) (LastPayload, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return LastPayload{}, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	headers := map[string]string{}
	for name, values := range request.Header {
		value := strings.Join(values, ", ")
		if strings.EqualFold(name, "Authorization") && value != "" {
			value = "Bearer ***"
		}
		headers[name] = value
	}
	return LastPayload{
		Method:  request.Method,
		URL:     request.URL.String(),
		Headers: headers,
		Body:    json.RawMessage(body),
		At:      at.UnixMilli(),
	}, nil
}

// FromConfig：**配置 → 线上形状的唯一转换处**。
//
// 为什么必须有它：`config.Provider` 与 `providers.Provider` 字段名一样但类型不同，
// 各处手搓 `providers.Provider{...}` 时**漏掉一个字段就是静默失效** ——
// 已经真发生过（`identity` 声明了却从没被转过去 ⇒ 配置里写 `"identity":"pi"` 被无声忽略）。
func FromConfig(c config.Provider) Provider {
	// Endpoints 是**预设专用**：配置里写了 ⇒ 忽略 + log（不断老配置）。
	// （类型是 map[Protocol]string，config 侧是 map[string]string —— 但**逐项也不搬**：整张忽略。）
	if len(c.Endpoints) > 0 {
		log.Printf("providers: 配置 %q 的 endpoints 是预设专用，已忽略（沿用预设端点表）", c.ID)
	}
	return Provider{
		ID: c.ID, Name: c.Name, Vendor: Vendor(c.Vendor), Kind: Kind(c.Kind),
		Protocol: Protocol(c.Protocol), BaseURL: c.BaseURL,
		Headers: c.Headers, APIKey: c.APIKey, Identity: Identity(c.Identity),
		ClientUAOverride: c.ClientUAOverride, SessionHeader: c.SessionHeader, ReasoningField: c.ReasoningField,
		AllowTools: c.AllowTools, ToolResultName: c.ToolResultName,
		Timeouts:       configTimeouts(c.Timeouts),
		Stream:         c.Stream,
		StoreReasoning: c.StoreReasoning,
	}
}

func configTimeouts(t *config.Timeouts) *Timeouts {
	if t == nil {
		return nil
	}
	return &Timeouts{ConnectSeconds: t.ConnectSeconds, TotalSeconds: t.TotalSeconds}
}

// PresetInfo：一个内建 vendor 的自述（给界面/客户端列出"可选的预设 provider"用）。
type PresetInfo struct {
	Vendor        Vendor     `json:"vendor"`
	Kind          Kind       `json:"kind"` // deprecated：值与 Vendor 逐字相同（老界面照常用）
	Name          string     `json:"name"`
	BaseURL       string     `json:"base_url"`
	NeedsKey      bool       `json:"needs_key"`
	KeyEnv        string     `json:"key_env"`
	SessionHeader string     `json:"session_header,omitempty"`
	Protocols     []Protocol `json:"protocols"`
	// Primary：界面上的"主选"三种（Dummy / 标准 OpenAI 兼容 / OpenCode GO）；其余是便利预设。
	Primary    bool     `json:"primary"`
	Identities []string `json:"identities"`
}

// primaryKinds：**主选**的三种（新建渠道时只该看到这三个 —— 其余是便利预设）。按 vendor。
var primaryKinds = []Vendor{VendorDummy, VendorOpenAICompat, VendorOpenCodeGo}

func isPrimary(vendor Vendor) bool {
	for _, candidate := range primaryKinds {
		if candidate == vendor {
			return true
		}
	}
	return false
}

// vendorProtocols：这个 vendor 支持哪些 protocol（Validate 兼容矩阵的另一面：给界面列"这家能说什么话"用）。
func vendorProtocols(vendor Vendor) []Protocol {
	switch vendor {
	case VendorTypesafe:
		return []Protocol{ProtocolSystemOne}
	case VendorOpenRouter, VendorCustom:
		return []Protocol{ProtocolChatCompletion, ProtocolOpenAIResponse, ProtocolSystemOne}
	case VendorOpenCodeGo, VendorOpenCode:
		return []Protocol{ProtocolChatCompletion, ProtocolOpenAIResponse}
	default:
		return []Protocol{ProtocolChatCompletion}
	}
}

var presetNames = map[Vendor]string{
	VendorOpenAICompat: "标准（OpenAI 兼容）",
	VendorCustom:       "自定义（OpenAI 兼容）",
	VendorDummy:        "本地假上游（不联网）",
	VendorOpenAI:       "OpenAI 官方",
	VendorOpenRouter:   "OpenRouter",
	VendorDeepseek:     "DeepSeek 官方",
	VendorOpenCodeGo:   "OpenCode Go",
	VendorOpenCode:     "OpenCode (Zen)",
	VendorOllama:       "Ollama（本地）",
	VendorLMStudio:     "LM Studio（本地）",
	VendorTypesafe:     "Typesafe (SystemOne)",
}

// Presets：全部内建预设（顺序固定 ⇒ 界面上的下拉稳定）。
func Presets() []PresetInfo {
	// 三种主选在前，其余便利预设殿后
	order := []Vendor{VendorDummy, VendorOpenAICompat, VendorOpenCodeGo, VendorOpenRouter, VendorDeepseek, VendorOpenCode, VendorOpenAI, VendorOllama, VendorLMStudio, VendorTypesafe, VendorCustom}
	list := make([]PresetInfo, 0, len(order))
	for _, vendor := range order {
		preset := presets[vendor]
		keyEnv := ""
		if len(preset.apiKeyEnvs) > 0 {
			keyEnv = preset.apiKeyEnvs[0]
		}
		list = append(list, PresetInfo{
			Vendor: vendor, Kind: Kind(vendor), Name: presetNames[vendor], BaseURL: preset.baseURL,
			NeedsKey: len(preset.apiKeyEnvs) > 0 && vendor != VendorDummy, KeyEnv: keyEnv,
			SessionHeader: preset.sessionHeader,
			Protocols:     vendorProtocols(vendor),
			Primary:       isPrimary(vendor),
			Identities:    []string{string(IdentityMicrochat), string(IdentityPi), string(IdentityBare)},
		})
	}
	return list
}

// Client：真发。只做一件事：把这个请求发出去并回响应。
type Client struct {
	http *http.Client
}

type netDialer struct {
	timeout time.Duration
}

func (d *netDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: d.timeout}).DialContext(ctx, network, address)
}

func NewClient(p Provider) *Client {
	connect, total := 15*time.Second, 300*time.Second // 缺省就像 providers.json 的注释说的：超时必配
	if p.Timeouts != nil {
		if p.Timeouts.ConnectSeconds > 0 {
			connect = time.Duration(p.Timeouts.ConnectSeconds * float64(time.Second))
		}
		if p.Timeouts.TotalSeconds > 0 {
			total = time.Duration(p.Timeouts.TotalSeconds * float64(time.Second))
		}
	}
	return &Client{http: &http.Client{
		Timeout: total,
		Transport: &http.Transport{
			DialContext:         (&netDialer{timeout: connect}).DialContext,
			MaxIdleConnsPerHost: 4,
		},
	}}
}

// Do：发出去。**不解析协议**，只把响应交回调用方（流式由上层用成熟 SSE 库处理）。
func (c *Client) Do(request *http.Request) (*http.Response, error) {
	return c.http.Do(request)
}

// GetJSON：GET 一个 JSON 端点（模型发现 `/models` 之类）—— 仍由**这一个**模块碰网络。
func (c *Client) GetJSON(p Provider, path string, out any) error {
	p = ApplyPreset(p)
	url := strings.TrimRight(p.BaseURL, "/") + path
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// 发现不属于任何会话 ⇒ 不给 SessionID，会话头自然不发（headersFor 只在有值时才加）
	headers := headersFor(p, Request{Model: "*"})
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if p.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	response, err := c.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("providers: GET %s => %d：%s", path, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(response.Body).Decode(out)
}
