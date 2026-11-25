package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"

	"microchat/internal/model"
)

// Build 在 SDK 完成序列化和请求头装配后截获请求。预演与真发共用参数和传输入口。
func Build(p Provider, r Request) (*http.Request, error) {
	var built *http.Request
	captured := errors.New("请求预演完成")
	_, err := invokeSDK(context.Background(), nil, p, r, nil, func(req *http.Request) error {
		built = req.Clone(context.Background())
		var err error
		built.Body, err = req.GetBody()
		if err != nil {
			return err
		}
		return captured
	})
	if errors.Is(err, captured) {
		return built, nil
	}
	return nil, err
}

func (c *Client) Chat(ctx context.Context, p Provider, r Request, localReply string, emit func(Delta)) (Result, error) {
	if p.Stream != nil && !*p.Stream {
		return Result{}, errors.New("providers: 渠道设置了 stream:false，一轮对话需要流式输出")
	}
	r.Stream = true
	return c.generate(ctx, p, r, localReply, emit)
}

func (c *Client) Complete(ctx context.Context, p Provider, r Request, localReply string) (Result, error) {
	r.Stream = false
	return c.generate(ctx, p, r, localReply, nil)
}

func recordRequest(req *http.Request) error {
	snapshot, err := Snapshot(req, time.Now())
	if err == nil {
		RecordLastPayload(snapshot)
	}
	return err
}

func (c *Client) generate(ctx context.Context, p Provider, r Request, localReply string, emit func(Delta)) (Result, error) {
	if localReply != "" {
		if p.Vendor == VendorDummy {
			if req, err := Build(p, r); err == nil {
				_ = recordRequest(req)
				req.Body.Close()
			}
		}
		if r.Stream {
			return streamLocal(ctx, localReply, emit)
		}
		return Result{Text: localReply}, nil
	}
	release, err := acquireEndpoint(ctx, p)
	if err != nil {
		return Result{}, err
	}
	defer release()
	return invokeSDK(ctx, c.http, p, r, emit, recordRequest)
}

// 直接构造两个 SDK service，所选渠道的凭据和地址由 ApplyPreset 唯一解析。
// 一条 custom 渠道不能意外继承另一条渠道在环境里的 OpenAI key、项目或请求头。
func invokeSDK(ctx context.Context, client *http.Client, p Provider, r Request, emit func(Delta), observe func(*http.Request) error) (Result, error) {
	p = ApplyPreset(p.Normalize())
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return Result{}, errors.New("providers: SessionID 必填")
	}
	if raw, ok := r.Override["stream"]; ok {
		var stream *bool
		if err := json.Unmarshal(raw, &stream); err != nil || stream == nil {
			return Result{}, errors.New("providers: stream 必须为布尔值")
		}
		r.Stream = *stream
	}
	if raw, ok := r.Override["model"]; ok {
		if err := json.Unmarshal(raw, &r.Model); err != nil || r.Model == "" {
			return Result{}, errors.New("providers: model 必须为非空字符串")
		}
	}
	if err := checkHistory(r.Messages); err != nil {
		return Result{}, err
	}
	protocol := p.EffectiveProtocol()
	if err := checkToolOverrides(protocol, r.Override); err != nil {
		return Result{}, err
	}
	opts, err := sdkOptions(p, r, client, observe)
	if err != nil {
		return Result{}, err
	}
	var result Result
	switch protocol {
	case ProtocolChatCompletion:
		params, buildErr := chatParams(p, r)
		if buildErr != nil {
			return Result{}, buildErr
		}
		service := openai.NewChatCompletionService(opts...)
		if r.Stream {
			result, err = collectChat(service.NewStreaming(ctx, params), emit)
		} else {
			var reply *openai.ChatCompletion
			reply, err = service.New(ctx, params)
			if err == nil {
				result, err = completionResult(reply)
			}
		}
	case ProtocolOpenAIResponse:
		params, buildErr := responseParams(p, r)
		if buildErr != nil {
			return Result{}, buildErr
		}
		service := responses.NewResponseService(opts...)
		if r.Stream {
			result, err = collectResponse(service.NewStreaming(ctx, params), emit)
		} else {
			var reply *responses.Response
			reply, err = service.New(ctx, params)
			if err == nil {
				result, err = responseResult(reply)
			}
		}
	default:
		return Result{}, fmt.Errorf("providers: %q 不属于对话协议", protocol)
	}
	if err != nil {
		var streamErr *ssestream.StreamError
		if errors.As(err, &streamErr) {
			return Result{}, &UpstreamError{Status: http.StatusOK, Body: snippet(string(streamErr.Event.Data))}
		}
		return Result{}, err
	}
	for _, call := range result.ToolCalls {
		if !allowsFunction(r, p, call.Name) {
			return Result{}, fmt.Errorf("providers: 模型请求了未声明的函数 %q", call.Name)
		}
	}
	if !r.Stream && emit != nil {
		emit(Delta{Text: result.Text, Reasoning: result.Reasoning})
	}
	result.Exchange.Provider = p.ID
	result.Exchange.Protocol = string(protocol)
	result.Exchange.Model = r.Model
	if protocol == ProtocolOpenAIResponse {
		result.Exchange.EndpointHash = responseEndpointHash(p)
	}
	result.Usage = p.costAwareUsage(result.Usage)
	result.Exchange.Usage = result.Usage
	return result, nil
}

