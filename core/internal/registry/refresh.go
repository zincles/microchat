package registry

import (
	"net"
	"net/url"
	"strings"
	"time"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/store"
)

// ── 刷新模型列表（拉上游 `/models` ⇒ **只写发现列**）────────────────────────
//
// 这一份是**唯一**的刷新实现，三处共用：HTTP 的 `POST /providers/{id}/refresh`、
// **启动时的自动刷新**、`-debug refresh-models`。别在客户端里再造第二份 ——
// 第二份几乎必然漏掉"不动用户列"或"这次没见到的删行"里的某一条，而那两条漏了都是**静默**的
// （列表看着正常，数据已经错了）。
//
// 三条口径：
//  1. **发现列与用户列分列**（不变量）：落库只走 `store.RefreshDiscovered` ——
//     `display_name` / `params` / `context_override` / `tokenizer` 一个字不动；
//  2. **这次没见到的删行**（列表 = 上游当前那份），也由那个函数管；
//  3. **跳过谁、失败怎么办**由 `Skip` / `RefreshAll` 定 —— 一个渠道失败**不影响**别的。

// Outcome：一个渠道这一趟的结果（启动那条路与 `-debug refresh-models` 打的就是它）。
type Outcome struct {
	Provider string `json:"provider"`
	// Skipped：**跳过**的原因（空 = 真去刷了）。文案是给人看的，直接打给用户。
	Skipped string `json:"skipped,omitempty"`
	// Models：上游这次给了几个模型（跳过 / 失败时是 0）。
	Models int `json:"models"`
	// Removed：这次没见到、被删掉的旧行（"没见到就删行"那条不变量的可见面）。
	Removed int `json:"removed"`
	// Error：失败原因（空 = 没失败）。**一个渠道失败不影响别的**（各自挂号、各自收尾）。
	Error string `json:"error,omitempty"`
}

// RefreshOne：**一次刷新的全程**（拉 `/models` → 落库发现列）。唯一的实体实现。
//
// 失败就照实回错（`Outcome` 里不带原因，由调用方填 —— 调用方还要决定挂号那个 Task 记成什么）。
func RefreshOne(provider config.Provider, st *store.Store) (Outcome, error) {
	outcome := Outcome{Provider: provider.ID}
	result, err := Probe(provider)
	if err != nil {
		return outcome, err
	}
	_, _, removed, err := st.RefreshDiscovered(provider.ID, result.Models, time.Now().UnixMilli())
	if err != nil {
		return outcome, err
	}
	outcome.Models = len(result.Models)
	outcome.Removed = removed
	return outcome, nil
}

// Probe：拉一次 `/models`（**不落库**）。网络只有 `providers` 碰得到 —— 这里只要一份列表。
func Probe(provider config.Provider) (ProbeResult, error) {
	// `FromConfig` + `ApplyPreset` 是**唯一**的转换处（手搓字面量会漏字段 ⇒ 静默失效）
	wire := providers.ApplyPreset(providers.FromConfig(provider))
	// 上游 `/models` 的形状是 **`{"data": [...]}`**（OpenAI 的；OpenRouter 同形多给几个字段）。
	// 早先这里解进了 `{"models": [...]}` ✗ —— 发现会静默拿到 0 个模型。
	var parsed ModelsResponse
	if err := providers.NewClient(wire).GetJSON(wire, "/models", &parsed); err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{Models: parsed.Data}, nil
}

// Skip：这个渠道**该不该**自动去问模型列表。返回非空 = 跳过（值就是给人看的原因）。
//
// 两条（2026-09-30 定，用户要的边界）：
//  1. **`dummy` 不联网** ⇒ 问了也没答案；
//  2. **要去外网、又没配密钥** ⇒ 刷出来只会是一屏 401（启动时尤其吵）；
//     **本机 / 内网端点不算**：ollama、LM Studio、本地假上游（httptest 那种）本来就可能不要密钥，照刷 ✓。
//
// 注意它只服务**自动**那条路（启动与 `-debug`）：HTTP 的 `POST /providers/{id}/refresh`
// 是人明确点的，照样发（有错就照实回 `upstream`）。
func Skip(provider config.Provider) string {
	// 密钥可能来自环境变量（`ApplyPreset` 会去 `OPENCODE_API_KEY` 那种地方找）⇒ 判定要在它之后
	wire := providers.ApplyPreset(providers.FromConfig(provider))
	if wire.Kind == providers.KindDummy {
		return "dummy（不联网）"
	}
	if wire.APIKey != "" || localEndpoint(wire.BaseURL) {
		return ""
	}
	return "没配 key"
}

// localEndpoint：这个端点是本机 / 内网吗（见 `Skip` 第 2 条；判不出来就算"不是"）。
func localEndpoint(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	if address == nil { // 域名：不是本机
		return false
	}
	return address.IsLoopback() || address.IsPrivate()
}

// RefreshFunc：**真去刷一个渠道**的那一下（由调用方注入 —— 它还要顺手挂号 Task）。
type RefreshFunc func(config.Provider) Outcome

// RefreshAll：**对每个渠道各刷一次**（启动与 `-debug refresh-models` 共用这段编排）。
//
// 四条口径（就是需求的边界）：
//  1. **串行**（并发要克制：别一开软件就把所有上游撞一遍）；
//  2. 该跳过的**不进 `refresh`**（见 `Skip`），原因进 `Outcome.Skipped`；
//  3. **一个失败不影响别的** —— 原因进 `Outcome.Error`，接着刷下一个；
//  4. 返回顺序 = 传入顺序（`-debug` 的输出因此稳定、好比对；两次跑逐行一样）。
//
// 它**不碰网络、不碰库**：真正的动作全在注入进来的 `refresh` 里（于是这条编排能单测）。
func RefreshAll(list []config.Provider, refresh RefreshFunc) []Outcome {
	outcomes := make([]Outcome, 0, len(list))
	for _, provider := range list {
		if reason := Skip(provider); reason != "" {
			outcomes = append(outcomes, Outcome{Provider: provider.ID, Skipped: reason})
			continue
		}
		outcome := refresh(provider)
		outcome.Provider = provider.ID // 牌子以**配置里的 id** 为准（调用方不必自己填）
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
