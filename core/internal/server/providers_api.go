package server

import (
	"encoding/json"
	"net/http"
	"os"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/registry"
	"microchat/internal/store"
)

// ProviderView：`GET /providers` 的一项。**绝不含密钥内容**（只回 `has_key`）。
type ProviderView struct {
	ID       string            `json:"id"`
	Name     *string           `json:"name"`
	Kind     string            `json:"kind"`
	Vendor   string            `json:"vendor"`
	Protocol string            `json:"protocol"`
	BaseURL  string            `json:"base_url"`
	Headers  map[string]string `json:"headers"`
	HasKey   bool              `json:"has_key"`
	// Identity：生效的身份（渠道自己的，或服务器级默认）。
	Identity      string      `json:"identity"`
	LastRefreshAt *int64      `json:"last_refresh_at"`
	Models        []ModelView `json:"models"`
}

// ModelView：一个模型的全貌（发现列 + 用户列；`name` 已是三级回退后的结果）。
type ModelView struct {
	UpstreamID      string          `json:"upstream_id"`
	Name            string          `json:"name"`
	UpstreamName    *string         `json:"upstream_name"`
	OwnedBy         *string         `json:"owned_by"`
	ContextLength   *int64          `json:"context_length"`
	MaxOutput       *int64          `json:"max_output"`
	ContextOverride *int64          `json:"context_override"`
	DisplayName     *string         `json:"display_name"`
	Params          json.RawMessage `json:"params"`
	UpstreamParams  json.RawMessage `json:"upstream_params"`
}

// ModelListItem：`GET /models` 的一项（跨 provider 拍平；"渠道 / 模型"下拉用）。
type ModelListItem struct {
	Provider     string  `json:"provider"`
	ProviderName *string `json:"provider_name"`
	UpstreamID   string  `json:"upstream_id"`
	Name         string  `json:"name"`
	DisplayName  *string `json:"display_name"`
}

type CreateProviderReq struct {
	ID       string            `json:"id"`
	Name     *string           `json:"name"`
	Kind     *string           `json:"kind"`
	Vendor   *string           `json:"vendor"`
	Protocol *string           `json:"protocol"`
	BaseURL  *string           `json:"base_url"`
	Headers  map[string]string `json:"headers"`
	APIKey   *string           `json:"api_key"`
}

type UpdateProviderReq struct {
	Vendor   *string           `json:"vendor"`   // None = 不动；"" = 清（回落到 Kind 别名）
	Protocol *string           `json:"protocol"` // None = 不动；"" = 清（回落到缺省 chat）
	BaseURL  *string           `json:"base_url"`
	Name     *string           `json:"name"` // None = 不动；"" = 清显示名
	Headers  map[string]string `json:"headers"`
	APIKey   *string           `json:"api_key"` // None = 不动；"" = 删除密钥
}

func modelView(row store.ModelRow) ModelView {
	return ModelView{
		UpstreamID:      row.UpstreamID,
		Name:            registry.DisplayName(row.DisplayName, row.UpstreamName, row.UpstreamID),
		UpstreamName:    row.UpstreamName,
		OwnedBy:         row.OwnedBy,
		ContextLength:   row.ContextLength,
		MaxOutput:       row.MaxOutput,
		ContextOverride: row.ContextOverride,
		DisplayName:     row.DisplayName,
		Params:          rawOrEmptyObject(row.Params),
		UpstreamParams:  rawOrEmptyObject(row.UpstreamParams),
	}
}

func rawOrEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	configs := s.loadProviders()
	models, err := s.store.ListModels("")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	views := make([]ProviderView, 0, len(configs.Providers))
	for _, provider := range configs.Providers {
		provider = s.config.WithIdentity(provider)
		lastRefresh, _ := s.store.LastRefreshAt(provider.ID)
		items := []ModelView{}
		for _, model := range models {
			if model.Provider == provider.ID {
				items = append(items, modelView(model))
			}
		}
		view := providerView(provider)
		view.LastRefreshAt = lastRefresh
		view.Models = items
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	var req CreateProviderReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID == "" {
		missingField(w, "id")
		return
	}
	configs := s.loadProviders()
	for _, existing := range configs.Providers {
		if existing.ID == req.ID {
			writeError(w, http.StatusConflict, "conflict", "已经有同名渠道")
			return
		}
	}
	provider := config.Provider{ID: req.ID, Kind: "", BaseURL: "", Headers: req.Headers, APIKey: deref(req.APIKey)}
	if req.Name != nil && *req.Name != "" {
		provider.Name = *req.Name
	}
	if req.Kind != nil {
		provider.Kind = *req.Kind
	}
	// vendor 是**唯一必填**（kind 只剩废弃读入）：缺了 ⇒ 422（别名 kind 不算数 ——
	// "写了 kind 没写 vendor"是旧习惯，必须 loud，否则新种类（typesafe）永远建不出来。
	if req.Vendor == nil || *req.Vendor == "" {
		missingField(w, "vendor")
		return
	}
	provider.Vendor = *req.Vendor
	if req.Protocol != nil {
		provider.Protocol = *req.Protocol
	}
	if req.BaseURL != nil {
		provider.BaseURL = *req.BaseURL
	}
	// 错配在**写入时**就拦（兼容矩阵即校验表）：custom 没端点、deepseek+systemone 这类不落地。
	if err := providers.FromConfig(provider).Normalize().Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	configs.Providers = append(configs.Providers, provider)
	if err := s.saveProviders(configs); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, providerView(provider))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider_id")
	var req UpdateProviderReq
	if !decodeJSON(w, r, &req) {
		return
	}
	configs := s.loadProviders()
	index := -1
	for i, provider := range configs.Providers {
		if provider.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, "not_found", "渠道不存在")
		return
	}
	provider := configs.Providers[index]
	if req.Vendor != nil { // None = 不动；"" = 清（回落到 Kind 别名）
		provider.Vendor = *req.Vendor
	}
	if req.Protocol != nil { // None = 不动；"" = 清（回落到缺省 chat）
		provider.Protocol = *req.Protocol
	}
	if req.BaseURL != nil {
		provider.BaseURL = *req.BaseURL
	}
	if req.Name != nil { // None = 不动；"" = 清显示名
		provider.Name = *req.Name
	}
	if req.Headers != nil {
		provider.Headers = req.Headers
	}
	if req.APIKey != nil { // None = 不动；"" = 删除密钥
		provider.APIKey = *req.APIKey
	}
	// 改完也要过矩阵（vendor/protocol/base_url 任一动了都可能错配）
	if err := providers.FromConfig(provider).Normalize().Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	configs.Providers[index] = provider
	if err := s.saveProviders(configs); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, providerView(provider))
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider_id")
	configs := s.loadProviders()
	kept := make([]config.Provider, 0, len(configs.Providers))
	found := false
	for _, provider := range configs.Providers {
		if provider.ID == id {
			found = true
			continue
		}
		kept = append(kept, provider)
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "渠道不存在")
		return
	}
	configs.Providers = kept
	if err := s.saveProviders(configs); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// refreshProvider：拉 `/models` 并落库（**发现列**）；这次没见到的删行。
//
// **怎么刷只有一份**（`registry.RefreshOne`）：启动时的自动刷新、`-debug refresh-models`
// 走的都是它 —— 别在这儿再写一份"拉 + 落库"。这里只管 HTTP 那一层（找渠道、定错误码、回视图）。
// 与自动那条路的区别：**这条是人明确点的**，所以不套 `registry.Skip`（dummy 照旧 400，没配 key 也真发）。
func (s *Server) refreshProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider_id")
	provider, found := s.providerByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "渠道不存在")
		return
	}
	if providers.FromConfig(provider).EffectiveVendor() == providers.VendorDummy {
		writeError(w, http.StatusBadRequest, "invalid", "dummy 渠道没有上游可拉")
		return
	}
	if _, err := registry.RefreshOne(provider, s.store); err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	updated, _ := s.providerByID(id)
	writeJSON(w, http.StatusOK, providerView(updated))
}

