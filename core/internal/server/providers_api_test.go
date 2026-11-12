package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/store"
)

// 建一个只带 providers.json 的沙盒服务端。
func newProvidersServer(t *testing.T, providersJSON string) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	if err := config.SaveJSON(dir+"/providers.json", mustJSON(t, providersJSON)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	enableService(t, st)
	server := newTestServer(st, config.DefaultConfig(), config.Paths{ConfigDir: dir, DataDir: dir})
	return server, st
}

func mustJSON(t *testing.T, text string) config.ProvidersConfig {
	t.Helper()
	var configs config.ProvidersConfig
	if err := json.Unmarshal([]byte(text), &configs); err != nil {
		t.Fatal(err)
	}
	return configs
}

func call(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("content-type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testSysToken) // 鉴权（DEFINE）：测试按系统身份跑
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// GET /providers：**绝不含密钥内容**，只回 has_key。
func TestProvidersNeverEchoSecrets(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","vendor":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","api_key":"sk-super-secret"}]}`)
	recorder := call(server, "GET", "/api/v1/providers", "")
	if recorder.Code != 200 {
		t.Fatalf("%d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "sk-super-secret") {
		t.Fatalf("密钥泄漏进响应：%s", recorder.Body.String())
	}
	var views []ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !views[0].HasKey || views[0].ID != "ocgo" {
		t.Fatalf("视图 = %+v", views)
	}
}

// 字段顺序是契约（照旧版逐字对齐）。
//
// 2026-10-28 加了三格（`vendor_name` / `protocol_name` / `protocol_options`）：界面从此打中文名，
// 不再把 `opencode-go` 这种内部 id 摆在用户面前。它们紧跟在 `vendor` / `protocol` 后面 ——
// 与哪些 id 是一对，排列本身就在说这件事。
func TestProviderViewShape(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","vendor":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","headers":{"X-Extra":"1"}}]}`)
	recorder := call(server, "GET", "/api/v1/providers", "")
	want := `[{"id":"ocgo","alias":"","name":null,"vendor":"opencode-go","protocol":"openai-chat-completion",` +
		`"vendor_name":"OpenCode Go","protocol_name":"对话补全",` +
		`"protocol_options":[{"protocol":"openai-chat-completion","label":"对话补全","hint":"最常用，绝大多数服务商都支持"},` +
		`{"protocol":"openai-response","label":"应答接口","hint":"另一种接口格式；部分服务商支持"}],` +
		`"base_url":"https://opencode.ai/zen/go/v1","headers":{"X-Extra":"1"},"has_key":false,"model_filter":"","max_concurrency":0,"identity":"pi","last_refresh_at":null,"models":[]}]`
	if got := recorder.Body.String(); got != want {
		t.Fatalf("形状变了：\n得到 %s\n想要 %s", got, want)
	}
}

// 刷新回来的视图必须**带模型**（2026-10-28 修的 bug）：
//
// 从前 `refreshProvider` 用 `providerView()` 收尾，而那函数把 `Models` 写死成 `[]ModelView{}`
// ⇒ 界面拿 `view.models.length` 报"已导入 N 个模型"时**永远报 0 个**（模型其实好好地进了库）。
// 改完筛选器之后那句"已导入 0 个"更糟：会让人以为筛选器把模型全筛没了。
//
// 这条**走真刷新**（假上游 + `POST .../refresh`），断言的是**响应体里**有没有那两个模型 ——
// 只测 `providerViewWithModels` 本身是守不住的：那函数写对了、`refreshProvider` 忘了调它，
// 界面上照样是 0 个。
func TestRefreshResponseCarriesModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
	}))
	defer upstream.Close()

	server, _ := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "POST", "/api/v1/providers", `{"id":"x","vendor":"openai","base_url":"`+upstream.URL+`"}`)
	if recorder.Code != 201 {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server, "POST", "/api/v1/providers/x/refresh", "")
	if recorder.Code != 200 {
		t.Fatalf("刷新：%d %s", recorder.Code, recorder.Body.String())
	}
	var view ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Models) != 2 {
		t.Fatalf("刷新回来的视图该带 2 个模型，得到 %d 个 —— 界面正是拿它报「已导入 N 个」：%s",
			len(view.Models), recorder.Body.String())
	}
	if view.Models[0].UpstreamID != "alpha" {
		t.Fatalf("模型行没填上游 id：%+v", view.Models[0])
	}
}

