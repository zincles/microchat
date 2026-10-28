package server

import (
	"encoding/json"
	"net/http"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/registry"
	"microchat/internal/store"
)

// ProviderView：`GET /providers` 的一项。**绝不含密钥内容**（只回 `has_key`）。
type ProviderView struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Kind     string  `json:"kind"`
	Vendor   string  `json:"vendor"`
	Protocol string  `json:"protocol"`
	// VendorName / ProtocolName：这两个 id 的**中文名**（唯一权威在 providers 包的协议与 vendor 表里）。
	// 界面直接显示它们 —— 渠道列表与编辑器从前把 `opencode-go` 这种内部 id 摆在用户面前，
	// 那是在让人读代码。
	VendorName   string `json:"vendor_name"`
	ProtocolName string `json:"protocol_name"`
	// ProtocolOptions：这个渠道能换成哪些协议（带中文名）。
	ProtocolOptions []providers.ProtocolOption `json:"protocol_options"`
	BaseURL         string                     `json:"base_url"`
	Headers         map[string]string          `json:"headers"`
	HasKey          bool                       `json:"has_key"`
	// ModelFilter：模型筛选器（空 = 不筛）。界面要能看见渠道上挂着什么筛选器 ——
	// 否则"这个渠道怎么只有 12 个模型"会变成一个查不出来的谜。
	ModelFilter string `json:"model_filter"`
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
	// ModelFilter：模型筛选器（省略 / 空 = 不筛）。校验在 ValidateModelFilter（坏写法 ⇒ 422）。
	ModelFilter *string `json:"model_filter"`
}

type UpdateProviderReq struct {
	Vendor   *string           `json:"vendor"`   // None = 不动；"" = 清（回落到 Kind 别名）
	Protocol *string           `json:"protocol"` // None = 不动；"" = 清（回落到缺省 chat）
	BaseURL  *string           `json:"base_url"`
	Name     *string           `json:"name"` // None = 不动；"" = 清显示名
	Headers  map[string]string `json:"headers"`
	APIKey   *string           `json:"api_key"` // None = 不动；"" = 删除密钥
	// ModelFilter：None = 不动；"" = 清（不筛）。**改了要再刷一次模型才生效**
	// （筛选是在发现那一步做的，配置本身不会回头去动已有的行）。
	ModelFilter *string `json:"model_filter"`
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
	if !s.requireAdmin(w, r) {
		return
	}
	configs := s.loadProviders()
	// 先探一次模型表：读不出来就整条路 500（别让每条渠道各自静默显示 0 个模型）。
	// 真正的填充在 `providerViewWithModels` 里按渠道查 —— 那一步的失败退回空列表，
	// 因为到那儿已经"这条渠道确实没有模型"与"读不出来"分不清了。
	if _, err := s.store.ListModels(""); err != nil {
		writeStoreError(w, err)
		return
	}
	views := make([]ProviderView, 0, len(configs.Providers))
	for _, provider := range configs.Providers {
		view := s.providerViewWithModels(provider)
		lastRefresh, _ := s.store.LastRefreshAt(provider.ID)
		view.LastRefreshAt = lastRefresh
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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
	provider := config.Provider{ID: req.ID, Kind: "", BaseURL: "", Headers: req.Headers,
		APIKey: deref(req.APIKey), ModelFilter: deref(req.ModelFilter)}
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
	// 筛选器坏了在**写入时**就拦（同 abilities / segments 的"写入即报错"口径）：
	// 让它落进文件再去刷新，就会出现"刷新失败/筛空"这种隔了一层才知道的错。
	if err := registry.ValidateModelFilter(provider.ModelFilter); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
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
	if !s.requireAdmin(w, r) {
		return
	}
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
	if req.ModelFilter != nil { // None = 不动；"" = 清（不筛）
		provider.ModelFilter = *req.ModelFilter
	}
	// 筛选器坏了在写入时就拦（createProvider 那边同一个口径）
	if err := registry.ValidateModelFilter(provider.ModelFilter); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
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

// deleteProvider：删渠道 = **配置 + 库里痕迹一起删**（2026-10-22 修）。
//
// 从前这里只改 `providers.json`，`models` 行留在库里 ⇒ `GET /models` 照样列、界面照样能选；
// 选中后 `SelectBackend` 找不到渠道 ⇒ 静默回落 fallback（"点了没反应"）。见
// `store.DeleteProviderData` 的注释（那里记了实测数据：4 个渠道配着 5 组模型）。
//
// **顺序是刻意的：先删库、再改配置。** 两个写操作不在同一个事务里（一个 SQLite、一个 JSON 文件），
// 所以只能选"失败时哪种残留更可接受"：
//   - 先删库后写配置，写配置失败 ⇒ 渠道还在、模型没了 ⇒ 用户再点一次"刷新模型"就长回来
//     （自愈，且渠道本身没丢 —— 用户的配置是他手填的，最不该丢的就是它）；
//   - 反过来（先写配置后删库），删库失败 ⇒ 渠道没了、模型却还在列表里 ⇒ **正是这个 bug 的形态**
//     （而且用户已经看不到那个渠道了，只能靠手改库才清得掉）。
//
// 所以选前者。删库失败就直接报错、**不动配置**（宁可这次没删成，也不留半截状态）。
func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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
	// ① 先清库里的痕迹（models / model_route / provider_state，同一事务）
	if err := s.store.DeleteProviderData(id); err != nil {
		writeStoreError(w, err)
		return
	}
	// ② 再改配置（失败的话：渠道还在、模型没了 ⇒ 刷新模型自愈，见上面的顺序说明）
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
	if !s.requireAdmin(w, r) {
		return
	}
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
	writeJSON(w, http.StatusOK, s.providerViewWithModels(updated))
}

// refreshProviderRoutes：刷**路由表**（models.dev 快照 → `model_route` 落库）。
// 与 `refreshProvider`（刷 `models` 表）各管各的：路由表决定模型走哪条 API，
// 快照失败 ⇒ 旧行留着（宁可用旧表，别拿空表把路断了）—— 同 `registry.RefreshRoutes` 口径。
func (s *Server) refreshProviderRoutes(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("provider_id")
	provider, found := s.providerByID(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "渠道不存在")
		return
	}
	outcomes := registry.RefreshRoutes(r.Context(), s.store, []config.Provider{provider}, nil)
	if len(outcomes) != 1 {
		writeError(w, http.StatusInternalServerError, "internal", "路由刷新没有返回结果")
		return
	}
	if outcomes[0].Error != "" {
		writeError(w, http.StatusBadGateway, "upstream", outcomes[0].Error)
		return
	}
	writeJSON(w, http.StatusOK, outcomes[0])
}

