package tg

// bot 命令面的单测：**不起真网络、不连真 TG**。
//
// 两条路：
//   - 纯函数（statusLines / resumeLines / modelButtons / parsePick / parseBlocks / parseModelData）——
//     喂结构体断言文案与按钮形状；
//   - Runner 的取数方法（cmdStatus / cmdResume …）—— 打一个 **httptest stub** 当后端，
//     这样连"四要素是不是真从四个接口拼出来的"也一起钉住（stub 说什么就该看到什么）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"microchat/internal/apiclient"
)

// stubClient：一个只认路径的假后端。
func stubClient(t *testing.T, handler http.HandlerFunc) *apiclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return apiclient.NewClient(server.URL, "test-token")
}

// ── 命令表 ─────────────────────────────────────────────────────────────

// 注册表：名字合法（Telegram 菜单只许 [a-z0-9_]）、有条数、help 不为空、run 都不缺。
func TestBotCommandTable(t *testing.T) {
	if len(botCommands) < 17 {
		t.Fatalf("命令太少（该覆盖 CLI 那一批）：%d", len(botCommands))
	}
	valid := regexp.MustCompile(`^[a-z0-9_]+$`)
	seen := map[string]bool{}
	for _, command := range botCommands {
		if !valid.MatchString(command.name) {
			t.Fatalf("命令名不合 Telegram 规矩：%q", command.name)
		}
		if len(command.name) > 32 {
			t.Fatalf("命令名太长：%q", command.name)
		}
		if strings.TrimSpace(command.help) == "" {
			t.Fatalf("%s 没有一句话说明", command.name)
		}
		if command.run == nil {
			t.Fatalf("%s 没有处理器", command.name)
		}
		if seen[command.name] {
			t.Fatalf("命令名重复：%s", command.name)
		}
		seen[command.name] = true
	}
	for _, want := range []string{"help", "status", "new", "resume", "rename", "copy", "delete",
		"cut", "stop", "compact", "state", "outgoing", "usage", "think", "system", "providers", "model"} {
		if !seen[want] {
			t.Fatalf("缺命令：/%s", want)
		}
	}
}

// 菜单与 /help 都从注册表派生（不许两处手写）。
func TestMenuAndHelpDeriveFromTable(t *testing.T) {
	if len(MenuCommands) != len(botCommands) {
		t.Fatalf("菜单与注册表条数不一致：%d vs %d", len(MenuCommands), len(botCommands))
	}
	help := helpText()
	for index, command := range botCommands {
		menu := MenuCommands[index]
		if menu.Command != command.name {
			t.Fatalf("菜单第 %d 条与注册表对不上：%q vs %q", index, menu.Command, command.name)
		}
		if strings.TrimSpace(menu.Description) == "" || len([]rune(menu.Description)) > 256 {
			t.Fatalf("%s 的菜单描述不合法：%q", command.name, menu.Description)
		}
		if strings.Contains(menu.Description, "\n") {
			t.Fatalf("%s 的菜单描述有换行", command.name)
		}
		if !strings.Contains(help, "/"+command.name) {
			t.Fatalf("/help 没列 %s", command.name)
		}
		if !strings.Contains(help, command.help) {
			t.Fatalf("/help 里 %s 的说明与注册表不一致", command.name)
		}
	}
	// 不搬的那些如实点名（不占位假装有）。
	for _, notSupported := range []string{"/provider-add", "/provider-del", "/reroll", "/telegram-", "/quit"} {
		if !strings.Contains(help, notSupported) {
			t.Fatalf("/help 尾注该点名没搬的：%s", notSupported)
		}
	}
}

// 派发：命中注册表；没登记过 ⇒ nil（那一条走"不认识"）。
func TestParseAndFindCommand(t *testing.T) {
	if name, args := parseCommand("/rename 我的 新名字"); name != "rename" || len(args) != 2 || args[1] != "新名字" {
		t.Fatalf("拆命令不对：%q %v", name, args)
	}
	if name, _ := parseCommand("/status@microchat_bot"); name != "status" {
		t.Fatalf("带 @bot 后缀该剥掉：%q", name)
	}
	if name, _ := parseCommand("/"); name != "" {
		t.Fatalf("光一个斜杠不算名字：%q", name)
	}
	if findCommand("status") == nil {
		t.Fatal("status 该在表里")
	}
	if findCommand("nope") != nil {
		t.Fatal("没登记的命令该返回 nil")
	}
}

