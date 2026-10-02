package tg

// 派发链路的冒烟测试：**真 bot.Bot + 真 HTTP**，只是把 Telegram 那端点换成 httptest 假的
// （`bot.WithServerURL`）—— 不连真 TG、不开长轮询；更新用 `ProcessUpdate` 喂进去，
// 所以"注册表挂上了没、回调前缀派对了没"这些**接线**也一起被真跑一遍。
//
// 纯函数那些单测测的是"文案对不对"，这个文件测的是"真跑起来会走哪条路"。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// sentMessage：假 Telegram 收到的一条 sendMessage。
type sentMessage struct {
	text   string
	markup *models.InlineKeyboardMarkup
}

// markupEdit：一次 editMessageReplyMarkup（挂按钮 / 清按钮）；markup 为 nil = 清掉。
type markupEdit struct {
	messageID int
	markup    *models.InlineKeyboardMarkup
}

// fakeTelegram：假 Bot API —— 只认发消息/改消息/回执/挂按钮/删消息（其余一律 404，测试就能发现"没想到的调用"）。
type fakeTelegram struct {
	mu          sync.Mutex
	nextID      int
	sent        []sentMessage
	edited      []string
	editMarkups []*models.InlineKeyboardMarkup // 与 edited 逐项对齐（editMessageText 带的按钮）
	markups     []markupEdit                   // editMessageReplyMarkup 那一路
	deleted     []int                          // deleteMessage
	answers     []string
	patched     []string // PATCH 的请求体（/model 选中时）
}

func (f *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		_ = r.ParseMultipartForm(1 << 20) // 库用 multipart/form-data（见 rawRequest）
		message := sentMessage{text: r.FormValue("text")}
		if rawMarkup := r.FormValue("reply_markup"); rawMarkup != "" {
			_ = json.Unmarshal([]byte(rawMarkup), &message.markup)
		}
		f.mu.Lock()
		f.nextID++
		id := f.nextID
		f.sent = append(f.sent, message)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":42,"type":"private"},"text":""}}`, id)
	case strings.HasSuffix(r.URL.Path, "/editMessageText"):
		_ = r.ParseMultipartForm(1 << 20)
		var markup *models.InlineKeyboardMarkup
		if rawMarkup := r.FormValue("reply_markup"); rawMarkup != "" {
			_ = json.Unmarshal([]byte(rawMarkup), &markup)
		}
		f.mu.Lock()
		f.edited = append(f.edited, r.FormValue("text"))
		f.editMarkups = append(f.editMarkups, markup)
		f.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"},"text":""}}`)
	case strings.HasSuffix(r.URL.Path, "/editMessageReplyMarkup"):
		_ = r.ParseMultipartForm(1 << 20)
		id, _ := strconv.Atoi(r.FormValue("message_id"))
		var markup *models.InlineKeyboardMarkup
		if rawMarkup := r.FormValue("reply_markup"); rawMarkup != "" {
			_ = json.Unmarshal([]byte(rawMarkup), &markup)
		}
		f.mu.Lock()
		f.markups = append(f.markups, markupEdit{messageID: id, markup: markup})
		f.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"},"text":""}}`)
	case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
		_ = r.ParseMultipartForm(1 << 20)
		id, _ := strconv.Atoi(r.FormValue("message_id"))
		f.mu.Lock()
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
		// 生成中报"正在输入"——不算意外调用，认下来（正文走 sendMessage）。
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		_ = r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.answers = append(f.answers, r.FormValue("text"))
		f.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTelegram) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage{}, f.sent...)
}

func (f *fakeTelegram) edits() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.edited...)
}

func (f *fakeTelegram) replyTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.answers...)
}

// markupEdits：所有 editMessageReplyMarkup（挂按钮 / 清按钮）—— 按到达顺序。
func (f *fakeTelegram) markupEdits() []markupEdit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]markupEdit{}, f.markups...)
}

// editButtons：editMessageText 带的按钮，与 edits() 逐项对齐（没带按钮的项是 nil）。
func (f *fakeTelegram) editButtons() []*models.InlineKeyboardMarkup {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*models.InlineKeyboardMarkup{}, f.editMarkups...)
}

