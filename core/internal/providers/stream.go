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
	"net/http"
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
}

// Result：一次流式调用的结局 —— **整段**正文 + 整段思考 + 归一化后的用量。
type Result struct {
	Text      string
	Reasoning string
	Usage     json.RawMessage
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
	// 非流式的处理还没实现：**明说**，别静默按流式发（那会让配了 stream:false 的人对着"时好时坏"猜）
	if p.Stream != nil && !*p.Stream {
		return Result{}, errors.New("providers: 这家渠道配了 stream:false，而非流式调用还没实现（先删掉这个字段）")
	}
	r.Stream = true
	if localReply != "" {
		// 本地假上游：**有渠道就把"本来会发出去什么"留档**（dummy 的意义就在这儿 ——
		// 没有 key 也能把装配验一遍）；没配渠道（fallback）就没有可留的。
		if p.Kind == KindDummy {
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
	return c.streamUpstream(ctx, request, onDelta)
}

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
	Content          string  `json:"content"`
	Reasoning        *string `json:"reasoning"`
	ReasoningContent *string `json:"reasoning_content"`
	ReasoningText    *string `json:"reasoning_text"`
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
	if result.Text == "" {
		return Result{}, ErrEmpty
	}
	return result, nil
}

// ── 本地假上游（dummy / fallback）──

// 本地假上游的节奏：**先停一下**（`pending` 才看得见），再**一个字一个字**吐（`streaming` 与
// "耗时"才看得见）。旧版的 dummy 是瞬时的 —— 那样这两个状态与界面动画根本观察不到，
// 联调与验收都没法做（用户 2026-09-29 的验收要求就是"能看到 pending/streaming/idle"）。
const (
	localFirstDelay = 400 * time.Millisecond
	localChunkDelay = 250 * time.Millisecond
)

// streamLocal：不联网的假上游 —— 把这句确定性的话分片吐出来。
//
// 被取消（用户按停 / 这一轮被顶掉）就在下一个停顿处退出，返回 ctx 的错（调用方据它判定"取消"）。
func streamLocal(ctx context.Context, reply string, onDelta func(Delta)) (Result, error) {
	if err := sleep(ctx, localFirstDelay); err != nil {
		return Result{}, err
	}
	var text strings.Builder
	for _, r := range reply {
		if err := sleep(ctx, localChunkDelay); err != nil {
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
		Raw:              raw,
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return nil
	}
	return encoded
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

// snippet：错误正文缩成一行、截到 300 字 —— 够看清原因，又不把整页 HTML 灌进界面。
func snippet(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= 300 {
		return flat
	}
	return string(runes[:300]) + "..."
}
