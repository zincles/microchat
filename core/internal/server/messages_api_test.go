package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"

	_ "modernc.org/sqlite"
)

// sandbox：一条会话 + 若干消息（id 是 UUIDv7 的形状 ⇒ 字符串序 = 先后）。
//
// `POST /messages` 还没搬（chat 那一波）⇒ 消息只能直接落库；`store.Store` 没开这个口子
// （它是"唯一碰 SQL"的生产代码，不许为测试开口），于是**测试自己开一个连接**。
type sandbox struct {
	server    *Server
	db        *sql.DB
	sessionID string
	messages  []string
}

func newSandbox(t *testing.T, count int) *sandbox {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/microchat.db"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session, err := st.CreateSession("dummy", "dummy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	box := &sandbox{db: db, sessionID: session.ID}
	box.server = newTestServer(st, config.DefaultConfig(), config.Paths{ConfigDir: dir, DataDir: dir})
	for index := range count {
		id := messageID(index + 1)
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		if _, err := db.Exec(
			`INSERT INTO messages (id, session_id, role, content, created_at, updated_at) VALUES (?1, ?2, ?3, ?4, ?5, ?5)`,
			id, session.ID, role, fmt.Sprintf("第 %d 条", index+1), int64(index+1)); err != nil {
			t.Fatal(err)
		}
		box.messages = append(box.messages, id)
	}
	return box
}

// seedSummary：直接落一份"盖住某段"的摘要（压缩模块还没建，没有别的生产路径）。
func (b *sandbox) seedSummary(t *testing.T, id, begin, end string) {
	t.Helper()
	if _, err := b.db.Exec(
		`INSERT INTO summaries
		   (id, session_id, parent_summary_id, source_kind, begin_message_id, end_message_id,
		    text, blocks, tokens, source_ids, provider, model, prompt_version, created_at)
		 VALUES (?1, ?2, NULL, 'message', ?3, ?4, '梗概', 1, 10, '[]', 'dummy', 'dummy', 1, 1)`,
		id, b.sessionID, begin, end); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("UPDATE messages SET summary_id = ?1 WHERE id = ?2", id, begin); err != nil {
		t.Fatal(err)
	}
}

func messageID(n int) string { return fmt.Sprintf("01a00000-0000-7000-8000-%012d", n) }

func summaryID(n int) string { return fmt.Sprintf("01b00000-0000-7000-8000-%012d", n) }

