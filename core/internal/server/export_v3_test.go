package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"microchat/internal/model"
	"microchat/internal/store"
)

// seedPyramid：造一条"压过两段、又把两段合并成一层"的会话 —— 正好三层金字塔的最底层与第二层。
//
// 消息 4 条（两轮），叶摘要 A 盖 1-2、B 盖 3-4，合并摘要 M 盖 A+B（⇒ A、B 的 parent 指向 M）。
// 用真路径 `RecordSummary` 落库（不是手写行）：这样测的是**库里真会发生的样子**。
func (b *dummySandbox) seedPyramid(t *testing.T) []model.Message {
	t.Helper()
	b.seedTurn(t, "开场")
	b.seedTurn(t, "继续")
	messages, err := b.store.ListMessages(b.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.MintOrderedIDs(3)
	if err != nil {
		t.Fatal(err)
	}
	// ⚠ 区间要**自己填**：`RecordSummary` 不替调用方算 `begin/end`（那是压缩那一步的事，
	// 见 compact.go 里 `BeginMessageID: &beginID` 那几行）。这里照真实形状填。
	leaf := func(id string, first, last int, text string) model.Summary {
		beginID, endID := messages[first-1].ID, messages[last-1].ID
		return model.Summary{
			ID: id, SessionID: b.sessionID, Type: model.TypeMessages,
			BeginMessageID: &beginID, EndMessageID: &endID,
			Text: text, Blocks: 1, Tokens: 40, Provider: "dummy", Model: "dummy",
			PromptVersion: 1, Dirty: true,
		}
	}
	if err := b.store.RecordSummary(leaf(ids[0], 1, 2, "第一段的梗概"),
		[]string{messages[0].ID, messages[1].ID}); err != nil {
		t.Fatal(err)
	}
	if err := b.store.RecordSummary(leaf(ids[1], 3, 4, "第二段的梗概"),
		[]string{messages[2].ID, messages[3].ID}); err != nil {
		t.Fatal(err)
	}
	mergedBegin, mergedEnd := messages[0].ID, messages[3].ID
	if err := b.store.RecordSummary(model.Summary{
		ID: ids[2], SessionID: b.sessionID, Type: model.TypeSummaries,
		BeginMessageID: &mergedBegin, EndMessageID: &mergedEnd,
		Text: "两段并起来的梗概", Blocks: 2, Tokens: 80, Provider: "dummy", Model: "dummy",
		PromptVersion: 1,
	}, []string{ids[0], ids[1]}); err != nil {
		t.Fatal(err)
	}
	return messages
}

// v3 导出：**整张金字塔都在** —— 三条摘要、父子关系、区间（1-based 消息序号）、
// 以及"当初那次压缩的账"（blocks/tokens/dirty）；消息仍然一条不少、正文原样。
func TestExportV3CarriesTheWholePyramid(t *testing.T) {
	box := newDummySandboxWith(t, true)
	messages := box.seedPyramid(t)

	exported := authCall(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/export", "", testSysToken)
	if exported.Code != http.StatusOK {
		t.Fatalf("导出 = %d（%s）", exported.Code, exported.Body.String())
	}
	var doc struct {
		Version  int `json:"version"`
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"messages"`
		Summaries []struct {
			Key       string  `json:"key"`
			ParentKey *string `json:"parent_key"`
			Type      string  `json:"type"`
			BeginIdx  *int    `json:"begin_idx"`
			EndIdx    *int    `json:"end_idx"`
			Text      string  `json:"text"`
			Blocks    int64   `json:"blocks"`
			Tokens    int64   `json:"tokens"`
			Dirty     bool    `json:"dirty"`
		} `json:"summaries"`
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 3 {
		t.Fatalf("v3 的 version 该是 3，得到 %d", doc.Version)
	}
	// **被压过的消息一条都不能少**（压缩在库里只写 summary_id，不动正文）。
	if len(doc.Messages) != len(messages) {
		t.Fatalf("导出该带全部 %d 条消息（含被摘要盖住的），得到 %d", len(messages), len(doc.Messages))
	}
	if len(doc.Summaries) != 3 {
		t.Fatalf("该带 3 条摘要（两叶 + 一合并），得到 %d", len(doc.Summaries))
	}
	byKey := map[string]int{}
	for index, sum := range doc.Summaries {
		byKey[sum.Key] = index
	}
	var merged *string
	for _, sum := range doc.Summaries {
		switch sum.Type {
		case "message":
			if sum.BeginIdx == nil || sum.EndIdx == nil {
				t.Fatalf("消息级摘要该带区间：%+v", sum)
			}
			if *sum.BeginIdx == 1 && *sum.EndIdx != 2 || *sum.BeginIdx == 3 && *sum.EndIdx != 4 {
				t.Fatalf("区间该是 1-2 / 3-4，得到 %d-%d", *sum.BeginIdx, *sum.EndIdx)
			}
			if !sum.Dirty {
				t.Fatalf("dirty 是「材料被改过」的标记，导出该照实带：%+v", sum)
			}
			if sum.ParentKey == nil {
				t.Fatalf("两条叶摘要都该指向合并摘要（parent_key）：%+v", sum)
			}
			if merged == nil {
				merged = sum.ParentKey
			} else if *merged != *sum.ParentKey {
				t.Fatal("两条叶摘要该指向同一个父亲")
			}
		case "summary":
			if sum.ParentKey != nil {
				t.Fatalf("合并摘要自己该是顶层：%+v", sum)
			}
			if *merged != sum.Key {
				t.Fatalf("叶摘要的 parent_key 该是合并摘要的 key：%s vs %s", *merged, sum.Key)
			}
			if *sum.BeginIdx != 1 || *sum.EndIdx != 4 {
				t.Fatalf("合并摘要的区间该盖满 1-4，得到 %d-%d", *sum.BeginIdx, *sum.EndIdx)
			}
		default:
			t.Fatalf("type 只许 message / summary，得到 %q", sum.Type)
		}
	}
	// 同一份文件里不许出现库内 id 那种"用了就撞"的东西（source_ids 不导，导入时重建）。
	if strings.Contains(exported.Body.String(), "source_ids") {
		t.Fatalf("source_ids 不该导出（导入时按真实成员重建）：%s", exported.Body.String())
	}
}

// v3 导入：金字塔原样压回去 —— 三条摘要、父子关系、区间、正文、
// 以及 `messages.summary_id`（装配靠它 O(1) 跳过那一段）。
func TestImportV3RestoresThePyramid(t *testing.T) {
	box := newDummySandboxWith(t, true)
	box.seedPyramid(t)
	raw := authCall(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/export", "", testSysToken).Body.String()

	imported := authCall(box.server, "POST", "/api/v1/sessions/import-json", raw, testSysToken)
	if imported.Code != http.StatusCreated {
		t.Fatalf("导入 = %d（%s）", imported.Code, imported.Body.String())
	}
	receipt := decodeBody[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Messages  int `json:"messages"`
		Summaries int `json:"summaries"`
	}](t, imported)
	if receipt.Messages != 4 || receipt.Summaries != 3 {
		t.Fatalf("回执该报 4 条消息 / 3 条摘要：%+v", receipt)
	}

	restored := decodeBody[[]struct {
		ID              string  `json:"id"`
		ParentSummaryID *string `json:"parent_summary_id"`
		Type            string  `json:"type"`
		BeginIdx        *int    `json:"begin_idx"`
		EndIdx          *int    `json:"end_idx"`
		Text            string  `json:"text"`
		Blocks          int64   `json:"blocks"`
		Tokens          int64   `json:"tokens"`
		Dirty           bool    `json:"dirty"`
	}](t, authCall(box.server, "GET", "/api/v1/sessions/"+receipt.Session.ID+"/summaries", "", testSysToken))
	if len(restored) != 3 {
		t.Fatalf("还原该有 3 条摘要，得到 %d", len(restored))
	}
	var top, leaves int
	for _, sum := range restored {
		switch sum.Type {
		case "summary":
			top++
			if sum.ParentSummaryID != nil {
				t.Fatalf("合并摘要该是顶层：%+v", sum)
			}
			if sum.Text != "两段并起来的梗概" || sum.Blocks != 2 || sum.Tokens != 80 {
				t.Fatalf("合并摘要的账没照抄：%+v", sum)
			}
			if sum.BeginIdx == nil || *sum.BeginIdx != 1 || sum.EndIdx == nil || *sum.EndIdx != 4 {
				t.Fatalf("合并摘要的区间该是 1-4：%+v", sum)
			}
		case "message":
			leaves++
			if sum.ParentSummaryID == nil {
				t.Fatalf("叶摘要该指回合并摘要：%+v", sum)
			}
			if !sum.Dirty {
				t.Fatalf("dirty 该照抄（不然一条过期摘要会被洗成新鲜的）：%+v", sum)
			}
			if !strings.Contains(sum.Text, "的梗概") {
				t.Fatalf("正文没还原：%+v", sum)
			}
		}
	}
	if top != 1 || leaves != 2 {
		t.Fatalf("该是 1 个合并 + 2 个叶，得到 %d + %d", top, leaves)
	}

	// 消息侧的 summary_id 也回填了（4 条消息两两被盖住）。
	after := decodeBody[[]struct {
		Idx       int     `json:"idx"`
		SummaryID *string `json:"summary_id"`
	}](t, authCall(box.server, "GET", "/api/v1/sessions/"+receipt.Session.ID+"/messages", "", testSysToken))
	if len(after) != 4 {
		t.Fatalf("导入消息数 = %d", len(after))
	}
	for _, message := range after {
		if message.SummaryID == nil {
			t.Fatalf("第 %d 条该被摘要盖住（summary_id 没回填）：%+v", message.Idx, message)
		}
	}
	if after[0].SummaryID == nil || after[1].SummaryID == nil || *after[0].SummaryID != *after[1].SummaryID {
		t.Fatal("1-2 两条该被同一条叶摘要盖住")
	}
	if *after[2].SummaryID == *after[0].SummaryID {
		t.Fatal("3-4 那两条该是另一条叶摘要")
	}

	// 同一个文件导两次都得成功（摘要在导入时铸新 id —— 照抄旧 id 会撞主键）。
	again := authCall(box.server, "POST", "/api/v1/sessions/import-json", raw, testSysToken)
	if again.Code != http.StatusCreated {
		t.Fatalf("同一份文件再导一次该照样成功：%d（%s）", again.Code, again.Body.String())
	}
}

// 坏金字塔一律 400，且**不许留下半条会话**：形状在动库之前就验完。
func TestImportV3RejectsBrokenPyramid(t *testing.T) {
	box := newDummySandboxWith(t, true)
	base := `{"version":3,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}],`
	cases := []struct {
		name      string
		summaries string
		want      string
	}{
		{"没 key", `[{"type":"message","text":"x","begin_idx":1,"end_idx":2}]`, "没有 key"},
		{"key 重复", `[{"key":"a","type":"message","text":"x","begin_idx":1,"end_idx":2},
			{"key":"a","type":"message","text":"y","begin_idx":1,"end_idx":2}]`, "key 重复"},
		{"父摘要不在文件里", `[{"key":"a","parent_key":"没这个人","type":"message","text":"x","begin_idx":1,"end_idx":2}]`,
			"不在文件里的父摘要"},
		{"父链成环", `[{"key":"a","parent_key":"b","type":"summary","text":"x","begin_idx":1,"end_idx":2},
			{"key":"b","parent_key":"a","type":"summary","text":"y","begin_idx":1,"end_idx":2}]`, "成环"},
		{"区间越界", `[{"key":"a","type":"message","text":"x","begin_idx":1,"end_idx":9}]`, "不在 1-2 里"},
		{"区间反了", `[{"key":"a","type":"message","text":"x","begin_idx":2,"end_idx":1}]`, "不在 1-2 里"},
		{"正文空", `[{"key":"a","type":"message","text":"   ","begin_idx":1,"end_idx":2}]`, "正文是空的"},
		{"type 不认识", `[{"key":"a","type":"什么","text":"x","begin_idx":1,"end_idx":2}]`, "只许 message / summary"},
	}
	for _, testCase := range cases {
		before := decodeBody[[]struct{}](t, authCall(box.server, "GET", "/api/v1/sessions", "", testSysToken))
		rec := authCall(box.server, "POST", "/api/v1/sessions/import-json", base+`"summaries":`+testCase.summaries+`}`, testSysToken)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 该 400，得到 %d（%s）", testCase.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), testCase.want) {
			t.Fatalf("%s 的报错该说清（含 %q）：%s", testCase.name, testCase.want, rec.Body.String())
		}
		after := decodeBody[[]struct{}](t, authCall(box.server, "GET", "/api/v1/sessions", "", testSysToken))
		if len(after) != len(before) {
			t.Fatalf("%s 失败了却留下一条会话（%d → %d）", testCase.name, len(before), len(after))
		}
	}
}

// 两条消息级摘要抢同一段 ⇒ 400（`messages.summary_id IS NULL` 那道守卫），不是悄悄盖掉一条。
func TestImportV3RejectsOverlappingLeafRanges(t *testing.T) {
	box := newDummySandboxWith(t, true)
	doc := `{"version":3,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}],
		"summaries":[
			{"key":"a","type":"message","text":"x","begin_idx":1,"end_idx":2},
			{"key":"b","type":"message","text":"y","begin_idx":1,"end_idx":2}]}`
	rec := authCall(box.server, "POST", "/api/v1/sessions/import-json", doc, testSysToken)
	if rec.Code == http.StatusCreated {
		t.Fatalf("重叠的区间该被挡住：%s", rec.Body.String())
	}
	// 半成品会话没留下（有消息、没金字塔比失败更糟）。
	list := decodeBody[[]struct{}](t, authCall(box.server, "GET", "/api/v1/sessions", "", testSysToken))
	if len(list) != 1 { // 只有 sandbox 自带的那条
		t.Fatalf("失败该把刚建的会话删掉，现在有 %d 条", len(list))
	}
}

// v1/v2 老文件（没有 summaries）照旧能导：摘要 0 条，回执报 summaries=0。
func TestImportOldVersionStillWorksWithoutSummaries(t *testing.T) {
	box := newDummySandboxWith(t, true)
	for _, doc := range []string{
		`{"version":1,"messages":[{"role":"user","content":"a"}]}`,
		`{"version":2,"provider":"dummy","model":"dummy","messages":[{"role":"user","content":"a"}]}`,
	} {
		rec := authCall(box.server, "POST", "/api/v1/sessions/import-json", doc, testSysToken)
		if rec.Code != http.StatusCreated {
			t.Fatalf("老文件该照旧能导：%d（%s）", rec.Code, rec.Body.String())
		}
		if got := decodeBody[struct {
			Summaries int `json:"summaries"`
		}](t, rec).Summaries; got != 0 {
			t.Fatalf("老文件没有摘要，回执该报 0，得到 %d", got)
		}
	}
}

// v3 起消息的 `reasoning`（模型的思考）也要往返：它是"模型说过的话"的一部分，
// 老版本把它丢了 —— 导回来那些推理型回复就只剩正文。
func TestExportV3CarriesReasoning(t *testing.T) {
	box := newDummySandboxWith(t, true)
	ids, err := store.MintOrderedIDs(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []model.Message{
		{ID: ids[0], SessionID: box.sessionID, Role: model.RoleUser, Content: "问一句"},
		{ID: ids[1], SessionID: box.sessionID, Role: model.RoleAssistant,
			Content: "答一句", Reasoning: "先想一下，再回答。"},
	} {
		if _, err := box.store.InsertMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	raw := authCall(box.server, "GET", "/api/v1/sessions/"+box.sessionID+"/export", "", testSysToken).Body.String()
	var doc struct {
		Messages []struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Messages) != 2 || doc.Messages[1].Reasoning != "先想一下，再回答。" {
		t.Fatalf("思考该跟着正文一起导出：%+v", doc.Messages)
	}
	// 用户的思考永远是空 ⇒ 那一格不出现（omitempty）。
	if doc.Messages[0].Reasoning != "" {
		t.Fatalf("user 消息没有思考：%+v", doc.Messages[0])
	}

	imported := authCall(box.server, "POST", "/api/v1/sessions/import-json", raw, testSysToken)
	if imported.Code != http.StatusCreated {
		t.Fatalf("导入 = %d（%s）", imported.Code, imported.Body.String())
	}
	newID := decodeBody[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, imported).Session.ID
	after := decodeBody[[]struct {
		Content   string `json:"content"`
		Reasoning string `json:"reasoning"`
	}](t, authCall(box.server, "GET", "/api/v1/sessions/"+newID+"/messages", "", testSysToken))
	if len(after) != 2 || after[1].Reasoning != "先想一下，再回答。" {
		t.Fatalf("思考没导回来：%+v", after)
	}
}
