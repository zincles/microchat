package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/store"
)

// newCORSServer：一台沙盒服务；token 非空就配到 `server.auth_token` 上（空 = 不配口令）。
func newCORSServer(t *testing.T, token string) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.DefaultConfig()
	if token != "" {
		cfg.Server.AuthToken = &token
	}
	return newTestServer(st, cfg, config.Paths{ConfigDir: dir, DataDir: dir})
}

// corsRequest：发一个请求（请求头按给定来），拿回 recorder。
func corsRequest(server *Server, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// assertCORS：钉住那套 CORS 头（`Allow-Headers` 会随请求回显，不在这一份里断言）。
func assertCORS(t *testing.T, recorder *httptest.ResponseRecorder, origin string) {
	t.Helper()
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("Allow-Origin = %q，想要 %q", got, origin)
	}
	if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q，应含 Origin", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PATCH, PUT, DELETE, OPTIONS" {
		t.Fatalf("Allow-Methods = %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("Max-Age = %q，想要 600", got)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != "Allow" {
		t.Fatalf("Expose-Headers = %q，想要 Allow", got)
	}
}

// 预检：**不带** Authorization 也要 204 —— 它必须绕开鉴权（先撞鉴权就是 401 ⇒ 正式请求发不出去）。
func TestPreflightBypassesAuth(t *testing.T) {
	server := newCORSServer(t, "secret")
	recorder := corsRequest(server, "OPTIONS", "/api/v1/sessions", map[string]string{
		"Origin":                         "http://x",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization,content-type",
	})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("预检状态码 = %d，想要 204", recorder.Code)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("预检响应体应当为空，得到 %q", body)
	}
	assertCORS(t, recorder, "http://x")
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "authorization,content-type" {
		t.Fatalf("Allow-Headers = %q，应回显请求头", got)
	}
}

// 没有 `Access-Control-Request-Headers` 时回一个够用的默认值。
func TestPreflightDefaultAllowHeaders(t *testing.T) {
	server := newCORSServer(t, "")
	recorder := corsRequest(server, "OPTIONS", "/api/v1/health", map[string]string{"Origin": "http://x"})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("预检状态码 = %d，想要 204", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("Allow-Headers = %q，想要默认值", got)
	}
}

// 配了口令：无 / 错 token ⇒ 401（且**带 CORS 头**，否则浏览器读不到这个错误）；对 token ⇒ 200。
func TestAuthEnforcedWhenTokenConfigured(t *testing.T) {
	server := newCORSServer(t, "secret")

	noToken := corsRequest(server, "GET", "/api/v1/health", map[string]string{"Origin": "http://x"})
	if noToken.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 状态码 = %d，想要 401", noToken.Code)
	}
	assertCORS(t, noToken, "http://x")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(noToken.Body.Bytes(), &body); err != nil {
		t.Fatalf("401 响应体不是固定错误体：%v（%s）", err, noToken.Body.String())
	}
	if body.Error.Code != "unauthorized" || body.Error.Message == "" {
		t.Fatalf("401 错误体 = %+v，想要 code=unauthorized", body.Error)
	}

	wrong := corsRequest(server, "GET", "/api/v1/health", map[string]string{"Authorization": "Bearer nope"})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("错 token 状态码 = %d，想要 401", wrong.Code)
	}

	ok := corsRequest(server, "GET", "/api/v1/health", map[string]string{"Authorization": "Bearer secret"})
	if ok.Code != http.StatusOK {
		t.Fatalf("对 token 状态码 = %d，想要 200", ok.Code)
	}
}

// 没配口令（默认）⇒ 一律照旧放行 —— 别把默认行为改掉。
func TestAuthOffWhenNoToken(t *testing.T) {
	server := newCORSServer(t, "")
	if got := corsRequest(server, "GET", "/api/v1/health", nil).Code; got != http.StatusOK {
		t.Fatalf("没配 token 状态码 = %d，想要 200", got)
	}
}

// CORS 头要盖住**每一个**响应：普通 200、错误体 404；没有 Origin 就回 `*`。
func TestCORSHeadersOnEveryResponse(t *testing.T) {
	server := newCORSServer(t, "")

	ok := corsRequest(server, "GET", "/api/v1/health", map[string]string{"Origin": "http://x"})
	if ok.Code != http.StatusOK {
		t.Fatalf("health 状态码 = %d", ok.Code)
	}
	assertCORS(t, ok, "http://x")

	missing := corsRequest(server, "GET", "/api/v1/sessions/does-not-exist/state", map[string]string{"Origin": "http://x"})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在会话的状态码 = %d，想要 404", missing.Code)
	}
	assertCORS(t, missing, "http://x")

	star := corsRequest(server, "GET", "/api/v1/health", nil)
	if got := star.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("无 Origin 时 Allow-Origin = %q，想要 *", got)
	}
}

// 405 那条口径不变：路径在、方法不对 ⇒ 框架回 405 且带 `allow:`（CORS 头照加）。
func TestMethodNotAllowedKeeps405(t *testing.T) {
	server := newCORSServer(t, "")
	recorder := corsRequest(server, "DELETE", "/api/v1/health", map[string]string{"Origin": "http://x"})
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d，想要 405", recorder.Code)
	}
	if recorder.Header().Get("Allow") == "" {
		t.Fatal("405 应当带 allow 头")
	}
	assertCORS(t, recorder, "http://x")
}
