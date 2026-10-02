package server

import (
	"encoding/json"
	"net/http"
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
