package tg

// 回复按钮（[◀ n/total ▶]）的冒烟测试：假 Telegram（见 dispatch_test.go）+ 脚本化假后端。
//
// 覆盖：一轮结束后每段都挂按钮、`◀` 到头提示、`▶` 到头真发起重摇再切到新版、旧消息 id 点了只说失效。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// 一轮超长回复（两段）结束后：两段都挂上三个按钮，中间显示 1/1，登记表认下这两段。
func TestRerollButtonsAfterTurn(t *testing.T) {
	const sid = "rr000001-aaaa"
	long := strings.Repeat("字", 5000)
	polls := 0
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/sessions":
			fmt.Fprintf(w, `[{"id":%q,"title":"甲","turn":{"phase":"idle"}}]`, sid)
		case r.URL.Path == "/api/v1/sessions/"+sid+"/messages" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"backend":"fake","turn":{"phase":"pending","message_id":"m1"}}`)
		case r.URL.Path == "/api/v1/sessions/"+sid+"/status":
			polls++
			if polls == 1 {
				fmt.Fprint(w, `{"phase":"streaming","elapsed_ms":10}`)
			} else {
				fmt.Fprint(w, `{"phase":"idle"}`)
			}
		case r.URL.Path == "/api/v1/sessions/"+sid+"/turn/text":
			if polls <= 1 {
				fmt.Fprintf(w, `{"text":%q,"next":5000}`, long)
			} else {
				fmt.Fprint(w, `{"text":"","next":5000,"done":true}`)
			}
		case r.URL.Path == "/api/v1/sessions/"+sid+"/reroll-message":
			fmt.Fprint(w, `{"active":false}`) // 还没重摇过
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("给我长文", 42))

	if sent := fake.messages(); len(sent) != 2 {
		t.Fatalf("超长该发成两条：%d", len(sent))
	}
	edits := fake.markupEdits()
	if len(edits) != 2 {
		t.Fatalf("两段都该挂上按钮：%+v", edits)
	}
	for _, edit := range edits {
		if edit.markup == nil || len(edit.markup.InlineKeyboard) != 1 || len(edit.markup.InlineKeyboard[0]) != 3 {
			t.Fatalf("该挂一组三个按钮：%+v", edit)
		}
		buttons := edit.markup.InlineKeyboard[0]
		if buttons[0].Text != "◀" || buttons[0].CallbackData != rrPrev ||
			buttons[2].Text != "▶" || buttons[2].CallbackData != rrNext {
			t.Fatalf("两侧箭头 / 回调不对：%+v", buttons)
		}
		if buttons[1].Text != "1/1" || buttons[1].CallbackData != rrNoop {
			t.Fatalf("中间该显示 1/1：%+v", buttons[1])
		}
	}
	if edits[0].messageID != 1 || edits[1].messageID != 2 {
		t.Fatalf("该挂在两段消息 1 / 2 上：%d / %d", edits[0].messageID, edits[1].messageID)
	}
	runner.replyMu.Lock()
	group := runner.activeReply[42]
	runner.replyMu.Unlock()
	if group == nil || len(group.segIDs) != 2 || group.current != 1 || group.total != 1 || group.targetID != "m1" {
		t.Fatalf("activeReply 没记对：%+v", group)
	}
}

// `◀` 已经在第一版：只给一句到头提示，不发任何切换请求、不改消息。
func TestRerollPrevAtFirstToast(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("到头提示不该打后端：%s", r.URL.Path)
	})
	runner.activeReply = map[int64]*replyButtons{
		42: {sessionID: "s1", targetID: "m1", segIDs: []int{1}, current: 1, total: 3},
	}
	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(rrPrev, 42, 1))

	replies := fake.replyTexts()
	if len(replies) != 1 || replies[0] != "当前是第一个备选回复" {
		t.Fatalf("该只回一句到头提示：%v", replies)
	}
	if len(fake.edits()) != 0 || len(fake.markupEdits()) != 0 {
		t.Fatalf("到头不该动消息：edits=%v markups=%v", fake.edits(), fake.markupEdits())
	}
	runner.replyMu.Lock()
	group := runner.activeReply[42]
	runner.replyMu.Unlock()
	if group.current != 1 || group.total != 3 {
		t.Fatalf("计数不该动：%d/%d", group.current, group.total)
	}
}

// 旧消息 id 上的按钮：只说"已失效"，一个后端请求都不发。
func TestRerollStaleMessageInvalid(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("失效按钮不该打后端：%s", r.URL.Path)
	})
	runner.activeReply = map[int64]*replyButtons{
		42: {sessionID: "s1", targetID: "m1", segIDs: []int{1, 2}, current: 1, total: 1},
	}
	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(rrNext, 42, 99)) // 99 不在 segIDs 里

	replies := fake.replyTexts()
	if len(replies) != 1 || !strings.Contains(replies[0], "旧按钮已失效") {
		t.Fatalf("该说过期：%v", replies)
	}
	if len(fake.edits()) != 0 || len(fake.markupEdits()) != 0 {
		t.Fatalf("失效不该动消息：edits=%v markups=%v", fake.edits(), fake.markupEdits())
	}
}

// `▶` 已经在最右（1/1）：发起重摇（202）→ 轮询到摇完 → 切到最新那版 → 正文被 edit 成新版、计数 2/2。
func TestRerollNextAtEndRerolls(t *testing.T) {
	const sid = "rr000003-cccc"
	const first = "第一版的正文"
	const second = "第二版的正文（重摇出来的）"
	var mu sync.Mutex
	turnPolls := 0
	started := false // 进重摇模式了没
	rerollGets := 0  // 进模式之后 GET 了几次
	count, currentIdx := 1, 1
	content := first
	rerollPosts := 0
	switches := []int{}
	stateJSON := func(running bool) string {
		return fmt.Sprintf(`{"active":true,"target_kind":"message","target_message_id":"m1","count":%d,"current_idx":%d,"running":%t}`, count, currentIdx, running)
	}
	backend := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/sessions":
			fmt.Fprintf(w, `[{"id":%q,"title":"甲","turn":{"phase":"idle"}}]`, sid)
		case r.URL.Path == "/api/v1/sessions/"+sid+"/messages" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"backend":"fake","turn":{"phase":"pending","message_id":"m1"}}`)
		case r.URL.Path == "/api/v1/sessions/"+sid+"/messages":
			fmt.Fprintf(w, `[{"id":"m1","role":"assistant","content":%q}]`, content)
		case r.URL.Path == "/api/v1/sessions/"+sid+"/status":
			turnPolls++
			if turnPolls == 1 {
				fmt.Fprint(w, `{"phase":"streaming","elapsed_ms":10}`)
			} else {
				fmt.Fprint(w, `{"phase":"idle"}`)
			}
		case r.URL.Path == "/api/v1/sessions/"+sid+"/turn/text":
			if turnPolls <= 1 {
				fmt.Fprintf(w, `{"text":%q,"next":14}`, first)
			} else {
				fmt.Fprint(w, `{"text":"","next":14,"done":true}`)
			}
		case r.URL.Path == "/api/v1/sessions/"+sid+"/reroll-message" && r.Method == http.MethodPost:
			rerollPosts++
			started = true
			count, currentIdx = 2, 1
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"target_message_id":"m1","task_id":"t1","state":%s}`, stateJSON(true))
		case r.URL.Path == "/api/v1/sessions/"+sid+"/reroll-message/switch" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Idx int `json:"idx"`
			}
			_ = json.Unmarshal(raw, &body)
			switches = append(switches, body.Idx)
			currentIdx = body.Idx
			content = second
			fmt.Fprint(w, stateJSON(false))
		case r.URL.Path == "/api/v1/sessions/"+sid+"/reroll-message":
			if !started {
				fmt.Fprint(w, `{"active":false}`)
				return
			}
			rerollGets++
			fmt.Fprint(w, stateJSON(rerollGets <= 1)) // 第一次还在摇，第二次摇完了
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("你好", 42))

	// 一轮结束，先确认 1/1 的按钮挂上了。
	runner.replyMu.Lock()
	group := runner.activeReply[42]
	runner.replyMu.Unlock()
	if group == nil || group.current != 1 || group.total != 1 {
		t.Fatalf("一轮结束后该是 1/1：%+v", group)
	}

	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(rrNext, 42, 1)) // 1/1 上点 ▶ ⇒ 再摇一版

	if rerollPosts != 1 {
		t.Fatalf("该发起一次重摇：%d", rerollPosts)
	}
	if len(switches) != 1 || switches[0] != 2 {
		t.Fatalf("该切到最新那版（idx=2）：%v", switches)
	}
	edits := fake.edits()
	if len(edits) == 0 || edits[len(edits)-1] != second {
		t.Fatalf("正文该被 edit 成新版：%v", edits)
	}
	buttons := fake.editButtons()
	if last := buttons[len(buttons)-1]; last == nil || last.InlineKeyboard[0][1].Text != "2/2" {
		t.Fatalf("新版该挂 2/2 的按钮：%+v", last)
	}
	runner.replyMu.Lock()
	group = runner.activeReply[42]
	runner.replyMu.Unlock()
	if group.current != 2 || group.total != 2 || group.sessionID != sid || group.targetID != "m1" {
		t.Fatalf("计数该变成 2/2：%+v", group)
	}
	if replies := fake.replyTexts(); len(replies) != 1 || !strings.Contains(replies[0], "重摇好了") {
		t.Fatalf("该给个摇完的 toast：%v", replies)
	}
}

// 新回复落地：旧回复那几段先被清掉按钮（markup=nil），再给新段挂上 —— 旧按钮从此点不动。
func TestRerollAttachClearsOldButtons(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reroll-message") {
			fmt.Fprint(w, `{"active":false}`)
			return
		}
		t.Errorf("不该打别的后端：%s", r.URL.Path)
	})
	runner.activeReply = map[int64]*replyButtons{
		42: {sessionID: "s1", targetID: "m1", segIDs: []int{11, 12}, current: 1, total: 1},
	}
	runner.attachReplyButtons(context.Background(), 42, "s1", "m2", []int{21, 22})

	edits := fake.markupEdits()
	if len(edits) != 4 {
		t.Fatalf("该先清两段（2）再挂两段（2）：%+v", edits)
	}
	for index, want := range []int{11, 12} {
		if edits[index].messageID != want || edits[index].markup != nil {
			t.Fatalf("第 %d 次该清旧按钮（markup=nil）：%+v", index, edits[index])
		}
	}
	for index, want := range []int{21, 22} {
		edit := edits[2+index]
		if edit.messageID != want || edit.markup == nil || len(edit.markup.InlineKeyboard[0]) != 3 {
			t.Fatalf("第 %d 次该给新段挂按钮：%+v", 2+index, edit)
		}
	}
	runner.replyMu.Lock()
	group := runner.activeReply[42]
	runner.replyMu.Unlock()
	if group == nil || group.targetID != "m2" || len(group.segIDs) != 2 || group.segIDs[0] != 21 {
		t.Fatalf("登记表该换成新的：%+v", group)
	}
}

// 上一击还在处理：只给一句提示，不叠第二次网络动作。
func TestRerollBusyToast(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("busy 时不该打后端：%s", r.URL.Path)
	})
	runner.activeReply = map[int64]*replyButtons{
		42: {sessionID: "s1", targetID: "m1", segIDs: []int{1}, current: 1, total: 3, busy: true},
	}
	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(rrNext, 42, 1))

	replies := fake.replyTexts()
	if len(replies) != 1 || replies[0] != "上一条点击还在处理" {
		t.Fatalf("该回 busy 提示：%v", replies)
	}
	if len(fake.edits()) != 0 || len(fake.markupEdits()) != 0 {
		t.Fatalf("busy 不该动消息：edits=%v markups=%v", fake.edits(), fake.markupEdits())
	}
}

// 中间那格只是个计数牌：点它静默回执（空文本），不占 busy、不打后端、不动消息。
func TestRerollNoopSilent(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("noop 不该打后端：%s", r.URL.Path)
	})
	runner.activeReply = map[int64]*replyButtons{
		42: {sessionID: "s1", targetID: "m1", segIDs: []int{1}, current: 1, total: 2},
	}
	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(rrNoop, 42, 1))

	if replies := fake.replyTexts(); len(replies) != 1 || replies[0] != "" {
		t.Fatalf("该静默回执（一条空文本）：%#v", replies)
	}
	runner.replyMu.Lock()
	group := runner.activeReply[42]
	runner.replyMu.Unlock()
	if group.busy || group.current != 1 || group.total != 2 {
		t.Fatalf("noop 不该动状态：%+v", group)
	}
	if len(fake.edits()) != 0 || len(fake.markupEdits()) != 0 {
		t.Fatalf("noop 不该动消息：edits=%v markups=%v", fake.edits(), fake.markupEdits())
	}
}

// 按钮形状与 callback_data 长度：三个短常量，都远在 64 字节以内。
func TestRerollMarkupData(t *testing.T) {
	markup := rerollMarkup(2, 3)
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 3 {
		t.Fatalf("该是一组三个按钮：%+v", markup)
	}
	buttons := markup.InlineKeyboard[0]
	if buttons[0].Text != "◀" || buttons[1].Text != "2/3" || buttons[2].Text != "▶" {
		t.Fatalf("按钮文案不对：%q %q %q", buttons[0].Text, buttons[1].Text, buttons[2].Text)
	}
	want := []string{rrPrev, rrNoop, rrNext}
	for index, button := range buttons {
		if button.CallbackData != want[index] {
			t.Fatalf("第 %d 个按钮的 callback_data 不对：%q", index, button.CallbackData)
		}
		if !strings.HasPrefix(button.CallbackData, cbReroll) {
			t.Fatalf("前缀不对：%q", button.CallbackData)
		}
		if len(button.CallbackData) > 64 {
			t.Fatalf("callback_data 超 64 字节：%q", button.CallbackData)
		}
	}
}
