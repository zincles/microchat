package server

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/registry"
	"microchat/internal/store"
)

// ProviderView：`GET /providers` 的一项。**绝不含密钥内容**（只回 `has_key`）。
type ProviderView struct {
	ID      string            `json:"id"`
	Name    *string           `json:"name"`
	Kind    string            `json:"kind"`
	BaseURL string            `json:"base_url"`
	Headers map[string]string `json:"headers"`
	HasKey  bool              `json:"has_key"`
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
	ID      string            `json:"id"`
	Name    *string           `json:"name"`
	Kind    *string           `json:"kind"`
	BaseURL *string           `json:"base_url"`
	Headers map[string]string `json:"headers"`
	APIKey  *string           `json:"api_key"`
}

type UpdateProviderReq struct {
	BaseURL *string           `json:"base_url"`
	Name    *string           `json:"name"` // None = 不动；"" = 清显示名
	Headers map[string]string `json:"headers"`
	APIKey  *string           `json:"api_key"` // None = 不动；"" = 删除密钥
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
		provider = s.withIdentity(provider)
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
	if req.BaseURL != nil {
		provider.BaseURL = *req.BaseURL
	}
	configs.Providers = append(configs.Providers, provider)
	if err := s.saveProviders(configs); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, providerView(provider))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	configs.Providers[index] = provider
	if err := s.saveProviders(configs); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, providerView(provider))
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
func (s *Server) refreshProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	provider, found := s.providerByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "渠道不存在")
		return
	}
	if provider.Kind == "dummy" {
		writeError(w, http.StatusBadRequest, "invalid", "dummy 渠道没有上游可拉")
		return
	}
	probe, err := s.probeModels(provider)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	if _, _, _, err := s.store.RefreshDiscovered(id, probe.Models, time.Now().UnixMilli()); err != nil {
		writeStoreError(w, err)
		return
	}
	updated, _ := s.providerByID(id)
	writeJSON(w, http.StatusOK, providerView(updated))
}

// probeModels：一次性拉 `/models`（不落库）。
func (s *Server) probeModels(provider config.Provider) (registry.ProbeResult, error) {
	wire := providers.FromConfig(provider) // 唯一转换处（手搓会漏字段 ⇒ 静默失效）
	// 上游 `/models` 的形状是 **`{"data": [...]}`**（OpenAI 的，OpenRouter 同形多给几个字段）。
	// 早先这里解进了 `{"models": [...]}` ✗ —— 发现会静默拿到 0 个模型。
	var parsed registry.ModelsResponse
	client := providers.NewClient(providers.ApplyPreset(wire))
	if err := client.GetJSON(providers.ApplyPreset(wire), "/models", &parsed); err != nil {
		return registry.ProbeResult{}, err
	}
	return registry.ProbeResult{Models: parsed.Data}, nil
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
			return s.withIdentity(provider), true
		}
	}
	return config.Provider{}, false
}

// withIdentity：**服务器级默认特征**（`config.json` 的 `server.identity`）—— 渠道自己没写就用它。
// 这就是"切换客户端特征"那个开关：一处改，所有没写 identity 的渠道都跟着变。
func (s *Server) withIdentity(provider config.Provider) config.Provider {
	if provider.Identity == "" {
		provider.Identity = s.config.Server.Identity
	}
	return provider
}
