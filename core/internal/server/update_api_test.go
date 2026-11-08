package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 更新是一条能把代码换成另一份的写口：本机浏览器里打开任意网页也能向
// 127.0.0.1 发请求，Origin 是唯一能区分“我们的页面”和“别人借你的浏览器”的东西。
func TestUpdatePullRejectsCrossOrigin(t *testing.T) {
	box := newDummySandbox(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/update/pull", nil)
	req.Host = "127.0.0.1:8788"
	req.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	box.server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("跨源更新该 403，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Origin") {
		t.Fatalf("403 没说清是 Origin 不对：%s", recorder.Body.String())
	}
}

// 宿主二进制没有 entrypoint 看家循环：不要假装拉完就会自己回来。
func TestUpdatePullRequiresContainerRestart(t *testing.T) {
	box := newDummySandbox(t)
	t.Setenv("MICROCHAT_RESTART_FILE", "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/update/pull", nil)
	req.Host = "127.0.0.1:8788"
	req.Header.Set("Origin", "http://127.0.0.1:8788")
	recorder := httptest.NewRecorder()
	box.server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("没有 entrypoint 时该 409，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "MICROCHAT_RESTART_FILE") {
		t.Fatalf("409 没说清缺了什么：%s", recorder.Body.String())
	}
}