// ── /status：CLI 底栏那份 ──────────────────────────────────────────────

// 四要素（会话 / 上下文 / 轮次 / TG）+ 后端版本；取不到的那几块如实标注。
func TestStatusLines(t *testing.T) {
	session := apiclient.Session{ID: "abcdefgh-1111", Title: "修 bug", Provider: "opencode-go", Model: "sonnet"}
	uptime := int64(3 * 60 * 1000)
	lines := statusLines(statusView{
		Session:   &session,
		Context:   &apiclient.ContextUsage{UsedTokens: 12345, BudgetTokens: 131000},
		Turn:      apiclient.TurnStatus{Phase: "streaming", ElapsedMS: 2400},
		Running:   true,
		AllowedID: 12345,
		UptimeMS:  &uptime,
		Version:   "0.1.2",
	})
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"会话：修 bug | abcdefgh | opencode-go/sonnet",
		"上下文：12.3k/131k (9.4%)",
		"轮次：生成中 2.4s",
		"TG：运行中 · 绑定 12345 · 在线 3m0s",
		"后端：v0.1.2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q：\n%s", want, text)
		}
	}

	// 空闲 + 没问到上下文 + 没跑：照实说，不装。
	idle := strings.Join(statusLines(statusView{Turn: apiclient.TurnStatus{Phase: "idle"}}), "\n")
	for _, want := range []string{"会话：没有（/new 建一条）", "上下文：—", "轮次：空闲", "TG：没在跑 · 还没绑定", "后端：—（没连上）"} {
		if !strings.Contains(idle, want) {
			t.Fatalf("缺 %q：\n%s", want, idle)
		}
	}
}

// /status 真从四个接口拼出来（stub 说什么就该看到什么）。
func TestCmdStatusGathersFromBackend(t *testing.T) {
	const id = "aaaaaaaa-1111-2222-3333"
	client := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			fmt.Fprint(w, `{"status":"ok","version":"0.1.2"}`)
		case "/api/v1/sessions":
			fmt.Fprint(w, `[{"id":"`+id+`","title":"修 bug","provider":"opencode-go","model":"sonnet","messages":4,"turn":{"phase":"idle"}}]`)
		case "/api/v1/sessions/" + id + "/status":
			fmt.Fprint(w, `{"phase":"streaming","elapsed_ms":2400}`)
		case "/api/v1/sessions/" + id + "/context":
			fmt.Fprint(w, `{"used_tokens":12345,"budget_tokens":131000,"over_budget":false}`)
		default:
			http.NotFound(w, r)
		}
	})
	runner := &Runner{api: client}
	text := runner.cmdStatus(context.Background())
	for _, want := range []string{
		"会话：修 bug | aaaaaaaa | opencode-go/sonnet",
		"上下文：12.3k/131k (9.4%)",
		"轮次：生成中 2.4s",
		"TG：",
		"后端：v0.1.2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q：\n%s", want, text)
		}
	}
}

// ── /resume：列出与切换 ────────────────────────────────────────────────

func TestResumeLines(t *testing.T) {
	sessions := []apiclient.Session{
		{ID: "aaaaaaaa-1111", Title: "甲"},
		{ID: "bbbbbbbb-2222", Title: ""},
	}
	lines := resumeLines(sessions, "aaaaaaaa-1111")
	text := strings.Join(lines, "\n")
	for _, want := range []string{"1. 甲 (aaaaaaaa) ← 当前", "2. （还没起名） (bbbbbbbb)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q：\n%s", want, text)
		}
	}
}

