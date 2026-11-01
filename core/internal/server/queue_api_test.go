package server

// 队列任务的 HTTP 契约（2026-11-01 用户定）。
//
//	POST /tasks/queues                     排一个队列（两种给法二选一）
//	POST /tasks/queues/{queue_id}/cancel   取消
//
// 串行 / 一步失败即整体结束 / 掐断在飞的那一发 —— 那三条在 `internal/queue` 自己的测试里；
// 这里只钉**接口那一层**：形状、状态码、面板看得到、取消回得来。

import (
	"encoding/json"
	"net/http"
	"testing"

	"microchat/internal/queue"
	"microchat/internal/task"
)

// boardOf：`GET /tasks` 的原始返回（含队列那一格）。
func (b *dummySandbox) boardOf(t *testing.T) taskBoardView {
	t.Helper()
	recorder := call(b.server, "GET", "/api/v1/tasks", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("读任务面板该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var view taskBoardView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

// 用户那个例子：「这条对话连压 100 次，每次 10 段」—— 一次请求排成 100 步。
func TestEnqueueQueueWithTimes(t *testing.T) {
	b := newDummySandbox(t)
	body := `{"session_id":"` + b.sessionID + `","blocks":10,"times":100,"title":"压 100 次"}`
	recorder := call(b.server, "POST", "/api/v1/tasks/queues", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("排队该 201，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var created queue.Queue
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Total != 100 {
		t.Fatalf("该排成 100 步，实际 %d", created.Total)
	}
	if created.Kind != queue.KindCompact || created.Group != task.GroupLLM {
		t.Fatalf("压缩队列该是 compact / llm，实际 %q / %q", created.Kind, created.Group)
	}

	// 面板里看得到它（队列只有 admin 看得见；这里用的就是系统身份 = admin）。
	view := b.boardOf(t)
	found := false
	for _, q := range view.Queues {
		if q.ID == created.ID {
			found = true
			if q.Total != 100 {
				t.Fatalf("面板里那个队列的 Total 该是 100，实际 %d", q.Total)
			}
		}
	}
	if !found {
		t.Fatalf("面板里没有刚排的队列：%+v", view.Queues)
	}
}

// `steps` 那一种形状：显式步骤列表（跨会话也走它）。
func TestEnqueueQueueWithSteps(t *testing.T) {
	b := newDummySandbox(t)
	other, err := b.store.CreateSession(0, "dummy", "dummy", "", "乙")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"steps":[{"session_id":"` + b.sessionID + `","blocks":3},{"session_id":"` + other.ID + `","blocks":3}]}`
	recorder := call(b.server, "POST", "/api/v1/tasks/queues", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("排队该 201，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var created queue.Queue
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Total != 2 {
		t.Fatalf("该排成 2 步，实际 %d", created.Total)
	}
}

// 两种形状都给 ⇒ 400（不猜）；一个都不给 ⇒ 400；会话不存在 / times 不是正数 ⇒ 400。
func TestEnqueueQueueRejectsBadShapes(t *testing.T) {
	b := newDummySandbox(t)
	cases := []struct {
		name string
		body string
	}{
		{"两种都给", `{"steps":[{"session_id":"` + b.sessionID + `"}],"session_id":"` + b.sessionID + `","times":3}`},
		{"一个都不给", `{}`},
		{"会话不存在", `{"session_id":"nope","blocks":3,"times":2}`},
		{"times 是 0", `{"session_id":"` + b.sessionID + `","blocks":3,"times":0}`},
		{"空 steps", `{"steps":[]}`},
	}
	for _, c := range cases {
		recorder := call(b.server, "POST", "/api/v1/tasks/queues", c.body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s：该 400，得到 %d：%s", c.name, recorder.Code, recorder.Body.String())
		}
	}
}

// 取消：幂等；不存在的 ⇒ 404。
func TestCancelQueueEndpoint(t *testing.T) {
	b := newDummySandbox(t)
	body := `{"session_id":"` + b.sessionID + `","blocks":3,"times":5}`
	recorder := call(b.server, "POST", "/api/v1/tasks/queues", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("排队该 201，得到 %d", recorder.Code)
	}
	var created queue.Queue
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	recorder = call(b.server, "POST", "/api/v1/tasks/queues/"+created.ID+"/cancel", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("取消该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var canceled queue.Queue
	if err := json.Unmarshal(recorder.Body.Bytes(), &canceled); err != nil {
		t.Fatal(err)
	}
	// 结局只该是"取消"或（已经跑完了的）"成功" —— 绝不能是"进行中"。
	if canceled.Running() {
		t.Fatalf("取消之后不该还在跑：%+v", canceled)
	}
	// 幂等：再来一次还是 200
	if recorder := call(b.server, "POST", "/api/v1/tasks/queues/"+created.ID+"/cancel", ""); recorder.Code != http.StatusOK {
		t.Fatalf("重复取消该 200，得到 %d", recorder.Code)
	}
	// 不存在的 ⇒ 404
	if recorder := call(b.server, "POST", "/api/v1/tasks/queues/nope/cancel", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("取消不存在的队列该 404，得到 %d", recorder.Code)
	}
}