func sdkOptions(p Provider, r Request, client *http.Client, observe func(*http.Request) error) ([]option.RequestOption, error) {
	protocol := p.EffectiveProtocol()
	base := p.BaseURL
	var endpoint *url.URL
	if target := p.Endpoints[protocol]; target != "" {
		var err error
		endpoint, err = url.Parse(target)
		if err != nil {
			return nil, err
		}
		base = target
	}
	if base == "" {
		if p.Vendor != VendorDummy {
			return nil, fmt.Errorf("providers: %s 缺少 base_url", p.ID)
		}
		base = "/" // dummy 预演保留相对地址
	}
	opts := []option.RequestOption{option.WithBaseURL(base), option.WithMaxRetries(0), option.WithAPIKey(p.APIKey), option.WithHeaderDel("User-Agent")}
	for name, value := range headersFor(p, r) {
		opts = append(opts, option.WithHeader(name, value))
	}
	if client != nil {
		opts = append(opts, option.WithHTTPClient(client))
	}
	opts = append(opts, option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		if endpoint != nil {
			req.URL = endpoint
			req.Host = endpoint.Host
		}
		if err := observe(req); err != nil {
			return nil, err
		}
		resp, err := next(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, &UpstreamError{Status: resp.StatusCode, Body: snippet(string(body))}
		}
		if !r.Stream {
			resp.Body = http.MaxBytesReader(nil, resp.Body, 4<<20)
			// 部分兼容端点把 JSON 标成 text/plain；这里明确请求的是 JSON。
			resp.Header.Set("Content-Type", "application/json")
		}
		return resp, nil
	}))
	return opts, nil
}

func responseEndpointHash(p Provider) string {
	target := p.Endpoints[ProtocolOpenAIResponse]
	if target == "" {
		target = urlForEndpoint(p.BaseURL, "/responses")
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(string(p.Vendor)+"\n"+target)))
}

// checkHistory 保证每次本地调用及其唯一结果在同一组，两个协议共享该约束。
func checkHistory(messages []ChatMessage) error {
	pending := map[string]bool{}
	for _, m := range messages {
		if m.Role == "tool" {
			if !pending[m.ToolCallID] {
				return fmt.Errorf("providers: 工具结果没有对应的调用 %q", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) != 0 {
			return errors.New("providers: 工具调用缺少结果")
		}
		for _, call := range m.ToolCalls {
			if m.Role != "assistant" || call.ID == "" || call.Name == "" || pending[call.ID] {
				return fmt.Errorf("providers: 无法配对工具调用 %q (%s)", call.ID, call.Name)
			}
			pending[call.ID] = true
		}
	}
	if len(pending) != 0 {
		return errors.New("providers: 工具调用缺少结果")
	}
	return nil
}

func cacheKey(p Provider, r Request) string {
	if isOpenAIOfficial(p) && r.CacheRetention != CacheNone || r.CacheRetention == CacheLong && presets[p.Vendor].longCache {
		return clampPromptCacheKey(r.SessionID)
	}
	return ""
}

// SDK 为 NewStreaming 设置 stream=true。显式 false 与渠道级扩展仍可在预演看到。
// 这里合并调试覆盖时只解释顶层键，保留 JSON 数值精度以及 null 的含义。
func extraFields(r Request) map[string]any {
	fields := map[string]any{"stream": r.Stream}
	for key, raw := range r.Override {
		fields[key] = raw
	}
	return fields
}

func newResult(id, reason string) Result {
	return Result{Exchange: &model.Exchange{ResponseID: id, FinishReason: reason}}
}

func (r Result) checked() (Result, error) {
	seen := make(map[string]bool, len(r.ToolCalls))
	for _, call := range r.ToolCalls {
		args := strings.TrimSpace(call.Arguments)
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || seen[call.ID] || !strings.HasPrefix(args, "{") || !json.Valid([]byte(args)) {
			return Result{}, fmt.Errorf("providers: 不完整或重复的工具调用 %q (%s)", call.ID, call.Name)
		}
		seen[call.ID] = true
	}
	if len(r.ToolCalls) == 0 && len(r.ServerTools) == 0 && strings.TrimSpace(r.Text) == "" {
		return Result{}, ErrEmpty
	}
	return r, nil
}

// Archive 只保存实际发生的模型调用，dummy/fallback 不产生上游记录。
func (r Result) Archive(keepReasoning bool) []*model.Exchange {
	if r.Exchange == nil {
		return nil
	}
	return []*model.Exchange{r.Exchange.ForStorage(keepReasoning)}
}
