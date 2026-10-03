package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"microchat/internal/apiclient"
)

// ── `/rename`：给当前会话改名（复用已有的 PATCH 路由，**不新增路由**）──

// 名字里的空格：整串拼回来（`/rename 我的 新名字`）；带引号就剥掉那对引号；空名字 ⇒ 拒绝。
func TestRenameArgParsing(t *testing.T) {
	for _, probe := range []struct {
		name string
		args []string
		want string
	}{
		{"单个词", []string{"标题"}, "标题"},
		{"整串拼回", []string{"我的", "新名字"}, "我的 新名字"},
		{"双引号", []string{`"我的`, `新名字"`}, "我的 新名字"},
		{"单引号", []string{`'我的`, `新名字'`}, "我的 新名字"},
		{"只有空白", []string{"   "}, ""},
		{"引号里是空的", []string{`""`}, ""},
		{"没给", nil, ""},
		{"只开了半个引号", []string{`"半截`}, `"半截`},
	} {
		if got := renameArg(probe.args); got != probe.want {
			t.Fatalf("%s：renameArg(%q) = %q，该是 %q", probe.name, probe.args, got, probe.want)
		}
	}
}

func TestRenameCommandNeedsAName(t *testing.T) {
	m := fixture()
	// 不给参数 ⇒ 只说用法，**不发请求**
	updated, cmd := m.runCommand("/rename")
	if cmd != nil {
		t.Fatal("没给名字不该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "用法") || !strings.Contains(said, "空名字不行") {
		t.Fatalf("该说清用法：%q", said)
	}
	// 名字是空的（引号里空的）⇒ 一样拒绝
	if _, cmd := m.runCommand(`/rename ""`); cmd != nil {
		t.Fatal("空名字该拒绝")
	}
	// 没有可用会话（兵灾态）⇒ 没有可改名的
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	if _, cmd := empty.runCommand("/rename 名字"); cmd != nil {
		t.Fatal("没有会话时不该发请求")
	}
	// 给了名字 ⇒ 真发一条 PATCH
	updated, cmd = m.runCommand("/rename 我的 新名字")
	if cmd == nil {
		t.Fatal("给了名字该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "改名中") {
		t.Fatalf("底栏该说在改：%q", said)
	}
}

// 改名回来 ⇒ 列表/状态行里的会话名**立刻**跟上（不许等下一次刷新）。
func TestRenameUpdatesSessionLocally(t *testing.T) {
	m := fixture()
	renamed := Session{
		ID: "c1", Title: "我的 新名字", Provider: "opencode-go", Model: "deepseek-v4-flash", AgentID: "跑团",
	}
	updated, _ := m.Update(renamedMsg{session: renamed})
	done := updated.(model)
	if got := done.currentSession(); got == nil || got.Title != "我的 新名字" {
		t.Fatalf("列表里那条该就地换掉：%+v", got)
	}
	if !strings.Contains(done.View().Content, "我的 新名字") {
		t.Fatalf("状态行该立刻显示新名字：\n%s", done.View().Content)
	}
	if !strings.Contains(done.lastAction, "已改名") {
		t.Fatalf("底栏该报成功：%q", done.lastAction)
	}
	// 失败 ⇒ 红着说清原因（不改本地那份）
	failed, _ := m.Update(renamedMsg{err: errors.New("404 not_found")})
	if !strings.Contains(failed.(model).lastAction, "改名失败") {
		t.Fatalf("失败该说话：%q", failed.(model).lastAction)
	}
}

// ── `/system`：消息区顶部那条"系统提示词"（调试用；开关记在本地）──

// systemFixture：进了一条会话、拉到了它的系统提示词、开关是开的。
func systemFixture() model {
	m := fixture()
	m.showSystemPrompt = true
	m.prompt = &SystemPrompt{Text: "你是跑团主持人。", Type: "agent"}
	m.promptFor = "c1"
	return m
}

// messageArea：只看**消息区**（输入行往上）—— 底栏那句话里也有"系统提示词"这几个字，
// 拿整屏断言会把"底栏报了一句"读成"那条还画着"。
func messageArea(m model) string {
	lines := viewLines(m)
	return strings.Join(lines[:inputLineIndex(lines)], "\n")
}

// drainCmd：把一个 Cmd 真跑到底（`/system` 回的是 `tea.Batch` ⇒ 里面的子命令要挨个跑），
// 返回它散出来的消息。
func drainCmd(cmd tea.Cmd) []tea.Msg {
	msgs := []tea.Msg{}
	var walk func(c tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		if batch, ok := c().(tea.BatchMsg); ok {
			for _, sub := range batch {
				walk(sub)
			}
			return
		}
		msgs = append(msgs, c())
	}
	walk(cmd)
	return msgs
}

// 开着 ⇒ 那条画在**消息区最上面**（暗色标签 + 正文）；关掉 ⇒ 它没了（消息照旧）。
func TestSystemPromptShowsAtTopAndHides(t *testing.T) {
	m := systemFixture()
	area := messageArea(m)
	if !strings.Contains(area, "系统提示词：") || !strings.Contains(area, "你是跑团主持人。") {
		t.Fatalf("该画出系统提示词：\n%s", area)
	}
	if strings.Index(area, "系统提示词") > strings.Index(area, "在吗") {
		t.Fatalf("该在最上面（第一条消息之前）：\n%s", area)
	}
	// 标签是**暗色**（调色板里 system 那一档）
	if !strings.Contains(messageArea(colored(m)), "\x1b[2m 系统提示词：\x1b[0m") {
		t.Fatalf("标签该是暗色：\n%q", messageArea(colored(m)))
	}

	// `/system` ⇒ 关掉（那条消失，消息照旧）
	updated, cmd := m.runCommand("/system")
	off := updated.(model)
	if cmd == nil {
		t.Fatal("/system 该把开关写下去")
	}
	if off.showSystemPrompt {
		t.Fatal("该关掉")
	}
	offArea := messageArea(off)
	if strings.Contains(offArea, "系统提示词") || strings.Contains(offArea, "你是跑团主持人。") {
		t.Fatalf("关掉后那条不该再画：\n%s", offArea)
	}
	if !strings.Contains(offArea, "在吗") || !strings.Contains(offArea, "有什么事？") {
		t.Fatalf("消息照旧：\n%s", offArea)
	}
	// 再切一次 ⇒ 又回来
	again, _ := off.runCommand("/system")
	if on := again.(model); !on.showSystemPrompt || !strings.Contains(messageArea(on), "系统提示词") {
		t.Fatalf("再开该回来：\n%s", messageArea(on))
	}
}

// 换会话时那份还旧着 ⇒ **不许拿来画**（与上下文占用同一条规矩）。
func TestSystemPromptIsNotDrawnForAnotherSession(t *testing.T) {
	m := systemFixture()
	m.promptFor = "c2"
	if strings.Contains(m.View().Content, "系统提示词") {
		t.Fatalf("不是这条会话的提示词不该画：\n%s", m.View().Content)
	}
}

// 进会话（拿到消息）时顺手拉一次 —— 与上下文占用同一个时机（`/system` 打开时也会补拉）。
func TestSystemPromptIsFetchedOnEnteringSession(t *testing.T) {
	m := fixture()
	m.contextFor = "c1" // 上下文已经量过了 ⇒ 这一趟该剩下的只有"拉系统提示词"
	if _, cmd := m.Update(messagesMsg{sessionID: "c1", messages: m.messages}); cmd == nil {
		t.Fatal("进会话该顺手拉一次生效的系统提示词")
	}
	// 回执存进 model，并记住它属于哪条会话
	updated, _ := m.Update(promptMsg{sessionID: "c1", prompt: SystemPrompt{Text: "底子", Type: "builtin"}})
	stored := updated.(model)
	if stored.prompt == nil || stored.prompt.Text != "底子" || stored.promptFor != "c1" {
		t.Fatalf("该把结果记下来：%+v（for=%q）", stored.prompt, stored.promptFor)
	}
	// 拉不到就只是不画 —— 不许去打扰底栏
	quiet, _ := m.Update(promptMsg{sessionID: "c1", err: errors.New("500 internal")})
	if quiet.(model).prompt != nil || quiet.(model).lastAction != m.lastAction {
		t.Fatalf("拉不到该静默：%+v / %q", quiet.(model).prompt, quiet.(model).lastAction)
	}
}

// 装不下就按现有规矩：**块不劈开、从顶部挤掉**（这条摆在最上面 ⇒ 最先被挤掉）。
func TestSystemPromptBlockIsPushedOffTheTop(t *testing.T) {
	m := systemFixture()
	m.height = 8 // 只装得下最新那一块加顶部那行提示（同 TestDroppedMessagesAreAnnounced）
	lines := viewLines(m)
	if len(lines) != m.height {
		t.Fatalf("该正好 %d 行：%d", m.height, len(lines))
	}
	body := strings.Join(lines, "\n")
	if strings.Contains(body, "你是跑团主持人。") {
		t.Fatalf("装不下就不画（整块挤掉）：\n%s", body)
	}
	if !strings.Contains(body, "上面还有") {
		t.Fatalf("挤掉了要如实报数：\n%s", body)
	}
}

// wroteUIPref：这批消息里有没有一次**成功**的偏好落盘。
func wroteUIPref(msgs []tea.Msg) bool {
	for _, msg := range msgs {
		if pref, ok := msg.(uiPrefMsg); ok && pref.err == nil {
			return true
		}
	}
	return false
}

// 开关记在 `~/.config/microchat/tui.json`（**不进**后端 config/ 与 data/）—— 重启 TUI 要读得回来。
func TestSystemTogglePersistsToTuiJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "microchat", "tui.json")
	m := fixture()
	m.uiPath = path
	m.showSystemPrompt = true

	updated, cmd := m.runCommand("/system")
	if cmd == nil {
		t.Fatal("/system 该写一次偏好")
	}
	msgs := drainCmd(cmd)
	if !wroteUIPref(msgs) {
		t.Fatalf("该把偏好写下去：%#v", msgs)
	}
	if loadShowSystemPrompt(path) {
		t.Fatal("关掉的开关该记得住")
	}
	// 文件里那一格是**明确写着 false** 的（`*bool` ⇒ "没写"与"写了 false"是两件事）
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"show_system_prompt": false`) {
		t.Fatalf("文件里该明确写着 false：%s", body)
	}
	// 再切一次 ⇒ 写回 true
	updated, cmd = updated.(model).runCommand("/system")
	if cmd == nil {
		t.Fatal("再切该再写一次")
	}
	if !wroteUIPref(drainCmd(cmd)) {
		t.Fatal("再切也该写下去")
	}
	if !loadShowSystemPrompt(path) {
		t.Fatal("该记住开着")
	}
	// 文件缺失 / 读坏 / 没写这一格 ⇒ **默认开**
	if !loadShowSystemPrompt(filepath.Join(t.TempDir(), "没有这个文件.json")) {
		t.Fatal("没有文件该按默认（开）")
	}
	broken := filepath.Join(t.TempDir(), "tui.json")
	if err := os.WriteFile(broken, []byte("{坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !loadShowSystemPrompt(broken) {
		t.Fatal("读坏了该按默认（开）")
	}
	empty := filepath.Join(t.TempDir(), "tui.json")
	if err := os.WriteFile(empty, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !loadShowSystemPrompt(empty) {
		t.Fatal("没写那一格该按默认（开）")
	}
}

// 打开时顺手补拉一次（关着的时候可能从没拉过 / 手里那份是上一条会话的）。
func TestSystemOnFetchesWhenMissing(t *testing.T) {
	m := fixture() // 开关默认关（零值），也还没拉过
	updated, cmd := m.runCommand("/system")
	if !updated.(model).showSystemPrompt {
		t.Fatal("该打开")
	}
	if cmd == nil {
		t.Fatal("打开时该顺手拉一次（并写偏好）")
	}
	// 已经拉过这条会话的 ⇒ 不必再拉（只写偏好）
	m.prompt, m.promptFor = &SystemPrompt{Text: "底子"}, "c1"
	if _, again := m.runCommand("/system"); again == nil {
		t.Fatal("写偏好那一下总要有")
	}
}

// 思考流的呈现口径：有 `reasoning` 的消息默认只显示**折叠行**「思考（2.1s）」；
// `/think` ⇒ 查看器里展开全文；没思考的消息不出现折叠行。
func TestThinkingFoldsThenThinkExpands(t *testing.T) {
	ms := int64(2100)
	m := fixture()
	m.messages = []Message{
		{ID: "m1", Role: "user", Content: "在吗"},
		{ID: "m2", Role: "assistant", Content: "在。", Reasoning: "先想一下\n再回答", ReasoningMS: &ms},
	}
	view := m.View().Content
	if !strings.Contains(view, "思考（2.1s）") {
		t.Fatalf("有思考的消息该有一行折叠的「思考（2.1s）」：\n%s", view)
	}
	if strings.Contains(view, "先想一下") {
		t.Fatalf("默认（折叠）不该显示思考全文：\n%s", view)
	}

	next, _ := m.runCommand("/think")
	after, ok := next.(model)
	if !ok || after.viewer == nil {
		t.Fatalf("/think 该打开查看器：%#v", next)
	}
	joined := strings.Join(after.viewer, "\n")
	if !strings.Contains(joined, "先想一下") || !strings.Contains(joined, "再回答") {
		t.Fatalf("/think 该展开思考全文：%q", joined)
	}

	// 没有思考的消息 ⇒ 不该出现折叠行；/think 如实说一句
	plain := fixture()
	plain.messages = []Message{{ID: "m1", Role: "user", Content: "在吗"}}
	if strings.Contains(plain.View().Content, "思考（") {
		t.Fatalf("没思考的消息不该有折叠行：\n%s", plain.View().Content)
	}
	next, _ = plain.runCommand("/think")
	plainAfter := next.(model)
	if plainAfter.viewer != nil {
		t.Fatal("没有思考时 /think 不该打开查看器")
	}
	if !strings.Contains(plainAfter.lastAction, "没有思考") {
		t.Fatalf("该如实说没有思考：%q", plainAfter.lastAction)
	}
}

// 生成中若已拿到"受理 → 第一段正文"的用时，思考行一并报出来（口径同落档后的 `reasoning_ms`）。
func TestLiveThinkingShowsElapsed(t *testing.T) {
	ms := int64(2100)
	m := liveFixture()
	m.turn.thinkMS = &ms
	if !strings.Contains(m.View().Content, "思考中… 3 字 · 2.1s") {
		t.Fatalf("生成中的思考行该带上用时：\n%s", m.View().Content)
	}
}

// `/usage`：进命令表；查回来用查看器逐窗口渲染（标签、百分比、重置时刻）。
func TestUsageCommandRendersWindows(t *testing.T) {
	found := false
	for _, cmd := range commands {
		if cmd.name == "usage" {
			found = true
		}
	}
	if !found {
		t.Fatal("/usage 该在命令表里")
	}

	m := fixture() // 当前会话 c1 的 provider = opencode-go
	next, run := m.runCommand("/usage")
	if run == nil {
		t.Fatal("/usage 该发起一次查询")
	}
	if !strings.Contains(next.(model).lastAction, "查余量") {
		t.Fatalf("底部该说在查：%q", next.(model).lastAction)
	}

	updated, _ := m.Update(usageMsg{
		providerID: "opencode-go",
		usage: PlanUsage{
			ProviderID: "opencode-go", Plan: "OpenCode Go",
			Windows: []PlanWindow{
				{ID: "rolling-5h", Label: "Rolling (5h)", Percent: 6, Status: "ok",
					ResetsAt: time.Date(2026, 9, 30, 13, 47, 1, 0, time.UTC)},
				{ID: "weekly", Label: "Weekly", Percent: 100, Status: "rate-limited",
					ResetsAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)},
			},
		},
	})
	after := updated.(model)
	if after.viewer == nil {
		t.Fatal("/usage 该打开查看器")
	}
	joined := strings.Join(after.viewer, "\n")
	for _, want := range []string{"Rolling (5h)", "6%", "重置 2026-09-30T13:47Z", "Weekly", "100%"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("查看器缺 %q：\n%s", want, joined)
		}
	}

	// 渠道不是 opencode-go ⇒ 如实说一句（后端 400 的消息透出来）
	bad, _ := m.Update(usageMsg{providerID: "dummy", err: errors.New("invalid（400）：渠道 dummy 不是 opencode-go")})
	if bad.(model).viewer != nil {
		t.Fatal("出错时不该打开查看器")
	}
	if !strings.Contains(bad.(model).lastAction, "查余量失败") {
		t.Fatalf("该如实报错：%q", bad.(model).lastAction)
	}
}

// ── 重摇模式：位次标记 / 「重摇中…」 / 那几条命令 ──────────────────────────

// followOnce：喂一条消息、**再追一层**它派生的 Cmd（`runCmds` 只跑一层）。
//
// 重摇那两条指令会派生出"重拉消息"（切换是**写库**的：那条消息的正文与附带信息都换了）
// —— 测试要看的就是"追完之后界面上是什么"，所以这一层得自己追。
func followOnce(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	updated, follow := m.Update(msg)
	return runCmds(t, updated.(model), follow)
}

// rerollFixture：重摇模式内的样子（尾条 m2 旁边该出现 `‹ 2/3 ›`）。
func rerollFixture() model {
	m := fixture()
	m.reroll = &RerollState{
		Active: true, TargetKind: RerollKindMessage, TargetMessageID: "m2", Count: 3, CurrentIdx: 2,
		Items: []RerollItem{
			{Idx: 1, Preview: "在。有什么事？"},
			{Idx: 2, Preview: "在的，说说看。", Current: true},
			{Idx: 3, Preview: "在。", Pending: true},
		},
	}
	m.rerollFor = "c1"
	return m
}

// 位次标记：**只在重摇模式里**、只画给那条被摇的尾条；分子是当前 apply 的那一版。
func TestRerollMarkerRendersOnTargetOnly(t *testing.T) {
	m := rerollFixture()
	view := m.View().Content
	if !strings.Contains(view, "‹ 2/3 ›") {
		t.Fatalf("模式内该在尾条旁画位次：\n%s", view)
	}
	// 标记挂在**助手那条**的署名行上（不在用户那条上）
	for _, line := range viewLines(m) {
		if strings.Contains(line, "‹") && !strings.Contains(line, "助手") {
			t.Fatalf("位次标记该跟着助手那条的署名：%q", line)
		}
	}
	// 当前那版高亮（粗体 + 青）—— 与挑选项的选中口径同一条
	coloredView := colored(m).View().Content
	if !strings.Contains(coloredView, "\x1b[1;36m ‹ 2/3 ›\x1b[0m") {
		t.Fatalf("当前那版的标记该高亮：\n%q", coloredView)
	}

	// 退出模式 ⇒ 标记消失
	off := m
	off.reroll = &RerollState{Active: false}
	if strings.Contains(off.View().Content, "‹") {
		t.Fatal("不在模式里就不该有标记")
	}
	// 换会话 ⇒ 那一份属于别人，不许画
	elsewhere := m
	elsewhere.selectedID = "c2"
	if strings.Contains(elsewhere.View().Content, "‹") {
		t.Fatal("换会话之后不许拿旧会话的位次画")
	}
}

// 「重摇中…」：状态行那一档 + 消息区那个合成块（**都是在摇时的真话**）；此时不许再发消息。
func TestRerollRunningIsVisibleAndBlocksSend(t *testing.T) {
	m := rerollFixture()
	m.reroll.Running, m.reroll.ElapsedMS = true, 1200
	view := m.View().Content
	if !strings.Contains(view, "重摇中 1.2s") {
		t.Fatalf("状态行该报重摇中与耗时：\n%s", view)
	}
	if !strings.Contains(view, "重摇中… 1.2s") {
		t.Fatalf("消息区该有那个合成块（别假装正文在往外蹦）：\n%s", view)
	}
	// 摇完了：不再说"重摇中"，但位次还在
	done := m
	done.reroll = &RerollState{Active: true, TargetMessageID: "m2", Count: 2, CurrentIdx: 2}
	if got := done.View().Content; strings.Contains(got, "重摇中") || !strings.Contains(got, "‹ 2/2 ›") {
		t.Fatalf("摇完该只剩位次：\n%s", got)
	}
	// 在摇时发消息：**本地就拦下**（后端也会 409 —— 两边说的是同一件事）
	blocked, cmd := m.send("插队的一句")
	if cmd != nil {
		t.Fatal("重摇在跑时不该真发出去")
	}
	if !strings.Contains(blocked.(model).lastAction, "正在重摇") {
		t.Fatalf("该说清是重摇在跑：%q", blocked.(model).lastAction)
	}
}

// `/reroll` 的命令解析：用法错当场说清；`off` 走退出；`list` 摊开每一版。
func TestCommandRerollUsageAndList(t *testing.T) {
	m := rerollFixture()
	// 用法错
	if bad, _ := commandReroll(m, []string{"switch"}); !strings.Contains(bad.(model).lastAction, "用法") {
		t.Fatalf("缺位次该说清用法：%q", bad.(model).lastAction)
	}
	if bad, _ := commandReroll(m, []string{"switch", "0"}); !strings.Contains(bad.(model).lastAction, "正整数") {
		t.Fatalf("位次非正该拒绝：%q", bad.(model).lastAction)
	}
	if bad, _ := commandReroll(m, []string{"乱来"}); !strings.Contains(bad.(model).lastAction, "不认识的用法") {
		t.Fatalf("不认识的用法该说清：%q", bad.(model).lastAction)
	}
	// `list`：铺满消息区的查看器，当前那版标出来、在摇的那版标出来
	listed, _ := commandReroll(m, []string{"list"})
	if listed.(model).viewer == nil {
		t.Fatal("/reroll list 该打开查看器")
	}
	joined := strings.Join(listed.(model).viewer, "\n")
	for _, want := range []string{"第 1 版", "第 2 版", "（当前）", "第 3 版", "（还在摇…）", "在的，说说看。"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("查看器缺 %q：\n%s", want, joined)
		}
	}
	// 没在模式里就如实说
	if plain, _ := commandReroll(fixture(), []string{"list"}); !strings.Contains(plain.(model).lastAction, "没在重摇模式里") {
		t.Fatalf("没进模式该说清：%q", plain.(model).lastAction)
	}
	// `off`：真发那一条 DELETE（204 就算成功）
	hits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/sessions/c1/reroll-message" {
			http.NotFound(w, r)
			return
		}
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	off := rerollFixture()
	off.client = NewClient(backend.URL, "")
	_, cmd := commandReroll(off, []string{"off"})
	after := runCmds(t, off, cmd)
	if hits != 1 {
		t.Fatalf("该真发一次 DELETE /reroll-message，实际 %d 次", hits)
	}
	if after.reroll != nil || !strings.Contains(after.lastAction, "已退出重摇模式") {
		t.Fatalf("退出之后本地那一份该清掉：%+v / %q", after.reroll, after.lastAction)
	}
}

// 进模式 / 切换 / 删除：命令真打后端，回什么写什么（位次、底栏、切换后重拉消息）。
func TestCommandRerollRoundTrip(t *testing.T) {
	var switchIdx int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/c1/reroll-message":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(RerollAccepted{
				TargetMessageID: "m2", TaskID: "t1",
				State: RerollState{Active: true, TargetKind: RerollKindMessage, TargetMessageID: "m2", Count: 2, CurrentIdx: 1, Running: true,
					Items: []RerollItem{{Idx: 1, Preview: "在。", Current: true}, {Idx: 2, Pending: true}}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/c1/reroll-message":
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetKind: RerollKindMessage, TargetMessageID: "m2", Count: 2, CurrentIdx: 1,
				Items: []RerollItem{{Idx: 1, Preview: "在。", Current: true}, {Idx: 2, Preview: "在的。"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/c1/reroll-message/switch":
			var body struct {
				Idx int `json:"idx"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switchIdx = body.Idx
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetKind: RerollKindMessage, TargetMessageID: "m2", Count: 2, CurrentIdx: 2,
				Items: []RerollItem{{Idx: 1, Preview: "在。"}, {Idx: 2, Preview: "在的。", Current: true}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/c1/messages":
			_ = json.NewEncoder(w).Encode([]Message{
				{ID: "m1", Role: "user", Content: "在吗"},
				{ID: "m2", Role: "assistant", Content: "在的。"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	m := fixture()
	m.client = NewClient(backend.URL, "")
	m.messagesFor = "c1"

	// `/reroll` ⇒ 202 那一刻就该有两条（原文 + 正在摇的那版），并约下一次轮询
	_, cmd := commandReroll(m, nil)
	entered := runCmds(t, m, cmd)
	if entered.reroll == nil || entered.reroll.Count != 2 || !entered.reroll.Running {
		t.Fatalf("进模式该拿到两条、且还在摇：%+v", entered.reroll)
	}
	if !strings.Contains(entered.View().Content, "‹ 1/2 ›") {
		t.Fatalf("进模式那一刻该画位次：\n%s", entered.View().Content)
	}
	// 轮询那一跳：摇完 ⇒ 位次变两条实打实、合成分块退场，底栏也从「重摇中…」换成结果
	// （按当前模式的 target_kind 挑家族的 GET —— 消息模式走 /reroll-message）。
	polled := runCmds(t, entered, pollRerollCmd(entered.client, "c1", rerollKindOf(entered.reroll)))
	if polled.rerollRunning() || polled.reroll.Items[1].Preview == "" {
		t.Fatalf("轮询回来该看到摇完的那一版：%+v", polled.reroll)
	}
	if strings.Contains(polled.lastAction, "还在摇") {
		t.Fatalf("摇完了底栏不该还停在「重摇中」上：%q", polled.lastAction)
	}

	// `/reroll switch 2` ⇒ 后端收到 idx=2；切完重拉消息（正文换成选中那一版）
	_, cmd = commandReroll(polled, []string{"switch", "2"})
	switched := followOnce(t, polled, cmd)
	if switchIdx != 2 {
		t.Fatalf("该把位次发给后端，收到 %d", switchIdx)
	}
	if switched.reroll == nil || switched.reroll.CurrentIdx != 2 {
		t.Fatalf("切完当前那版该是第 2 版：%+v", switched.reroll)
	}
	if got := switched.messages; len(got) == 0 || got[len(got)-1].Content != "在的。" {
		t.Fatalf("切完该重拉消息（正文就是选中那一版）：%+v", got)
	}
	if !strings.Contains(switched.View().Content, "‹ 2/2 ›") {
		t.Fatalf("位次该跟着变：\n%s", switched.View().Content)
	}
}

// rerollSummaryFixture：**摘要重摇**模式内的样子（目标盖第 2–3 条；消息区**不画**位次标记）。
//
// 故意把 TargetMessageID 也摆上（"m2"）：摘要模式**即便**目标消息对得上也不该画标记
// —— 摇的是摘要、没有对应的那条消息可挂（判据是 target_kind，不是 target_message_id 对不对）。
func rerollSummaryFixture() model {
	m := fixture()
	m.reroll = &RerollState{
		Active: true, TargetKind: RerollKindSummary, TargetSummaryID: "s1", TargetMessageID: "m2",
		FromIdx: new(2), ToIdx: new(3), Count: 3, CurrentIdx: 2,
		Items: []RerollItem{
			{Idx: 1, Preview: "早先那一版。"},
			{Idx: 2, Preview: "压缩后的第二版。", Current: true},
			{Idx: 3, Preview: "还在写。", Pending: true},
		},
	}
	m.rerollFor = "c1"
	return m
}

// 摘要模式**不画**消息旁的位次标记；目标（第 a–b 条）放底栏那一句里。
func TestRerollSummaryHasNoMarkerButShowsTarget(t *testing.T) {
	m := rerollSummaryFixture()
	if strings.Contains(m.View().Content, "‹") {
		t.Fatalf("摘要模式不该在消息旁画位次标记：\n%s", m.View().Content)
	}
	m.lastAction = rerollAction(*m.reroll)
	if !strings.Contains(m.lastAction, "第 2–3 条那条摘要") {
		t.Fatalf("底栏该说清目标是哪一段：%q", m.lastAction)
	}
	if !strings.Contains(m.lastAction, "/reroll-summary list") || !strings.Contains(m.lastAction, "/reroll-summary off") {
		t.Fatalf("底栏该给摘要家族的命令名：%q", m.lastAction)
	}
	// 消息模式那一句**逐字不变**（断言别动）
	message := rerollFixture()
	if got := rerollAction(*message.reroll); got != "重摇：第 2/3 版（`/reroll list` 看有哪几版 · `/reroll off` 退出）" {
		t.Fatalf("消息模式底栏口径该不变：%q", got)
	}
}

// `/reroll-summary` 的命令解析：进模式要 idx；用法错当场说清；`list` 摊开每一版（含目标那一行）。
func TestCommandRerollSummaryUsageAndList(t *testing.T) {
	m := rerollSummaryFixture()
	// 不带 idx ⇒ 说清用法（摘要有的是"我到底要摇哪一段"，缺了没得猜）
	if bad, _ := commandRerollSummary(m, nil); !strings.Contains(bad.(model).lastAction, "用法") {
		t.Fatalf("缺 idx 该说清用法：%q", bad.(model).lastAction)
	}
	// idx 非正 / 不是数 ⇒ 拒绝
	if bad, _ := commandRerollSummary(m, []string{"0"}); !strings.Contains(bad.(model).lastAction, "正整数") {
		t.Fatalf("idx 非正该拒绝：%q", bad.(model).lastAction)
	}
	if bad, _ := commandRerollSummary(m, []string{"abc"}); !strings.Contains(bad.(model).lastAction, "正整数") {
		t.Fatalf("idx 不是数该拒绝：%q", bad.(model).lastAction)
	}
	// switch 缺位次 ⇒ 说清用法
	if bad, _ := commandRerollSummary(m, []string{"switch"}); !strings.Contains(bad.(model).lastAction, "用法") {
		t.Fatalf("switch 缺位次该说清用法：%q", bad.(model).lastAction)
	}
	// `list`：铺满消息区的查看器，带"目标"那一行 + 每一版的预览
	listed, _ := commandRerollSummary(m, []string{"list"})
	if listed.(model).viewer == nil {
		t.Fatal("/reroll-summary list 该打开查看器")
	}
	joined := strings.Join(listed.(model).viewer, "\n")
	for _, want := range []string{"目标：第 2–3 条", "第 1 版", "第 2 版", "（当前）", "第 3 版", "（还在摇…）", "压缩后的第二版。"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("查看器缺 %q：\n%s", want, joined)
		}
	}
	// 消息模式进的模式 ⇒ `/reroll-summary list` 该说没在摘要那一档
	if wrong, _ := commandRerollSummary(rerollFixture(), []string{"list"}); !strings.Contains(wrong.(model).lastAction, "没在摘要重摇模式里") {
		t.Fatalf("不在摘要模式该说清：%q", wrong.(model).lastAction)
	}
	// 摘要模式进的模式 ⇒ `/reroll list` 也要说没在**消息**那一档（两家互不顶替）
	if wrong, _ := commandReroll(m, []string{"list"}); !strings.Contains(wrong.(model).lastAction, "没在重摇模式里") {
		t.Fatalf("摘要模式上 `/reroll list` 该说没在消息那一档：%q", wrong.(model).lastAction)
	}
}

// 进摘要模式（带 idx）/ 切换 / 删除 / 退出：请求形状与消息家族一一对应，只换家族。
func TestCommandRerollSummaryRoundTrip(t *testing.T) {
	var enterIdx, switchIdx int
	var deletedPath, clearedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/c1/reroll-summary":
			var body struct {
				Idx int `json:"idx"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			enterIdx = body.Idx
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(RerollAccepted{
				TargetSummaryID: "s1", TaskID: "t1",
				State: RerollState{Active: true, TargetKind: RerollKindSummary, TargetSummaryID: "s1",
					FromIdx: new(2), ToIdx: new(3), Count: 2, CurrentIdx: 1, Running: true,
					Items: []RerollItem{{Idx: 1, Preview: "压缩一版。", Current: true}, {Idx: 2, Pending: true}}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/c1/reroll-summary":
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetKind: RerollKindSummary, TargetSummaryID: "s1",
				FromIdx: new(2), ToIdx: new(3), Count: 2, CurrentIdx: 1,
				Items: []RerollItem{{Idx: 1, Preview: "压缩一版。", Current: true}, {Idx: 2, Preview: "压缩二版。"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/c1/reroll-summary/switch":
			var body struct {
				Idx int `json:"idx"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switchIdx = body.Idx
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetKind: RerollKindSummary, TargetSummaryID: "s1",
				FromIdx: new(2), ToIdx: new(3), Count: 2, CurrentIdx: 2,
				Items: []RerollItem{{Idx: 1, Preview: "压缩一版。"}, {Idx: 2, Preview: "压缩二版。", Current: true}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sessions/c1/reroll-summary/1":
			deletedPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetKind: RerollKindSummary, TargetSummaryID: "s1",
				FromIdx: new(2), ToIdx: new(3), Count: 1, CurrentIdx: 1,
				Items: []RerollItem{{Idx: 1, Preview: "压缩一版。", Current: true}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sessions/c1/reroll-summary":
			clearedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	m := fixture()
	m.client = NewClient(backend.URL, "")

	// `/reroll-summary 3` ⇒ 202 那一刻该把 idx（1-based 消息序号）带给后端并收下状态
	_, cmd := commandRerollSummary(m, []string{"3"})
	entered := runCmds(t, m, cmd)
	if enterIdx != 3 {
		t.Fatalf("该把 idx 发给后端，收到 %d", enterIdx)
	}
	if entered.reroll == nil || !entered.reroll.Active || entered.reroll.TargetKind != RerollKindSummary ||
		entered.reroll.TargetSummaryID != "s1" {
		t.Fatalf("进摘要模式该把状态收进来：%+v", entered.reroll)
	}
	if !entered.reroll.Running {
		t.Fatalf("受理那一刻那一版还在摇：%+v", entered.reroll)
	}
	// 轮询那一跳：摇完 ⇒ 两条实打实
	polled := runCmds(t, entered, pollRerollCmd(entered.client, "c1", rerollKindOf(entered.reroll)))
	if polled.rerollRunning() || polled.reroll.Items[1].Preview == "" {
		t.Fatalf("轮询回来该看到摇完的那一版：%+v", polled.reroll)
	}
	if !strings.Contains(polled.lastAction, "第 2–3 条那条摘要") {
		t.Fatalf("摘要模式底栏该报目标区间：%q", polled.lastAction)
	}

	// `/reroll-summary switch 2` ⇒ 后端收到 idx=2；摘要换了正文（消息列表看不见，不重拉）
	_, cmd = commandRerollSummary(polled, []string{"switch", "2"})
	switched := runCmds(t, polled, cmd)
	if switchIdx != 2 {
		t.Fatalf("该把位次发给后端，收到 %d", switchIdx)
	}
	if switched.reroll == nil || switched.reroll.CurrentIdx != 2 {
		t.Fatalf("切完当前那版该是第 2 版：%+v", switched.reroll)
	}

	// `/reroll-summary delete 1` ⇒ DELETE .../reroll-summary/1
	_, cmd = commandRerollSummary(switched, []string{"delete", "1"})
	deleted := runCmds(t, switched, cmd)
	if deletedPath != "/api/v1/sessions/c1/reroll-summary/1" {
		t.Fatalf("删除该打 .../reroll-summary/{idx}：%q", deletedPath)
	}
	if deleted.reroll == nil || deleted.reroll.Count != 1 {
		t.Fatalf("删完该只剩一版：%+v", deleted.reroll)
	}

	// `/reroll-summary off` ⇒ DELETE .../reroll-summary（204）
	_, cmd = commandRerollSummary(deleted, []string{"off"})
	after := runCmds(t, deleted, cmd)
	if clearedPath != "/api/v1/sessions/c1/reroll-summary" {
		t.Fatalf("退出该打 DELETE .../reroll-summary：%q", clearedPath)
	}
	if after.reroll != nil || !strings.Contains(after.lastAction, "已退出摘要重摇模式") {
		t.Fatalf("退出之后本地那一份该清掉：%+v / %q", after.reroll, after.lastAction)
	}
}

// 轮询按 `target_kind` 挑家族的 GET：摘要模式只问 /reroll-summary（不碰消息那一档），反之亦然；
// 还不知道家族（刚进会话，空串）⇒ 两个都问，捡起活着的那个。
func TestPollRerollDispatchesByKind(t *testing.T) {
	var messageHits, summaryHits int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/sessions/c1/reroll-message":
			messageHits++
			_ = json.NewEncoder(w).Encode(RerollState{Active: false})
		case "/api/v1/sessions/c1/reroll-summary":
			summaryHits++
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetKind: RerollKindSummary, TargetSummaryID: "s1",
				FromIdx: new(2), ToIdx: new(3), Count: 2, CurrentIdx: 1,
				Items: []RerollItem{{Idx: 1, Preview: "压缩一版。", Current: true}, {Idx: 2, Preview: "压缩二版。"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	client := NewClient(backend.URL, "")

	// kind=summary ⇒ 只问摘要那一档
	got := runCmds(t, fixture(), pollRerollCmd(client, "c1", RerollKindSummary))
	if summaryHits != 1 || messageHits != 0 {
		t.Fatalf("摘要模式该只问 /reroll-summary：message=%d summary=%d", messageHits, summaryHits)
	}
	if got.reroll == nil || got.reroll.TargetKind != RerollKindSummary {
		t.Fatalf("该把摘要那一档收进来：%+v", got.reroll)
	}
	// kind=message ⇒ 只问消息那一档
	messageHits, summaryHits = 0, 0
	_ = runCmds(t, fixture(), pollRerollCmd(client, "c1", RerollKindMessage))
	if messageHits != 1 || summaryHits != 0 {
		t.Fatalf("消息模式该只问 /reroll-message：message=%d summary=%d", messageHits, summaryHits)
	}
	// 空串（还不知道家族）⇒ 先问消息那一档（这里 active:false），再问摘要那一档
	messageHits, summaryHits = 0, 0
	discovered := runCmds(t, fixture(), pollRerollCmd(client, "c1", ""))
	if messageHits != 1 || summaryHits != 1 {
		t.Fatalf("未知家族该两档都问：message=%d summary=%d", messageHits, summaryHits)
	}
	if discovered.reroll == nil || !discovered.reroll.Active || discovered.reroll.TargetKind != RerollKindSummary {
		t.Fatalf("该捡起活着的摘要那一档：%+v", discovered.reroll)
	}
}

// ── /providers · /provider-add · /provider-del ──

// providerTestBackend：渠道的假后端 —— 记下 POST /providers 的请求体，按需回 422/409。
type providerTestBackend struct {
	body    map[string]any
	raw     []byte
	created ProviderInfo
	status  int
	reply   string
}

func newProviderTestBackend(status int, reply string) (*httptest.Server, *providerTestBackend) {
	captured := &providerTestBackend{status: status, reply: reply}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/providers":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "ocgo", "vendor": "opencode-go", "protocol": "openai-chat-completion", "base_url": "https://x", "has_key": true},
				{"id": "local", "vendor": "dummy", "protocol": "openai-chat-completion", "base_url": "", "has_key": false},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			_ = json.NewEncoder(w).Encode([]ModelListItem{
				{Provider: "ocgo", UpstreamID: "m1", Name: "M1"},
				{Provider: "ocgo", UpstreamID: "m2", Name: "M2"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/providers/presets":
			_ = json.NewEncoder(w).Encode([]PresetItem{
				{Vendor: "dummy", Name: "本地假上游", Protocols: []string{"openai-chat-completion"}},
				{Vendor: "deepseek", Name: "DeepSeek 官方", Protocols: []string{"openai-chat-completion"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/providers":
			raw, _ := io.ReadAll(r.Body)
			captured.raw = raw
			_ = json.Unmarshal(raw, &captured.body)
			if captured.status != 0 {
				w.WriteHeader(captured.status)
				_, _ = w.Write([]byte(captured.reply))
				return
			}
			captured.created = ProviderInfo{ID: "x", Vendor: "deepseek", Protocol: "openai-chat-completion"}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(captured.created)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/providers/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	return server, captured
}

// answerWizardText：往向导里喂一句答案（输入行非 / 开头 ⇒ submit 走 answerWizard）。
func answerWizardText(m model, text string) (model, tea.Cmd) {
	m.input, m.inputCursor = text, len([]rune(text))
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return updated.(model), cmd
}

// 四问拼出正确 POST 体（含 key 为空不发、缺省 protocol 不发）。
func TestProviderAddWizardBuildsCorrectBody(t *testing.T) {
	backend, captured := newProviderTestBackend(0, "")
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")

	updated, _ := m.runCommand("/provider-add")
	m = updated.(model)
	if m.addStep != 1 {
		t.Fatalf("重进该从第一问开始：%d", m.addStep)
	}
	// presets 名单先回来（不然第二问认不出 vendor）
	updated, _ = m.Update(presetsMsg{presets: []PresetItem{
		{Vendor: "dummy", Protocols: []string{"openai-chat-completion"}},
		{Vendor: "deepseek", Protocols: []string{"openai-chat-completion"}},
	}})
	m = updated.(model)
	m, _ = answerWizardText(m, "x")
	m, _ = answerWizardText(m, "deepseek")
	// protocol 空 = 缺省（draft 留空，Create 不发 protocol）
	m, _ = answerWizardText(m, "")
	if m.addDraft.protocol != "" {
		t.Fatalf("缺省 protocol 该留空（Create 不发）：%q", m.addDraft.protocol)
	}
	// key 空 ⇒ 发 Create（key 为空不发这个键）
	m, cmd := answerWizardText(m, "")
	if cmd == nil {
		t.Fatal("第四问答完该发 Create")
	}
	m = runCmds(t, m, cmd)
	if !strings.Contains(m.lastAction, "已添加 x（deepseek/openai-chat-completion）") {
		t.Fatalf("成功该报已添加：%q", m.lastAction)
	}
	if _, ok := captured.body["api_key"]; ok {
		t.Fatalf("key 为空不该发 api_key：%s", string(captured.raw))
	}
	if _, ok := captured.body["protocol"]; ok {
		t.Fatalf("缺省 protocol 不该发 protocol：%s", string(captured.raw))
	}
	if captured.body["id"] != "x" || captured.body["vendor"] != "deepseek" {
		t.Fatalf("POST 体不对：%s", string(captured.raw))
	}
}

// 422 原样说（错配矩阵也在这一条里出来）。
func TestProviderAddReports422Verbatim(t *testing.T) {
	backend, _ := newProviderTestBackend(http.StatusUnprocessableEntity,
		`{"error":{"code":"invalid","message":"providers: vendor \"deepseek\" 不支持 protocol \"systemone\""}}`)
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	presets := []PresetItem{
		{Vendor: "dummy", Protocols: []string{"openai-chat-completion"}},
		{Vendor: "deepseek", Protocols: []string{"openai-chat-completion"}},
	}
	updated, _ := m.runCommand("/provider-add")
	m = updated.(model)
	updated, _ = m.Update(presetsMsg{presets: presets})
	m = updated.(model)
	// protocol 问里 deepseek 只认 chat —— systemone 在向导就被拦下，不发请求
	m2 := fixture()
	m2.client = NewClient(backend.URL, "")
	updated, _ = m2.runCommand("/provider-add")
	m2 = updated.(model)
	updated, _ = m2.Update(presetsMsg{presets: presets})
	m2 = updated.(model)
	m2, _ = answerWizardText(m2, "bad")
	m2, _ = answerWizardText(m2, "deepseek")
	m2, cmd := answerWizardText(m2, "systemone")
	if cmd != nil {
		t.Fatal("错配 protocol 在向导就该拦下，不发请求")
	}
	if !strings.Contains(m2.lastAction, "不支持 protocol") {
		t.Fatalf("该说清错配：%q", m2.lastAction)
	}
	// 后端 422 也原样透出来（create 回来的错）
	m3 := fixture()
	m3.client = NewClient(backend.URL, "")
	updated, _ = m3.Update(createdProviderMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "添加渠道失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

// Esc 取消（输入框以 / 开头时 Esc 清输入即取消 —— 沿用现有行为）。
func TestProviderAddEscCancels(t *testing.T) {
	m := fixture()
	updated, _ := m.runCommand("/provider-add")
	m = updated.(model)
	m, _ = answerWizardText(m, "x")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cancelled := updated.(model); cancelled.addStep != 0 || cancelled.input != "" {
		t.Fatalf("Esc 该清草稿与输入：step=%d input=%q", cancelled.addStep, cancelled.input)
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "已取消") {
		t.Fatalf("该说已取消：%q", said)
	}
}

// /providers 查看器行（渠道行 + 预设名单行）。
func TestProvidersViewerLines(t *testing.T) {
	backend, _ := newProviderTestBackend(0, "")
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/providers")
	if cmd == nil {
		t.Fatal("/providers 该去拉两份")
	}
	updated = runCmds(t, updated.(model), cmd)
	after := updated.(model)
	if after.viewer == nil {
		t.Fatalf("该打开查看器：%q", after.lastAction)
	}
	body := strings.Join(after.viewer, "\n")
	for _, want := range []string{"ocgo · opencode-go/openai-chat-completion · 有 key · 2 个模型", "local · dummy/openai-chat-completion · 无 key · 0 个模型", "presets: dummy, deepseek"} {
		if !strings.Contains(body, want) {
			t.Fatalf("缺 %q：\n%s", want, body)
		}
	}
	// 失败照实说
	failed, _ := fixture().Update(providersMsg{err: errTest})
	if said := failed.(model).lastAction; !strings.Contains(said, "看渠道失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

// /provider-del 走 confirm（点头真删 204，取消什么都不发生）。
func TestProviderDelConfirmsThenDeletes(t *testing.T) {
	backend, _ := newProviderTestBackend(0, "")
	defer backend.Close()
	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/provider-del ocgo")
	if cmd != nil {
		t.Fatal("确认前不许发请求")
	}
	armed := updated.(model)
	if armed.confirm == nil || armed.confirm.kind != confirmDeleteProvider || armed.confirm.provider != "ocgo" {
		t.Fatalf("该存下删渠道意图：%+v", armed.confirm)
	}
	updated, cmd = armed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("点头该真删")
	}
	updated = runCmds(t, updated.(model), cmd)
	if said := updated.(model).lastAction; !strings.Contains(said, "已删除渠道 ocgo") {
		t.Fatalf("删完该报数：%q", said)
	}
}

// 压缩受理后沿 turnTick 同一趟节拍轮询：done 报区间（消息级/合并级两套文案），error 如实说。
func TestCompactPollReportsDoneRangeAndMerged(t *testing.T) {
	m := fixture()
	accepted, _ := m.Update(compactMsg{sessionID: "c1", status: CompactStatus{State: "running", Blocks: 2}})
	next := accepted.(model)
	if next.compact == nil || next.compactFor != "c1" {
		t.Fatalf("受理该记下这一次：%+v", next.compact)
	}
	if said := next.lastAction; !strings.Contains(said, "已受理") {
		t.Fatalf("受理该说话：%q", said)
	}
	ticked, cmd := next.Update(turnTickMsg{sessionID: "c1"})
	if cmd == nil {
		t.Fatal("受理后该沿 turnTick 同一趟约下一次压缩轮询")
	}
	_ = ticked
	// 消息级 done
	done, _ := next.Update(compactPollMsg{sessionID: "c1",
		status: &CompactStatus{State: "done", FromIdx: 1, ToIdx: 4, Compacted: 2}})
	if said := done.(model).lastAction; !strings.Contains(said, "压好第 1–4 条") || !strings.Contains(said, "2 块") {
		t.Fatalf("消息级 done 该报区间与块数：%q", said)
	}
	if after := done.(model); after.compact != nil {
		t.Fatal("done 之后不该还记着这一次")
	}
	// 合并级 done：换文案
	running, _ := m.Update(compactMsg{sessionID: "c1", status: CompactStatus{State: "running", Blocks: 2}})
	merged, _ := running.(model).Update(compactPollMsg{sessionID: "c1",
		status: &CompactStatus{State: "done", FromIdx: 1, ToIdx: 8, Compacted: 2, Merged: true}})
	if said := merged.(model).lastAction; !strings.Contains(said, "并好第 1–8 条") || !strings.Contains(said, "2 坨") {
		t.Fatalf("合并级 done 该换文案：%q", said)
	}
	// 还在跑 ⇒ 继续约下一次；error ⇒ 如实说并清掉
	again, _ := m.Update(compactMsg{sessionID: "c1", status: CompactStatus{State: "running", Blocks: 2}})
	waiting, cmd := again.(model).Update(compactPollMsg{sessionID: "c1",
		status: &CompactStatus{State: "running"}})
	if cmd == nil {
		t.Fatal("还在跑 ⇒ 该约下一次")
	}
	failed, _ := waiting.(model).Update(compactPollMsg{sessionID: "c1",
		status: &CompactStatus{State: "error", Error: "上游 500"}})
	if said := failed.(model).lastAction; !strings.Contains(said, "压缩失败") || !strings.Contains(said, "上游 500") {
		t.Fatalf("error 该报原因：%q", said)
	}
	if after := failed.(model); after.compact != nil {
		t.Fatal("error 之后不该还记着这一次")
	}
}

// 滚动按 message 走（聊天的最小单位，不是压缩块）：pgup 往上翻一条，到底回 0。
func TestScrollMovesByMessage(t *testing.T) {
	m := fixture()
	m.messages = append(m.messages,
		Message{ID: "m3", Role: "user", Content: "第三句"},
		Message{ID: "m4", Role: "assistant", Content: "收到第三句"},
	)
	m.height = 10
	m.width = 40
	pressed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	up := pressed.(model)
	if up.scroll != 1 {
		t.Fatalf("pgup 该翻 1 条：%d", up.scroll)
	}
	view := up.View().Content
	if !strings.Contains(view, "下面还有") && !strings.Contains(view, "上面还有") {
		t.Fatalf("往上翻了该说还有：\n%s", view)
	}
	if strings.Contains(view, "在。") {
		t.Fatalf("往上翻 1 条，最新的助手回复该被翻过去：\n%s", view)
	}
	back, _ := up.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if back.(model).scroll != 0 {
		t.Fatal("pgdn 该回到到底")
	}
	// 新消息到了 ⇒ 回到底
	top, _ := up.Update(messagesMsg{sessionID: "c1", messages: m.messages})
	if top.(model).scroll != 0 {
		t.Fatal("新消息到了该回到底")
	}
}

// 长块超一屏：露末尾 + 如实说"本条上面还有 N 行"（块不断头，只断尾）。
func TestLongBlockShowsTail(t *testing.T) {
	m := fixture()
	m.messages = []Message{{ID: "m1", Role: "assistant", Content: strings.Repeat("长\n", 30)}}
	m.height = 10
	m.width = 40
	view := m.View().Content
	if !strings.Contains(view, "本条上面还有") {
		t.Fatalf("长块该露末尾并报数：\n%s", view)
	}
}

// Tasks 指示器：有在跑的活才亮（搭 turnTick 节拍问，没活不问）。
func TestTasksIndicatorLightsOnRunning(t *testing.T) {
	m := fixture()
	updated, cmd := m.Update(tasksPollMsg{board: TaskBoard{Running: 1, Tasks: []apiclient.TaskRecord{
		{ID: "t1", Kind: "compact", Title: "压缩", Outcome: "running"},
	}}})
	if got := updated.(model).turnLabel(updated.(model).currentSession()); !strings.Contains(got, "压缩中") {
		t.Fatalf("有活在跑该亮：%q", got)
	}
	if cmd != nil {
		t.Fatal("tasks 轮询不约下一次（搭 turnTick 的车）")
	}
	idle, _ := m.Update(tasksPollMsg{board: TaskBoard{}})
	if got := idle.(model).turnLabel(idle.(model).currentSession()); got != "空闲" {
		t.Fatalf("没活该空闲：%q", got)
	}
}

// /telegram-bind：纯数字才收（@用户名不行）；成功报"已绑定 + 重启生效"。
func TestTelegramBindValidatesID(t *testing.T) {
	m := fixture()
	bad, _ := m.runCommand("/telegram-bind @someone")
	if !strings.Contains(bad.(model).lastAction, "纯数字") {
		t.Fatalf("@用户名该被拦：%q", bad.(model).lastAction)
	}
	zero, _ := m.runCommand("/telegram-bind 0")
	if !strings.Contains(zero.(model).lastAction, "纯数字") {
		t.Fatalf("0 该被拦：%q", zero.(model).lastAction)
	}
}

// /telegram-bot-token-set：空的拒收（token 不许空）。
func TestTelegramTokenSetRejectsEmpty(t *testing.T) {
	m := fixture()
	empty, _ := m.runCommand("/telegram-bot-token-set")
	if !strings.Contains(empty.(model).lastAction, "用法") {
		t.Fatalf("空 token 该说用法：%q", empty.(model).lastAction)
	}
}

// 括号粘贴：整段插到光标处（与打字同一条路；picker/viewer/confirm 开着不吃）。
func TestPasteInsertsAtCursor(t *testing.T) {
	m := fixture()
	m.input, m.inputCursor = "ab", 1
	updated, _ := m.Update(tea.PasteMsg{Content: "XYZ"})
	got := updated.(model)
	if got.input != "aXYZb" || got.inputCursor != 4 {
		t.Fatalf("粘贴该插光标处：%q cursor=%d", got.input, got.inputCursor)
	}
	// 中文不断半个字
	cjk, _ := m.Update(tea.PasteMsg{Content: "你好世界"})
	if cjk.(model).inputCursor != 1+4 {
		t.Fatalf("中文光标该按 rune 走：%d", cjk.(model).inputCursor)
	}
}

// ── 底栏 TG 连通性指示（用户 2026-10-02 定）──
//
// 五种状态各自的段（零值 styler ⇒ 纯文本断言）：没拉过 / 没配**不显示**；
// 开着跑着 = 绿勾；开着有 token 没跑 = 红叉；开着没 token = 红「缺token」；有 token 没开 = 暗「关」。
func TestTelegramIndicatorStates(t *testing.T) {
	cases := []struct {
		name    string
		loaded  bool
		binding TelegramBinding
		want    string
		color   string
	}{
		{"没拉过不显示", false, TelegramBinding{}, "", ""},
		{"没配不显示", true, TelegramBinding{}, "", ""},
		{"开了+跑着=绿勾", true, TelegramBinding{HasToken: true, Enabled: true, Running: true}, "TG ✓", "\x1b[32mTG ✓\x1b[0m"},
		{"开了+有token没跑=红叉", true, TelegramBinding{HasToken: true, Enabled: true}, "TG ✗", "\x1b[31mTG ✗\x1b[0m"},
		{"开了+没token=缺token", true, TelegramBinding{Enabled: true}, "TG 缺token", "\x1b[31mTG 缺token\x1b[0m"},
		{"有token没开=暗色关", true, TelegramBinding{HasToken: true}, "TG 关", "\x1b[2mTG 关\x1b[0m"},
	}
	for _, tc := range cases {
		m := fixture()
		m.tgLoaded, m.tg = tc.loaded, tc.binding
		status := strings.Join(m.renderStatus(m.width), "\n")
		if tc.want == "" {
			if strings.Contains(status, "TG") {
				t.Fatalf("%s：不该显示 TG 段：%q", tc.name, status)
			}
			continue
		}
		if !strings.Contains(status, tc.want) {
			t.Fatalf("%s：缺 %q：%q", tc.name, tc.want, status)
		}
		// 段的位置：插在 `已连接 vX` **之前**
		if at, conn := strings.Index(status, tc.want), strings.Index(status, "已连接 v"); conn >= 0 && at > conn {
			t.Fatalf("%s：TG 段该在「已连接」之前：%q", tc.name, status)
		}
		if got := colored(m).View().Content; !strings.Contains(got, tc.color) {
			t.Fatalf("%s：颜色不对（该有 %q）：\n%q", tc.name, tc.color, got)
		}
	}
}

// 低频轮询：还没拉过 ⇒ 去拉一次；拉到"没配" ⇒ **不再约下一次**（一个包都不多发）；
// 配了 ⇒ 续下一次；拉不到 ⇒ 静默丢（不刷屏报错）但该续的还续（别把轮询掐死）。
func TestTelegramPollSelfRenewsOnlyWhenConfigured(t *testing.T) {
	m := fixture()
	if got, _ := m.Update(tgTickMsg{}); got.(model).tgLoaded {
		t.Fatal("节拍本身不该改状态")
	}
	if _, cmd := m.Update(tgTickMsg{}); cmd == nil {
		t.Fatal("还没拉过时该去拉一次")
	}
	after, _ := m.Update(tgStatusMsg{binding: TelegramBinding{}})
	if !after.(model).tgLoaded {
		t.Fatal("拉到了该记下（好区分「没拉过」与「没配」）")
	}
	if _, cmd := after.(model).Update(tgStatusMsg{binding: TelegramBinding{}}); cmd != nil {
		t.Fatal("没配 TG 的人不该再约下一次轮询")
	}
	if _, cmd := after.(model).Update(tgTickMsg{}); cmd != nil {
		t.Fatal("没配的人就算节拍到了也不该再问（一个包都不多发）")
	}
	if _, cmd := after.(model).Update(tgStatusMsg{binding: TelegramBinding{HasToken: true}}); cmd == nil {
		t.Fatal("配了 TG 该续下一次轮询")
	}
	silent, _ := m.Update(tgStatusMsg{err: errTest})
	if said := silent.(model).lastAction; said != m.lastAction {
		t.Fatalf("拉不到该静默丢、不动底栏：%q", said)
	}
	if _, cmd := silent.(model).Update(tgTickMsg{}); cmd == nil {
		t.Fatal("拉不到之后还得接着试（还没拉过）")
	}
}

// /telegram-toggle：先 GET 拿当前开关，再 PUT `{"enabled": <翻转>}`（别把"没读到"当"关着"给开了）；
// 回执如实回显翻转后的状态，底栏那段指示也当场刷新。
func TestTelegramToggleRoundTrip(t *testing.T) {
	var puts []map[string]any
	gets := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/config/telegram" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			gets++
			_ = json.NewEncoder(w).Encode(TelegramBinding{HasToken: true, AllowedID: 42, Enabled: false})
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			puts = append(puts, body)
			_ = json.NewEncoder(w).Encode(TelegramBinding{HasToken: true, AllowedID: 42, Enabled: true, Running: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.runCommand("/telegram-toggle")
	if cmd == nil {
		t.Fatal("/telegram-toggle 该去读开关再翻转")
	}
	next := runCmds(t, updated.(model), cmd)
	if gets != 1 {
		t.Fatalf("该先 GET 一次拿到当前开关：%d", gets)
	}
	if len(puts) != 1 {
		t.Fatalf("该 PUT 一次：%+v", puts)
	}
	if got := puts[0]; len(got) != 1 || got["enabled"] != true {
		t.Fatalf("PUT 体该只有翻转后的 enabled=true：%+v", got)
	}
	if said := next.lastAction; !strings.Contains(said, "开关开") || !strings.Contains(said, "跑着") {
		t.Fatalf("回执该回显翻转后的状态：%q", said)
	}
	// 回执就是最新状态 ⇒ 指示器当场亮起来（不必等下一拍）
	if !strings.Contains(next.View().Content, "TG ✓") {
		t.Fatalf("翻转后指示器该当场刷新：\n%s", next.View().Content)
	}
}
