package tg

// 服务端 Markdown 渲染的单测：转义表逐字 + 发送/edit 的正常/回退路径。
//
// 不打真网络 —— 走 dispatch_test.go 的 fakeTelegram + bot.WithServerURL（真 bot.Bot + 真 HTTP，
// TG 那端点换成 httptest 假的）。回退场景用独立 fake（failFirstTelegram）：第一次返回
// 400 can't parse entities，第二次记下 parse_mode 与文本。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// 转义表逐字：官方表里的每个特殊字符前都必须多一个 `\`。
func TestEscapeMarkdownV2Table(t *testing.T) {
	specials := []rune{'\\', '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!'}
	for _, r := range specials {
		got := escapeMarkdownV2(string(r))
		if want := "\\" + string(r); got != want {
			t.Fatalf("%q 该转成 %q：%q", string(r), want, got)
		}
	}
}

// 不转义的照原样（字母数字中文空格一个都不动；不许顺手加粗/加标题/改行）。
func TestEscapeMarkdownV2Passthrough(t *testing.T) {
	for _, text := range []string{"", "hello 你好 123", "a b\nc d"} {
		if got := escapeMarkdownV2(text); got != text {
			t.Fatalf("%q 不该动：%q", text, got)
		}
	}
}

// 混在一起：特殊字符全转义、普通字符不动。
func TestEscapeMarkdownV2Mixed(t *testing.T) {
	if got := escapeMarkdownV2("a_b*c[d]e"); got != `a\_b\*c\[d\]e` {
		t.Fatalf("混排转义不对：%q", got)
	}
	// 反斜杠先转义 —— 原文的 `\n` 是两个字符，转完必须是 `\\n`（TG 才不会把 n 吃掉）。
	if got := escapeMarkdownV2(`a\b`); got != `a\\b` {
		t.Fatalf("反斜杠该先转义：%q", got)
	}
}

// 回退只认 `can't parse entities` 这个串。
func TestIsParseError(t *testing.T) {
	if !isParseError(fmt.Errorf("bad request, Bad Request: can't parse entities: ...")) {
		t.Fatal("含 can't parse entities 的该认成 parse 失败")
	}
	for _, err := range []error{
		nil,
		fmt.Errorf("too many requests: retry_after 3"),
		fmt.Errorf("bad request, something else"),
		fmt.Errorf("error decode response body for method sendMessage"),
	} {
		if isParseError(err) {
			t.Fatalf("%v 不该认成 parse 失败（429/别的 400 必须冒泡）", err)
		}
	}
}

// failFirstTelegram：第一次 sendMessage/editMessageText 按剧本报错，第二次记下 parse_mode 与文本。
// parseCalls 记录每次调用的 parse_mode（"" = 没带，即纯文本重发）；
// fallbackTexts/fallbackModes 只记第二次（回退那次）。
type failFirstTelegram struct {
	mu            sync.Mutex
	sendCalls     int
	editCalls     int
	parseModes    []string // 每次 sendMessage 的 parse_mode（edit 同理记 editModes）
	editModes     []string
	fallbackText  string
	fallbackMode  string
	fallbackEdit  string
	fallbackEditM string
	sendErr       error // 第一次 sendMessage 的错
	editErr       error // 第一次 editMessageText 的错
}

func (f *failFirstTelegram) handler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		f.mu.Lock()
		f.sendCalls++
		call := f.sendCalls
		f.parseModes = append(f.parseModes, r.FormValue("parse_mode"))
		if call == 2 {
			f.fallbackText, f.fallbackMode = r.FormValue("text"), r.FormValue("parse_mode")
		}
		err := f.sendErr
		f.mu.Unlock()
		if call == 1 && err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: ..."}`)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":42,"type":"private"},"text":""}}`, call)
	case strings.HasSuffix(r.URL.Path, "/editMessageText"):
		f.mu.Lock()
		f.editCalls++
		call := f.editCalls
		f.editModes = append(f.editModes, r.FormValue("parse_mode"))
		if call == 2 {
			f.fallbackEdit, f.fallbackEditM = r.FormValue("text"), r.FormValue("parse_mode")
		}
		err := f.editErr
		f.mu.Unlock()
		if call == 1 && err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: ..."}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"},"text":""}}`)
	default:
		http.NotFound(w, r)
	}
}
func newFailBot(t *testing.T, fake *failFirstTelegram) *bot.Bot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	b, err := bot.New("123:fake-token",
		bot.WithServerURL(server.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	if err != nil {
		t.Fatalf("建 bot 失败：%v", err)
	}
	return b
}

// 正常路径：sendMarkdown 带 MarkdownV2 发；editMarkdown 也是。
func TestSendEditMarkdownNormal(t *testing.T) {
	fake := &failFirstTelegram{}
	b := newFailBot(t, fake)
	ctx := context.Background()

	if _, err := sendMarkdown(ctx, b, 42, "a_b"); err != nil {
		t.Fatalf("正常发送不该错：%v", err)
	}
	if len(fake.parseModes) != 1 || fake.parseModes[0] != string(models.ParseModeMarkdown) {
		t.Fatalf("正常发送该带 MarkdownV2：%q", fake.parseModes)
	}

	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "x", CallbackData: "y"}},
	}}
	if _, err := editMarkdown(ctx, b, 42, 1, "a_b", markup); err != nil {
		t.Fatalf("正常 edit 不该错：%v", err)
	}
	if len(fake.editModes) != 1 || fake.editModes[0] != string(models.ParseModeMarkdown) {
		t.Fatalf("正常 edit 该带 MarkdownV2：%q", fake.editModes)
	}
	// 转义后的正文到了线上（fake 只按 FormValue 记文本，这儿顺手验一次）。
	if _, err := sendMarkdownWithMarkup(ctx, b, 42, "a_b", nil); err != nil {
		t.Fatalf("带 markup 版正常发送不该错：%v", err)
	}
}