// POST：201；重名 ⇒ 409；缺 vendor ⇒ 422；错配（矩阵）⇒ 422。
func TestCreateProvider(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "POST", "/api/v1/providers", `{"id":"x","vendor":"openai"}`)
	if recorder.Code != 201 {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server, "POST", "/api/v1/providers", `{"id":"x"}`)
	if recorder.Code != 409 {
		t.Fatalf("重名该 409：%d", recorder.Code)
	}
	recorder = call(server, "POST", "/api/v1/providers", `{}`)
	if recorder.Code != 422 {
		t.Fatalf("缺 id 该 422：%d", recorder.Code)
	}
	// vendor 是唯一必填
	recorder = call(server, "POST", "/api/v1/providers", `{"id":"novendor"}`)
	if recorder.Code != 422 {
		t.Fatalf("缺 vendor 该 422：%d %s", recorder.Code, recorder.Body.String())
	}
	// protocol 缺省 = chat：deepseek 不给 protocol 照建
	recorder = call(server, "POST", "/api/v1/providers", `{"id":"ds","vendor":"deepseek","api_key":"sk-x"}`)
	if recorder.Code != 201 {
		t.Fatalf("缺省 protocol 该 201：%d %s", recorder.Code, recorder.Body.String())
	}
	// 错配：deepseek + systemone 落不了地
	recorder = call(server, "POST", "/api/v1/providers", `{"id":"bad","vendor":"deepseek","protocol":"systemone","base_url":"http://127.0.0.1:1/x"}`)
	if recorder.Code != 422 {
		t.Fatalf("错配该 422：%d %s", recorder.Code, recorder.Body.String())
	}
	// custom 没端点也落不了地
	recorder = call(server, "POST", "/api/v1/providers", `{"id":"bad2","vendor":"custom"}`)
	if recorder.Code != 422 {
		t.Fatalf("custom 缺端点该 422：%d %s", recorder.Code, recorder.Body.String())
	}
}

