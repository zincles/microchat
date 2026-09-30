package providers

// OpenCode GO 的**套餐用量**查询（只读）。
//
// 上游 2026-09 才把它放出来 —— 社区从 03 月追到 09 月（`anomalyco/opencode#16017` 的
// 收尾评论里，08/07 还有人贴出"`/zen/go/v1/usage` 回的是 SPA 的 404 HTML"）。现在它在了。
//
// **实测（2026-09-30，本机真 key，只读 GET）**：
//
//   - `GET https://opencode.ai/zen/go/v1/usage` + `authorization: Bearer <key>` ⇒ **200**；
//   - 和 `/chat/completions` **不同**：这里**不挑客户端身份** —— 裸 `curl`（不带 UA、不带
//     `x-opencode-session`）一样 200。用量是**账号级**的，不属于任何会话 ⇒ **不发会话头**；
//   - 响应：`{"usage":{"rolling":{...},"weekly":{...},"monthly":{...}}}`，
//     每个窗口 `{status, percent, resetsAt}`（`percent` = **该窗口自己**的已用百分比 0–100，
//     `resetsAt` = RFC3339；`status` 实测见过 `"ok"`，omp 的校验还认一个 `"rate-limited"`）；
//   - 401 两种：没带 key ⇒ `"Missing API key."`；key 不对 ⇒ `"Unauthorized"`
//     （体是 `{"type":"error","error":{"type":"AuthError","message":…}}`）；
//   - **路径必须逐字**：多一个尾斜杠 `/v1/usage/` 会 401（不是重定向）；写错路径 ⇒ 站点的 404 HTML。
//
// 证据（端点、响应样例、探过的墙）都在 `reminder/opencode-usage.md`。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// OpencodeGoUsageURL：套餐用量端点的 base（**不带** `/v1`，见 usageURL）。
const OpencodeGoUsageURL = "https://opencode.ai/zen/go"

// OpencodeUsagePath：用量相对 base 的路径。
const OpencodeUsagePath = "/v1/usage"

// OpencodeUsageTimeout：查用量的**总超时**。
//
// 本仓硬规矩：不配总超时 = 上游卡住，界面转一辈子。查用量是个小 GET，20 秒足够；
// 调用方还能用 ctx 再压短（两个都生效，先到的那个说了算）。
const OpencodeUsageTimeout = 20 * time.Second

// 窗口的 `status`（实测 + 照 omp 的校验）。
const (
	UsageStatusOK          = "ok"           // 还有余量
	UsageStatusRateLimited = "rate-limited" // 这个窗口已经顶了
)

// PlanWindow：一个套餐配额窗口（5 小时 / 每周 / 每月）。
type PlanWindow struct {
	// ID：稳定标识（`rolling-5h` / `weekly` / `monthly`）—— 上游回的是 `rolling`，这里改叫
	// `rolling-5h` 是为了"名字自带语义"（照 omp 的窗口 id）。
	ID string `json:"id"`
	// Label：人看的名字（`Rolling (5h)` / `Weekly` / `Monthly`）。
	Label string `json:"label"`
	// Percent：**这个窗口自己**的已用百分比（0–100，不是占月度的比例）。
	Percent float64 `json:"percent"`
	// Status：`ok` 或 `rate-limited`（见上面的常量）。
	Status string `json:"status"`
	// ResetsAt：这个窗口的重置时刻（RFC3339 解析来的）。
	ResetsAt time.Time `json:"resets_at"`
}

// PlanUsage：一次套餐用量查询的归一化结果。
//
// 与 `Usage`（那一发对话的 token 用量）**不是一回事**：那个是每轮 chat 回来的 token 数，
// 这个是**订阅套餐**还剩多少 —— 名字分开，免得两个"usage"在同一层里打架。
type PlanUsage struct {
	// Plan：套餐名。接口**不回**这个字段（`usage` 里只有三个窗口）—— 是我们自己贴的标签。
	Plan string `json:"plan"`
	// Endpoint：这次真打的 URL（排查"到底问了谁"用）。
	Endpoint string `json:"endpoint"`
	// FetchedAt：拿到这份数据的时刻。
	FetchedAt time.Time `json:"fetched_at"`
	// Windows：三个窗口，顺序固定 rolling / weekly / monthly。
	Windows []PlanWindow `json:"windows"`
}

// OpencodeUsage：拿一个 API key 去问 OpenCode GO 的套餐余量。**只读**。
//
// 超时：`OpencodeUsageTimeout`（或 ctx 更早的那个）；**不重试**（错误原样往上抛）；
// **不打印 key**（错误文本里只有状态码与上游的 message）。
func OpencodeUsage(ctx context.Context, apiKey string) (PlanUsage, error) {
	return OpencodeUsageAt(ctx, OpencodeGoUsageURL, apiKey)
}