// 正常路径的文本：转义后的发出去（fake 端按 FormValue 记 text）。
func TestSendMarkdownEscapesWireText(t *testing.T) {
	fakeTG := &fakeTelegram{}
	server := httptest.NewServer(http.HandlerFunc(fakeTG.handler))
	t.Cleanup(server.Close)
	b, err := bot.New("123:fake-token",
		bot.WithServerURL(server.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	if err != nil {
		t.Fatalf("建 bot 失败：%v", err)
	}
	if _, err := sendMarkdown(context.Background(), b, 42, "a_b*c"); err != nil {
		t.Fatalf("正常发送不该错：%v", err)
	}
	sent := fakeTG.messages()
	if len(sent) != 1 || sent[0].text != `a\_b\*c` {
		t.Fatalf("线上该是转义后的文本：%+v", sent)
	}
}

// 回退路径：第一次 400 can't parse entities ⇒ 第二次无 parse_mode、文本一致（原文，不转义）。
func TestSendMarkdownFallback(t *testing.T) {
	fake := &failFirstTelegram{sendErr: fmt.Errorf("seed")}
	b := newFailBot(t, fake)

	if _, err := sendMarkdown(context.Background(), b, 42, "a_b"); err != nil {
		t.Fatalf("回退后不该错：%v", err)
	}
	if fake.sendCalls != 2 {
		t.Fatalf("该发两次（先 MarkdownV2 再回退）：%d", fake.sendCalls)
	}
	if fake.parseModes[0] != string(models.ParseModeMarkdown) {
		t.Fatalf("第一次该带 MarkdownV2：%q", fake.parseModes)
	}
	if fake.fallbackMode != "" {
		t.Fatalf("回退那次不许带 parse_mode：%q", fake.fallbackMode)
	}
	if fake.fallbackText != "a_b" {
		t.Fatalf("回退该发原文（不转义）：%q", fake.fallbackText)
	}
}

// edit 回退同理：markup 原样透传（按钮不断）。
func TestEditMarkdownFallbackKeepsMarkup(t *testing.T) {
	fake := &failFirstTelegram{editErr: fmt.Errorf("seed")}
	b := newFailBot(t, fake)
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "x", CallbackData: "y"}},
	}}

	if _, err := editMarkdown(context.Background(), b, 42, 7, "a_b", markup); err != nil {
		t.Fatalf("回退后不该错：%v", err)
	}
	if fake.editCalls != 2 {
		t.Fatalf("该 edit 两次：%d", fake.editCalls)
	}
	if fake.fallbackEditM != "" {
		t.Fatalf("回退那次不许带 parse_mode：%q", fake.fallbackEditM)
	}
	if fake.fallbackEdit != "a_b" {
		t.Fatalf("回退该发原文：%q", fake.fallbackEdit)
	}
}

// 非 parse 错不回退：只发一次、原样返回（429 必须冒泡给上层节流）。
func TestSendMarkdownNonParseErrorBubbles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 3","parameters":{"retry_after":3}}`)
	}))
	t.Cleanup(server.Close)
	b, err := bot.New("123:fake-token",
		bot.WithServerURL(server.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	if err != nil {
		t.Fatalf("建 bot 失败：%v", err)
	}
	_, err = sendMarkdown(context.Background(), b, 42, "hi")
	if err == nil {
		t.Fatal("429 该冒泡，不该吞掉")
	}
	if !strings.Contains(err.Error(), "too many requests") {
		t.Fatalf("429 文案该透出来：%v", err)
	}
}