// PATCH：api_key 的 None/"" 语义（None = 不动 ✓ "" = 删除 ✓）。
func TestUpdateProviderKeySemantics(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"x","vendor":"openai","api_key":"old"}]}`)
	call(server, "PATCH", "/api/v1/providers/x", `{"name":"新名"}`)
	var views []ProviderView
	_ = json.Unmarshal(call(server, "GET", "/api/v1/providers", "").Body.Bytes(), &views)
	if views[0].HasKey == false {
		t.Fatal("没给 api_key 就不该动密钥")
	}
	call(server, "PATCH", "/api/v1/providers/x", `{"api_key":""}`)
	_ = json.Unmarshal(call(server, "GET", "/api/v1/providers", "").Body.Bytes(), &views)
	if views[0].HasKey {
		t.Fatal("空串 = 删除密钥")
	}
	call(server, "PATCH", "/api/v1/providers/x", `{"api_key":"new"}`)
	_ = json.Unmarshal(call(server, "GET", "/api/v1/providers", "").Body.Bytes(), &views)
	if !views[0].HasKey {
		t.Fatal("给了就设置")
	}
}

// 刷新：发现列整轮更新，**用户列一个字不动**；这次没见到的删行。
func TestRefreshTouchesOnlyDiscoveryColumns(t *testing.T) {
	_, st := newProvidersServer(t, `{"providers":[{"id":"x","vendor":"openai","base_url":"http://127.0.0.1:0"}]}`)
	if _, _, _, err := st.RefreshDiscovered("x", []store.Discovered{
		{UpstreamID: "a"}, {UpstreamID: "b"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	// 用户改了 a 的显示名
	name := "我的名字"
	if err := st.SetModelOverride("x", "a", &name, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 再刷新：a 还在（改了名），b 没见到 ⇒ 删行
	owned := "someone"
	if _, _, removed, err := st.RefreshDiscovered("x", []store.Discovered{
		{UpstreamID: "a", OwnedBy: &owned},
	}, 2); err != nil {
		t.Fatal(err)
	} else if removed != 1 {
		t.Fatalf("这次没见到的该删 1 行：%d", removed)
	}
	rows, _ := st.ListModels("x")
	if len(rows) != 1 {
		t.Fatalf("只剩 a：%+v", rows)
	}
	if rows[0].DisplayName == nil || *rows[0].DisplayName != "我的名字" {
		t.Fatalf("用户列被刷新动了：%+v", rows[0])
	}
	if rows[0].OwnedBy == nil || *rows[0].OwnedBy != "someone" {
		t.Fatalf("发现列该被刷新覆写：%+v", rows[0])
	}
}

// 端到端：真拉一次 `/models` —— 断言**发现确实落库**（这会当场抓"解错 JSON 字段"这类静默 bug）。
func TestRefreshDiscoversModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash","owned_by":"deepseek","context_length":131072}]}`))
	}))
	defer upstream.Close()

	server, st := newProvidersServer(t, `{"providers":[]}`)
	// 先建渠道（指到假上游）
	recorder := call(server, "POST", "/api/v1/providers",
		`{"id":"x","vendor":"openai","base_url":"`+upstream.URL+`"}`)
	if recorder.Code != 201 {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server, "POST", "/api/v1/providers/x/refresh", "")
	if recorder.Code != 200 {
		t.Fatalf("刷新：%d %s", recorder.Code, recorder.Body.String())
	}
	rows, err := st.ListModels("x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UpstreamID != "deepseek-v4-flash" {
		t.Fatalf("发现没落库：%+v", rows)
	}
	if rows[0].ContextLength == nil || *rows[0].ContextLength != 131072 {
		t.Fatalf("发现列：%+v", rows[0])
	}
	// 拍平那份也该看得见，且显示名走了 prettify
	var items []ModelListItem
	_ = json.Unmarshal(call(server, "GET", "/api/v1/models", "").Body.Bytes(), &items)
	if len(items) != 1 || items[0].Name != "Deepseek V4 Flash" {
		t.Fatalf("拍平 = %+v", items)
	}
}

// refresh-routes：非聚合站 502 说清楚；不存在的渠道 404。
func TestRefreshRoutesRejectsNonAggregators(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	if recorder := call(server, "POST", "/api/v1/providers/nope/refresh-routes", ""); recorder.Code != 404 {
		t.Fatalf("不存在该 404：%d", recorder.Code)
	}
	recorder := call(server, "POST", "/api/v1/providers",
		`{"id":"ds","vendor":"deepseek","api_key":"sk-x"}`)
	if recorder.Code != 201 {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(server, "POST", "/api/v1/providers/ds/refresh-routes", ""); recorder.Code != 502 {
		t.Fatalf("非聚合站该 502 说清楚：%d %s", recorder.Code, recorder.Body.String())
	}
}

// 预设清单接口：客户端靠它列出"可选的预设 provider"。
func TestProviderPresetsEndpoint(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "GET", "/api/v1/providers/presets", "")
	if recorder.Code != 200 {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var list []providers.PresetInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]providers.PresetInfo{}
	for _, item := range list {
		kinds[string(item.Vendor)] = item
	}
	if _, ok := kinds["opencode-go"]; !ok {
		t.Fatalf("清单里得有 opencode-go：%+v", kinds)
	}
	if kinds["opencode-go"].SessionHeader != "x-opencode-session" {
		t.Fatal("OpenCode Go 的会话头要如实告诉客户端")
	}
	// 协议选项要带中文名（2026-10-28）：界面拿它画下拉，**不许自己另抄一张表**。
	for _, option := range kinds["opencode-go"].ProtocolOptions {
		if option.Label == "" {
			t.Fatalf("协议 %q 没有中文名", option.Protocol)
		}
	}
	if kinds["opencode-go"].ProtocolOptions[0].Label != "对话补全" {
		t.Fatalf("第一个该是对话补全（缺省那条），得到 %+v", kinds["opencode-go"].ProtocolOptions)
	}
	// 它不该被 /providers/{id} 抢走（字面段优先）
	if recorder.Code == 404 {
		t.Fatal("被 {id} 抢走了")
	}
}