func TestCmdResumeListAndSwitch(t *testing.T) {
	client := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions" {
			fmt.Fprint(w, `[{"id":"aaaaaaaa-1111","title":"甲"},{"id":"bbbbbbbb-2222","title":"乙"}]`)
			return
		}
		http.NotFound(w, r)
	})
	runner := &Runner{api: client}

	text := runner.cmdResume(context.Background(), "")
	for _, want := range []string{"1. 甲 (aaaaaaaa)", "2. 乙 (bbbbbbbb)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("清单缺 %q：\n%s", want, text)
		}
	}

	// 切换：成功说清切到哪，且内存里的当前会话真换了。
	text = runner.cmdResume(context.Background(), "2")
	if !strings.Contains(text, "已切到：乙 (bbbbbbbb)") {
		t.Fatalf("切换回执不对：%q", text)
	}
	if got := runner.sessionID(); got != "bbbbbbbb-2222" {
		t.Fatalf("当前会话没跟着切：%q", got)
	}
	// 切完再列清单：当前那条该标出来。
	if text := runner.cmdResume(context.Background(), ""); !strings.Contains(text, "2. 乙 (bbbbbbbb) ← 当前") {
		t.Fatalf("清单没标当前：\n%s", text)
	}

	// 越界 / 非数字：说清是哪不对（不是一句 "error"）。
	if text := runner.cmdResume(context.Background(), "9"); !strings.Contains(text, "1–2") {
		t.Fatalf("越界该说范围：%q", text)
	}
	if text := runner.cmdResume(context.Background(), "abc"); !strings.Contains(text, "不是数字") {
		t.Fatalf("非数字该说清：%q", text)
	}
}

func TestParsePick(t *testing.T) {
	if index, err := parsePick("1", 3); err != nil || index != 0 {
		t.Fatalf("1 ⇒ 0：%d %v", index, err)
	}
	if _, err := parsePick("0", 3); err == nil || !strings.Contains(err.Error(), "1–3") {
		t.Fatalf("0 该越界：%v", err)
	}
	if _, err := parsePick("4", 3); err == nil {
		t.Fatal("4 超出 3 条该报错")
	}
	if _, err := parsePick("x", 3); err == nil || !strings.Contains(err.Error(), "不是数字") {
		t.Fatalf("非数字该报错：%v", err)
	}
	if _, err := parsePick("1", 0); err == nil || !strings.Contains(err.Error(), "一条会话都没有") {
		t.Fatalf("空清单该给出路：%v", err)
	}
}

// ── /model：按钮翻页 ───────────────────────────────────────────────────

func TestModelButtonsPaging(t *testing.T) {
	items := make([]apiclient.ModelListItem, 19) // 8 + 8 + 3 ⇒ 3 页
	for index := range items {
		items[index] = apiclient.ModelListItem{
			Provider: "opencode-go", UpstreamID: "model-" + strconv.Itoa(index), Name: "模型" + strconv.Itoa(index),
		}
	}
	rows, pages := modelButtons(items, 0)
	if pages != 3 {
		t.Fatalf("19 个该 3 页：%d", pages)
	}
	// 第一页：8 个模型 + 一行翻页。
	if len(rows) != 9 {
		t.Fatalf("第一页该 8 个模型 + 翻页行：%d 行", len(rows))
	}
	if got := len(rows[0][0].Text); got == 0 {
		t.Fatal("模型按钮得有个名字")
	}
	// 越界页码夹回最后一页：19 个 ⇒ 3 个模型 + 翻页行。
	if rows, _ := modelButtons(items, 99); len(rows) != 4 {
		t.Fatalf("越界页码该夹到末页：%d 行", len(rows))
	}
	// 一页装得下就不摆翻页行。
	if rows, pages := modelButtons(items[:3], 0); pages != 1 || len(rows) != 3 {
		t.Fatalf("3 个该一页三行：%d 页 %d 行", pages, len(rows))
	}
	if rows, pages := modelButtons(nil, 0); rows != nil || pages != 0 {
		t.Fatal("没有模型就没有按钮")
	}
}

