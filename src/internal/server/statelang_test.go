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

// `POST /api/v1/statelang`：解析 + 计算，给外部工具用。三条路都测：
// 正常（形状与 Rust 版逐字节一致）、缺字段（422）、请求体语法错（400）。
func TestStatelangEndpoint(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := New(st, config.DefaultConfig(), config.Paths{ConfigDir: t.TempDir(), DataDir: t.TempDir()})

	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/statelang", strings.NewReader(body))
		request.Header.Set("content-type", "application/json")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	t.Run("正常：解析与计算分开给", func(t *testing.T) {
		recorder := post(`{"text":"开场。\n<state 玩家状态>心情=疲惫;delete(想法)</state><state>HP = 10</state>"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，body = %s", recorder.Code, recorder.Body.String())
		}
		var view StatelangView
		if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		// 计算：未命名表用空串作键；删掉的 想法 不出现
		if view.Tables["玩家状态"]["心情"] != "疲惫" || view.Tables[""]["HP"] != "10" {
			t.Fatalf("tables = %+v", view.Tables)
		}
		if _, exists := view.Tables["玩家状态"]["想法"]; exists {
			t.Fatalf("删掉的键不该出现：%+v", view.Tables)
		}
		// 解析：读出来的操作**按出现顺序**，删除也在里面（解析 ≠ 计算）
		if len(view.Statements) != 3 {
			t.Fatalf("statements = %+v", view.Statements)
		}
		if view.Statements[0].Kind != "set" || view.Statements[0].Table != "玩家状态" ||
			view.Statements[1].Kind != "delete" || view.Statements[1].Key != "想法" ||
			view.Statements[2].Kind != "set" || view.Statements[2].Table != "" {
			t.Fatalf("顺序/归属不对：%+v", view.Statements)
		}
		// 正文里的块被整块剔除
		if strings.Contains(recorder.Body.String(), "<state") {
			t.Fatalf("标签泄漏进响应：%s", recorder.Body.String())
		}
	})

	t.Run("字段顺序是契约（两版逐字节一致的形状）", func(t *testing.T) {
		recorder := post(`{"text":"<state 玩家状态>心情=疲惫</state>"}`)
		want := `{"tables":{"玩家状态":{"心情":"疲惫"}},` +
			`"statements":[{"kind":"set","table":"玩家状态","key":"心情","value":"疲惫"}],` +
			`"diagnostics":[]}`
		if got := recorder.Body.String(); got != want {
			t.Fatalf("响应形状变了：\n得到 %s\n想要 %s", got, want)
		}
	})

	t.Run("缺 text ⇒ 422", func(t *testing.T) {
		recorder := post(`{}`)
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("状态码 = %d，body = %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"message":"缺 text"`) {
			t.Fatalf("错误体 = %s", recorder.Body.String())
		}
	})

	t.Run("请求体语法错 ⇒ 400", func(t *testing.T) {
		recorder := post(`{not json`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，body = %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"code":"invalid"`) {
			t.Fatalf("错误体 = %s", recorder.Body.String())
		}
	})

	t.Run("错误方法 ⇒ 405（路由在，方法不对）", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/statelang", nil)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
	})
}