// 只配 `kind` 的渠道：视图要回**生效的端点**（否则界面以为没端点）。
func TestProviderViewShowsEffectiveBaseURL(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","vendor":"opencode-go"}]}`)
	var views []ProviderView
	if err := json.Unmarshal(call(server, "GET", "/api/v1/providers", "").Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("%+v", views)
	}
	if views[0].BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("该回预设的端点，得到 %q", views[0].BaseURL)
	}
}

// GET/PUT /config/defaults：缺省三件各改各的（nil 不动；"" 清回内置缺省）。
func TestDefaultsEndpoints(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	// 缺省读出来是内置值（沙盒 config.json 缺失 ⇒ LoadConfig 兜底）
	var defaults map[string]string
	if recorder := call(server, "GET", "/api/v1/config/defaults", ""); recorder.Code != 200 {
		t.Fatalf("GET 缺省：%d %s", recorder.Code, recorder.Body.String())
	} else if err := json.Unmarshal(recorder.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	} else if defaults["provider"] != "dummy" || defaults["model"] != "dummy" || defaults["agent"] != "default" {
		t.Fatalf("内置缺省 = %+v", defaults)
	}
	// 只改 provider 一格，其余不动
	recorder := call(server, "PUT", "/api/v1/config/defaults", `{"provider":"deepseek"}`)
	if recorder.Code != 200 {
		t.Fatalf("PUT 缺省：%d %s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(server, "GET", "/api/v1/config/defaults", ""); recorder.Code != 200 {
		t.Fatalf("重读：%d", recorder.Code)
	} else if err := json.Unmarshal(recorder.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	} else if defaults["provider"] != "deepseek" || defaults["model"] != "dummy" {
		t.Fatalf("只该动 provider：%+v", defaults)
	}
	// "" 清回内置缺省
	if recorder := call(server, "PUT", "/api/v1/config/defaults", `{"provider":""}`); recorder.Code != 200 {
		t.Fatalf("清缺省：%d %s", recorder.Code, recorder.Body.String())
	} else if err := json.Unmarshal(recorder.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	} else if defaults["provider"] != "dummy" {
		t.Fatalf("清空该回 dummy：%+v", defaults)
	}
}

// 删渠道 ⇒ **它的模型一并消失**（2026-10-22 用户报："删除一个 Provider 不会导致它的模型被删掉。
// 我还是能选中！"）。
//
// 证伪点（这个 bug 的每一环都要钉住）：
//
//	① `GET /models` 里不再出现已删渠道的模型（用户报的直接现象）；
//	② 库里那几行真的没了（不是只在接口层过滤 —— 否则换个读口又漏出来）；
//	③ **别的渠道的模型一个都不许少**（删 A 不能连带删 B，最容易写错的方向）；
//	④ `provider_state` 也清了（渠道回来时重新发现，不该继承旧时间戳）。
func TestDeleteProviderAlsoDeletesItsModels(t *testing.T) {
	server, st := newProvidersServer(t, `{"providers":[
	  {"id":"gone","vendor":"custom","base_url":"http://127.0.0.1:1/v1"},
	  {"id":"kept","vendor":"custom","base_url":"http://127.0.0.1:2/v1"}
	]}`)
	// 两个渠道各铺两个模型（发现列走 RefreshDiscovered，与真实刷新同一条路）
	if _, _, _, err := st.RefreshDiscovered("gone", []store.Discovered{
		{UpstreamID: "g-1"}, {UpstreamID: "g-2"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.RefreshDiscovered("kept", []store.Discovered{
		{UpstreamID: "k-1"}, {UpstreamID: "k-2"},
	}, 1); err != nil {
		t.Fatal(err)
	}

	if recorder := call(server, "DELETE", "/api/v1/providers/gone", ""); recorder.Code != 204 {
		t.Fatalf("删渠道：%d %s", recorder.Code, recorder.Body.String())
	}

	// ① 接口层：只剩 kept 的两个
	recorder := call(server, "GET", "/api/v1/models", "")
	if recorder.Code != 200 {
		t.Fatalf("列模型：%d %s", recorder.Code, recorder.Body.String())
	}
	var items []ModelListItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Provider == "gone" {
			t.Fatalf("已删渠道的模型还在列表里（用户就是这么撞上的）：%+v", item)
		}
	}
	if len(items) != 2 {
		t.Fatalf("该只剩 kept 的 2 个，得到 %d 个：%+v", len(items), items)
	}

	// ② 库层：那两行真的没了
	if rows, err := st.ListModels("gone"); err != nil {
		t.Fatal(err)
	} else if len(rows) != 0 {
		t.Fatalf("库里还留着已删渠道的模型：%+v", rows)
	}

	// ③ 别的渠道一个不少（删 A 连带删 B 是这类改动最容易犯的错）
	if rows, err := st.ListModels("kept"); err != nil {
		t.Fatal(err)
	} else if len(rows) != 2 {
		t.Fatalf("误删了别的渠道的模型：%+v", rows)
	}

	// ④ 刷新时间也清了
	if at, err := st.LastRefreshAt("gone"); err != nil {
		t.Fatal(err)
	} else if at != nil {
		t.Fatalf("provider_state 该被清掉，仍留着 %d", *at)
	}
}

// 渠道不存在 ⇒ 404，且**不许**顺手删掉同名的模型数据（那条路径不该有任何副作用）。
func TestDeleteUnknownProviderIs404AndTouchesNothing(t *testing.T) {
	server, st := newProvidersServer(t, `{"providers":[{"id":"kept","vendor":"custom","base_url":"http://127.0.0.1:1/v1"}]}`)
	if _, _, _, err := st.RefreshDiscovered("kept", []store.Discovered{{UpstreamID: "k-1"}}, 1); err != nil {
		t.Fatal(err)
	}
	if recorder := call(server, "DELETE", "/api/v1/providers/nope", ""); recorder.Code != 404 {
		t.Fatalf("该 404：%d", recorder.Code)
	}
	if rows, err := st.ListModels("kept"); err != nil {
		t.Fatal(err)
	} else if len(rows) != 1 {
		t.Fatalf("404 那条路不该动数据：%+v", rows)
	}
}

// 模型筛选器（2026-10-22 用户定）：建渠道能带上、PATCH 能改能清、视图回显、坏写法 422。
//
// 这条钉的是**契约那一层**（字段名 / 三态语义 None=不动、""=清）。筛选的语义本身在
// `registry` 的测试里（`FilterModels` / `RefreshOne`），这里不重复。
func TestProviderModelFilterRoundTrip(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"or","vendor":"openrouter","api_key":"sk-x"}]}`)

	// ① 建渠道时带上筛选器
	recorder := call(server, "POST", "/api/v1/providers",
		`{"id":"or2","vendor":"openrouter","api_key":"sk-y","model_filter":"gpt"}`)
	if recorder.Code != 201 {
		t.Fatalf("建渠道：%d %s", recorder.Code, recorder.Body.String())
	}
	var view ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ModelFilter != "gpt" {
		t.Fatalf("视图该回显 model_filter=gpt，得到 %q", view.ModelFilter)
	}

	// ② 写进文件了没（配置是整体重写的，字段漏了就是静默丢）
	configs, err := config.LoadProviders(config.Paths{ConfigDir: server.paths.ConfigDir, DataDir: server.paths.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, provider := range configs.Providers {
		if provider.ID == "or2" {
			found = true
			if provider.ModelFilter != "gpt" {
				t.Fatalf("落盘的 model_filter 是 %q，该是 gpt", provider.ModelFilter)
			}
		}
	}
	if !found {
		t.Fatal("or2 没写进 providers.json")
	}

	// ③ PATCH 改
	recorder = call(server, "PATCH", "/api/v1/providers/or2", `{"model_filter":"claude"}`)
	if recorder.Code != 200 {
		t.Fatalf("PATCH：%d %s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ModelFilter != "claude" {
		t.Fatalf("PATCH 后该是 claude，得到 %q", view.ModelFilter)
	}

	// ④ PATCH 不带这个字段 = 不动（None 语义）
	if recorder = call(server, "PATCH", "/api/v1/providers/or2", `{"name":"新名字"}`); recorder.Code != 200 {
		t.Fatalf("PATCH name：%d", recorder.Code)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ModelFilter != "claude" {
		t.Fatalf("没给 model_filter 就不该动它，得到 %q", view.ModelFilter)
	}

	// ⑤ PATCH 给 "" = 清（不筛）
	if recorder = call(server, "PATCH", "/api/v1/providers/or2", `{"model_filter":""}`); recorder.Code != 200 {
		t.Fatalf("PATCH 清空：%d", recorder.Code)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ModelFilter != "" {
		t.Fatalf("该被清空，得到 %q", view.ModelFilter)
	}
}

// 别名（2026-11-09）：`alias` 只是**显示层** —— id 不变、会话 / 模型引用不变，
// 但 `GET /providers`、`GET /models` 与 providers.json 三处都要能带上它。
func TestProviderAliasRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
	}))
	defer upstream.Close()

	server, _ := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "POST", "/api/v1/providers",
		`{"id":"opencode-go-2","vendor":"openai","base_url":"`+upstream.URL+`","alias":"  OpenCode GO  "}`)
	if recorder.Code != 201 {
		t.Fatalf("建渠道：%d %s", recorder.Code, recorder.Body.String())
	}
	view := decodeBody[ProviderView](t, recorder)
	// 首尾空白吃掉（纯空白的别名等于没写）；id 一个字符都不许动。
	if view.Alias != "OpenCode GO" || view.ID != "opencode-go-2" {
		t.Fatalf("视图 = alias %q / id %q", view.Alias, view.ID)
	}

	// 配置真的落盘了（别名不参与 id，只多一格）
	configs, err := config.LoadProviders(config.Paths{ConfigDir: server.paths.ConfigDir, DataDir: server.paths.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := configs.Get("opencode-go-2")
	if !ok || stored.Alias != "OpenCode GO" {
		t.Fatalf("providers.json = %+v", configs.Providers)
	}

	// 刷一次模型，`GET /models` 的每一项都该带渠道别名（模型选择器组头用）。
	if recorder = call(server, "POST", "/api/v1/providers/opencode-go-2/refresh", ""); recorder.Code != 200 {
		t.Fatalf("刷新：%d %s", recorder.Code, recorder.Body.String())
	}
	var items []ModelListItem
	if err := json.Unmarshal(call(server, "GET", "/api/v1/models", "").Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Provider != "opencode-go-2" || items[0].Alias != "OpenCode GO" {
		t.Fatalf("拍平列表 = %+v", items)
	}

	// PATCH 不带 alias = 不动
	if recorder = call(server, "PATCH", "/api/v1/providers/opencode-go-2", `{"name":"顺手改个名"}`); recorder.Code != 200 {
		t.Fatalf("PATCH：%d", recorder.Code)
	}
	view = decodeBody[ProviderView](t, recorder)
	if view.Alias != "OpenCode GO" || view.ID != "opencode-go-2" {
		t.Fatalf("没给 alias 就不该动它：%+v", view)
	}

	// PATCH 给 "" = 清别名（id 仍然不变）
	if recorder = call(server, "PATCH", "/api/v1/providers/opencode-go-2", `{"alias":""}`); recorder.Code != 200 {
		t.Fatalf("PATCH 清别名：%d", recorder.Code)
	}
	view = decodeBody[ProviderView](t, recorder)
	if view.Alias != "" || view.ID != "opencode-go-2" {
		t.Fatalf("清别名后 = %+v", view)
	}
}

// `*` 在**写入时**就 422：它没有"全部"的含义，而照字面匹配会把渠道的模型表悄悄清空
// （详见 registry.ValidateModelFilter 的注释）。
func TestProviderModelFilterRejectsWildcard(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"or","vendor":"openrouter"}]}`)

	recorder := call(server, "POST", "/api/v1/providers",
		`{"id":"bad","vendor":"openrouter","model_filter":"*"}`)
	if recorder.Code != 422 {
		t.Fatalf("建渠道带 * 该 422，得到 %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "通配符") {
		t.Fatalf("报错该讲清是哪不对：%s", recorder.Body.String())
	}

	// PATCH 这条路同样要拦（两条写入口同一个口径）
	recorder = call(server, "PATCH", "/api/v1/providers/or", `{"model_filter":"gpt*"}`)
	if recorder.Code != 422 {
		t.Fatalf("PATCH 带 gpt* 该 422，得到 %d %s", recorder.Code, recorder.Body.String())
	}
}

