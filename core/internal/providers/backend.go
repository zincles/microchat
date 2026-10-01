// 选后端：**哪条渠道来答这一发** —— 规则只有这一处（一轮生成与压缩共用）。
//
// 为什么搬到 `providers`：两个调用点（`chat` 的一轮生成、`compact` 的压缩）都要回答同一个问题，
// 各写一份必然漂移（"provider 不在配置里 ⇒ 回兜底话术"这种分支最容易只改一处）。
package providers

import (
	"strings"

	"microchat/internal/config"
)

// 后端名：`Backend.Name` 的取值。`dummy` / `fallback` 是**本地假上游**（不联网），
// 其余 = 渠道的 kind（真发）。
const (
	// BackendDummy：显式选中了 `kind: dummy` 的渠道 —— 它就是为"不联网但有回话"而存在的。
	BackendDummy = "dummy"
	// BackendFallback：没有可用的上游（渠道不在配置里 / 模型名为空）⇒ 回一句兜底话术。
	BackendFallback = "fallback"
)

// Backend：这一发由谁来答。
//
// `Provider` 只在真的能找到渠道时非零值（`fallback` 时是零值 —— 没有渠道可发）。
type Backend struct {
	Name     string
	Provider config.Provider
}

// SelectBackend：选后端 —— **规则只有这一处**（照旧版）：
//
//  1. provider 不在 `providers.json` 里（没配 / 被删）⇒ `fallback`；
//  2. provider 是 `dummy` 类型 ⇒ `dummy`（它就是为"显式选中"而存在的）；
//  3. 模型名为空 ⇒ `fallback`；否则真上游（`Name` 用渠道的 kind）。
//
// 必须检查 provider 是否存在：`providers.json` 是手写文件，删掉一个渠道后历史会话里仍留着它的名字
// —— 那种情况该回兜底话术，而不是去打一个不存在的上游。
func SelectBackend(provider, model string, providersConfig config.ProvidersConfig) Backend {
	found, ok := providersConfig.Get(provider)
	if !ok {
		return Backend{Name: BackendFallback}
	}
	// vendor 字符串与原来逐字相同（`openai-compat` 这种遗留值也原样回 Name）；
	// dummy 只按 EffectiveVendor 判（`dummy` 两种写法都收敛到它）。
	vendor := Vendor(found.Kind)
	if (Provider{Vendor: vendor}).EffectiveVendor() == VendorDummy {
		return Backend{Name: BackendDummy, Provider: found}
	}
	if strings.TrimSpace(model) == "" {
		return Backend{Name: BackendFallback}
	}
	return Backend{Name: string(vendor), Provider: found}
}

// StoresReasoning：这个渠道要不要把"思考"留档（`providers.json` 的 `store_reasoning`，缺省要）。
//
// 默认为"要"：留档是保守选择，丢了才是真丢。
func (b Backend) StoresReasoning() bool {
	return b.Provider.StoreReasoning == nil || *b.Provider.StoreReasoning
}