// deletedIDs：deleteMessage 删掉的段 id。
func (f *fakeTelegram) deletedIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int{}, f.deleted...)
}

// newFakeRunner：假 Telegram + 假后端，装出一个**已绑定**的 Runner（allowed = 42）。
func newFakeRunner(t *testing.T, backend http.HandlerFunc) (*Runner, *fakeTelegram) {
	t.Helper()
	fake := &fakeTelegram{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	b, err := bot.New("123:fake-token",
		bot.WithServerURL(server.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	if err != nil {
		t.Fatalf("建 bot 失败：%v", err)
	}
	runner := &Runner{
		bot: b, allowed: 42, api: stubClient(t, backend),
		pending: map[string]*pendingOp{},
	}
	runner.register(b)
	return runner, fake
}

// textUpdate：造一条来自已绑定账户的文本消息。
func textUpdate(text string, fromID int64) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: 1, Text: text, Chat: models.Chat{ID: fromID}, From: &models.User{ID: fromID},
	}}
}

// callbackUpdate：造一条按钮回调（挂在 messageID 那条消息上）。
func callbackUpdate(data string, fromID int64, messageID int) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb-1", From: models.User{ID: fromID}, Data: data,
		Message: models.MaybeInaccessibleMessage{
			Type:    models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: messageID, Chat: models.Chat{ID: fromID}},
		},
	}}
}

// /status 一路走通：注册 → 派发 → 取数 → 发出去的那条消息就是底栏那份四要素。
func TestDispatchStatusEndToEnd(t *testing.T) {
	const id = "aaaaaaaa-1111"
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			fmt.Fprint(w, `{"status":"ok","version":"0.1.2"}`)
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"`+id+`","title":"修 bug","provider":"opencode-go","model":"sonnet","turn":{"phase":"idle"}}]`)
		case "/api/v1/sessions/" + id + "/status":
			fmt.Fprint(w, `{"phase":"streaming","elapsed_ms":2400}`)
		case "/api/v1/sessions/" + id + "/context":
			fmt.Fprint(w, `{"used_tokens":12345,"budget_tokens":131000}`)
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("/status", 42))

	sent := fake.messages()
	if len(sent) != 1 {
		t.Fatalf("该只发一条：%d", len(sent))
	}
	for _, want := range []string{"会话：修 bug | aaaaaaaa | opencode-go/sonnet", "上下文：12.3k/131k (9.4%)", "轮次：生成中 2.4s", "TG：", "后端：v0.1.2"} {
		if !strings.Contains(sent[0].text, want) {
			t.Fatalf("发出去的 /status 缺 %q：\n%s", want, sent[0].text)
		}
	}
}

// 未绑定：只收到绑定提示，命令一个都不执行（也不泄露命令清单）。
func TestDispatchUnboundOnlyGetsHint(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("未绑定不该打后端：%s", r.URL.Path)
	})
	runner.bot.ProcessUpdate(context.Background(), textUpdate("/status", 7))
	sent := fake.messages()
	if len(sent) != 1 || !strings.Contains(sent[0].text, "/telegram-bind") {
		t.Fatalf("该只回绑定提示：%+v", sent)
	}
	if strings.Contains(sent[0].text, "/status 看状态") {
		t.Fatal("未绑定不该泄露命令面")
	}
}

// 斜杠形状但没登记过：明说"不认识"（不当普通文本发进会话）。
func TestDispatchUnknownCommand(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("不认识命令不该打后端：%s", r.URL.Path)
	})
	runner.bot.ProcessUpdate(context.Background(), textUpdate("/nope 一下", 42))
	sent := fake.messages()
	if len(sent) != 1 || !strings.Contains(sent[0].text, "不认识这个命令") {
		t.Fatalf("该说不认识：%+v", sent)
	}
	if !strings.Contains(sent[0].text, "/status") {
		t.Fatalf("该列出可用命令：%s", sent[0].text)
	}
}

// /delete：真发出确认按钮，callback_data 前缀对且 ≤ 64 字节（pending 表里落地了这件事）。
func TestDispatchDeleteConfirmButtons(t *testing.T) {
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"aaaaaaaa-1111","title":"甲"}]`)
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("/delete", 42))

	sent := fake.messages()
	if len(sent) != 1 || sent[0].markup == nil || len(sent[0].markup.InlineKeyboard) == 0 {
		t.Fatalf("该发出确认按钮：%+v", sent)
	}
	buttons := sent[0].markup.InlineKeyboard[0]
	if len(buttons) != 2 {
		t.Fatalf("该两个按钮（确认 / 取消）：%d", len(buttons))
	}
	for _, button := range buttons {
		if len(button.CallbackData) > 64 {
			t.Fatalf("callback_data 超 64 字节：%q", button.CallbackData)
		}
		if !strings.HasPrefix(button.CallbackData, cbConfirm) && !strings.HasPrefix(button.CallbackData, cbCancel) {
			t.Fatalf("前缀不对：%q", button.CallbackData)
		}
	}
	// 待办真落进了内存表（确认回调才有东西可执行）。
	key := strings.TrimPrefix(buttons[0].CallbackData, cbConfirm)
	if op, ok := runner.popPending(key); !ok || op.kind != "delete" || op.sessionID != "aaaaaaaa-1111" {
		t.Fatalf("待办没记住：%+v %v", op, ok)
	}
}