// 筛选器在**真 HTTP 刷新链路**上也生效（用户点的是「刷新模型」这个按钮，不是 registry 的函数）。
// 顺带钉住"改筛选器 ⇒ 再刷一次才生效，且旧行会被剪掉"这个时序。
func TestRefreshProviderAppliesModelFilterOverHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"openai/gpt-5"},{"id":"gpt-6"},{"id":"openai/gpt-5.6-sol"},
		  {"id":"anthropic/claude-sonnet-4.5"}]}`))
	}))
	defer upstream.Close()

	server, st := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "POST", "/api/v1/providers",
		`{"id":"or","vendor":"openai","base_url":"`+upstream.URL+`","model_filter":"gpt"}`)
	if recorder.Code != 201 {
		t.Fatalf("建渠道：%d %s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(server, "POST", "/api/v1/providers/or/refresh", ""); recorder.Code != 200 {
		t.Fatalf("刷新：%d %s", recorder.Code, recorder.Body.String())
	}

	// ① 只导入了匹配的 3 个（用户原话那三个）
	rows, err := st.ListModels("or")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.UpstreamID)
	}
	if len(rows) != 3 {
		t.Fatalf("该只导入 3 个，得到 %d：%v", len(rows), got)
	}
	for _, id := range got {
		if !strings.Contains(id, "gpt") {
			t.Fatalf("不匹配的模型被导入了：%v", got)
		}
	}

	// ② 模型下拉（`GET /models`）看到的也只有这 3 个
	recorder = call(server, "GET", "/api/v1/models", "")
	var items []ModelListItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("/models 该只有 3 个，得到 %d", len(items))
	}

	// ③ 换成 claude 再刷 ⇒ 先前那 3 行被"没见到就删行"剪掉，只剩 claude
	if recorder = call(server, "PATCH", "/api/v1/providers/or", `{"model_filter":"claude"}`); recorder.Code != 200 {
		t.Fatalf("改筛选器：%d %s", recorder.Code, recorder.Body.String())
	}
	if recorder = call(server, "POST", "/api/v1/providers/or/refresh", ""); recorder.Code != 200 {
		t.Fatalf("再刷：%d %s", recorder.Code, recorder.Body.String())
	}
	rows, err = st.ListModels("or")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UpstreamID != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("换筛选器后该只剩 claude，得到 %+v", rows)
	}
}

// 手改配置文件塞进 `*` 的兜底：刷新**报错**而不是把模型表清空（Probe 里先验后拉）。
func TestRefreshProviderWithWildcardFilterFailsAndKeepsModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-5"}]}`))
	}))
	defer upstream.Close()

	server, st := newProvidersServer(t, `{"providers":[]}`)
	// 走写入接口建不了（`*` 会被 422 拦）⇒ 这里直接造库里的旧数据模拟"以前导入过"
	if _, _, _, err := st.RefreshDiscovered("or", []store.Discovered{{UpstreamID: "openai/gpt-5"}}, 1); err != nil {
		t.Fatal(err)
	}
	// 再把手写的 `*` 塞进配置文件（绕开写入校验，模拟用户直接改文件）
	configs, err := config.LoadProviders(config.Paths{ConfigDir: server.paths.ConfigDir, DataDir: server.paths.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	configs.Providers = append(configs.Providers, config.Provider{
		ID: "or", Vendor: "openai", BaseURL: upstream.URL, ModelFilter: "*",
	})
	if err := config.SaveJSON(server.paths.Config("providers.json"), configs); err != nil {
		t.Fatal(err)
	}

	if recorder := call(server, "POST", "/api/v1/providers/or/refresh", ""); recorder.Code == 200 {
		t.Fatalf("`*` 该让刷新失败，得到 %d %s", recorder.Code, recorder.Body.String())
	}
	rows, err := st.ListModels("or")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("坏筛选器不许动数据，该还是 1 行，得到 %d", len(rows))
	}
}