// listProviderPresets：内建预设清单 —— 界面拿它生成"选一个内置 provider"的下拉。
func (s *Server) listProviderPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, providers.Presets())
}

// listModels：跨 provider 拍平（"渠道 / 模型"下拉用）。
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	configs := s.loadProviders()
	names := map[string]*string{}
	for _, provider := range configs.Providers {
		view := providerView(provider)
		names[provider.ID] = view.Name
	}
	rows, err := s.store.ListModels("")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items := make([]ModelListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, ModelListItem{
			Provider: row.Provider, ProviderName: names[row.Provider],
			UpstreamID:  row.UpstreamID,
			Name:        registry.DisplayName(row.DisplayName, row.UpstreamName, row.UpstreamID),
			DisplayName: row.DisplayName,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// setModelOverride：设/清模型的用户列（显示名 / 上下文覆盖 / 采样参数）。**刷新永不覆盖这些**。
type SetModelOverrideReq struct {
	Provider        string          `json:"provider"`
	UpstreamID      string          `json:"upstream_id"`
	ContextOverride *int64          `json:"context_override"` // null = 清
	DisplayName     *string         `json:"display_name"`
	Params          json.RawMessage `json:"params"`
}

func (s *Server) setModelOverride(w http.ResponseWriter, r *http.Request) {
	var req SetModelOverrideReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Provider == "" || req.UpstreamID == "" {
		missingField(w, "provider/upstream_id")
		return
	}
	if err := s.store.SetModelOverride(req.Provider, req.UpstreamID,
		req.DisplayName, req.ContextOverride, req.Params); err != nil {
		writeStoreError(w, err)
		return
	}
	rows, err := s.store.ListModels(req.Provider)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, row := range rows {
		if row.UpstreamID == req.UpstreamID {
			writeJSON(w, http.StatusOK, modelView(row))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "模型不存在")
}

// ── 小工具 ─────────────────────────────────────────────

func providerView(provider config.Provider) ProviderView {
	name := (*string)(nil)
	if provider.Name != "" {
		name = &provider.Name
	}
	// **回生效的端点**：配置里只写 `kind` 时端点由预设供给 ⇒ 这里补上，
	// 免得界面看着一个空 base_url 以为"这个渠道没有端点"。
	effective := providers.ApplyPreset(providers.FromConfig(provider))
	if effective.Identity == "" {
		effective.Identity = providers.IdentityPi // 视图给"生效值"，别让界面猜（默认 = pi）
	}
	return ProviderView{
		ID: provider.ID, Name: name, Kind: provider.Kind,
		Vendor: string(effective.EffectiveVendor()), Protocol: string(effective.EffectiveProtocol()),
		BaseURL: effective.BaseURL, Headers: provider.Headers,
		HasKey: provider.APIKey != "", Identity: string(effective.Identity), Models: []ModelView{},
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// loadProviders / saveProviders：`providers.json` 的读写（配置层负责严格 JSON；这里只搬运）。
func (s *Server) loadProviders() config.ProvidersConfig {
	var providers config.ProvidersConfig
	raw, err := os.ReadFile(s.paths.Config("providers.json"))
	if err == nil {
		_ = json.Unmarshal(raw, &providers)
	}
	return providers
}

func (s *Server) saveProviders(configs config.ProvidersConfig) error {
	return config.SaveJSON(s.paths.Config("providers.json"), configs)
}

func (s *Server) providerByID(id string) (config.Provider, bool) {
	for _, provider := range s.loadProviders().Providers {
		if provider.ID == id {
			return s.config.WithIdentity(provider), true
		}
	}
	return config.Provider{}, false
}
