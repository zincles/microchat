// 模型调用的通用结果、本地回声与用量归一化。协议适配器在 sdk_*.go。
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"microchat/internal/model"
)

// Delta：流式增量的一小段（**正文与思考分开**：思考只服务动画，正文才是最终消息的内容）。
//
// 一个事件可以同时携带正文和渠道扩展的思考。
type Delta struct {
	Text      string
	Reasoning string
	// Tool：服务端工具的状态变化（只有 kind/state，不产生正文）。ok=nil 表示这帧不是工具事件。
	Tool *ToolEvent
}

// ToolSource：服务端搜索工具拿回来的一个来源（xAI 的 web_search / x_search 都有）。
type ToolSource = model.MessageToolSource

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
// TextOffset：该调用出现在本轮正文中的 rune 偏移；由 chat 层统一补上，0 也是合法值。
type ToolCall struct {
	ID         string
	Name       string
	Arguments  string
	TextOffset *int
}

// Result：一次流式调用的结局 —— **整段**正文 + 整段思考 + 归一化后的用量 + 工具调用。
type Result struct {
	Exchange    *model.Exchange
	ServerTools []ToolEvent

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
		// Chat Completions
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		// Responses
		InputTokens   *int64 `json:"input_tokens"`
		OutputTokens  *int64 `json:"output_tokens"`
		TotalTokens   *int64 `json:"total_tokens"`
		PromptDetails struct {
			CachedTokens *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputDetails struct {
			CachedTokens     *int64 `json:"cached_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
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
	if json.Unmarshal(raw, &wire) != nil {
		return nil
	}
	prompt := firstNumber(wire.PromptTokens, wire.InputTokens)
	if prompt == 0 && wire.PromptTokens == nil && wire.InputTokens == nil {
		return nil // 连输入 token 都没有 ⇒ 这份用量我们不认识
	}
	completion := firstNumber(wire.CompletionTokens, wire.OutputTokens)
	usage := Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      numberOr(wire.TotalTokens, prompt+completion),
		CachedTokens:     firstNumber(wire.PromptDetails.CachedTokens, wire.InputDetails.CachedTokens, wire.PromptCacheHitTokens, wire.CacheReadInputTokens),
		ReasoningTokens:  firstNumber(wire.CompletionDetails.ReasoningTokens, wire.OutputDetails.ReasoningTokens, wire.ReasoningTokens),
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