// OpencodeUsageAt：同上，但端点可换（自建代理 / 测试用 httptest 钉）。
func OpencodeUsageAt(ctx context.Context, baseURL, apiKey string) (PlanUsage, error) {
	if strings.TrimSpace(apiKey) == "" {
		return PlanUsage{}, errors.New("providers: 查 OpenCode GO 用量需要 API key")
	}
	endpoint := usageURL(baseURL)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return PlanUsage{}, err
	}
	request.Header.Set("Accept", "application/json")
	// 自报身份照本仓口径（默认 = Pi 的形状）：实测这里**不挑**，但"用库名"迟早被拦，不占这个便宜。
	request.Header.Set("User-Agent", userAgent(Provider{Kind: KindOpenCodeGo}))
	request.Header.Set("Authorization", "Bearer "+apiKey)

	client := NewClient(Provider{
		Kind:     KindOpenCodeGo,
		Timeouts: &Timeouts{ConnectSeconds: 10, TotalSeconds: OpencodeUsageTimeout.Seconds()},
	})
	response, err := client.Do(request)
	if err != nil {
		return PlanUsage{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return PlanUsage{}, usageStatusError(response.StatusCode, body)
	}

	var envelope usageEnvelope
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&envelope); err != nil {
		return PlanUsage{}, fmt.Errorf("providers: GET %s 的响应不是 JSON：%w", OpencodeUsagePath, err)
	}
	return normalizeUsage(envelope, endpoint)
}

// usageURL：把 base 拼成用量端点。
//
// 配置里写的是 chat 用的那个 base（多半以 `/v1` 结尾）—— 用量是它的**兄弟**路径，
// 所以先剥掉末尾的 `/v1` 再拼（照 omp 的 `vQi`）；空值兜到官方端点。
func usageURL(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = OpencodeGoUsageURL
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		base = base[:len(base)-len("/v1")]
	}
	return strings.TrimRight(base, "/") + OpencodeUsagePath
}

// usageEnvelope / usageWindows / usageWindowWire：线上形状（**唯一**的解析处）。
type usageEnvelope struct {
	Usage *usageWindows `json:"usage"`
}

type usageWindows struct {
	Rolling *usageWindowWire `json:"rolling"`
	Weekly  *usageWindowWire `json:"weekly"`
	Monthly *usageWindowWire `json:"monthly"`
}

type usageWindowWire struct {
	Status   string   `json:"status"`
	Percent  *float64 `json:"percent"` // 指针：缺字段要能和 `0` 分开
	ResetsAt string   `json:"resetsAt"`
}

// windowSpec：窗口的**顺序与命名**（上游回的是键，这里给它们稳定的 id / 名字）。
type windowSpec struct {
	id, label string
	wire      func(*usageWindows) *usageWindowWire
}

var usageWindowSpecs = []windowSpec{
	{"rolling-5h", "Rolling (5h)", func(w *usageWindows) *usageWindowWire { return w.Rolling }},
	{"weekly", "Weekly", func(w *usageWindows) *usageWindowWire { return w.Weekly }},
	{"monthly", "Monthly", func(w *usageWindows) *usageWindowWire { return w.Monthly }},
}

// normalizeUsage：线上形状 → 归一化（**严格**：三个窗口缺一个就报，不编半个结果）。
func normalizeUsage(envelope usageEnvelope, endpoint string) (PlanUsage, error) {
	if envelope.Usage == nil {
		return PlanUsage{}, fmt.Errorf("providers: GET %s 的响应里没有 usage 对象", OpencodeUsagePath)
	}
	windows := make([]PlanWindow, 0, len(usageWindowSpecs))
	for _, spec := range usageWindowSpecs {
		window, ok := normalizeWindow(spec, envelope.Usage)
		if !ok {
			return PlanUsage{}, fmt.Errorf("providers: GET %s 的响应里 %s 那个窗口缺失或形状不对", OpencodeUsagePath, spec.id)
		}
		windows = append(windows, window)
	}
	return PlanUsage{
		Plan:      "OpenCode Go",
		Endpoint:  endpoint,
		FetchedAt: time.Now(),
		Windows:   windows,
	}, nil
}

// normalizeWindow：一个窗口的校验与归一化（照 omp 的校验面：percent ∈ [0,100]、status 认两个值、
// resetsAt 必须能解析）。`ok=false` ⇒ 这个窗口不可用。
func normalizeWindow(spec windowSpec, windows *usageWindows) (PlanWindow, bool) {
	wire := spec.wire(windows)
	if wire == nil || wire.Percent == nil {
		return PlanWindow{}, false
	}
	percent := *wire.Percent
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
		return PlanWindow{}, false
	}
	if wire.Status != UsageStatusOK && wire.Status != UsageStatusRateLimited {
		return PlanWindow{}, false
	}
	resetsAt, err := time.Parse(time.RFC3339, wire.ResetsAt)
	if err != nil {
		return PlanWindow{}, false
	}
	return PlanWindow{
		ID:       spec.id,
		Label:    spec.label,
		Percent:  percent,
		Status:   wire.Status,
		ResetsAt: resetsAt,
	}, true
}

// usageStatusError：非 200 的错误体 —— 优先取上游的 `error.message`（那才是"为什么"），
// 取不到就把体缩成一行（404 时上游回的是整页 HTML，别把它整篇灌进错误里）。
func usageStatusError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := ""
	if json.Unmarshal(body, &envelope) == nil {
		message = strings.TrimSpace(envelope.Error.Message)
	}
	if message == "" {
		message = snippet(string(body))
	}
	return fmt.Errorf("providers: GET %s => %d：%s", OpencodeUsagePath, status, message)
}
