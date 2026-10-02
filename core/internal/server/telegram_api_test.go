package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 绑定：写 allowed_id（顶掉旧的）；token 内容永不回显。
func TestTelegramBindWritesAllowedID(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "GET", "/api/v1/config/telegram", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("读该 200，得到 %d", recorder.Code)
	}
	var before telegramView
	_ = json.Unmarshal(recorder.Body.Bytes(), &before)
	if before.AllowedID != 0 {
		t.Fatalf("没绑过该是 0：%+v", before)
	}
	recorder = call(server, "PUT", "/api/v1/config/telegram", `{"allowed_id":12345}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("绑该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var after telegramView
	_ = json.Unmarshal(recorder.Body.Bytes(), &after)
	if after.AllowedID != 12345 {
		t.Fatalf("绑完该是 12345：%+v", after)
	}
	// 非数字 id ⇒ 422（@用户名不行）
	recorder = call(server, "PUT", "/api/v1/config/telegram", `{"allowed_id":0}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("0 该 422，得到 %d", recorder.Code)
	}
}

// 三格各改各的：只给 bot_token 不动 allowed_id；只给 enabled 不动别的；
// 非法的 allowed_id 才 422。token 内容永不回显（只回 has_token）。
func TestTelegramSetFieldsIndependently(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	call(server, "PUT", "/api/v1/config/telegram", `{"allowed_id":111}`)

	recorder := call(server, "PUT", "/api/v1/config/telegram", `{"bot_token":"sk-secret"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("只给 token 该 200：%d %s", recorder.Code, recorder.Body.String())
	}
	var view telegramView
	_ = json.Unmarshal(recorder.Body.Bytes(), &view)
	if view.AllowedID != 111 {
		t.Fatalf("只给 token 不许动 allowed_id：%+v", view)
	}
	if !view.HasToken {
		t.Fatal("给了 token 该报 has_token")
	}
	if strings.Contains(recorder.Body.String(), "sk-secret") {
		t.Fatalf("token 内容永不回显：%s", recorder.Body.String())
	}

	recorder = call(server, "PUT", "/api/v1/config/telegram", `{"enabled":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("只给 enabled 该 200：%d %s", recorder.Code, recorder.Body.String())
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &view)
	if !view.Enabled || !view.HasToken || view.AllowedID != 111 {
		t.Fatalf("enabled 与其余两格互不动：%+v", view)
	}

	recorder = call(server, "PUT", "/api/v1/config/telegram", `{"allowed_id":0}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 allowed_id 该 422：%d", recorder.Code)
	}
}
