package server

// 测试公用的小工具（2026-11-01 从 `auth_api_test.go` 挪过来 —— 那个文件随鉴权一起删了，
// 但 `decodeBody` 与 `authCall` 是**别的测试都在用**的，得留在这儿）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// authCall：发一个请求（带 JSON 内容类型）。`token` 现在**没有用了** —— 鉴权已砍
// （2026-11-01），留着这个参数只为不惊动那 90 多处调用点；新写的测试直接用 `call` 就行。
func authCall(server *Server, method, path, body, token string) *httptest.ResponseRecorder {
	_ = token
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// decodeBody：把响应体解成 T（解不动就当场炸，并把原文打出来）。
func decodeBody[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("解响应失败：%v（%s）", err, recorder.Body.String())
	}
	return out
}