// 每个按钮的 callback_data 都 ≤ 64 字节、前缀对（Telegram 硬上限）。
func TestModelButtonCallbackData(t *testing.T) {
	items := make([]apiclient.ModelListItem, 40) // 多页，翻页两种形状都出现
	for index := range items {
		items[index] = apiclient.ModelListItem{Provider: "p", UpstreamID: "m" + strconv.Itoa(index)}
	}
	for page := range 5 {
		rows, _ := modelButtons(items, page)
		if len(rows) == 0 {
			t.Fatalf("第 %d 页没有按钮", page)
		}
		for _, row := range rows {
			for _, button := range row {
				data := button.CallbackData
				if len(data) > 64 {
					t.Fatalf("callback_data 超 64 字节：%q（%d）", data, len(data))
				}
				if !strings.HasPrefix(data, cbModel) {
					t.Fatalf("callback_data 前缀不对：%q", data)
				}
				if _, _, err := parseModelData(data); err != nil {
					t.Fatalf("自己发的回调自己解不开：%q（%v）", data, err)
				}
			}
		}
	}
}

func TestParseModelData(t *testing.T) {
	if page, index, err := parseModelData("model:2"); err != nil || page != 2 || index != -1 {
		t.Fatalf("翻页形状：%d %d %v", page, index, err)
	}
	if page, index, err := parseModelData("model:2:13"); err != nil || page != 2 || index != 13 {
		t.Fatalf("选择形状：%d %d %v", page, index, err)
	}
	if _, _, err := parseModelData("model:x"); err == nil {
		t.Fatal("页码不是数字该报错")
	}
	if _, _, err := parseModelData("model:1:2:3"); err == nil {
		t.Fatal("多一段该报错")
	}
}

func TestModelPageText(t *testing.T) {
	items := []apiclient.ModelListItem{{Provider: "opencode-go", UpstreamID: "sonnet", Name: "sonnet"}}
	text := modelPageText(items, 0, 1)
	if !strings.Contains(text, "共 1 个") || !strings.Contains(text, "opencode-go/sonnet") {
		t.Fatalf("面板文案不对：%q", text)
	}
}

// ── 确认按钮 / 预览 / 参数解析 ─────────────────────────────────────────

// 确认按钮的 callback_data：前缀对、≤ 64 字节（操作细节不在里面，全在内存 pending 表）。
func TestConfirmCallbackData(t *testing.T) {
	key := "0123456789abcdef" // 4 字节随机数 hex 出来就是这个长度
	for _, data := range []string{confirmData(key), cancelData(key)} {
		if len(data) > 64 {
			t.Fatalf("callback_data 超 64 字节：%q", data)
		}
		if !strings.HasPrefix(data, cbConfirm) && !strings.HasPrefix(data, cbCancel) {
			t.Fatalf("前缀不对：%q", data)
		}
	}
}

func TestDeletionPreviewText(t *testing.T) {
	plan := apiclient.DeletionPlan{
		DeletedMessageIDs:    []string{"a", "b"},
		DeletedSummaryIDs:    []string{"s"},
		UnlinkedMessageIDs:   []string{"c"},
		LastDeletedMessageID: "abcdefgh-9999",
	}
	text := deletionPreviewText(plan, "第 3 条（assistant abcdefgh）")
	for _, want := range []string{"第 3 条", "消息 2 条、摘要 1 条", "末尾那条：abcdefgh", "确认后不可逆"} {
		if !strings.Contains(text, want) {
			t.Fatalf("预览缺 %q：%s", want, text)
		}
	}
}

func TestParseBlocks(t *testing.T) {
	if blocks, err := parseBlocks(""); err != nil || blocks != 0 {
		t.Fatalf("不给 N ⇒ 0（后端默认）：%d %v", blocks, err)
	}
	if blocks, err := parseBlocks("3"); err != nil || blocks != 3 {
		t.Fatalf("3 ⇒ 3：%d %v", blocks, err)
	}
	for _, arg := range []string{"0", "-1", "x"} {
		if _, err := parseBlocks(arg); err == nil {
			t.Fatalf("%q 该报错（说清怎么给）", arg)
		}
	}
}

