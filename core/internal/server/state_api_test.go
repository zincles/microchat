package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"microchat/internal/state"
)

// ── `GET /sessions/{session_id}/state`（+ `?at_idx=N`）────────────────────────
//
// 三件事钉在这里：
//   - 层名是 `baseline`（**不是** `global` —— 那现在是**表名**，同一个 JSON 里不许两义）；
//   - `tables` 里**恒有**那张叫 `global` 的表（一个变量都没有也回 `{}`）；
//   - `at_idx=N`：**底子永远用当前的生效提示词**，正文只 fold 到第 N 条（含）。

// stateView：拉一次状态（`query` 直接拼在路径后面，例如 `?at_idx=2`）。
func (b *dummySandbox) stateView(t *testing.T, query string) state.View {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/state"+query, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /state%s 该 200，得到 %d：%s", query, recorder.Code, recorder.Body.String())
	}
	var view state.View
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

// 层名：响应里只有 `session` / `effective` / `tables` —— 系统提示词的块一律无视，
// `baseline` / `baseline_values` 已死；顶层也没有 `global` / `global_values`。
func TestStateResponseUsesBaselineLayerName(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "开场")

	recorder := call(box.server, "GET", box.path+"/state", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", recorder.Code, recorder.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session", "effective", "tables"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("缺字段 %q：%s", name, recorder.Body.String())
		}
	}
	for _, gone := range []string{"global", "global_values", "baseline", "baseline_values"} {
		if _, ok := fields[gone]; ok {
			t.Fatalf("`%s` 不该再是层名（`global` 只许是 tables 里那张表）：%s", gone, recorder.Body.String())
		}
	}
	if _, ok := box.stateView(t, "").Tables["global"]; !ok {
		t.Fatalf("tables 里该有那张叫 global 的表")
	}
}

// 空会话：`tables` 里**有** `global`（空的那张），`effective` 是空对象而不是 null。
//
// 拿掉"global 恒在"这条例外，这条当场红。
func TestEmptySessionStateStillHasGlobalTable(t *testing.T) {
	box := newDummySandbox(t)
	view := box.stateView(t, "")

	table, ok := view.Tables["global"]
	if !ok || table == nil || len(table) != 0 {
		t.Fatalf("空会话该回 {\"global\": {}}：%+v", view.Tables)
	}
	if view.Effective == nil {
		t.Fatalf("effective 永不给 null：%+v", view.Effective)
	}
	if _, ok := view.Tables[""]; ok {
		t.Fatalf("空串不再是表的键：%+v", view.Tables)
	}
}

// `<state>` 与 `<state global>` 在**同一条会话**里也对：同一张表、后写覆盖先写。
func TestSessionStateTreatsUnnamedAndGlobalAsOneTable(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "<state>A = 1</state><state global>A = 2</state>")
	box.seedTurn(t, "<state global>B = 3</state>")

	view := box.stateView(t, "")
	if len(view.Tables) != 1 || view.Tables["global"]["A"] != "2" {
		t.Fatalf("两种写法该是同一张表、后写覆盖先写：%+v", view.Tables)
	}
	if view.Tables["global"]["B"] != "3" {
		t.Fatalf("global 那张表该带着后来的键：%+v", view.Tables)
	}
}

// `at_idx`：三档关系（`2` / `6` / 不给参数）—— 第 2 条里写的可见、后面写的看不见。
//
// 造 3 轮（6 条：用户 1/3/5、助手 2/4/6），每一轮的用户话里写一个变量。
func TestStateAtIndexFoldsOnlyUpToThatMessage(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "<state>第一 = 有</state>")
	box.seedTurn(t, "<state>第二 = 有</state>")
	box.seedTurn(t, "<state>第三 = 有</state>")
	if got := len(box.messages(t)); got != 6 {
		t.Fatalf("该有 6 条消息，得到 %d", got)
	}

	two := box.stateView(t, "?at_idx=2")
	six := box.stateView(t, "?at_idx=6")
	now := box.stateView(t, "")

	if two.Tables["global"]["第一"] != "有" {
		t.Fatalf("第 1 条带来的变量在 at_idx=2 该看得见：%+v", two.Tables)
	}
	if _, ok := two.Tables["global"]["第二"]; ok {
		t.Fatalf("第 3 条带来的变量在 at_idx=2 不该看得见：%+v", two.Tables)
	}
	if len(two.Session) != 2 {
		t.Fatalf("只该 fold 前两条的操作：%+v", two.Session)
	}
	// 越界与"到最后一条"一致，也与不给参数一致（三档里那两档重合）
	// （每轮两条消息都带同一个块 ⇒ 一共 6 条操作）
	for name, view := range map[string]state.View{"at_idx=6": six, "不给参数": now} {
		if view.Tables["global"]["第三"] != "有" || view.Tables["global"]["第二"] != "有" {
			t.Fatalf("%s 该看得见全部三档：%+v", name, view.Tables)
		}
		if len(view.Session) != 6 {
			t.Fatalf("%s 该 fold 全部六条操作：%+v", name, view.Session)
		}
	}
	// at_idx=3 ⇒ 恰好加上第 3 条（第二轮那句用户消息）带来的东西
	if three := box.stateView(t, "?at_idx=3"); three.Tables["global"]["第二"] != "有" ||
		len(three.Session) != 3 || three.Tables["global"]["第三"] == "有" {
		t.Fatalf("at_idx=3 该含第 3 条、不含后面的：%+v", three.Tables)
	}
	over := box.stateView(t, "?at_idx=99")
	if len(over.Session) != len(now.Session) {
		t.Fatalf("越界当作到最后一条：%+v vs %+v", over.Session, now.Session)
	}

	// at_idx=0 ⇒ 空（系统提示词的块无视，正文一条不 fold）
	zero := box.stateView(t, "?at_idx=0")
	if len(zero.Session) != 0 || len(zero.Tables["global"]) != 0 {
		t.Fatalf("at_idx=0 该是空的：%+v", zero)
	}
}

// 参数层的错 ⇒ 400（固定错误体）；`-1`（负）/ `abc`（非整数）/ 空 ⇒ 都不是"当前状态"。
func TestStateAtIndexRejectsBadValues(t *testing.T) {
	box := newDummySandbox(t)
	for _, query := range []string{"?at_idx=-1", "?at_idx=abc", "?at_idx=1.5", "?at_idx="} {
		recorder := call(box.server, "GET", box.path+"/state"+query, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 该 400，得到 %d：%s", query, recorder.Code, recorder.Body.String())
		}
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.Error.Code != "invalid" || failure.Error.Message == "" {
			t.Fatalf("%s 的错误体不合契约：%s", query, recorder.Body.String())
		}
	}
}
