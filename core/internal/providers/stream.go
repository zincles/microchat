// 流式调用：一次上游请求的**全程** —— 拼请求（`Build`）→ 留档（`RecordLastPayload`）→
// 真发 → 用**成熟库**解析 SSE → 把增量交给动画 → 返回**整段**。
//
// 四条纪律：
//  1. **不手搓分帧**：`data:` 的分帧 / 多行 / `[DONE]` / 注释帧都归 `go-sse`（成熟库，专门干这个）；
//  2. **发之前留档**：`/debug/last-payload` 靠它 —— 网关拒的往往是**头**不是体；
//  3. **上游报错原样返回**：不重试、不吞错（非 2xx 连正文一起带回；200 开流后报的错也算）；
//  4. **增量只服务动画**：落库与一切计算只认返回的整段正文。
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tmaxmax/go-sse"
)

// Delta：流式增量的一小段（**正文与思考分开**：思考只服务动画，正文才是最终消息的内容）。
//
// 一帧里两者最多只有一个非空（上游就是这么发的：先思考、后正文）。
type Delta struct {
	Text      string
	Reasoning string
	// Tool：服务端工具的状态变化（只有 kind/state，不产生正文）。ok=nil 表示这帧不是工具事件。
	Tool *ToolEvent
}

// ToolSource：服务端搜索工具拿回来的一个来源（xAI 的 web_search / x_search 都有）。
type ToolSource struct {
	Type  string `json:"type,omitempty"`
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// ToolEvent：服务端工具的一次状态变化（xAI Responses 的 *_call.in_progress/searching/completed）。
// `Kind` 用上游事件里的名字（web_search / x_search / code_interpreter …），界面负责翻成人话。
// Sources 只在 output_item.done 那一帧有值（action.sources）。
type ToolEvent struct {
	ItemID  string
	Kind    string
	State   string
	Query   string
	Queries []string
	Sources []ToolSource
}

// ToolCall：模型发起的一次客户端 function call（流式里按 index 累积）。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Result：一次流式调用的结局 —— **整段**正文 + 整段思考 + 归一化后的用量 + 工具调用。
type Result struct {
	Text      string
	Reasoning string
	Usage     json.RawMessage
	ToolCalls []ToolCall
}

// UpstreamError：上游报的错（非 2xx，或 200 开着流却在帧里报 error 对象）。
//
// **原样带回**：不重试、不吞错、不改写文案 —— 密钥错 / 模型名错 / 额度用完，用户要看的就是上游那句话。
type UpstreamError struct {
	Status int
	Body   string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("上游 %d：%s", e.Status, e.Body)
}

// ErrEmpty：上游 2xx、流也读完了，但一个字正文都没有。
//
// 这不是"空回复"而是**异常**：多半是上游把错误写在了别处、或这家根本不支持流式。
// 与其往库里塞一条空消息，不如让这一轮失败（库里干干净净）。
var ErrEmpty = errors.New("providers: 上游回了个空的（整条流里没有正文）")

// maxEventSize：单帧上限（默认 64KB 对长思考段太紧）。
const maxEventSize = 1 << 20

// Chat：一次**流式**调用 —— 本项目唯一真发的地方。
//
// `localReply` 非空 ⇒ 走**本地假上游**（`dummy` / 没配模型）：请求照拼、照留档（调试页看得到），
// 但不联网，把这句确定性的话**分片**吐出来。
//
// `onDelta` 只服务动画（可以为 nil）；返回的整段才是落库与一切计算的依据。
func (c *Client) Chat(ctx context.Context, p Provider, r Request, localReply string, onDelta func(Delta)) (Result, error) {
	// 一轮生成**只走流式**（界面要动画）；这家渠道要是配了 `stream:false` ⇒ **明说**，
	// 别静默按流式发（那会让配了 stream:false 的人对着"时好时坏"猜）。
	// （"整段拿结果"的辅助调用不走这里 —— 它们走 `Complete`，那一发才是非流式的。）
	if p.Stream != nil && !*p.Stream {
		return Result{}, errors.New("providers: 这家渠道配了 stream:false，而一轮生成只走流式（这家的模型用来聊天得支持流式）")
	}
	r.Stream = true
	if localReply != "" {
		// 本地假上游：**有渠道就把"本来会发出去什么"留档**（dummy 的意义就在这儿 ——
		// 没有 key 也能把装配验一遍）；没配渠道（fallback）就没有可留的。
		if p.EffectiveVendor() == VendorDummy {
			if request, err := Build(p, r); err == nil {
				if payload, err := Snapshot(request, time.Now()); err == nil {
					RecordLastPayload(payload)
				}
			}
		}
		return streamLocal(ctx, localReply, onDelta)
	}
	request, err := Build(p, r)
	if err != nil {
		return Result{}, err
	}
	// **发之前**留档：调试页要看的正是"最近一次真发出去的请求"（含请求头；密钥已打码）
	if payload, err := Snapshot(request, time.Now()); err == nil {
		RecordLastPayload(payload)
	}
	release, err := acquireEndpoint(ctx, p)
	if err != nil {
		return Result{}, err
	}
	defer release()
	// 按 `p.EffectiveProtocol()` 分两路：chat 走老 `streamUpstream`，response 走新解析
	// （URL/体由 `Build` 按 protocol 拼好 —— Chat 不自己拼 URL）。
	var result Result
	if p.EffectiveProtocol() == ProtocolOpenAIResponse {
		result, err = c.streamResponseUpstream(ctx, request, onDelta)
	} else {
		result, err = c.streamUpstream(ctx, request, onDelta)
	}
	if err != nil {
		return Result{}, err
	}
	// 不报费用的渠道：cost_usd 一律摘掉（含上游回了 cost: 0 的情况）。
	result.Usage = p.costAwareUsage(result.Usage)
	return result, nil
}

// completionResponse：**非流式**那一发的响应形状（OpenAI 兼容的正常形状）。
//
// 复用 `streamDelta`：它认的那几个字段名（正文 + 三种思考字段）与非流式的 `message` 逐字一样。
type completionResponse struct {
	Choices []struct {
		Message streamDelta `json:"message"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete：一次**非流式**调用 —— 摘要 / 标题这类"整段拿结果"的辅助调用用它。
//
// 与 `Chat` 共用同一套东西：`Build` 拼请求、`Snapshot` + `RecordLastPayload` 留档、
// `NormalizeUsage` 归口用量、上游报错原样返回（**不重试、不吞错**）。
//
// 为什么辅助调用不流式：没人看它一个字一个字往外蹦（增量只服务动画）；整段一次拿到反而干净。
// `localReply` 非空 ⇒ 走本地假上游（不联网），**并且立刻返回**（没有 `streamLocal` 那套节奏 ——
// 那是给"看得到 pending/streaming"的一轮生成用的）。
func (c *Client) Complete(ctx context.Context, p Provider, r Request, localReply string) (Result, error) {
	r.Stream = false
	if localReply != "" {
		// 有渠道就把"本来会发出去什么"留档（dummy 的意义就在这儿：没有 key 也能把装配验一遍）
		if p.EffectiveVendor() == VendorDummy {
			if request, err := Build(p, r); err == nil {
				if payload, err := Snapshot(request, time.Now()); err == nil {
					RecordLastPayload(payload)
				}
			}
		}
		return Result{Text: localReply}, nil
	}
	request, err := Build(p, r)
	if err != nil {
		return Result{}, err
	}
	// **发之前**留档：与流式那条路同一个口径（`/debug/last-payload` 要看的正是它）
	if payload, err := Snapshot(request, time.Now()); err == nil {
		RecordLastPayload(payload)
	}
	release, err := acquireEndpoint(ctx, p)
	if err != nil {
		return Result{}, err
	}
	defer release()
	response, err := c.Do(request.WithContext(ctx))
	if err != nil {
		return Result{}, err // 连不上 / 超时 / 被取消：原样返回（不重试、不吞）
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCompletionBytes))
	if err != nil {
		return Result{}, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return Result{}, &UpstreamError{Status: response.StatusCode, Body: snippet(string(body))}
	}
	// response 系走 output_text 数组拼正文（reasoning_summary 吞了记 log）。
	if p.EffectiveProtocol() == ProtocolOpenAIResponse {
		result, err := completeResponse(response.StatusCode, body)
		if err != nil {
			return Result{}, err
		}
		result.Usage = p.costAwareUsage(result.Usage)
		return result, nil
	}
	var parsed completionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Result{}, fmt.Errorf("providers: 解析响应失败：%w", err)
	}
	if parsed.Error != nil {
		return Result{}, &UpstreamError{Status: response.StatusCode, Body: snippet(parsed.Error.Message)}
	}
	result := Result{Usage: NormalizeUsage(parsed.Usage)}
	for _, choice := range parsed.Choices {
		result.Text += choice.Message.Content
		result.Reasoning += choice.Message.thinking()
	}
	if strings.TrimSpace(result.Text) == "" {
		return Result{}, ErrEmpty
	}
	result.Usage = p.costAwareUsage(result.Usage)
	return result, nil
}

// maxCompletionBytes：非流式响应体的上限（一段摘要不该有几十 MB；防的是"上游回了一整页 HTML"）。
const maxCompletionBytes = 1 << 22

// streamUpstream：真发一次并把响应读完。
func (c *Client) streamUpstream(ctx context.Context, request *http.Request, onDelta func(Delta)) (Result, error) {
	response, err := c.Do(request.WithContext(ctx))
	if err != nil {
		return Result{}, err // 连不上 / 超时 / 被取消：原样返回（不重试、不吞）
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		// 密钥错 / 模型名错 / 额度这类问题要在**开流之前**说清楚，
		// 别等流里冒出一堆解析错误（旧版的原话，照做）
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Result{}, &UpstreamError{Status: response.StatusCode, Body: snippet(string(body))}
	}
	return readStream(response.StatusCode, response.Body, onDelta)
}

// streamResponseUpstream：Response 协议那半程 —— 状态码检查与 streamUpstream 同口径，
// 解析走 `readResponseStream`（URL/体由 `Build` 按 protocol 拼好，这里不拼）。
func (c *Client) streamResponseUpstream(ctx context.Context, request *http.Request, onDelta func(Delta)) (Result, error) {
	response, err := c.Do(request.WithContext(ctx))
	if err != nil {
		return Result{}, err // 连不上 / 超时 / 被取消：原样返回（不重试、不吞）
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Result{}, &UpstreamError{Status: response.StatusCode, Body: snippet(string(body))}
	}
	return readResponseStream(response.StatusCode, response.Body, onDelta)
}

// streamChunk：一帧的 JSON（OpenAI 兼容的流式形状）。
type streamChunk struct {
	Choices []struct {
		Delta streamDelta `json:"delta"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	// Error：有些上游 200 开着流再报错（形状各家不同，只取人话那一句）。
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamDelta：增量。正文一个字段，**思考认三种字段名**（发送侧用哪个由 `reasoning_field` 决定，
// 接收侧一律全认 —— 各家写法不同：OpenRouter 系 `reasoning`、DeepSeek 官方 `reasoning_content`）。
type streamDelta struct {
	Content          string          `json:"content"`
	Reasoning        *string         `json:"reasoning"`
	ReasoningContent *string         `json:"reasoning_content"`
	ReasoningText    *string         `json:"reasoning_text"`
	ToolCalls        []toolCallDelta `json:"tool_calls"`
}

type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (d streamDelta) thinking() string {
	for _, candidate := range []*string{d.Reasoning, d.ReasoningContent, d.ReasoningText} {
		if candidate != nil && *candidate != "" {
			return *candidate
		}
	}
	return ""
}

// readStream：SSE 解析 —— **分帧交给 go-sse**，这里只做"把 delta 拼起来"。
//
// 解析不了的帧（心跳、注释、别家塞的私货）跳过，不打断整条流；`[DONE]` 收工。
func readStream(status int, body io.Reader, onDelta func(Delta)) (Result, error) {
	var result Result
	calls := map[int]*ToolCall{}
	callOrder := []int{}
	for event, err := range sse.Read(body, &sse.ReadConfig{MaxEventSize: maxEventSize}) {
		if err != nil {
			return Result{}, fmt.Errorf("providers: 解析流失败：%w", err)
		}
		data := strings.TrimSpace(event.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue // 心跳 / 注释 / 别家的私货：跳过，别让一个怪帧打断整条流
		}
		if chunk.Error != nil {
			return Result{}, &UpstreamError{Status: status, Body: snippet(data)}
		}
		// 用量在最后一个 chunk 里报告；一路覆盖，最后那个为准
		if usage := NormalizeUsage(chunk.Usage); len(usage) > 0 {
			result.Usage = usage
		}
		for _, choice := range chunk.Choices {
			for _, delta := range choice.Delta.ToolCalls {
				call, exists := calls[delta.Index]
				if !exists {
					call = &ToolCall{}
					calls[delta.Index] = call
					callOrder = append(callOrder, delta.Index)
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Function.Name != "" {
					call.Name = delta.Function.Name
				}
				call.Arguments += delta.Function.Arguments
			}
			if thinking := choice.Delta.thinking(); thinking != "" {
				result.Reasoning += thinking
				if onDelta != nil {
					onDelta(Delta{Reasoning: thinking})
				}
			}
			if content := choice.Delta.Content; content != "" {
				result.Text += content
				if onDelta != nil {
					onDelta(Delta{Text: content})
				}
			}
		}
	}
	for _, index := range callOrder {
		result.ToolCalls = append(result.ToolCalls, *calls[index])
	}
	if result.Text == "" && len(result.ToolCalls) == 0 {
		return Result{}, ErrEmpty
	}
	return result, nil
}

// toolEventFromType：从 `response.<kind>_call.<state>` 认出服务端工具状态。
//
// 不写死 kind/state：xAI 的工具列表会变（web_search / x_search / code_interpreter /
// image_generation / mcp / shell …），只要上游沿用 `*_call.<state>` 这一套，就能显示。
// 只认以 `.` 分开的两段，别把 `response.completed` 或 `..._call_code.delta` 误认成工具。
func toolEventFromType(eventType string) *ToolEvent {
	const prefix = "response."
	if !strings.HasPrefix(eventType, prefix) {
		return nil
	}
	rest := strings.TrimPrefix(eventType, prefix)
	dot := strings.LastIndexByte(rest, '.')
	if dot <= 0 || dot == len(rest)-1 {
		return nil
	}
	kindPart, state := rest[:dot], rest[dot+1:]
	if !strings.HasSuffix(kindPart, "_call") {
		return nil
	}
	kind := strings.TrimSuffix(kindPart, "_call")
	if kind == "" || kind == "reasoning" || kind == "message" {
		return nil
	}
	return &ToolEvent{Kind: kind, State: state}
}

// toolEventFromOutputItem：`response.output_item.done` 里的 web_search_call 带
// `action.query` 与 `action.sources`（实测）；把来源收成一条工具事件，供界面列出来。
//
// 没有 sources 的 item（reasoning / message / code）返回 nil。
func toolEventFromOutputItem(raw string) *ToolEvent {
	var frame struct {
		Item struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
			Action struct {
				Type    string   `json:"type"`
				Query   string   `json:"query"`
				Queries []string `json:"queries"`
				Sources []struct {
					Type  string `json:"type"`
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"sources"`
			} `json:"action"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(raw), &frame) != nil || frame.Item.Status != "completed" {
		return nil
	}
	kind := strings.TrimSuffix(frame.Item.Type, "_call")
	if kind == "" {
		return nil
	}
	sources := make([]ToolSource, 0, len(frame.Item.Action.Sources))
	for _, source := range frame.Item.Action.Sources {
		url := strings.TrimSpace(source.URL)
		if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
			continue // 不把 javascript: / data: 一类的 URL 交给界面
		}
		sources = append(sources, ToolSource{Type: source.Type, URL: url, Title: source.Title})
	}
	queries := append([]string{}, frame.Item.Action.Queries...)
	if strings.TrimSpace(frame.Item.Action.Query) != "" {
		queries = append([]string{frame.Item.Action.Query}, queries...)
	}
	if len(sources) == 0 && len(queries) == 0 {
		return nil
	}
	return &ToolEvent{
		ItemID: strings.TrimSpace(frame.Item.ID), Kind: kind, State: "completed",
		Query: strings.TrimSpace(frame.Item.Action.Query), Queries: queries, Sources: sources,
	}
}

// readResponseStream：Response 协议的 SSE 解析（照 Pi `processResponsesStream` 全套事件，
// **先只做文本**：`response.output_text.delta` → 正文）。
//
// 事件分派：
//   - `response.output_text.delta`（`delta` 字符串）拼正文；
//   - `response.reasoning_summary_text.delta/part.done` 与 `response.reasoning_text.delta` ⇒
//     吞了记 log（别丢、别入库 —— 思考摘要只服务动画那类观测，这里连动画都不进）；
//   - `response.refusal`/`response.failed`/`response.error` ⇒ 整轮失败（文案带事件名）；
//   - `response.completed` ⇒ 收工（usage 在 `response.completed.response.usage` 里，认出来就归一化 ——
//     认不出也不报错，usage 本来就是"有就好"）；`[DONE]` 照旧收工；别的事件跳过。
//
// 空正文 ⇒ `ErrEmpty`（与 chat 同一条脾气）。
func readResponseStream(status int, body io.Reader, onDelta func(Delta)) (Result, error) {
	var result Result
	for event, err := range sse.Read(body, &sse.ReadConfig{MaxEventSize: maxEventSize}) {
		if err != nil {
			return Result{}, fmt.Errorf("providers: 解析流失败：%w", err)
		}
		data := strings.TrimSpace(event.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(data), &envelope) != nil || envelope.Type == "" {
			continue // 心跳 / 注释 / 别家的私货：跳过，别让一个怪帧打断整条流
		}
		switch envelope.Type {
		case "response.output_text.delta":
			var frame struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil {
				continue
			}
			if frame.Delta == "" {
				continue
			}
			result.Text += frame.Delta
			if onDelta != nil {
				onDelta(Delta{Text: frame.Delta})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_summary_part.done",
			"response.reasoning_text.delta":
			log.Printf("providers: response reasoning 事件吞掉（%s，别入库）", envelope.Type)
		case "response.refusal", "response.refusal.delta":
			return Result{}, &UpstreamError{Status: status, Body: snippet("response.refusal：" + data)}
		case "response.failed", "response.error", "error":
			return Result{}, &UpstreamError{Status: status, Body: snippet(envelope.Type + "：" + data)}
		case "response.output_item.done":
			if event := toolEventFromOutputItem(data); event != nil {
				if onDelta != nil {
					onDelta(Delta{Tool: event})
				}
				continue
			}
		case "response.completed", "response.incomplete":
			var frame struct {
				Response struct {
					Usage json.RawMessage `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(data), &frame) == nil {
				if usage := NormalizeUsage(frame.Response.Usage); len(usage) > 0 {
					result.Usage = usage
				}
			}
			if envelope.Type == "response.incomplete" {
				log.Printf("providers: response incomplete（照 completed 收工，原因见帧）")
			}
		default:
			// 工具调用：response.web_search_call.in_progress / .searching / .completed
			// （X 搜索同理；代码执行的上游事件名是 code_interpreter_call）。
			if event := toolEventFromType(envelope.Type); event != nil {
				if onDelta != nil {
					onDelta(Delta{Tool: event})
				}
				continue
			}
			// created/output_item.added/part.done/function_call_arguments/custom_tool_call_input/
			// output_item.done 等：现在只做文本，别的都跳过。
		}
	}
	if strings.TrimSpace(result.Text) == "" {
		return Result{}, ErrEmpty
	}
	return result, nil
}

// responseOutputText：非流式 Response 体里一段 content（`output_text` 拼正文，`refusal` 照失败算）。
// 只认 `type` + `text` 两个键，别家塞的私货跳过。
func responseOutputText(content []map[string]any) (text string, refused bool) {
	for _, part := range content {
		kind, _ := part["type"].(string)
		words, _ := part["text"].(string)
		switch kind {
		case "output_text":
			text += words
		case "refusal":
			refused = true
			text += words
		}
	}
	return text, refused
}

// completeResponse：Response 协议非流式那一发 —— `response.output.output[].content[]` 里
// `output_text` 数组拼正文；reasoning_summary 吞了记 log；refusal ⇒ 整轮失败；空正文 ⇒ ErrEmpty。
func completeResponse(status int, body []byte) (Result, error) {
	var parsed struct {
		Output []struct {
			Type    string `json:"type"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Content []map[string]any `json:"content"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Result{}, fmt.Errorf("providers: 解析响应失败：%w", err)
	}
	if parsed.Error != nil {
		return Result{}, &UpstreamError{Status: status, Body: snippet(parsed.Error.Code + "：" + parsed.Error.Message)}
	}
	var result Result
	for _, item := range parsed.Output {
		if item.Type == "reasoning" && len(item.Summary) > 0 {
			log.Printf("providers: response reasoning 摘要吞掉（非流式，别入库）")
			continue
		}
		if len(item.Content) == 0 {
			continue
		}
		text, refused := responseOutputText(item.Content)
		if refused {
			return Result{}, &UpstreamError{Status: status, Body: snippet("response.refusal：" + text)}
		}
		result.Text += text
	}
	if usage := NormalizeUsage(parsed.Usage); len(usage) > 0 {
		result.Usage = usage
	}
	if strings.TrimSpace(result.Text) == "" {
		return Result{}, ErrEmpty
	}
	return result, nil
}

// ── 本地假上游（dummy / fallback）──

// 本地假上游的节奏：**先停一下**（`pending` 才看得见），再**一个字一个字**吐（`streaming` 与
// "耗时"才看得见）。快节奏（0.2s 首字 + 全文约 0.8s）：足够看清三个状态，又不碍事。
const (
	localFirstDelay = 200 * time.Millisecond
	// localChunkTotal：正文分片阶段的总时长预算 —— 按字数均摊，约 0.8s 说完。
	localChunkTotal = 800 * time.Millisecond
)

// streamLocal：不联网的假上游 —— 把这句确定性的话分片吐出来。
//
// 分片阶段的总时长固定（`localChunkTotal`）：长话短话都约 0.8s 说完 ——
// 真上游的节奏本来就与字数无关（吐得快慢看 tokens/s），固定每字延时会让短话快、长话慢。
// 被取消（用户按停 / 这一轮被顶掉）就在下一个停顿处退出，返回 ctx 的错（调用方据它判定"取消"）。
func streamLocal(ctx context.Context, reply string, onDelta func(Delta)) (Result, error) {
	if err := sleep(ctx, localFirstDelay); err != nil {
		return Result{}, err
	}
	runes := []rune(reply)
	chunkDelay := time.Duration(0)
	if len(runes) > 0 {
		chunkDelay = localChunkTotal / time.Duration(len(runes))
	}
	var text strings.Builder
	for _, r := range runes {
		if err := sleep(ctx, chunkDelay); err != nil {
			return Result{}, err
		}
		chunk := string(r)
		text.WriteString(chunk)
		if onDelta != nil {
			onDelta(Delta{Text: chunk})
		}
	}
	return Result{Text: text.String()}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ── 用量归一化（**只有这一处**认识各家字段名）──

// Usage：**归一化后**的用量。界面与接口只认这几个键（`cached_tokens` 这类），
// 别处的字段名（`prompt_cache_hit_tokens` 之类）只在 `NormalizeUsage` 里出现。
//
// 未命中部分 = `prompt_tokens − cached_tokens`（**推导**，不另存 —— 存了就是第二个真相来源）。
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	// CostUSD：这一发**实际扣了多少钱**（上游自己报的，不是我们按价目表算的）。
	//
	// 只有聚合站报（OpenRouter 的 `usage.cost`，见 `reminder/openrouter.md`）；直连渠道
	// （DeepSeek / OpenCode GO）**不回这一格** ⇒ 指针为 nil。用指针就是为了把
	// **"免费（0）"与"上游没说（nil）"**分开 —— 后者不该在界面上显示成 $0.00。
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// Raw：上游原样的那份（留档备查；**别再往它上面写逻辑**）。
	Raw json.RawMessage `json:"raw,omitempty"`
}

// NormalizeUsage：上游 `usage` → 本项目口径。认不出来 ⇒ **回 nil**：
// 宁可不显示，也别显示一排 0 假装有数据。
func NormalizeUsage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var wire struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		TotalTokens      *int64 `json:"total_tokens"`
		PromptDetails    struct {
			CachedTokens *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		// 缓存命中的其它写法：DeepSeek 系 `prompt_cache_hit_tokens`、Anthropic 系 `cache_read_input_tokens`
		PromptCacheHitTokens *int64 `json:"prompt_cache_hit_tokens"`
		CacheReadInputTokens *int64 `json:"cache_read_input_tokens"`
		CompletionDetails    struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		ReasoningTokens *int64 `json:"reasoning_tokens"`
		// Cost：OpenRouter 的 `usage.cost`（**美元**，已经扣完的那一笔）。
		// 用 RawMessage 接：正常是数字，但"数字写成字符串"这种事在上游见得够多了 ——
		// 那不该让整份用量归零（宁可这一格没有，别的数照给）。
		Cost json.RawMessage `json:"cost"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.PromptTokens == nil {
		return nil // 连 prompt_tokens 都没有 ⇒ 这份用量我们不认识
	}
	prompt := *wire.PromptTokens
	completion := numberOr(wire.CompletionTokens, 0)
	usage := Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      numberOr(wire.TotalTokens, prompt+completion),
		CachedTokens:     firstNumber(wire.PromptDetails.CachedTokens, wire.PromptCacheHitTokens, wire.CacheReadInputTokens),
		ReasoningTokens:  firstNumber(wire.CompletionDetails.ReasoningTokens, wire.ReasoningTokens),
		CostUSD:          parseCost(wire.Cost),
		Raw:              raw,
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return nil
	}
	return encoded
}

// costAwareUsage：按渠道能力决定这份用量能不能带 cost_usd。
//
// 不支持费用的渠道（DeepSeek / OpenCode GO / 本地模型…）即使上游回了 `cost: 0`，
// 那也不是"免费"，而是"这家不报"—— 从归一化结果里摘掉，界面就不会画出 $0。
// OpenRouter 的 `cost: 0` 是真的免费，仍然保留。
func (p Provider) costAwareUsage(usage json.RawMessage) json.RawMessage {
	if p.ReportsCost() || len(usage) == 0 {
		return usage
	}
	var normalized Usage
	if json.Unmarshal(usage, &normalized) != nil || normalized.CostUSD == nil {
		return usage
	}
	normalized.CostUSD = nil
	out, err := json.Marshal(normalized)
	if err != nil {
		return usage
	}
	return out
}

func numberOr(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}

// firstNumber：取第一个给出来的数（各家字段名不一，谁先给出就用谁）。
func firstNumber(values ...*int64) int64 {
	for _, value := range values {
		if value != nil {
			return *value
		}
	}
	return 0
}

// parseCost：把上游那个"这一发花了多少钱"解成美元数。
//
// 认两种写法：JSON 数字（正常）与数字字符串（见过上游这么发）。**认不出来就回 nil**
// —— 别用 0 冒充"免费"：这一格的含义是"上游说的"，不是"我们猜的"。
func parseCost(raw json.RawMessage) *float64 {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil
	}
	text = strings.Trim(text, `"`)
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

// snippet：错误正文缩成一行、截到 300 字 —— 够看清原因，又不把整页 HTML 灌进界面。
func snippet(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= 300 {
		return flat
	}
	return string(runes[:300]) + "..."
}
