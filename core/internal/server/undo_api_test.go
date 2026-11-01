package server

// 撤销压缩的 HTTP 契约（`DELETE /sessions/{session_id}/summaries/{summary_id}`）。
//
// 判定本身在 `compact.PlanUndo`，落库在 `store.UndoSummary` —— 这里只钉**状态码**那一层，
// 因为那是我自己定的口径，改起来没人拦：
//
//	404 —— 这条会话里没这条摘要（"没有"就是"没有"）
//	409 —— 请求没毛病，是**现在这个状态**不允许撤（被上层盖着 / 不是该层最近那次）
//	204 —— 撤掉了，**不回体**
//
// 还有一条顺带钉住的：`GET /summaries` 得把 `level` / `undoable` 带出来 ——
// 界面上那颗按钮亮不亮，全靠这两格。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/model"
)

// summariesOf：`GET /summaries` 的原始返回（含派生字段）。
func (b *dummySandbox) summariesOf(t *testing.T) []model.Summary {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/summaries", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("列摘要该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var summaries []model.Summary
	if err := json.Unmarshal(recorder.Body.Bytes(), &summaries); err != nil {
		t.Fatal(err)
	}
	return summaries
}

// undoableMap：id → undoable（缺席当 false，与前端 `!!s.undoable` 同一个口径）。
func undoableMap(summaries []model.Summary) map[string]bool {
	out := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		if s.Undoable {
			out[s.ID] = true
		}
	}
	return out
}

// record：按真实落库口（`RecordSummary`）落一条摘要 —— 顺带把 overlap / 成员校验也走一遍。
func (b *dummySandbox) record(t *testing.T, summary model.Summary, sourceIDs []string) {
	t.Helper()
	summary.SessionID = b.sessionID
	if summary.Text == "" {
		summary.Text = "梗概"
	}
	if summary.Type == "" {
		summary.Type = model.TypeMessages
	}
	if err := b.store.RecordSummary(summary, sourceIDs); err != nil {
		t.Fatalf("落摘要 %s 失败：%v", summary.ID, err)
	}
}

