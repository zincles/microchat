package server

import (
	"net/http"

	"microchat/internal/providers"
)

// ProviderUsageView：`GET /providers/{provider_id}/usage` 的响应。
//
// 形状就是 `providers.PlanUsage`（`plan` / `windows[].label` / `.percent` / `.resets_at` …），
// **多带一个 `provider_id`** —— 让客户端知道这份数是谁的（同一屏里可能问好几条渠道）。
type ProviderUsageView struct {
	providers.PlanUsage
	ProviderID string `json:"provider_id"`
}

// providerUsage：查**套餐余量**（只读）。**只对 `kind = opencode-go` 的渠道有义** ——
// 别的 kind 一律 400 说清楚（用量端点只有 OpenCode GO 有）。
//
// 拉取**复用** `providers.OpencodeUsageAt`（唯一实现 —— 不在这儿再写一份 HTTP + 解析）；
// 端点取渠道**生效的** `base_url`（配了就用配置的，没配落官方预设）⇒ 自建代理 / 测试假上游也指得进来。
func (s *Server) providerUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider_id")
	provider, found := s.providerByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "渠道不存在")
		return
	}
	if providers.FromConfig(provider).EffectiveVendor() != providers.VendorOpenCodeGo {
		writeError(w, http.StatusBadRequest, "invalid",
			"渠道 "+id+" 不是 opencode-go（套餐用量端点只有 OpenCode GO 有）—— 换个渠道，或把它的 vendor 改成 opencode-go")
		return
	}
	effective := providers.ApplyPreset(providers.FromConfig(provider))
	if effective.APIKey == "" {
		writeError(w, http.StatusBadRequest, "invalid", "这个渠道没配 key（"+id+"）")
		return
	}
	usage, err := providers.OpencodeUsageAt(r.Context(), effective.BaseURL, effective.APIKey)
	if err != nil {
		// 上游错 **原样传**（状态码与原因都在错误里）—— 不在这儿吞掉、也不改写
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ProviderUsageView{PlanUsage: usage, ProviderID: id})
}
