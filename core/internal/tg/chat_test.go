package tg

// 纯文本消息"真发一轮"的冒烟测试：假 Telegram（见 dispatch_test.go）+ 脚本化假后端。
//
// 测的是**真跑起来会走哪条路**：注册 → 派发 → 发进会话 → 轮询 → 节流 edit 把回复长出来。
// 轮询间隔是 700ms，所以这两个"跑满一轮"的用例会各等约一拍（可接受）。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"microchat/internal/apiclient"
)

// 真发一轮：受理 202 ⇒ 轮询先给半段、再给终稿 ⇒ 占位消息先出现、被 edit 成终稿。
func TestPlainTextRunsTurn(t *testing.T) {
	const sid = "aaaaaaaa-1111"
	content := ""
	polls := 0
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"`+sid+`","title":"甲","provider":"p","model":"m","turn":{"phase":"idle"}}]`)
		case "/api/v1/sessions/" + sid + "/messages":
			raw := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(raw)
			content = string(raw)
			fmt.Fprint(w, `{"backend":"fake","turn":{"phase":"pending","message_id":"m1"}}`)
		case "/api/v1/sessions/" + sid + "/status":
			polls++
			if polls == 1 {
				fmt.Fprint(w, `{"phase":"streaming","elapsed_ms":2400}`)
			} else {
				fmt.Fprint(w, `{"phase":"idle"}`)
			}
		case "/api/v1/sessions/" + sid + "/turn/text":
			if polls <= 1 {
				fmt.Fprint(w, `{"text":"说到一半","next":12}`)
			} else {
				fmt.Fprint(w, `{"text":"，接着说完了。","next":24,"done":true}`)
			}
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("你好", 42))

	if !strings.Contains(content, "你好") {
		t.Fatalf("那句话没发进会话：%q", content)
	}
	sent := fake.messages()
	if len(sent) == 0 || sent[0].text != turnPlaceholder {
		t.Fatalf("该先摆占位消息：%+v", sent)
	}
	edits := fake.edits()
	if len(edits) == 0 || !contains(edits, "说到一半") {
		t.Fatalf("生成中该把半段 edit 上去：%+v", edits)
	}
	if edits[len(edits)-1] != "说到一半，接着说完了。" {
		t.Fatalf("终稿没 sync 到占位消息：%+v", edits)
	}
}

// 超长（>4096 rune）：第一条摆不下 ⇒ 第二条消息被发出（分段），且第二条正好是剩下的 904 字。
func TestPlainTextLongTurnSplits(t *testing.T) {
	const sid = "bbbbbbbb-2222"
	long := strings.Repeat("字", 5000)
	polls := 0
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"`+sid+`","title":"乙","turn":{"phase":"idle"}}]`)
		case "/api/v1/sessions/" + sid + "/messages":
			fmt.Fprint(w, `{"backend":"fake","turn":{"phase":"pending"}}`)
		case "/api/v1/sessions/" + sid + "/status":
			polls++
			if polls == 1 {
				fmt.Fprint(w, `{"phase":"streaming","elapsed_ms":10}`)
			} else {
				fmt.Fprint(w, `{"phase":"idle"}`)
			}
		case "/api/v1/sessions/" + sid + "/turn/text":
			if polls <= 1 {
				fmt.Fprintf(w, `{"text":%q,"next":5000}`, long)
			} else {
				fmt.Fprint(w, `{"text":"","next":5000,"done":true}`)
			}
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("给我长文", 42))

	sent := fake.messages()
	if len(sent) != 2 {
		t.Fatalf("超长该发成两条：%d", len(sent))
	}
	if sent[0].text != turnPlaceholder {
		t.Fatalf("第一条该是占位消息：%q", sent[0].text)
	}
	if got := utf8.RuneCountInString(sent[1].text); got != 904 {
		t.Fatalf("补发的第二条该是剩下的 904 字：%d", got)
	}
	first := string([]rune(long)[:tgMaxLen])
	if !contains(fake.edits(), first) {
		t.Fatalf("占位消息该被 edit 成第 1 段的 %d 字（分段前先占满第一条）", tgMaxLen)
	}
}