// twoCompressions：造出"同一层压过两次"的状态 —— AB（早）与 CD（晚）。
func (b *dummySandbox) twoCompressions(t *testing.T) (ab, cd string) {
	t.Helper()
	b.seedTurn(t, "一") // m1, m2
	b.seedTurn(t, "二") // m3, m4
	messages, err := b.store.ListMessages(b.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("该有 4 条消息，实际 %d", len(messages))
	}
	ids := make([]string, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	span := func(from, to string) (*string, *string) { return &from, &to }

	idAB, idCD := "sum-ab", "sum-cd"
	begin, end := span(ids[0], ids[1])
	b.record(t, model.Summary{ID: idAB, Type: model.TypeMessages, BeginMessageID: begin, EndMessageID: end,
		Blocks: 1, Tokens: 10, Provider: "dummy", Model: "dummy", CreatedAt: 100}, []string{ids[0], ids[1]})
	begin, end = span(ids[2], ids[3])
	b.record(t, model.Summary{ID: idCD, Type: model.TypeMessages, BeginMessageID: begin, EndMessageID: end,
		Blocks: 1, Tokens: 10, Provider: "dummy", Model: "dummy", CreatedAt: 200}, []string{ids[2], ids[3]})
	return idAB, idCD
}

// 只允许撤该层最近那一次；404 / 409 / 204 各就各位。
func TestUndoSummaryEndpointStatuses(t *testing.T) {
	b := newDummySandbox(t)
	idAB, idCD := b.twoCompressions(t)

	// GET /summaries 要带 level / undoable：CD 更新 ⇒ 只有它可撤。
	summaries := b.summariesOf(t)
	if undoable := undoableMap(summaries); len(undoable) != 1 || !undoable[idCD] {
		t.Fatalf("该只有 CD 可撤，实际：%v", undoable)
	}
	for _, s := range summaries {
		if s.Level != 1 {
			t.Fatalf("%s 的 level 该是 1（盖消息），实际 %d", s.ID, s.Level)
		}
	}

	// 不是该层最近那次 ⇒ 409（不是 400：请求没毛病，是状态不允许）。
	recorder := call(b.server, "DELETE", b.path+"/summaries/"+idAB, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("撤 AB 该 409，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "层") {
		t.Fatalf("409 的理由该说清是哪条闸拦的（提到层级），实际：%s", recorder.Body.String())
	}

	// 不存在的 ⇒ 404。
	if recorder := call(b.server, "DELETE", b.path+"/summaries/nope", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("撤一条不存在的该 404，得到 %d", recorder.Code)
	}

	// 该层最近那次 ⇒ 204，**不回体**。
	recorder = call(b.server, "DELETE", b.path+"/summaries/"+idCD, "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("撤 CD 该 204，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("204 不该有响应体，实际：%q", recorder.Body.String())
	}

	// 撤完之后 AB 成了该层唯一（也就是最近）⇒ 它变成可撤。
	summaries = b.summariesOf(t)
	if len(summaries) != 1 || summaries[0].ID != idAB {
		t.Fatalf("撤完 CD 该只剩 AB，实际 %+v", summaries)
	}
	if !undoableMap(summaries)[idAB] {
		t.Fatal("CD 撤掉之后 AB 该变成可撤（它现在是该层最近的一次）")
	}

	// 再撤一次同一条 ⇒ 404（已经没了）。
	if recorder := call(b.server, "DELETE", b.path+"/summaries/"+idCD, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("重复撤同一条该 404，得到 %d", recorder.Code)
	}
}

// 被上层盖住的摘要 ⇒ 409，且**上层仍可撤**；撤掉上层之后孩子回到顶层。
func TestUndoSummaryEndpointRefusesCoveredChild(t *testing.T) {
	b := newDummySandbox(t)
	idAB, idCD := b.twoCompressions(t)

	// 合并级：ABCD 盖住 AB+CD（type=summary，成员是那两条摘要）。
	b.record(t, model.Summary{ID: "sum-abcd", Type: model.TypeSummaries,
		Blocks: 2, Tokens: 12, Provider: "dummy", Model: "dummy", CreatedAt: 300},
		[]string{idAB, idCD})

	// 层级：AB/CD 变成第 1 层，ABCD 是第 2 层。
	levels := make(map[string]int)
	for _, s := range b.summariesOf(t) {
		levels[s.ID] = s.Level
	}
	if levels[idAB] != 1 || levels[idCD] != 1 || levels["sum-abcd"] != 2 {
		t.Fatalf("层级不对：%v", levels)
	}

	// 撤被盖住的孩子 ⇒ 409。
	recorder := call(b.server, "DELETE", b.path+"/summaries/"+idAB, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("撤被盖住的 AB 该 409，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "上层") {
		t.Fatalf("该说清是'被上层盖住'，实际：%s", recorder.Body.String())
	}

	// 撤上层 ⇒ 204。
	if recorder := call(b.server, "DELETE", b.path+"/summaries/sum-abcd", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("撤 ABCD 该 204，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	// 孩子回到顶层（parent 空）⇒ 现在它们又能撤了，且只有 CD（较新）可撤。
	summaries := b.summariesOf(t)
	if len(summaries) != 2 {
		t.Fatalf("撤掉上层该剩 2 条，实际 %d", len(summaries))
	}
	for _, s := range summaries {
		if s.ParentSummaryID != nil {
			t.Fatalf("%s 的 parent 该被置空，实际 %q", s.ID, *s.ParentSummaryID)
		}
		if s.Level != 1 {
			t.Fatalf("%s 的 level 该回到 1，实际 %d", s.ID, s.Level)
		}
	}
	if undoable := undoableMap(summaries); len(undoable) != 1 || !undoable[idCD] {
		t.Fatalf("撤掉上层之后该只有 CD 可撤，实际：%v", undoable)
	}
}