// 测试连通性：只读、不落库；dummy 直接回"不用测"，缺端点的 transient 配置回 ok:false。
// 2026-11-02：编辑器从「保存 / 刷新模型 / 刷路由」三个按钮改成「保存并刷新 / 测试连通性」。
func TestTestProviderEndpoint(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"dummy","vendor":"dummy"}]}`)

	rec := call(server, "POST", "/api/v1/providers/dummy/test", `{}`)
	if rec.Code != 200 {
		t.Fatalf("dummy 测试该 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[TestProviderResult](t, rec)
	if !got.OK || got.Note == "" {
		t.Fatalf("dummy 该回 ok + note，得到 %+v", got)
	}

	// 还没保存的新渠道：用请求体里的临时配置测；缺 base_url 的 custom ⇒ ok:false（不是 500）。
	rec = call(server, "POST", "/api/v1/providers/brand-new/test", `{"vendor":"custom"}`)
	if rec.Code != 200 {
		t.Fatalf("配置不完整的测试该 200+ok:false，得到 %d：%s", rec.Code, rec.Body.String())
	}
	got = decodeBody[TestProviderResult](t, rec)
	if got.OK || got.Error == "" {
		t.Fatalf("缺端点的 custom 该回 ok:false + error，得到 %+v", got)
	}
}

// 渠道标识可以改（2026-11-05 用户定：「渠道标识为啥不能修改了？要修改啊！」）。
//
// 这条钉的是**跨两个存储的一致**：`providers.json` 换名 + 库里的引用（会话 / 模型 / 路由 /
// 上次刷新 / 摘要）一起搬。只改配置的话，会话会指向一个不存在的渠道 —— 发消息静默回落
// `BackendFallback`（"改名之后全坏了"），而测试全绿，因为没人在断言这条链。
func TestRenameProviderViaPatch(t *testing.T) {
	server, st := newProvidersServer(t, `{"providers":[{"id":"a","vendor":"dummy"},{"id":"b","vendor":"dummy"}]}`)
	session, err := st.CreateSession("a", "m", "", "")
	if err != nil {
		t.Fatal(err)
	}

	rec := call(server, "PATCH", "/api/v1/providers/a", `{"new_id":"a-renamed"}`)
	if rec.Code != 200 {
		t.Fatalf("改名该 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	view := decodeBody[ProviderView](t, rec)
	if view.ID != "a-renamed" {
		t.Fatalf("回执的 id = %q", view.ID)
	}
	// 配置真的写回去了（不是只在内存里改了）
	if _, ok := server.loadProviders().Get("a-renamed"); !ok {
		t.Fatal("providers.json 里没有新标识")
	}
	if _, ok := server.loadProviders().Get("a"); ok {
		t.Fatal("providers.json 里还留着旧标识")
	}
	// 库里的活引用跟着搬
	if got, err := st.GetSession(session.ID); err != nil || got.Provider != "a-renamed" {
		t.Fatalf("会话没跟着改名：%+v %v", got, err)
	}
	// 旧标识现在 404（改名之后它不再是任何渠道）
	if rec := call(server, "PATCH", "/api/v1/providers/a", `{"vendor":"dummy"}`); rec.Code != 404 {
		t.Fatalf("旧标识该 404，得到 %d", rec.Code)
	}

	// 撞已有渠道 ⇒ 409（不合并）
	if rec := call(server, "PATCH", "/api/v1/providers/a-renamed", `{"new_id":"b"}`); rec.Code != 409 {
		t.Fatalf("撞名该 409，得到 %d：%s", rec.Code, rec.Body.String())
	}
	// 空 / 纯空白 ⇒ 422
	for _, body := range []string{`{"new_id":""}`, `{"new_id":"   "}`} {
		if rec := call(server, "PATCH", "/api/v1/providers/a-renamed", body); rec.Code != 422 {
			t.Fatalf("%s 该 422，得到 %d", body, rec.Code)
		}
	}
	// 拒绝之后渠道还在原标识下（没被半途改掉）
	if _, ok := server.loadProviders().Get("a-renamed"); !ok {
		t.Fatal("被拒绝的改名把渠道弄丢了")
	}
}
