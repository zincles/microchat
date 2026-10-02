package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// NpmToAPI 全分支：三类 npm + 缺省（含空与陌生）。
func TestNpmToAPI(t *testing.T) {
	cases := map[string]string{
		"@ai-sdk/openai":    "openai-responses",
		"@ai-sdk/anthropic": "anthropic-messages",
		"@ai-sdk/google":    "google-generative-ai",
		"":                  "openai-completions",
		"@ai-sdk/other":     "openai-completions",
	}
	for in, want := range cases {
		if got := NpmToAPI(in); got != want {
			t.Fatalf("NpmToAPI(%q) = %q，想要 %q", in, got, want)
		}
	}
}

const routeFixture = `{
	"opencode": {"models": {
		"a": {"provider": {"npm": "@ai-sdk/openai"}},
		"b": {"provider": {"npm": "@ai-sdk/anthropic"}},
		"c": {"provider": {"npm": "@ai-sdk/google"}},
		"d": {},
		"e": {"provider": {"npm": null}},
		"f": {"provider": null}
	}},
	"opencode-go": {"models": {
		"g": {"provider": {"npm": "@ai-sdk/openai"}}
	}},
	"someone-else": {"models": {
		"h": {"provider": {"npm": "@ai-sdk/openai"}}
	}}
}`

// Parse：三类 npm + null/缺字段 ⇒ 缺省；只要两节；坏 JSON ⇒ 错。
func TestParseModelsDev(t *testing.T) {
	routes, err := ParseModelsDev([]byte(routeFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("只要两节：%v", routes)
	}
	want := map[string]string{
		"a": "openai-responses", "b": "anthropic-messages", "c": "google-generative-ai",
		"d": "openai-completions", "e": "openai-completions", "f": "openai-completions",
	}
	for id, api := range want {
		if routes["opencode"][id] != api {
			t.Fatalf("%s = %q，想要 %q", id, routes["opencode"][id], api)
		}
	}
	if routes["opencode-go"]["g"] != "openai-responses" {
		t.Fatalf("g = %q", routes["opencode-go"]["g"])
	}
	if _, err := ParseModelsDev([]byte("{坏")); err == nil {
		t.Fatal("坏 JSON 该报错")
	}
}

// rewriteHost：把一切请求改写到本地 httptest（只为测 Fetch 的取数与状态码分支）。
type rewriteHost struct{ target string }

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(r.target)
	if err != nil {
		return nil, err
	}
	dup := req.Clone(req.Context())
	dup.URL.Scheme = u.Scheme
	dup.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(dup)
}

// Fetch：httptest 走通；非 200 ⇒ 错。
func TestFetchModelsDev(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(routeFixture))
	}))
	t.Cleanup(server.Close)

	// httptest 的地址盖不掉常量 URL —— 这里把 Transport 劫到本地（URL 本人不动）。
	client := &http.Client{Transport: rewriteHost{target: server.URL}}
	routes, err := FetchModelsDev(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if routes["opencode"]["a"] != "openai-responses" || routes["opencode-go"]["g"] != "openai-responses" {
		t.Fatalf("拉到的形状不对：%v", routes)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	if _, err := FetchModelsDev(context.Background(),
		&http.Client{Transport: rewriteHost{target: broken.URL}}); err == nil {
		t.Fatal("非 200 该报错")
	}
}