// 删除预览：**只算不动**（GET 完消息一条不少），五组 id 的形状按 DEFINE.md。
func TestDeletionPreviewOnlyCounts(t *testing.T) {
	box := newSandbox(t, 5)
	recorder := call(box.server, "GET",
		"/api/v1/sessions/"+box.sessionID+"/messages/"+box.messages[2]+"/deletion-preview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	want := `{"deleted_message_ids":["` + box.messages[2] + `","` + box.messages[3] + `","` + box.messages[4] +
		`"],"deleted_summary_ids":[],"unlinked_message_ids":[],"unlinked_summary_ids":[],` +
		`"last_deleted_message_id":"` + box.messages[4] + `"}`
	if got := recorder.Body.String(); got != want {
		t.Fatalf("形状变了：\n得到 %s\n想要 %s", got, want)
	}
	// 预览不许动数据
	after := call(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/messages", "")
	if !containsAll(after.Body.String(), box.messages[0], box.messages[4]) {
		t.Fatalf("预览把消息删了：%s", after.Body.String())
	}
	// 消息不在这条会话里 ⇒ 404（不是 500，也不是静默删别的）
	other := call(box.server, "GET",
		"/api/v1/sessions/"+box.sessionID+"/messages/01a00000-0000-7000-8000-0000000000ff/deletion-preview", "")
	if other.Code != http.StatusNotFound {
		t.Fatalf("不存在的消息该 404：%d", other.Code)
	}
}

// DELETE：核对 `last_deleted_message_id` —— 不符 409（什么都不删），对了才照计划删。
func TestDeleteMessageChecksTheLastID(t *testing.T) {
	box := newSandbox(t, 4)
	path := "/api/v1/sessions/" + box.sessionID + "/messages/" + box.messages[1]

	// 不带核对字段 ⇒ 422（必填）
	if recorder := call(box.server, "DELETE", path, `{}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺核对字段该 422：%d %s", recorder.Code, recorder.Body.String())
	}
	// 核对不符（末尾已经换了）⇒ 409，且**一个字节都不动**
	stale := `{"last_deleted_message_id":"` + box.messages[2] + `"}`
	conflict := call(box.server, "DELETE", path, stale)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("核对不符该 409：%d %s", conflict.Code, conflict.Body.String())
	}
	if body := call(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/messages", "").Body.String(); !containsAll(body, box.messages[0], box.messages[3]) {
		t.Fatalf("409 之后消息该原样：%s", body)
	}
	// 核对对了 ⇒ 删这条及之后
	ok := call(box.server, "DELETE", path, `{"last_deleted_message_id":"`+box.messages[3]+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("该 200：%d %s", ok.Code, ok.Body.String())
	}
	rest := call(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/messages", "")
	if containsAll(rest.Body.String(), box.messages[1]) || !containsAll(rest.Body.String(), box.messages[0]) {
		t.Fatalf("该只剩第一条：%s", rest.Body.String())
	}
}

// 删这条及之后 ⇒ 覆盖区间与后缀相交的摘要一起走，幸存者里指向死摘要的指针被置空。
func TestDeleteMessageCascadesSummaries(t *testing.T) {
	box := newSandbox(t, 4)
	// 一份盖住 m1..m2 的摘要（它的右端落在后缀里 ⇒ 死），m1 是幸存者 ⇒ 指针要被置空
	box.seedSummary(t, summaryID(1), box.messages[0], box.messages[1])
	path := "/api/v1/sessions/" + box.sessionID + "/messages/" + box.messages[1]
	preview := call(box.server, "GET", path+"/deletion-preview", "")
	if preview.Code != http.StatusOK {
		t.Fatalf("%d：%s", preview.Code, preview.Body.String())
	}
	if !containsAll(preview.Body.String(), summaryID(1), `"unlinked_message_ids":["`+box.messages[0]+`"]`) {
		t.Fatalf("盖到后缀上的摘要该一起死、幸存的 m1 该被解链：%s", preview.Body.String())
	}
	if done := call(box.server, "DELETE", path,
		`{"last_deleted_message_id":"`+box.messages[3]+`"}`); done.Code != http.StatusOK {
		t.Fatalf("该 200：%d %s", done.Code, done.Body.String())
	}
	// 摘要没了（剩下的是唯一那条消息，指针也空了）
	var summaryCount int
	if err := box.db.QueryRow("SELECT count(*) FROM summaries WHERE id = ?1", summaryID(1)).Scan(&summaryCount); err != nil {
		t.Fatal(err)
	}
	if summaryCount != 0 {
		t.Fatalf("相交的摘要该被删掉：%d", summaryCount)
	}
	var summaryIDOfFirst sql.NullString
	if err := box.db.QueryRow("SELECT summary_id FROM messages WHERE id = ?1", box.messages[0]).Scan(&summaryIDOfFirst); err != nil {
		t.Fatal(err)
	}
	if summaryIDOfFirst.Valid {
		t.Fatalf("幸存者的指针该被置空：%v", summaryIDOfFirst.String)
	}
	// 幸存的那条还在
	if body := call(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/messages", "").Body.String(); !containsAll(body, box.messages[0]) {
		t.Fatalf("m1 该活着：%s", body)
	}
}

// Copy 走通了整条路：新 session id、消息条数一致、消息与摘要都换了 id。
func TestCopySessionRoute(t *testing.T) {
	box := newSandbox(t, 3)
	box.seedSummary(t, summaryID(1), box.messages[0], box.messages[0])
	recorder := call(box.server, "POST", "/api/v1/sessions/"+box.sessionID+"/copy", "")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var copied struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	if copied.ID == "" || copied.ID == box.sessionID {
		t.Fatalf("该回新会话 id：%+v", copied)
	}
	body := call(box.server, "GET", "/api/v1/sessions/"+copied.ID+"/messages", "").Body.String()
	if !containsAll(body, "第 1 条", "第 3 条") {
		t.Fatalf("副本该有同样的三条：%s", body)
	}
	if containsAll(body, box.messages[0]) {
		t.Fatalf("副本的消息该铸新 id：%s", body)
	}
	// 摘要也跟着复制：新 id、区间指到新的消息上、消息的指针指到新摘要上
	var copiedBegin, copiedSummaryID string
	if err := box.db.QueryRow(
		"SELECT begin_message_id, id FROM summaries WHERE session_id = ?1",
		copied.ID).Scan(&copiedBegin, &copiedSummaryID); err != nil {
		t.Fatalf("摘要该被复制出来：%v", err)
	}
	if copiedBegin == box.messages[0] {
		t.Fatalf("副本的区间该指到副本自己的消息上：%s", copiedBegin)
	}
	var summaryIDOfFirst sql.NullString
	if err := box.db.QueryRow("SELECT summary_id FROM messages WHERE id = ?1", copiedBegin).Scan(&summaryIDOfFirst); err != nil {
		t.Fatal(err)
	}
	if !summaryIDOfFirst.Valid || summaryIDOfFirst.String != copiedSummaryID {
		t.Fatalf("副本里的指针该指到副本自己的摘要上：%v ≠ %s", summaryIDOfFirst, copiedSummaryID)
	}
	// 原会话一条不少
	if original := call(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/messages", "").Body.String(); !containsAll(
		original, box.messages[0], box.messages[2]) {
		t.Fatalf("原会话该原样：%s", original)
	}
	// 不存在的会话 ⇒ 404
	if missing := call(box.server, "POST", "/api/v1/sessions/"+messageID(9)+"/copy", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话该 404：%d", missing.Code)
	}
}

// 树那套路由**没了**：`/branches` 与 `/siblings` 都 404。
func TestTreeRoutesAreGone(t *testing.T) {
	box := newSandbox(t, 2)
	if recorder := call(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/branches", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("/branches 该 404：%d %s", recorder.Code, recorder.Body.String())
	}
	siblings := "/api/v1/sessions/" + box.sessionID + "/messages/" + box.messages[0] + "/siblings"
	if recorder := call(box.server, "DELETE", siblings, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("/siblings 该 404：%d %s", recorder.Code, recorder.Body.String())
	}
}

func containsAll(body string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			return false
		}
	}
	return true
}

// ── 序号 `idx`（**派生**、不落库）与三个查询参数 ──────────────────────────
//
// `idx` = 这条会话里"按 `id` 排第几条"（1-based；`0` 是合成的系统提示词，不是消息）。
// 它**不进库**：没有那一列、也没有迁移 —— 存它就是第二个真相来源。

// messagesPath：这条会话的消息路由（带查询串）。
func (b *sandbox) messagesPath(query string) string {
	return "/api/v1/sessions/" + b.sessionID + "/messages" + query
}

// list：拉一次消息列表（GET，必须 200）。
func (b *sandbox) list(t *testing.T, query string) []model.Message {
	t.Helper()
	recorder := call(b.server, "GET", b.messagesPath(query), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET messages%s 该 200，得到 %d：%s", query, recorder.Code, recorder.Body.String())
	}
	var messages []model.Message
	if err := json.Unmarshal(recorder.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

// appendMessage：往尾巴上直接落一条（沙盒的 id 是 UUIDv7 的形状 ⇒ 字符串序 = 先后）。
func (b *sandbox) appendMessage(t *testing.T, n int) {
	t.Helper()
	if _, err := b.db.Exec(
		`INSERT INTO messages (id, session_id, role, content, created_at, updated_at) VALUES (?1, ?2, 'user', ?3, ?4, ?4)`,
		messageID(n), b.sessionID, fmt.Sprintf("第 %d 条", n), int64(n)); err != nil {
		t.Fatal(err)
	}
}

// assertIndexes：核一次结果：条数对、每条的 `idx` 就是期望的那个数，**且 idx 与 id 配得上**
// （序号 = "按 id 数第几条" ⇒ 沙盒里 id 是 `messageID(n)` ⇒ 两者必须一致；窗口切错 / 重排都会露馅）。
func assertIndexes(t *testing.T, messages []model.Message, want ...int) {
	t.Helper()
	if len(messages) != len(want) {
		t.Fatalf("该 %d 条，得到 %d：%+v", len(want), len(messages), messages)
	}
	for index := range messages {
		if messages[index].Idx != want[index] {
			t.Fatalf("第 %d 项该是 idx %d，得到 %d", index+1, want[index], messages[index].Idx)
		}
		if messages[index].ID != messageID(want[index]) {
			t.Fatalf("idx %d 该指的是 %s，得到 %s（序号与顺序脱节了）",
				want[index], messageID(want[index]), messages[index].ID)
		}
	}
}

// 序号是**派生**的：1..N；**编辑不改**、**删尾部也不重排**（只删后缀 ⇒ 不留洞 ⇒ 号永不改变）。
func TestMessageIndexIsDerivedAndSurvivesEditsAndDeletions(t *testing.T) {
	box := newSandbox(t, 4)
	// 每条都带 `idx`，而且它**真的出现在 JSON 里**（`omitempty` ⇒ 0 就不带这一格）
	body := call(box.server, "GET", box.messagesPath(""), "").Body.String()
	if !strings.Contains(body, `"idx":1`) || !strings.Contains(body, `"idx":4`) {
		t.Fatalf("消息该带 idx（序号）：%s", body)
	}
	assertIndexes(t, box.list(t, ""), 1, 2, 3, 4)

	// 编辑一条：就地换正文、id 不变 ⇒ 序号一个都不动
	recorder := call(box.server, "PATCH", box.messagesPath("")+"/"+box.messages[1], `{"content":"改过了"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("编辑该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	assertIndexes(t, box.list(t, ""), 1, 2, 3, 4)

	// 删掉**尾部两条**（既有删除路由）⇒ 剩下的两条**仍然是 1、2**（没有重排 —— 最关键的一条）
	path := box.messagesPath("") + "/" + box.messages[2]
	done := call(box.server, "DELETE", path,
		`{"last_deleted_message_id":"`+box.messages[3]+`"}`)
	if done.Code != http.StatusOK {
		t.Fatalf("删除该 200，得到 %d：%s", done.Code, done.Body.String())
	}
	assertIndexes(t, box.list(t, ""), 1, 2)

	// 再往尾巴上追加 ⇒ 它是**第 3 条**：号是"位置"，不是 id 里的那个数（新条铸的是新 id）
	box.appendMessage(t, 5)
	appended := box.list(t, "")
	assertIndexes(t, appended[:2], 1, 2)
	if len(appended) != 3 || appended[2].Idx != 3 || appended[2].ID != messageID(5) {
		t.Fatalf("新条该是第 3 条：%+v", appended)
	}
}

// 序号跟着 **id** 走，不是插入顺序（也不是 rowid）：乱序插进去，`idx` 仍按 id 数。
// （把 `allMessages` 的 `ORDER BY id` 拿掉、改成按 rowid 出来，这条就该红。）
func TestMessageIndexFollowsIDOrderNotInsertOrder(t *testing.T) {
	box := newSandbox(t, 0)
	for _, n := range []int{3, 1, 4, 2} { // 插入顺序与 id 的顺序**故意不一样**
		box.appendMessage(t, n)
	}
	messages := box.list(t, "")
	if len(messages) != 4 {
		t.Fatalf("该 4 条，得到 %d", len(messages))
	}
	for index, n := range []int{1, 2, 3, 4} {
		if messages[index].ID != messageID(n) || messages[index].Idx != n {
			t.Fatalf("id 排第 %d 的那个该是 messageID(%d)/idx %d，得到 %s/idx %d",
				index+1, n, n, messages[index].ID, messages[index].Idx)
		}
	}
}

// 三个查询参数：闭区间（含两端）/ 取尾 / 缺一端补默认 / 越界给现有的（**不算错**）/ 参数错 ⇒ 400。
func TestMessageWindowQueries(t *testing.T) {
	box := newSandbox(t, 6)

	// 不给参数 = 全部（语义不变）
	assertIndexes(t, box.list(t, ""), 1, 2, 3, 4, 5, 6)

	// 区间：闭区间 ⇒ 3、4、5
	assertIndexes(t, box.list(t, "?from_idx=3&to_idx=5"), 3, 4, 5)
	// 取尾 2 条 ⇒ 5、6
	assertIndexes(t, box.list(t, "?last=2"), 5, 6)
	// 只给一端 ⇒ 另一端补默认（起点 1 / 末尾）：说得出口的"第 5 条往后"与"前 3 条"
	assertIndexes(t, box.list(t, "?from_idx=5"), 5, 6)
	assertIndexes(t, box.list(t, "?to_idx=3"), 1, 2, 3)

	// 越界**不算错**：给现有的那几条（分页的常见语义）
	assertIndexes(t, box.list(t, "?from_idx=4&to_idx=99"), 4, 5, 6)
	assertIndexes(t, box.list(t, "?last=99"), 1, 2, 3, 4, 5, 6)
	// 起点落在末尾之后 ⇒ **空数组**（不是 404、也不是 null）
	if body := call(box.server, "GET", box.messagesPath("?from_idx=99"), "").Body.String(); body != "[]" {
		t.Fatalf("起点越界该回空数组：%s", body)
	}
	// 响应仍是**数组**（没换成对象 —— 免得破坏现有客户端）
	if body := call(box.server, "GET", box.messagesPath("?last=1"), "").Body.String(); !strings.HasPrefix(body, "[") {
		t.Fatalf("响应该是数组：%s", body)
	}

	// 参数错一律 400（错误体固定：`code = invalid`）
	for _, query := range []string{
		"?last=2&from_idx=1",   // 混用
		"?last=2&to_idx=3",     // 混用
		"?from_idx=0",          // 非正数
		"?to_idx=-1",           // 非正数
		"?last=0",              // 非正数
		"?from_idx=5&to_idx=3", // 反了
		"?last=abc",            // 不是整数
		"?from_idx=x",
	} {
		recorder := call(box.server, "GET", box.messagesPath(query), "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 该 400，得到 %d：%s", query, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"code":"invalid"`) {
			t.Fatalf("%s 的错误体不合契约：%s", query, recorder.Body.String())
		}
	}
}
