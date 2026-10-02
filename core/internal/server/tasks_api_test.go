package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// /tasks：进程内的任务面板（只读；轮询用，不进库）。
// dummy 的压缩后台跑一小会儿：轮询等到它出现（受理与挂号之间有一步之遥）。
func TestTasksBoardListsRunningFirst(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var board struct {
		Running int `json:"running"`
		Tasks   []struct {
			Kind    string `json:"kind"`
			Outcome string `json:"outcome"`
		} `json:"tasks"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		recorder = call(box.server, "GET", "/api/v1/tasks", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("面板该 200，得到 %d", recorder.Code)
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &board); err != nil {
			t.Fatal(err)
		}
		if len(board.Tasks) > 0 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(board.Tasks) == 0 || board.Tasks[0].Kind != "compact" {
		t.Fatalf("压缩这一条该在面板上：%+v", board)
	}
	done := box.waitCompact(t)
	if done.State != "done" {
		t.Fatalf("压缩该跑完：%+v", done)
	}
}
