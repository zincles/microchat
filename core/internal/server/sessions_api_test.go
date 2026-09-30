package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"microchat/internal/model"
)

// ── `POST /sessions` 的 `title` 与 `GET /sessions` 的 `messages` ──────────────
//
// 两条都是**契约字段真生效**那一类：收下不用（静默忽略）比报错难查得多 —— 都踩过。

// listSessions：真发一次 `GET /sessions`，按 id 找那一项。
func listSessions(t *testing.T, box *sandbox) []model.SessionView {
	t.Helper()
	recorder := call(box.server, "GET", "/api/v1/sessions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var views []model.SessionView
	if err := json.Unmarshal(recorder.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	return views
}

// `POST /sessions {"title": …}` 的标题**真写进库**（不是收下丢掉）：建一条已命名的会话
// （如"重建后的新库"）不必再多发一次 PATCH。不给 title ⇒ 照旧空着（等首条用户消息自动起名）。
func TestCreateSessionTitleTakesEffect(t *testing.T) {
	box := newSandbox(t, 0)
	recorder := call(box.server, "POST", "/api/v1/sessions", `{"title":"重建后的新库"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var created model.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Title != "重建后的新库" {
		t.Fatalf("响应里就该有标题：%+v", created)
	}
	// 库里真的写进去了（响应说谎没意义：列表读的是库）
	for _, view := range listSessions(t, box) {
		if view.ID == created.ID && view.Title != "重建后的新库" {
			t.Fatalf("库里该有标题：%+v", view)
		}
	}
	// 不给 ⇒ 空着（"还没起名"）
	recorder = call(box.server, "POST", "/api/v1/sessions", `{}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var unnamed model.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &unnamed); err != nil {
		t.Fatal(err)
	}
	if unnamed.Title != "" {
		t.Fatalf("不给 title 就该空着：%q", unnamed.Title)
	}
}

// `GET /sessions` 的每一项带**消息条数** `messages` —— 客户端的启动编排据此一眼认出"空会话"
// （以前是每条无标题会话再发一次 `GET /messages` ⇒ 会话一多就是 N 次请求）。
func TestListSessionsCarriesMessageCount(t *testing.T) {
	box := newSandbox(t, 3)
	recorder := call(box.server, "POST", "/api/v1/sessions", `{}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var empty model.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	views := listSessions(t, box)
	counts := map[string]int{}
	for _, view := range views {
		counts[view.ID] = view.Messages
	}
	if counts[box.sessionID] != 3 {
		t.Fatalf("有 3 条消息的会话该报 3：%+v", views)
	}
	if got, ok := counts[empty.ID]; !ok || got != 0 {
		t.Fatalf("新建的空会话该报 0：%v（在不在列表里：%v）", got, ok)
	}
}