// /cut 的参数解析：序号 / id 前缀 / 两种错都说得清。
func TestResolveMessage(t *testing.T) {
	client := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions/s1/messages" {
			fmt.Fprint(w, `[{"id":"aaaa1111-0000","role":"user","content":"你好"},
				{"id":"bbbb2222-0000","role":"assistant","content":"在"}]`)
			return
		}
		http.NotFound(w, r)
	})
	runner := &Runner{api: client}

	if message, label, err := runner.resolveMessage(context.Background(), "s1", "2"); err != nil || message.ID != "bbbb2222-0000" {
		t.Fatalf("序号解析不对：%v %v %q", message.ID, err, label)
	}
	if message, _, err := runner.resolveMessage(context.Background(), "s1", "aaaa"); err != nil || message.ID != "aaaa1111-0000" {
		t.Fatalf("id 前缀解析不对：%v %v", message.ID, err)
	}
	if _, _, err := runner.resolveMessage(context.Background(), "s1", "9"); err == nil || !strings.Contains(err.Error(), "1–2") {
		t.Fatalf("越界该说范围：%v", err)
	}
	if _, _, err := runner.resolveMessage(context.Background(), "s1", "zzz"); err == nil || !strings.Contains(err.Error(), "序号也行") {
		t.Fatalf("没有这个前缀该给出路：%v", err)
	}
	if _, _, err := runner.resolveMessage(context.Background(), "s1", ""); err == nil {
		t.Fatal("空参数该报错")
	}
}

// 注册表与菜单共用的 models 包不被忘在脑后：清菜单的作用域照旧四个。
func TestCommandScopesStillFour(t *testing.T) {
	var scopes []models.BotCommandScope = commandScopes()
	if len(scopes) != 4 {
		t.Fatalf("该四个作用域：%d", len(scopes))
	}
}

// ── 确认之后真动手 ─────────────────────────────────────────────────────

// 删会话：删完切最近一条；一条都没了就新建一条（与 CLI 口径一致）—— 两条路都走一遍。
func TestExecutePendingDeleteSettles(t *testing.T) {
	deleted := false
	created := false
	client := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sessions/aaa":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			if deleted {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[{"id":"aaa","title":"甲"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			created = true
			fmt.Fprint(w, `{"id":"bbb","title":""}`)
		default:
			http.NotFound(w, r)
		}
	})
	runner := &Runner{api: client}

	text := runner.executePending(context.Background(), &pendingOp{kind: "delete", sessionID: "aaa"})
	if !deleted || !created {
		t.Fatalf("该删了再建：deleted=%v created=%v", deleted, created)
	}
	if !strings.Contains(text, "又新建了一条") {
		t.Fatalf("回执该说清落到哪：%q", text)
	}
	if got := runner.sessionID(); got != "bbb" {
		t.Fatalf("当前会话该是新建那条：%q", got)
	}
}

// 删消息：last_deleted_message_id 必须**真的带上**（后端靠它核对"还是不是末尾"）。
func TestExecutePendingCutCarriesLastDeleted(t *testing.T) {
	var body map[string]any
	var path string
	client := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"deleted_message_ids":["m1","m2"],"deleted_summary_ids":["s1"],"last_deleted_message_id":"m2"}`)
	})
	runner := &Runner{api: client}

	text := runner.executePending(context.Background(), &pendingOp{
		kind: "cut", sessionID: "s1", messageID: "m1", lastDeleted: "m2",
	})
	if path != "/api/v1/sessions/s1/messages/m1" {
		t.Fatalf("删的路径不对：%q", path)
	}
	if body["last_deleted_message_id"] != "m2" {
		t.Fatalf("没带上 last_deleted_message_id：%v", body)
	}
	if !strings.Contains(text, "消息 2 条、摘要 1 条") {
		t.Fatalf("回执该报删了多少：%q", text)
	}
}

// 一条会话都没有时 /delete：如实说，不偷偷建一条再删。
func TestCmdDeleteWithoutSessions(t *testing.T) {
	runner, fake := newFakeRunner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions" {
			fmt.Fprint(w, `[]`)
			return
		}
		t.Errorf("空清单不该再发别的请求：%s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	runner.onMessage(context.Background(), runner.bot, textUpdate("/delete", 42))
	sent := fake.messages()
	if len(sent) != 1 || !strings.Contains(sent[0].text, "没得删") {
		t.Fatalf("该如实说没得删：%+v", sent)
	}
}