// 409（同一会话还在跑）：明说"还在跑"，不再发消息、也不进轮询。
func TestPlainTextConflict(t *testing.T) {
	const sid = "cccccccc-3333"
	polled := false
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"`+sid+`","title":"丙","turn":{"phase":"idle"}}]`)
		case "/api/v1/sessions/" + sid + "/messages":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":"conflict","message":"会话还在生成中"}}`)
		case "/api/v1/sessions/" + sid + "/status":
			polled = true
			fmt.Fprint(w, `{"phase":"streaming"}`)
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("再问一句", 42))

	sent := fake.messages()
	if len(sent) != 1 || !strings.Contains(sent[0].text, "上一轮还在跑") {
		t.Fatalf("该只回一句「还在跑」：%+v", sent)
	}
	if polled {
		t.Fatal("409 不该进轮询")
	}
}

// 生成失败（phase=error）：占位消息被 edit 成失败原因。
func TestPlainTextTurnError(t *testing.T) {
	const sid = "dddddddd-4444"
	backend := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"`+sid+`","title":"丁","turn":{"phase":"idle"}}]`)
		case "/api/v1/sessions/" + sid + "/messages":
			fmt.Fprint(w, `{"backend":"fake","turn":{"phase":"pending"}}`)
		case "/api/v1/sessions/" + sid + "/status":
			fmt.Fprint(w, `{"phase":"error","error":"上游 500"}`)
		default:
			http.NotFound(w, r)
		}
	}
	runner, fake := newFakeRunner(t, backend)
	runner.bot.ProcessUpdate(context.Background(), textUpdate("问一句", 42))

	edits := fake.edits()
	if len(edits) == 0 || edits[len(edits)-1] != "生成失败：上游 500" {
		t.Fatalf("失败该把原因写到占位消息上：%+v", edits)
	}
}

// 生成中的占位口径：没正文就摆计时；只有思考就报字数；有正文就摆正文。
func TestTurnDisplayPlaceholder(t *testing.T) {
	if got := turnDisplay("", "", apiclient.TurnStatus{ElapsedMS: 2400}); len(got) != 1 || got[0] != "生成中… 2.4s" {
		t.Fatalf("没正文该摆计时：%q", got)
	}
	if got := turnDisplay("", "嗯嗯嗯", apiclient.TurnStatus{}); len(got) != 1 || got[0] != "思考中… 3 字" {
		t.Fatalf("只有思考该报字数：%q", got)
	}
	if got := turnDisplay("正文", "嗯嗯嗯", apiclient.TurnStatus{}); len(got) != 1 || got[0] != "正文" {
		t.Fatalf("有正文该摆正文：%q", got)
	}
}

// 节流与"没变不 edit"：同一条消息 1s 内不重复 edit（force 例外）；文本没变一个 edit 都不发。
func TestTurnStreamThrottlesEdits(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("这条用例不该打后端：%s", r.URL.Path)
	})
	stream := &turnStream{
		chatID: 42, ids: []int{1}, written: []string{turnPlaceholder}, lastEdit: make([]time.Time, 1),
	}
	ctx := context.Background()
	stream.sync(ctx, runner.bot, []string{"A"}, false) // 第一次 edit：放行
	stream.sync(ctx, runner.bot, []string{"B"}, false) // 立刻再来：节流挡住
	stream.sync(ctx, runner.bot, []string{"B"}, true)  // 终稿 force：绕过节流
	stream.sync(ctx, runner.bot, []string{"B"}, true)  // 文本没变：不发

	if got := fake.edits(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("节流 / force / 没变不 edit 的口径不对：%+v", got)
	}
}

// contains：切片里有没有这一项（小工具，省得每处写循环）。
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
