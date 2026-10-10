package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microchat/internal/auth"
)

// authCall：鉴权专测的请求帮手 —— **不**默认带系统令牌，凭据按参数给。
func authCall(server *Server, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("content-type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func decodeBody[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("解响应失败：%v（%s）", err, recorder.Body.String())
	}
	return out
}

// 规矩（DEFINE「用户与鉴权」）：**uid 0 = root 恒在、口令空着等 setup**；setup 只开一次。
// 空口令的 root 绝不能登录（哪怕就试在 setup 之前）。
func TestRootSetupOnceAndEmptyHashCannotLogin(t *testing.T) {
	box := newDummySandboxWith(t, false)

	status := authCall(box.server, "GET", "/api/v1/auth/status", "", "")
	st := decodeBody[struct {
		Status     string `json:"status"`
		NeedsSetup bool   `json:"needs_setup"`
	}](t, status)
	if status.Code != 200 || st.Status != "ok" || !st.NeedsSetup {
		t.Fatalf("未初始化时的 status = %d %+v", status.Code, st)
	}

	// 未初始化 = 服务未启用：**连 login 都走不通**（403 setup_required，不是 401）——
	// 空口令的 root 根本没有可登录的路径（"必须先设密码才启用服务"，2026-10-10 定）。
	for _, body := range []string{`{"user":"root","password":""}`, `{"user":"root","password":"whatever"}`} {
		got := authCall(box.server, "POST", "/api/v1/auth/login", body, "")
		if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "setup_required") {
			t.Fatalf("未初始化 root 登录（%s）= %d %s，想要 403 setup_required", body, got.Code, got.Body.String())
		}
	}

	if wrong := authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"short"}`, ""); wrong.Code != 400 {
		t.Fatalf("短口令 = %d，想要 400", wrong.Code)
	}

	first := authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, "")
	if first.Code != 200 {
		t.Fatalf("setup = %d（%s）", first.Code, first.Body.String())
	}
	got := decodeBody[struct {
		Token string `json:"token"`
		User  struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			IsAdmin bool   `json:"is_admin"`
		} `json:"user"`
	}](t, first)
	if got.User.ID != 0 || !got.User.IsAdmin || got.User.Name != "root" || got.Token == "" {
		t.Fatalf("root 的形状不对：%+v", got)
	}

	// 第二次 setup ⇒ 403；status 也翻了面
	second := authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, "")
	if second.Code != http.StatusForbidden {
		t.Fatalf("第二次 setup = %d，想要 403", second.Code)
	}
	after := decodeBody[struct {
		NeedsSetup bool `json:"needs_setup"`
	}](t, authCall(box.server, "GET", "/api/v1/auth/status", "", ""))
	if after.NeedsSetup {
		t.Fatal("setup 完成后 needs_setup 应为 false")
	}
}

// 登录换 JWT；`ver` 一变（改口令）旧令牌立刻失效。
func TestLoginAndVerBumpRevokes(t *testing.T) {
	box := newDummySandboxWith(t, false)
	reg := decodeBody[struct {
		Token string `json:"token"`
	}](t, authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, ""))

	if bad := authCall(box.server, "POST", "/api/v1/auth/login", `{"user":"alice","password":"nope"}`, ""); bad.Code != http.StatusUnauthorized {
		t.Fatalf("错口令 = %d，想要 401", bad.Code)
	}
	login := decodeBody[struct {
		Token string `json:"token"`
	}](t, authCall(box.server, "POST", "/api/v1/auth/login", `{"user":"root","password":"hunter22"}`, ""))
	if login.Token == "" {
		t.Fatal("登录没拿到令牌")
	}

	me := authCall(box.server, "GET", "/api/v1/auth/me", "", login.Token)
	if me.Code != 200 {
		t.Fatalf("me = %d", me.Code)
	}
	who := decodeBody[struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		IsAdmin bool   `json:"is_admin"`
	}](t, me)
	if who.ID != 0 || who.Name != "root" || !who.IsAdmin {
		t.Fatalf("me = %+v", who)
	}

	// 改口令 ⇒ ver+1 ⇒ 旧令牌失效（哪怕是刚签的）
	hash, err := auth.HashPassword("hunter23")
	if err != nil {
		t.Fatal(err)
	}
	if err := box.store.UpdateUserPassword(0, hash); err != nil {
		t.Fatal(err)
	}
	if got := authCall(box.server, "GET", "/api/v1/auth/me", "", login.Token); got.Code != http.StatusUnauthorized {
		t.Fatalf("改口令后旧令牌 = %d，想要 401", got.Code)
	}
	_ = reg
}

// 会话归属：别人的会话按 **404** 处理；列表只见自己的；admin 直取不受限。
func TestSessionOwnershipScoped(t *testing.T) {
	box := newDummySandboxWith(t, false)
	rootTok := decodeBody[struct {
		Token string `json:"token"`
	}](t, authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, "")).Token

	// bob：直接落库（S2 才有 /users 路由），**不是 admin**
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := box.store.CreateUser("bob", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	bobTok, err := auth.Sign([]byte(testJWTKey), bob.ID, bob.IsAdmin, bob.Ver)
	if err != nil {
		t.Fatal(err)
	}

	// root 建一条会话
	created := authCall(box.server, "POST", "/api/v1/sessions", `{}`, rootTok)
	if created.Code != 201 {
		t.Fatalf("root 建会话 = %d（%s）", created.Code, created.Body.String())
	}
	session := decodeBody[struct {
		ID      string `json:"id"`
		OwnerID int64  `json:"owner_id"`
	}](t, created)
	if session.OwnerID != 0 {
		t.Fatalf("owner_id = %d，想要 0（root）", session.OwnerID)
	}

	// bob 看不见它：列表空 + 直取 404（PATCH 也 404）
	list := decodeBody[[]map[string]any](t, authCall(box.server, "GET", "/api/v1/sessions", "", bobTok))
	if len(list) != 0 {
		t.Fatalf("bob 的列表应空，得到 %d 条", len(list))
	}
	if got := authCall(box.server, "GET", "/api/v1/sessions/"+session.ID+"/messages", "", bobTok); got.Code != 404 {
		t.Fatalf("bob 直取 root 的会话 = %d，想要 404", got.Code)
	}
	if got := authCall(box.server, "PATCH", "/api/v1/sessions/"+session.ID, `{"title":"x"}`, bobTok); got.Code != 404 {
		t.Fatalf("bob 改 root 的会话 = %d，想要 404", got.Code)
	}
	// root 自己没问题
	if got := authCall(box.server, "GET", "/api/v1/sessions/"+session.ID+"/messages", "", rootTok); got.Code != 200 {
		t.Fatalf("root 直取自己的会话 = %d，想要 200", got.Code)
	}
}

// 实例级写口只认 admin：bob 403、root 放行。
func TestInstanceWritesAreAdminOnly(t *testing.T) {
	box := newDummySandboxWith(t, false)
	rootTok := decodeBody[struct {
		Token string `json:"token"`
	}](t, authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, "")).Token
	hash, _ := auth.HashPassword("hunter22")
	bob, err := box.store.CreateUser("bob", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	bobTok, err := auth.Sign([]byte(testJWTKey), bob.ID, bob.IsAdmin, bob.Ver)
	if err != nil {
		t.Fatal(err)
	}

	if got := authCall(box.server, "POST", "/api/v1/providers", `{"id":"x","vendor":"custom","base_url":"http://x"}`, bobTok); got.Code != http.StatusForbidden {
		t.Fatalf("bob 建渠道 = %d，想要 403", got.Code)
	}
	if got := authCall(box.server, "POST", "/api/v1/providers", `{"id":"x","vendor":"custom","base_url":"http://x"}`, rootTok); got.Code != 201 {
		t.Fatalf("root 建渠道 = %d（%s）", got.Code, got.Body.String())
	}
}

// **服务未启用 = root 无口令**（DEFINE「用户与鉴权」）：除 status/setup 外全挡 ——
// 连 login 与环回系统令牌也一样（"必须先设密码才能启用服务功能；否则其他用户也无法创建"）。
// 设完口令之后回头就是正常的"要凭据"（无凭据 401、系统令牌 200）。
func TestServiceDisabledUntilRootPassword(t *testing.T) {
	box := newDummySandboxWith(t, false)
	for _, tc := range []struct {
		name, method, path, body, token string
	}{
		{"health 无凭据", "GET", "/api/v1/health", "", ""},
		{"health 系统令牌也不行", "GET", "/api/v1/health", "", testSysToken},
		{"sessions 也不行", "GET", "/api/v1/sessions", "", testSysToken},
		{"login 也不行（服务未启用）", "POST", "/api/v1/auth/login", `{"user":"root","password":"hunter22"}`, ""},
	} {
		got := authCall(box.server, tc.method, tc.path, tc.body, tc.token)
		if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "setup_required") {
			t.Fatalf("%s = %d %s，想要 403 setup_required", tc.name, got.Code, got.Body.String())
		}
	}
	if got := authCall(box.server, "GET", "/api/v1/auth/status", "", ""); got.Code != 200 {
		t.Fatalf("auth/status = %d，想要 200（开放口）", got.Code)
	}
	if got := authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, ""); got.Code != 200 {
		t.Fatalf("setup = %d，想要 200", got.Code)
	}
	// 启用之后：无凭据 401、系统令牌 200、login 可用
	if got := authCall(box.server, "GET", "/api/v1/health", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("启用后 health 无凭据 = %d，想要 401", got.Code)
	}
	if got := authCall(box.server, "GET", "/api/v1/health", "", testSysToken); got.Code != 200 {
		t.Fatalf("启用后 health 系统令牌 = %d，想要 200", got.Code)
	}
	if got := authCall(box.server, "POST", "/api/v1/auth/login", `{"user":"root","password":"hunter22"}`, ""); got.Code != 200 {
		t.Fatalf("启用后 login = %d，想要 200", got.Code)
	}
	// **用户名或 uid 都认**（用户定）：`0` 与 `root` 是同一张票
	if got := authCall(box.server, "POST", "/api/v1/auth/login", `{"user":"0","password":"hunter22"}`, ""); got.Code != 200 {
		t.Fatalf("用 uid 登录 = %d，想要 200（%s）", got.Code, got.Body.String())
	}
}