// /model：面板发出来 → 点一个 → 就地回执 + PATCH 当前会话（回调前缀也真派到 onModel）。
func TestDispatchModelPick(t *testing.T) {
	items := `[{"provider":"opencode-go","upstream_id":"sonnet","name":"sonnet"},
		{"provider":"opencode-go","upstream_id":"opus","name":"opus"}]`
	patched := ""
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			fmt.Fprint(w, items)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"s1","title":"甲"}]`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sessions/s1":
			raw := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(raw)
			patched = string(raw)
			fmt.Fprint(w, `{"id":"s1","title":"甲","provider":"opencode-go","model":"opus"}`)
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)

	runner.bot.ProcessUpdate(context.Background(), textUpdate("/model", 42))
	sent := fake.messages()
	if len(sent) != 1 || sent[0].markup == nil {
		t.Fatalf("该发出模型面板：%+v", sent)
	}
	pick := sent[0].markup.InlineKeyboard[1][0] // 第 2 个模型（opus）
	if !strings.HasPrefix(pick.CallbackData, cbModel) || len(pick.CallbackData) > 64 {
		t.Fatalf("模型按钮的 callback_data 不对：%q", pick.CallbackData)
	}

	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(pick.CallbackData, 42, 1))
	if !strings.Contains(patched, `"model":"opus"`) || !strings.Contains(patched, `"provider":"opencode-go"`) {
		t.Fatalf("没切到选中的模型：%q", patched)
	}
	edits := fake.edits()
	if len(edits) != 1 || !strings.Contains(edits[0], "已切到：opencode-go/opus") {
		t.Fatalf("该就地回执：%v", edits)
	}
	if replies := fake.replyTexts(); len(replies) != 1 || !strings.Contains(replies[0], "opus") {
		t.Fatalf("该给个 toast：%v", replies)
	}
}

// 确认按钮真的把删除执行了（回调 → pending 表 → 后端 DELETE）。
func TestDispatchConfirmExecutesDelete(t *testing.T) {
	deleted := false
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			if deleted {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[{"id":"aaa","title":"甲"}]`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sessions/aaa":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			fmt.Fprint(w, `{"id":"bbb","title":""}`)
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("/delete", 42))

	buttons := fake.messages()[0].markup.InlineKeyboard[0]
	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(buttons[0].CallbackData, 42, 1))

	if !deleted {
		t.Fatal("确认之后该真删")
	}
	edits := fake.edits()
	if len(edits) != 1 || !strings.Contains(edits[0], "又新建了一条") {
		t.Fatalf("该把结局就地写在那条消息上：%v", edits)
	}
	if got := runner.sessionID(); got != "bbb" {
		t.Fatalf("当前会话该落到新建那条：%q", got)
	}
	// 再点一次：认不出来了（一次性待办）—— 不下第二次手。
	runner.bot.ProcessUpdate(context.Background(), callbackUpdate(buttons[0].CallbackData, 42, 1))
	if replies := fake.replyTexts(); len(replies) != 2 || !strings.Contains(replies[1], "已经处理过了") {
		t.Fatalf("第二次点该说处理过了：%v", replies)
	}
}
