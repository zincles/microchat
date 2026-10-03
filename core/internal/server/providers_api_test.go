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
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// GET /providers：**绝不含密钥内容**，只回 has_key。
func TestProvidersNeverEchoSecrets(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","kind":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","api_key":"sk-super-secret"}]}`)
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
func TestProviderViewShape(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","kind":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","headers":{"X-Extra":"1"}}]}`)
	recorder := call(server, "GET", "/api/v1/providers", "")
	want := `[{"id":"ocgo","name":null,"kind":"opencode-go","vendor":"opencode-go","protocol":"openai-chat-completion","base_url":"https://opencode.ai/zen/go/v1","headers":{"X-Extra":"1"},"has_key":false,"identity":"pi","last_refresh_at":null,"models":[]}]`
	if got := recorder.Body.String(); got != want {
		t.Fatalf("形状变了：\n得到 %s\n想要 %s", got, want)
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
	// vendor 是唯一必填：只给 kind 不算数
	recorder = call(server, "POST", "/api/v1/providers", `{"id":"novendor","kind":"openai"}`)
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
		kinds[string(item.Kind)] = item
	}
	if _, ok := kinds["opencode-go"]; !ok {
		t.Fatalf("清单里得有 opencode-go：%+v", kinds)
	}
	if kinds["opencode-go"].SessionHeader != "x-opencode-session" {
		t.Fatal("OpenCode Go 的会话头要如实告诉客户端")
	}
	// 它不该被 /providers/{id} 抢走（字面段优先）
	if recorder.Code == 404 {
		t.Fatal("被 {id} 抢走了")
	}
}

// 只配 `kind` 的渠道：视图要回**生效的端点**（否则界面以为没端点）。
func TestProviderViewShowsEffectiveBaseURL(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","kind":"opencode-go"}]}`)
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