// listProviderPresets：内建预设清单 —— 界面拿它生成"选一个内置 provider"的下拉。
func (s *Server) listProviderPresets(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
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
	if !s.requireAdmin(w, r) {
		return
	}
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
		VendorName: effective.EffectiveVendor().VendorName(), ProtocolName: providers.ProtocolLabel(effective.EffectiveProtocol()),
		ProtocolOptions: providers.ProtocolOptionsFor(effective),
		BaseURL:         effective.BaseURL, Headers: provider.Headers,
		HasKey: provider.APIKey != "", ModelFilter: provider.ModelFilter,
		Identity: string(effective.Identity), Models: []ModelView{},
	}
}

// providerViewWithModels：视图 + **这条渠道发现到的模型** —— 只有 `GET /providers` 与
// `POST /providers/{id}/refresh` 两条路会给它（列表里要显示"这个渠道有几个模型"；
// 刷新那条要能回报"这次导入了几个"）。
//
// 为什么单独一个函数：`providerView()` 是**单条渠道、不查模型表**的廉价视图，
// 建/改/删渠道那几条路都复用它。从前 refresh 也复用它 ⇒ 回来的 `models` 恒为 `[]`，
// 而界面正是拿 `view.models.length` 报"已导入 N 个模型"的 ⇒ **永远报 0 个**
// （模型其实好好地进了库，只是这句话骗人；改完筛选器后那句"已导入 0 个"更让人以为筛选器坏了）。
func (s *Server) providerViewWithModels(provider config.Provider) ProviderView {
	provider = s.config.WithIdentity(provider)
	rows, err := s.store.ListModels(provider.ID)
	if err != nil {
		// 模型表读不出来不该让整个视图失败（渠道本身是好的）：退回空列表，
		// 与"这个渠道确实还没有模型"看起来一样 —— 这两件事本来就分不清，不假装分得清。
		return providerView(provider)
	}
	items := make([]ModelView, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelView(row))
	}
	view := providerView(provider)
	view.Models = items
	return view
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// loadProviders / saveProviders：`providers.json` 的读写（配置层负责严格 JSON；这里只搬运）。
func (s *Server) loadProviders() config.ProvidersConfig {
	// 走 config.LoadProviders（唯一入口）：**全新装的 dummy 兜底与 chat 同一条口径**。
	// 读坏/读不到就退回空表（与旧行为一致：页面显示"还没有渠道"，而不是 500）。
	providers, _ := config.LoadProviders(s.paths)
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
