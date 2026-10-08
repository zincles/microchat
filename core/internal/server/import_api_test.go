package server

// `POST /sessions/import-st`：ST JSONL → 建会话 + 按序落库（设置面板导入口）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestImportStBuildsSessionInOrder(t *testing.T) {
	box := newSandbox(t, 0)
	body := strings.Join([]string{
		`{"chat_metadata": {"integrity": "x"}}`,
		`{"name": "User", "is_user": true, "send_date": "2026-10-08T09:46:06.189Z", "mes": "你好。"}`,
		`{"extra": {"reasoning": "问清楚。"}, "name": "Assistant", "is_user": false, "send_date": "2026-10-08T09:48:20.617Z", "mes": "收到，我在。"}`,
		`{"name": "System", "is_system": true, "mes": "旁白"}`,
		"",
	}, "\n")
	recorder := call(box.server, "POST", "/api/v1/sessions/import-st?title=ST", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("该 201，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Session  map[string]any `json:"session"`
		Messages int            `json:"messages"`
		Skipped  int            `json:"skipped"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Messages != 2 || result.Skipped != 2 {
		t.Fatalf("该 2 条 + 跳 3 行：%+v", result)
	}
	sessionID, _ := result.Session["id"].(string)
	recorder = call(box.server, "GET", "/api/v1/sessions/"+sessionID+"/messages", "")
	var messages []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0]["role"] != "user" || messages[1]["role"] != "assistant" {
		t.Fatalf("顺序/角色错：%+v", messages)
	}
	if messages[1]["reasoning"] != "问清楚。" {
		t.Fatalf("assistant 的 reasoning 该落库：%+v", messages[1])
	}
	if messages[0]["content"] != "你好。" {
		t.Fatalf("mes 该原样：%+v", messages[0])
	}
}

func TestImportStRejectsEmpty(t *testing.T) {
	box := newSandbox(t, 0)
	for _, body := range []string{"", "{}\n", `{"mes": "   "}`} {
		recorder := call(box.server, "POST", "/api/v1/sessions/import-st", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%q 该 400，得到 %d", body, recorder.Code)
		}
	}
}
