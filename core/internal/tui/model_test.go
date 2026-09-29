package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// fixture：一份手搭的界面状态（**不碰网络** —— 这正是 Elm 式的好处：状态与渲染分开，测起来就是喂数据）。
func fixture() model {
	version := int64(2100)
	return model{
		client:     NewClient("http://127.0.0.1:8787", ""), // 测试不执行 Cmd，所以不会真发请求
		version:    "0.1.0",
		lastAction: "消息 2 条",
		width:      100, height: 24, ready: true,
		sessions: []Session{
			{ID: "c1", Title: "逆序数该怎么写", Provider: "opencode-go", Model: "deepseek-v4-flash", AgentID: "跑团",
				Turn: TurnStatus{Phase: "idle"}},
			{ID: "c2", Title: "第二段对话", Provider: "dummy", Model: "dummy", AgentID: "default"},
		},
		selectedID:  "c1",
		messagesFor: "c1", // 上面这两条属于 c1（isEmptySession 靠它 —— 别拿"还没拉完"当"这条没有消息"）
		messages: []Message{
			{ID: "m1", Role: "user", Content: "在吗", SummaryID: new("s1")},
			{ID: "m2", Role: "assistant", Content: "在。\n有什么事？", DurationMS: &version},
		},
		models: []ModelListItem{
			{Provider: "opencode-go", UpstreamID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash"},
			{Provider: "opencode-go", UpstreamID: "kimi-k2", Name: "Kimi K2"},
			{Provider: "dummy", UpstreamID: "dummy", Name: "dummy"},
		},
	}
}

// viewLines：把 View() 切成行 —— 布局测试都从这儿数行（免得每处都 Split 一遍）。
func viewLines(m model) []string { return strings.Split(m.View().Content, "\n") }

// inputLineIndex：输入行（`>` 提示符那行）在第几行 —— 它下面那行是分隔线，再下面是会话状态行。
func inputLineIndex(lines []string) int {
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.HasPrefix(lines[index], ">") {
			return index
		}
	}
	return -1
}

// emptyFixture：刚打开 TUI 的样子（**空会话**，还没连上后端）。
func emptyFixture() model {
	return model{client: NewClient("http://127.0.0.1:8787", ""), width: 60, height: 12, ready: true}
}

// narrowFixture：窄屏 —— 状态行装不下一行就该折成两行（**最多两行**）。
func narrowFixture() model {
	m := fixture()
	m.width, m.height = 46, 12
	return m
}

// View() 是纯函数：同样的 model ⇒ 同样的字符串（这是选它的全部理由）。
func TestViewIsPure(t *testing.T) {
	m := fixture()
	if m.View().Content != m.View().Content {
		t.Fatal("同样的 model 该渲染出同样的字符串")
	}
	if !strings.Contains(m.View().Content, "逆序数该怎") { // 标题按宽度截断，断言前缀
		t.Fatalf("会话标题该出现：\n%s", m.View().Content)
	}
}

// **宽度账**：每行都不许超宽（中文占两格 ⇒ 只能按显示宽度算）。
// 新口径**没有竖线、没有左栏** —— 原来那条"竖线该落在第 28 列"的断言随左栏一起删了。
func TestEveryLineFitsWidth(t *testing.T) {
	for _, m := range []model{fixture(), liveFixture(), emptyFixture(), narrowFixture()} {
		for _, line := range viewLines(m) {
			if got := runewidth.StringWidth(line); got > m.width {
				t.Fatalf("这一行超出宽度（%d > %d）：%q", got, m.width, line)
			}
		}
		if got := len(viewLines(m)); got != m.height {
			t.Fatalf("该正好 %d 行，得到 %d 行", m.height, got)
		}
	}
}

// 新的三块：**消息区**（只画当前会话的对话）→ 输入行 → ASCII 分隔线 → 会话状态行。
// 左栏（会话列表）不该再出现（用户 2026-09-30 定的：会话选择只走 /resume）。
func TestLayoutSinceNoSidebar(t *testing.T) {
	m := fixture()
	lines := viewLines(m)
	if strings.Contains(m.View().Content, "＋ 新建对话") {
		t.Fatalf("不该再有左侧会话列表：\n%s", m.View().Content)
	}
	input := inputLineIndex(lines)
	if input < 1 {
		t.Fatalf("该有输入行：\n%s", m.View().Content)
	}
	if lines[input+1] != rule(m.width, m.width) {
		t.Fatalf("输入行下面该是一条满线（宽屏 `─` / 窄屏 `-`）：%q", lines[input+1])
	}
	// 状态行在分隔线下面（1-2 行，别超过两行）
	status := lines[input+2:]
	if len(status) < 1 || len(status) > 2 {
		t.Fatalf("会话状态行该在 1-2 行之间：%v", status)
	}
	if !strings.Contains(strings.Join(status, "\n"), "空闲") {
		t.Fatalf("状态行该说在不在跑：%v", status)
	}
}

// 消息区**最新的贴着输入框**：最后一块就在输入行上面（整块排、不劈开）。
func TestMessagesSitAboveInputLine(t *testing.T) {
	m := fixture()
	lines := viewLines(m)
	input := inputLineIndex(lines)
	// 最后一块带一个空行 ⇒ 输入行上面那行是空行，再上面是助手那条的内容
	if lines[input-1] != "" {
		t.Fatalf("块尾该留一个空行：%q", lines[input-1])
	}
	if !strings.Contains(lines[input-2], "有什么事？") {
		t.Fatalf("最新那条该贴在输入行上面：%q", lines[input-2])
	}
	// 顺序没反：用户那句在上面
	body := strings.Join(lines[:input], "\n")
	if strings.Index(body, "在吗") > strings.Index(body, "有什么事？") {
		t.Fatalf("消息该按顺序排（老的在上面）：\n%s", body)
	}
}

// 空会话也别只剩输入行：消息区还得在（说话、提示都行），状态行照旧。
func TestEmptySessionStillHasMessageArea(t *testing.T) {
	m := emptyFixture()
	lines := viewLines(m)
	if len(lines) != m.height {
		t.Fatalf("该正好 %d 行：%d", m.height, len(lines))
	}
	input := inputLineIndex(lines)
	if body := strings.Join(lines[:input], "\n"); !strings.Contains(body, "空会话") {
		t.Fatalf("空会话的消息区该有话说：\n%s", m.View().Content)
	}
	if !strings.Contains(strings.Join(lines[input+2:], "\n"), "未连接") {
		t.Fatalf("状态行该如实说没连上：\n%s", m.View().Content)
	}
	// 空会话**不是**会话列表
	if strings.Contains(m.View().Content, "挑一条已有会话") {
		t.Fatalf("空会话不该铺开挑选项：\n%s", m.View().Content)
	}
}

// 消息块该带署名与耗时（有数据才显示耗时）。
func TestMessageBlocksShowWhoAndElapsed(t *testing.T) {
	m := fixture()
	body := m.View().Content
	if !strings.Contains(body, "你：") || !strings.Contains(body, "助手 2.1s：") {
		t.Fatalf("消息该带署名（助手那条还有耗时）：\n%s", body)
	}
}

// 会话状态行：会话名 | 短 id | 渠道/模型 | 在不在跑 —— **只属于当前会话**。
func TestStatusLineShowsSession(t *testing.T) {
	m := fixture()
	status := strings.Join(viewLines(m)[inputLineIndex(viewLines(m))+2:], "\n")
	for _, want := range []string{"逆序数该怎么写", shortID("c1"), "opencode-go/deepseek-v4-flash", "空闲"} {
		if !strings.Contains(status, want) {
			t.Fatalf("会话状态行缺 %q：%q", want, status)
		}
	}
	// 换一条会话 ⇒ 状态行跟着换（它认的是**当前会话**）
	m.selectedID = "c2"
	status = strings.Join(viewLines(m)[inputLineIndex(viewLines(m))+2:], "\n")
	if !strings.Contains(status, "第二段对话") || strings.Contains(status, "逆序数该怎么写") {
		t.Fatalf("状态行该换成当前会话：%q", status)
	}
	if !strings.Contains(status, "dummy/dummy") {
		t.Fatalf("状态行该报渠道 / 模型：%q", status)
	}
}

// 状态行两半**互不顶替**（旧口径定的）：连接状态与"最近一次动作"各说各的。
// 一行放不下就折成两行 —— 但**最多两行**。
func TestStatusLineKeepsBothHalves(t *testing.T) {
	m := fixture()
	lines := viewLines(m)
	status := strings.Join(lines[inputLineIndex(lines)+2:], "\n")
	if !strings.Contains(status, "已连接 v0.1.0") || !strings.Contains(status, "消息 2 条") {
		t.Fatalf("会话状态行 = %q", status)
	}
	// 窄屏：折成两行，两半都还在，且不超宽
	narrow := narrowFixture()
	lines = viewLines(narrow)
	statusLines := lines[inputLineIndex(lines)+2:]
	if len(statusLines) != 2 {
		t.Fatalf("窄屏该折成两行：%v", statusLines)
	}
	joined := strings.Join(statusLines, "\n")
	if !strings.Contains(joined, "已连接 v0.1.0") || !strings.Contains(joined, "消息 2 条") {
		t.Fatalf("折行也不许丢掉后一半：%v", statusLines)
	}
	// 没连上要如实说（版本号清掉就是没连上）
	m.version = ""
	if !strings.Contains(m.View().Content, "未连接") {
		t.Fatal("没连上时要如实说")
	}
}

// 生成中：**状态行**说"生成中 + 耗时"，消息区那条合成气泡说"生成中… 耗时"与「/stop」。
func TestGeneratingShowsElapsedInStatusAndBubble(t *testing.T) {
	m := liveFixture()
	view := m.View().Content
	lines := viewLines(m)
	status := strings.Join(lines[inputLineIndex(lines)+2:], "\n")
	if !strings.Contains(status, "生成中 12.3s") {
		t.Fatalf("状态行该说在生成：%q", status)
	}
	for _, want := range []string{"生成中… 12.3s", "说到一半", "/stop", "思考中… 3 字"} {
		if !strings.Contains(view, want) {
			t.Fatalf("生成中的气泡该有 %q：\n%s", want, view)
		}
	}
	// 别条会话的轮次：状态行说空闲，气泡也不该画在这儿
	other := liveFixture()
	other.turn.sessionID = "c2"
	view = other.View().Content
	if strings.Contains(view, "生成中… 12.3s") || strings.Contains(view, "/stop") {
		t.Fatalf("别条会话的轮次不该画在这儿：\n%s", view)
	}
	if status := strings.Join(viewLines(other)[inputLineIndex(viewLines(other))+2:], "\n"); !strings.Contains(status, "空闲") {
		t.Fatalf("当前会话没在跑就该说空闲：%q", status)
	}
}

// 长中文标题按**显示宽度**截断（不是按字符数）。
func TestTruncateUsesDisplayWidth(t *testing.T) {
	long := strings.Repeat("很长的标题", 20) // 每字 2 格
	got := truncate(long, 20)
	if runewidth.StringWidth(got) > 20 {
		t.Fatalf("截断后宽 %d：%q", runewidth.StringWidth(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("该带省略号：%q", got)
	}
	// ASCII 也不出错
	if got := truncate("abcdefghij", 5); got != "ab..." {
		t.Fatalf("ASCII 截断 = %q", got)
	}
}

// Update 也能直接测（不必起终端）：方向键不再换会话（没有左栏了）、ctrl+c 退出、回车真发。
func TestUpdateKeysAndQuit(t *testing.T) {
	m := fixture()
	// 上下键**不再**在会话列表里走（用户 2026-09-30 定的：会话选择只走 /resume）
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd != nil || updated.(model).selectedID != "c1" {
		t.Fatalf("方向键不该换会话：selectedID=%q", updated.(model).selectedID)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if after := updated.(model); after.selectedID != "c1" {
		t.Fatalf("方向键不该换会话：selectedID=%q", after.selectedID)
	}
	// 退出走 /quit 与 ctrl+c —— **没有裸 q**（那会和打字打架；Pi 也是这么做的）
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} // v2 没有 KeyCtrlC 常量，用修饰键构造
	_, quit := m.Update(ctrlC)
	if quit == nil {
		t.Fatal("ctrl+c 该退出")
	}
	if _, isQuit := quit().(tea.QuitMsg); !isQuit {
		t.Fatal("ctrl+c 该返回 Quit")
	}
	// 打了字再回车：**真发**（不再说"还没实现"）—— 空会话下也一样（sendCmd 顺手建会话）
	typed := m
	for _, r := range "你好" {
		updated, _ = typed.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		typed = updated.(model)
	}
	if typed.input != "你好" {
		t.Fatalf("打字该进输入行：%q", typed.input)
	}
	updated, cmd = typed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("回车该真发出去（给一个 Cmd）")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "发送中") {
		t.Fatalf("回车之后底栏该说在发：%q", said)
	}
	if after := updated.(model); after.input != "" {
		t.Fatalf("回车之后输入行该清空：%q", after.input)
	}
}

// 输入行渲染 + `/命令` 派发（照 Pi 的用法）。
func TestInputLineAndCommands(t *testing.T) {
	m := fixture()
	// 输入行长得像样（`> ` + 内容）
	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	typed := updated.(model)
	if !strings.Contains(typed.View().Content, "> /") {
		t.Fatalf("输入行该有提示符：\n%s", typed.View().Content)
	}
	// 退格删字
	updated, _ = typed.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if back := updated.(model); back.input != "" {
		t.Fatalf("退格该删掉：%q", back.input)
	}

	// /help 列出能用的与还没搬完的
	updated, _ = m.runCommand("/help")
	help := updated.(model).lastAction
	if !strings.Contains(help, "/new") || !strings.Contains(help, "/compact") {
		t.Fatalf("/help = %q", help)
	}
	// 不认识
	updated, _ = m.runCommand("/nonsense")
	if said := updated.(model).lastAction; !strings.Contains(said, "不认识") {
		t.Fatalf("不认识的命令 = %q", said)
	}
	// /new 会发命令（真发不发由 Cmd 决定，这里只断言它给了 Cmd）
	updated, cmd := m.runCommand("/new")
	if cmd == nil || !strings.Contains(updated.(model).lastAction, "新建") {
		t.Fatalf("/new 该动手：%q", updated.(model).lastAction)
	}
	// /state 要先选会话（fixture 里选了 c1，所以它该去取状态）
	updated, cmd = m.runCommand("/state")
	if cmd == nil {
		t.Fatal("/state 该去取状态")
	}
	// /quit 退出
	_, quit := m.runCommand("/quit")
	if quit == nil {
		t.Fatal("/quit 该退出")
	}
	if _, isQuit := quit().(tea.QuitMsg); !isQuit {
		t.Fatal("/quit 该返回 Quit")
	}
}

// 离线/空数据时也要有像样的画面（**不 panic**、有话可说）。
func TestEmptyState(t *testing.T) {
	// 空会话（还没进任何会话）：**消息区照旧在**（有话可说）+ 输入行 + 分隔线 + 状态行。
	// 没有左栏（会话列表走 /resume），也不该出现会话列表。
	m := model{width: 40, height: 6, ready: true}
	body := m.View().Content
	if strings.Contains(body, "＋ 新建对话") {
		t.Fatalf("不该有左栏：\n%s", body)
	}
	lines := strings.Split(body, "\n")
	if len(lines) != m.height {
		t.Fatalf("该正好 %d 行：\n%s", m.height, body)
	}
	input := inputLineIndex(lines)
	if input < 1 || !strings.HasPrefix(lines[input], ">") {
		t.Fatalf("该有输入行：\n%s", body)
	}
	if !strings.HasPrefix(strings.Join(lines[:input], "\n"), " 空会话") {
		t.Fatalf("消息区该给一句空会话的话：\n%s", body)
	}
	if !strings.Contains(body, "未连接") {
		t.Fatal("没连上后端要如实说")
	}
	// 挑选项开着（/resume）就铺满消息区 —— 那时确实有东西要看
	picking := model{width: 40, height: 6, ready: true, picker: pickerResume}
	if !strings.Contains(picking.View().Content, "挑一条") { // 窄宽度下标题会被截断，断言前缀
		t.Fatalf("挑选项要看得见：\n%s", picking.View().Content)
	}
}

// 装不下的消息**整块**不显示，并在消息区顶部如实说"上面还有几条"（别让界面骗人）。
func TestDroppedMessagesAreAnnounced(t *testing.T) {
	m := fixture()
	m.height = 8 // 只装得下最新那一块（署名 + 两行正文 + 空行）加顶部那行提示
	lines := viewLines(m)
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, "（上面还有 1 条）") {
		t.Fatalf("挤掉了就该说：\n%s", body)
	}
	// 装不下的那条**整块**不显示（不许露半截 —— 半截看起来像"不知道谁说的"）
	if strings.Contains(body, "在吗") {
		t.Fatalf("装不下的块不该露出来：\n%s", body)
	}
	// 留下来的那条**整块**都在：署名 + 正文
	area := strings.Join(lines[:inputLineIndex(lines)], "\n")
	for _, want := range []string{"助手 2.1s：", "在。", "有什么事？"} {
		if !strings.Contains(area, want) {
			t.Fatalf("留下的块该是完整的（缺 %q）：\n%s", want, body)
		}
	}
	// 署名与正文必须同时出现：署名行后面空着 ⇒ 那一块被劈开了
	for index, line := range lines {
		if !strings.HasSuffix(strings.TrimSpace(line), "：") {
			continue
		}
		if index+1 >= len(lines) || strings.TrimSpace(lines[index+1]) == "" {
			t.Fatalf("署名后面没有正文 ⇒ 块被劈开了：%q\n%s", line, body)
		}
	}
}

// 初次连接失败后，只要后来刷新成功，底栏就该补回"已连接 vX"（这个 bug 是真机上抓到的）。
func TestReconnectUpdatesFooter(t *testing.T) {
	m := fixture()
	m.version = "" // 假装刚才连不上
	updated, cmd := m.Update(sessionsMsg{sessions: m.sessions})
	next := updated.(model)
	if cmd == nil {
		t.Fatal("取数成功却没有后续命令（该顺手补一次 health）")
	}
	if !strings.Contains(next.View().Content, "未连接") {
		t.Fatal("还没拿到版本号之前，仍该显示未连接")
	}
	updated, _ = next.Update(healthMsg{health: Health{Status: "ok", Version: "0.1.0"}})
	if body := updated.(model).View().Content; !strings.Contains(body, "已连接 v0.1.0") {
		t.Fatal("补到版本号之后该显示已连接")
	}
	// 连不上时把版本号清掉（别留着陈旧状态）
	updated, _ = next.Update(healthMsg{err: errTest})
	if body := updated.(model).View().Content; !strings.Contains(body, "未连接") {
		t.Fatal("连不上不该显示已连接")
	}
}

var errTest = fmt.Errorf("dial tcp: connection refused")

// ── 命令面板（照 Pi：输入以 `/` 开头就出提示）──

func typeText(m model, text string) model {
	for _, r := range text {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(model)
	}
	return m
}

func TestPaletteShowsAndFilters(t *testing.T) {
	m := fixture()
	// 一打 `/` 就出面板，且列出全部命令
	m = typeText(m, "/")
	body := m.View().Content
	if !strings.Contains(body, "/new") || !strings.Contains(body, "/resume") || !strings.Contains(body, "/quit") {
		t.Fatalf("命令面板该列出全部命令：\n%s", body)
	}
	// 宽度账不许乱（面板也是界面的一部分）
	for _, line := range strings.Split(body, "\n") {
		if runewidth.StringWidth(line) > m.width {
			t.Fatalf("面板行超宽：%q", line)
		}
	}
	// 继续打就过滤（只留 resume）
	m = typeText(m, "res")
	body = m.View().Content
	if !strings.Contains(body, "/resume") || strings.Contains(body, "/quit") {
		t.Fatalf("该只剩 /resume：\n%s", body)
	}
	// `/res` 只命中 resume 一条（前缀匹配）
	if got := len(m.paletteMatches()); got != 1 {
		t.Fatalf("`/res` 该命中 1 条：%d", got)
	}
	// 退一格 ⇒ `/re`：resume 与 refresh 都算
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if back := updated.(model); len(back.paletteMatches()) != 2 {
		t.Fatalf("`/re` 该命中 2 条：%d", len(back.paletteMatches()))
	}
	// 一路删到 `/` ⇒ 恢复全部
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if back := updated.(model); len(back.paletteMatches()) != len(commands) {
		t.Fatalf("退格回 `/` 该恢复全部：%d", len(back.paletteMatches()))
	}
}

// 方向键在面板里走（面板没开就什么都不做）；Tab 补全；回车跑**高亮**那条。
func TestPaletteNavigationAndRun(t *testing.T) {
	m := fixture()
	m = typeText(m, "/")
	// 下键两次 ⇒ 高亮第 3 条（`/cut`），当前会话**不动**
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	walked := updated.(model)
	if walked.paletteIndex != 2 {
		t.Fatalf("下键该走面板：%d", walked.paletteIndex)
	}
	if walked.selectedID != "c1" {
		t.Fatalf("面板开着时方向键不该换会话：%q", walked.selectedID)
	}
	if matches := walked.paletteMatches(); matches[walked.paletteIndex].name != "cut" {
		t.Fatalf("第 3 条该是 cut：%s", matches[walked.paletteIndex].name)
	}
	// Tab 补全
	updated, _ = walked.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if filled := updated.(model); filled.input != "/cut " {
		t.Fatalf("Tab 该补全：%q", filled.input)
	}
	// 半截名字 + 回车 ⇒ 跑高亮那条（`/re` ⇒ resume ⇒ 打开挑选项）
	half := typeText(fixture(), "/re")
	updated, _ = half.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opened := updated.(model); opened.picker != pickerResume {
		t.Fatalf("`/re` 回车该跑 /resume（打开挑选项），得到 picker=%d", opened.picker)
	}
	// Esc 清输入（面板是推导出来的，输入空了面板自然没了）
	updated, _ = typeText(fixture(), "/re").Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cleared := updated.(model); cleared.input != "" || len(cleared.paletteMatches()) != 0 {
		t.Fatalf("Esc 该清输入：%q", cleared.input)
	}
}

// ── /new：当前会话为空时**无效**（空会话已经是新的了）──

func TestNewIsInvalidOnEmptySession(t *testing.T) {
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	updated, cmd := empty.runCommand("/new")
	if cmd != nil {
		t.Fatal("空会话上 /new 不该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "无效") {
		t.Fatalf("该如实说无效：%q", said)
	}
	// 选中的会话确实没有消息（拉过了）⇒ 也算空会话
	messageless := fixture()
	messageless.messages, messageless.messagesFor = nil, "c1"
	if _, cmd := messageless.runCommand("/new"); cmd != nil {
		t.Fatal("没有消息的会话上 /new 也不该发请求")
	}
	// 有消息 ⇒ 真建（并且把 /model 选的渠道 / 模型带上）
	busy := fixture()
	busy.chosenProvider, busy.chosenModel = "opencode-go", "kimi-k2"
	if _, cmd := busy.runCommand("/new"); cmd == nil {
		t.Fatal("有内容的会话上 /new 该真建")
	}
}

// ── /delete：删掉当前会话（**先摊开、再点头**），回到空会话 ──

func TestDeleteAsksBeforeRemovingTheSession(t *testing.T) {
	m := fixture()
	// 还没拉全消息 ⇒ 先拉（报不出"会删几条"就不该动手）
	m.messagesFor = ""
	if _, cmd := m.runCommand("/delete"); cmd == nil {
		t.Fatal("消息还没拉全时该先去拉")
	}
	// 拉全了：只摊开，**不发删除**
	m = fixture()
	updated, cmd := m.runCommand("/delete")
	armed := updated.(model)
	if cmd != nil {
		t.Fatal("点头之前不许发删除")
	}
	if armed.confirm == nil || armed.confirm.kind != confirmDeleteSession {
		t.Fatalf("该等确认：%+v", armed.confirm)
	}
	body := armed.View().Content
	if !strings.Contains(body, "2 条消息") || !strings.Contains(body, "1 份摘要") || !strings.Contains(body, "不可逆") {
		t.Fatalf("该说清会没掉什么：\n%s", body)
	}
	// 不答应 ⇒ 什么都不发生
	updated, _ = armed.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cancelled := updated.(model); cancelled.confirm != nil || cancelled.viewer != nil {
		t.Fatal("取消该把确认收掉")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "取消") {
		t.Fatalf("该如实说取消：%q", said)
	}
	// 答应（y 或回车）⇒ 才真发删除
	updated, cmd = armed.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("答应了该发删除")
	}
	if after := updated.(model); after.confirm != nil || after.viewer != nil {
		t.Fatal("答应了该把确认收掉")
	}
	updated, cmd = armed.confirmExecute()
	if cmd == nil {
		t.Fatal("回车 / y 该发删除")
	}
	updated, cmd = m.Update(deletedMsg{id: "c1"})
	next := updated.(model)
	if next.selectedID != "" || len(next.messages) != 0 {
		t.Fatalf("删完该回到空会话：selectedID=%q", next.selectedID)
	}
	if cmd == nil {
		t.Fatal("删完该重拉列表")
	}
	// 空会话上再删：什么都不发生
	if _, again := next.runCommand("/delete"); again != nil {
		t.Fatal("空会话上没有可删的")
	}
	// 失败要如实说
	updated, _ = m.Update(deletedMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "删除失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

// ── /cut：删一条消息及其之后的全部（**先预览、再点头**）──

func TestCutPreviewsThenDeletes(t *testing.T) {
	m := fixture()
	updated, cmd := m.runCommand("/cut")
	if cmd == nil {
		t.Fatal("/cut 该去取预览")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "预览") {
		t.Fatalf("该说去取预览：%q", said)
	}
	// 预览回来了：摊开五组，等点头（**仍然没删**）
	plan := DeletionPlan{
		DeletedMessageIDs:    []string{"m2", "m3"},
		DeletedSummaryIDs:    []string{"s1"},
		UnlinkedMessageIDs:   []string{"m1"},
		UnlinkedSummaryIDs:   []string{"s0"},
		LastDeletedMessageID: "m3",
	}
	updated, cmd = m.Update(cutPreviewMsg{sessionID: "c1", messageID: "m2", plan: plan})
	armied := updated.(model)
	if cmd != nil {
		t.Fatal("预览之后还不许删")
	}
	if armied.confirm == nil || armied.confirm.kind != confirmCut || armied.confirm.plan.LastDeletedMessageID != "m3" {
		t.Fatalf("该存下预览等点头：%+v", armied.confirm)
	}
	body := armied.View().Content
	for _, want := range []string{"消息 2 条", "摘要 1 份", "解链的消息 1 条", "回车 / y 执行"} {
		if !strings.Contains(body, want) {
			t.Fatalf("该摊开 %q：\n%s", want, body)
		}
	}
	// 点头 ⇒ 执行（带回预览里的末尾 id）
	updated, cmd = armied.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("点头该真删")
	}
	if after := updated.(model); after.confirm != nil || after.viewer != nil {
		t.Fatal("点头后该收掉确认框")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "删除 m2") {
		t.Fatalf("该说在删哪条：%q", said)
	}
	// 删回来了：如实报数并重拉
	updated, cmd = updated.(model).Update(cutDoneMsg{plan: plan})
	if cmd == nil {
		t.Fatal("删完该重拉消息与列表")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "已删 2 条消息") {
		t.Fatalf("该报数：%q", said)
	}
	// 失败要如实说（409 要重新预览）
	updated, _ = fixture().Update(cutDoneMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "删除失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

func TestCutGuards(t *testing.T) {
	// 空会话：没有可删的
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	if _, cmd := empty.runCommand("/cut"); cmd != nil {
		t.Fatal("空会话上不该发请求")
	}
	// 消息还没拉全 ⇒ 先拉
	m := fixture()
	m.messagesFor = ""
	if _, cmd := m.runCommand("/cut"); cmd == nil {
		t.Fatal("没拉全该先去拉")
	}
	// 找不到那条消息 ⇒ 报错，什么都不发
	m = fixture()
	updated, cmd := m.runCommand("/cut 查无此消息")
	if cmd != nil {
		t.Fatal("找不到就不该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "找不到") || !strings.Contains(said, "什么都没有发生") {
		t.Fatalf("该如实说找不到：%q", said)
	}
	// 预览失败：如实说，不进确认态
	updated, _ = m.Update(cutPreviewMsg{sessionID: "c1", messageID: "m2", err: errTest})
	if failed := updated.(model); failed.confirm != nil {
		t.Fatal("预览失败不该进确认态")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "预览失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

// ── /copy：把当前会话复制成新的一条（线性会话里的"分岔"）──

func TestCopySessionCommand(t *testing.T) {
	m := fixture()
	updated, cmd := m.runCommand("/copy")
	if cmd == nil {
		t.Fatal("/copy 该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "复制") {
		t.Fatalf("该说在复制：%q", said)
	}
	// 复制回来：**立刻**认新会话当当前会话（认 id，不用等列表刷新），并重拉列表与消息
	updated, cmd = m.Update(copiedMsg{session: Session{ID: "c9", Title: "副本"}})
	next := updated.(model)
	if cmd == nil {
		t.Fatal("复制完该重拉列表")
	}
	if next.selectedID != "c9" {
		t.Fatalf("该盯上新会话：%q", next.selectedID)
	}
	if said := next.lastAction; !strings.Contains(said, "已复制成") {
		t.Fatalf("该报新会话：%q", said)
	}
	// 空会话：没有可复制的
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	if _, cmd := empty.runCommand("/copy"); cmd != nil {
		t.Fatal("空会话上不该发请求")
	}
	// 失败要如实说
	updated, _ = fixture().Update(copiedMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "复制失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

// 确认框开着时，别的键都不该被当成打字或导航（只有"答应 / 不答应"）。
func TestConfirmBoxSwallowsOtherKeys(t *testing.T) {
	m := fixture()
	updated, _ := m.runCommand("/delete")
	armed := updated.(model)
	updated, cmd := armed.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	cancelled := updated.(model)
	if cmd != nil {
		t.Fatal("取消不该发命令")
	}
	if cancelled.input != "" || cancelled.confirm != nil {
		t.Fatalf("取消该原样收掉确认框：input=%q confirm=%v", cancelled.input, cancelled.confirm)
	}
}

// ── /resume：挑列表 或 直接给 uuid ──

func TestResumeWithUUID(t *testing.T) {
	m := fixture()
	// 完整 id
	updated, cmd := m.runCommand("/resume c2")
	if cmd == nil || updated.(model).selectedID != "c2" {
		t.Fatalf("给 uuid 该直接进：selectedID=%q", updated.(model).selectedID)
	}
	// 找不到 ⇒ 报错，**什么都不发生**
	before := m.selectedID
	beforeMessages := len(m.messages)
	updated, cmd = m.runCommand("/resume 00000000-0000-0000-0000-000000000000")
	after := updated.(model)
	if cmd != nil || after.selectedID != before || len(after.messages) != beforeMessages || after.picker != pickerNone {
		t.Fatalf("找不到就不许动：selectedID=%q cmd=%v picker=%d", after.selectedID, cmd != nil, after.picker)
	}
	if said := after.lastAction; !strings.Contains(said, "找不到") {
		t.Fatalf("该说找不到：%q", said)
	}
}

func TestResumePicker(t *testing.T) {
	m := fixture()
	updated, cmd := m.runCommand("/resume")
	opened := updated.(model)
	if cmd != nil || opened.picker != pickerResume {
		t.Fatalf("不给 uuid 该开挑选项：picker=%d", opened.picker)
	}
	if !strings.Contains(opened.View().Content, "挑一条已有会话") {
		t.Fatalf("挑选项该有标题：\n%s", opened.View().Content)
	}
	// 挑选项**铺满消息区**（不是挤在输入行上面一小块）
	if !strings.Contains(opened.View().Content, "第二段对话") {
		t.Fatalf("挑选项该列出会话：\n%s", opened.View().Content)
	}
	// 下键 + 回车 ⇒ 进第二条
	updated, _ = opened.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, cmd = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := updated.(model)
	if next.picker != pickerNone || next.selectedID != "c2" || cmd == nil {
		t.Fatalf("回车该进第二条：picker=%d selectedID=%q", next.picker, next.selectedID)
	}
	// Esc 取消
	updated, _ = opened.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cancelled := updated.(model); cancelled.picker != pickerNone || cancelled.selectedID != m.selectedID {
		t.Fatal("Esc 该原样取消")
	}
	// 没有会话时 /resume 说清楚
	blank := model{width: 60, height: 14, ready: true}
	if _, cmd := blank.runCommand("/resume"); cmd != nil {
		t.Fatal("没有会话时不该开挑选项")
	}
}

// ── /model：按 provider 排列，选中后落到会话（或留给下一条）──

func TestModelPickerGroupsByProvider(t *testing.T) {
	m := fixture()
	updated, cmd := m.runCommand("/model")
	asking := updated.(model)
	if cmd == nil || asking.picker != pickerModel {
		t.Fatalf("/model 该去拉模型并开挑选项：picker=%d", asking.picker)
	}
	updated, _ = asking.Update(modelsMsg{models: m.models})
	loaded := updated.(model)
	if len(loaded.models) != 3 {
		t.Fatalf("模型该存下来：%d", len(loaded.models))
	}
	body := loaded.View().Content
	for _, want := range []string{"opencode-go", "dummy", "DeepSeek V4 Flash", "Kimi K2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("面板该按 provider 分组显示：缺 %q\n%s", want, body)
		}
	}
	// 回车 ⇒ 改当前会话（有选中会话 ⇒ 发 PATCH）
	updated, cmd = loaded.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	chosen := updated.(model)
	if chosen.chosenProvider != "opencode-go" || chosen.chosenModel != "deepseek-v4-flash" {
		t.Fatalf("该选中第一条：%s / %s", chosen.chosenProvider, chosen.chosenModel)
	}
	if cmd == nil {
		t.Fatal("有会话时该发 PATCH")
	}
	// 空会话时 ⇒ 只记下来，不发请求
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	empty.models, empty.picker, empty.pickerIndex = m.models, pickerModel, 0
	updated, cmd = empty.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("没有会话时不该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "留给下一条") {
		t.Fatalf("该说清楚留给下一条：%q", said)
	}
	// 拉失败要如实说
	updated, _ = fixture().Update(modelsMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "拉模型失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
}

// 打开 TUI 就是**空会话**（不建库里的行、也不自动跳进旧会话）。
func TestInitialModelStartsEmpty(t *testing.T) {
	m := initialModel(NewClient("http://127.0.0.1:8787", ""))
	if m.selectedID != "" {
		t.Fatalf("该停在空会话：selectedID=%q", m.selectedID)
	}
	// 拿到会话列表也不自动选（用户没点就不动）
	updated, _ := m.Update(sessionsMsg{sessions: fixture().sessions})
	if after := updated.(model); after.selectedID != "" {
		t.Fatalf("列表回来也不该自动选：selectedID=%q", after.selectedID)
	}
	// 空会话的界面形状：**消息区还在**（有话说），只是没有会话列表
	if body := updated.(model).View().Content; !strings.Contains(body, "空会话") || strings.Contains(body, "＋ 新建对话") {
		t.Fatalf("空会话该是消息区 + 底部，不是会话列表：\n%s", body)
	}
}

// liveFixture：一条正在生成的会话（界面按状态**合成**气泡；库里还没有它）。
func liveFixture() model {
	m := fixture()
	m.turn = &liveTurn{
		sessionID: "c1", messageID: "m3", phase: "streaming", elapsedMS: 12300,
		text: "说到一半", thinking: "嗯……",
	}
	return m
}

// 轮询：增量按游标累加，**读不消费**（同一段不会画两遍）；还在跑就接着约下一次。
func TestTurnPollingAppendsIncrementsAndSchedulesNextTick(t *testing.T) {
	m := liveFixture()
	updated, cmd := m.Update(turnPollMsg{
		sessionID: "c1",
		status:    TurnStatus{Phase: "streaming", ElapsedMS: 13000, Chars: 9},
		slice:     StreamSlice{Text: "，然后", Next: 9, Thinking: "……", ThinkNext: 5},
	})
	next := updated.(model)
	if next.turn.text != "说到一半，然后" || next.turn.thinking != "嗯……"+"……" {
		t.Fatalf("增量该接着画：%+v", next.turn)
	}
	if next.turn.from != 9 || next.turn.thinkFrom != 5 {
		t.Fatalf("游标该跟着走：%+v", next.turn)
	}
	if cmd == nil {
		t.Fatal("还在跑 ⇒ 该约下一次轮询")
	}
	// 同一个游标再来一次（后端说 next 还是 9、增量空）= 不重复画
	repeated, _ := next.Update(turnPollMsg{
		sessionID: "c1",
		status:    TurnStatus{Phase: "streaming", ElapsedMS: 13300},
		slice:     StreamSlice{Next: 9, ThinkNext: 5},
	})
	if again := repeated.(model); again.turn.text != "说到一半，然后" {
		t.Fatalf("空增量不该改画面：%q", again.turn.text)
	}
}

// 收到 `idle` ⇒ 气泡消失 + **一次性重拉**消息（真消息取代它）；收到 `error` ⇒ 气泡消失 + 报原因。
func TestTurnPollingSettlesAndReportsErrors(t *testing.T) {
	m := liveFixture()
	done, cmd := m.Update(turnPollMsg{
		sessionID: "c1", status: TurnStatus{Phase: "idle"}, slice: StreamSlice{Done: true},
	})
	if done.(model).turn != nil {
		t.Fatal("结束之后气泡该消失（库里那条真消息接手）")
	}
	if cmd == nil {
		t.Fatal("收到 idle 该一次性重拉消息列表")
	}
	if said := done.(model).lastAction; !strings.Contains(said, "完成") {
		t.Fatalf("底栏该说完成：%q", said)
	}
	failed, _ := m.Update(turnPollMsg{
		sessionID: "c1", status: TurnStatus{Phase: "error", Error: "上游 429：额度"},
	})
	if failed.(model).turn != nil {
		t.Fatal("失败之后气泡该消失（库里半条都没有）")
	}
	if said := failed.(model).lastAction; !strings.Contains(said, "上游 429") {
		t.Fatalf("底栏该报出失败原因：%q", said)
	}
}

// 按停：真发 `/stop`；回 false 就如实说"什么都没发生"（幂等）。
func TestStopCommand(t *testing.T) {
	m := liveFixture()
	updated, cmd := m.runCommand("/stop")
	if cmd == nil {
		t.Fatal("/stop 该真去按停")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "停止") {
		t.Fatalf("底栏该说在停：%q", said)
	}
	stopped, _ := updated.(model).Update(stoppedMsg{sessionID: "c1", stopped: true})
	if after := stopped.(model); after.turn != nil {
		t.Fatal("停下来了 ⇒ 气泡该消失")
	}
	// 没在跑就回 false：底栏要如实说
	idle := fixture()
	// 空会话（没有任何会话）时连发都不用发
	if _, cmd := (model{client: NewClient("http://127.0.0.1:1", ""), selectedID: ""}).runCommand("/stop"); cmd != nil {
		t.Fatal("没有会话时 /stop 不该发请求")
	}
	_ = idle
}

// 生成中再回车发送：**先说清楚**（后端会 409），不去白跑一次。
func TestSendWhileGeneratingIsRefusedLocally(t *testing.T) {
	m := liveFixture()
	typed := typeText(m, "插队")
	updated, cmd := typed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("生成中不该再发（后端会 409）")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "还在生成中") {
		t.Fatalf("该说清楚在生成中：%q", said)
	}
}

// 202 回来：进列表（空会话里冒出的新会话）、**立刻认它当当前会话**、开始看进度；
// 库里的用户那句靠重拉消息拿到。
func TestSentMsgStartsPolling(t *testing.T) {
	m := initialModel(NewClient("http://127.0.0.1:8787", ""))
	created := Session{ID: "c9", Title: "新会话", Provider: "dummy", Model: "dummy", AgentID: "default"}
	messageID := "m9"
	updated, cmd := m.Update(sentMsg{
		sessionID: "c9", created: &created,
		accepted: TurnAccepted{Backend: "dummy", Turn: TurnStatus{Phase: "pending", MessageID: &messageID}},
	})
	next := updated.(model)
	if next.turn == nil || next.turn.sessionID != "c9" || next.turn.messageID != "m9" {
		t.Fatalf("该开始盯这一轮：%+v", next.turn)
	}
	if next.selectedID != "c9" {
		t.Fatalf("新建的会话该当上当前会话：%q", next.selectedID)
	}
	if cmd == nil {
		t.Fatal("该去重拉会话列表与消息、并开始轮询")
	}
	if said := next.lastAction; !strings.Contains(said, "dummy") {
		t.Fatalf("底栏该报是哪条后端答的：%q", said)
	}
}

// ── 用户实跑抓到的 bug：**发一句话后消息区一片空白** ──

// 根因：当前会话以前存的是**下标**，而 `GET /sessions` 按 `updated_at` 排序 ——
// 发一句话就把那条会话顶到最前 ⇒ 下标指到了**别人**身上 ⇒ 消息回来时
// `message.sessionID != currentSession().ID` ⇒ 被丢掉 ⇒ 消息区一片空白。
// 现在认 **id**：列表怎么重排都不影响"选中的是哪条"。
func TestSelectionSurvivesListResorting(t *testing.T) {
	m := fixture()
	m.selectedID = "c2" // 选中第二条（发送前它排在后面）
	m.messagesFor, m.messages = "c2", nil
	// 后端把刚发过话的 c2 排到最前（顺序变了 —— 以前这一下就把选中甩到别人身上）
	updated, _ := m.Update(sessionsMsg{sessions: []Session{
		{ID: "c2", Title: "第二段对话", Provider: "dummy", Model: "dummy"},
		{ID: "c1", Title: "逆序数该怎么写", Provider: "opencode-go", Model: "deepseek-v4-flash"},
	}})
	next := updated.(model)
	if next.currentSession() == nil || next.currentSession().ID != "c2" {
		t.Fatalf("列表重排后仍该盯着 c2：%+v", next.currentSession())
	}
	// 用户那句回来了 ⇒ 必须收下（以前会因对不上号被丢掉 ⇒ 空白）
	updated, _ = next.Update(messagesMsg{sessionID: "c2", messages: []Message{
		{ID: "m8", Role: "user", Content: "发出去的那句话"},
	}})
	if body := updated.(model).View().Content; !strings.Contains(body, "发出去的那句话") {
		t.Fatalf("发出去的那句该出现在消息区：\n%s", body)
	}
	// 选中的会话被别处删了：老实回到空会话（不许赖在别人身上）
	updated, _ = next.Update(sessionsMsg{sessions: []Session{{ID: "c1", Title: "只剩这条"}}})
	if after := updated.(model); after.selectedID != "" || !strings.Contains(after.lastAction, "别处删的") {
		t.Fatalf("选中的会话没了就该回空会话：selectedID=%q lastAction=%q", after.selectedID, after.lastAction)
	}
}

// ── 满线 / 着色（2026-09-30：升到"现代终端"观感；规矩见 AGENTS.md）──

// stripANSI：去掉转义序列 —— 它是**零宽**的，宽度账与逐字节断言都只看去掉之后的文本。
func stripANSI(text string) string {
	var builder strings.Builder
	escaping := false
	for _, r := range text {
		switch {
		case escaping:
			escaping = r != 'm' // 我们只发 SGR，都以 m 收尾
		case r == 0x1b:
			escaping = true
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// colored：同一个 model，只是把着色打开（**布局必须一字不差** —— 颜色是最后一步，零宽）。
func colored(m model) model {
	m.style = styler{enabled: true}
	return m
}

// **先排版、后上色**：着色版剥掉转义之后，必须与纯文本版**逐字节相同**。
// 差一个字就说明"先上色再截断"了（转义序列零宽，会多算格 ⇒ 行超宽 / 块被劈开）。
func TestColorsDoNotChangeLayout(t *testing.T) {
	withContext := fixture()
	withContext.context = &ContextUsage{UsedTokens: 12300, BudgetTokens: 131000}
	withContext.contextFor = "c1"
	overBudget := fixture()
	overBudget.context = &ContextUsage{UsedTokens: 140000, BudgetTokens: 131000, OverBudget: true}
	overBudget.contextFor = "c1"
	withViewer := fixture()
	withViewer.viewer, withViewer.viewerTitle, withViewer.viewerHint = []string{" 一行", " 两行"}, "确认删除", "回车 / y 执行 · 其他键取消"
	withPicker := fixture()
	withPicker.picker, withPicker.pickerIndex = pickerResume, 1

	cases := map[string]model{
		"三块": fixture(), "生成中": liveFixture(), "空会话": emptyFixture(), "窄屏": narrowFixture(),
		"上下文占用": withContext, "超预算": overBudget, "查看器": withViewer, "挑选项": withPicker,
	}
	for name, plain := range cases {
		if strings.Contains(plain.View().Content, "\x1b[") {
			t.Fatalf("%s：零值 styler = 关 ⇒ 测试路径必须是纯文本", name)
		}
		if got := stripANSI(colored(plain).View().Content); got != plain.View().Content {
			t.Fatalf("%s：着色不该改排版\n--- 纯文本 ---\n%s\n--- 去色后 ---\n%s", name, plain.View().Content, got)
		}
	}
	// 真开着色时要真的有颜色（上面那些断言别成了"两边都纯文本"的假绿）
	if !strings.Contains(colored(fixture()).View().Content, "\x1b[") {
		t.Fatal("开着色时该真的有颜色")
	}
}

// 满线：宽屏用 `─`，**窄屏退回 ASCII `-`**（`─` 是模糊宽度字符，CJK 终端可能按两格排 ⇒ 窄屏会溢出换行）。
func TestRuleFallsBackToASCIIOnNarrowScreens(t *testing.T) {
	if got := rule(10, ruleMinWidth-1); got != strings.Repeat("-", 10) {
		t.Fatalf("窄屏该退回 ASCII：%q", got)
	}
	if got := rule(10, ruleMinWidth); got != strings.Repeat("─", 10) {
		t.Fatalf("宽屏该用满线：%q", got)
	}
	// 整屏：46 列的屏上一个 `─` 都不许有；100 列的有
	for _, line := range viewLines(narrowFixture()) {
		if strings.Contains(line, "─") {
			t.Fatalf("窄屏不该出现模糊宽度的满线：%q", line)
		}
	}
	if !strings.Contains(strings.Join(viewLines(fixture()), "\n"), "─") {
		t.Fatal("宽屏该画满线")
	}
}

// token 缩写（照 Pi 的形状 `12.3k/131k (9.4%)`）：整千不带小数点，千以下原样。
func TestFormatTokens(t *testing.T) {
	for want, given := range map[string]int{"0": 0, "999": 999, "1k": 1000, "12.3k": 12300, "131k": 131000} {
		if got := formatTokens(given); got != want {
			t.Fatalf("formatTokens(%d) = %q，该是 %q", given, got, want)
		}
	}
}

// 状态行那一档"上下文占用"：会话名之后、在不在跑之前；**换会话时旧的那份不许拿来画**。
func TestStatusLineShowsContextUsage(t *testing.T) {
	m := fixture()
	m.context = &ContextUsage{UsedTokens: 12300, BudgetTokens: 131000}
	m.contextFor = "c1"
	status := strings.Join(viewLines(m)[inputLineIndex(viewLines(m))+2:], "\n")
	if !strings.Contains(status, "12.3k/131k (9.4%)") {
		t.Fatalf("状态行该报上下文占用：%q", status)
	}
	if !strings.Contains(status, "opencode-go/deepseek-v4-flash") || !strings.Contains(status, "空闲") {
		t.Fatalf("占用该夹在渠道/模型与在不在跑之间：%q", status)
	}
	// 换一条会话：那份还旧着 ⇒ 不许显示
	m.selectedID = "c2"
	if strings.Contains(m.View().Content, "12.3k") {
		t.Fatal("换会话之后不许拿旧会话的占用画")
	}
	// 没拉到（没进过这个接口）就不显示这一档 —— 界面不许编数
	if fresh := fixture(); strings.Contains(fresh.View().Content, "%") {
		t.Fatalf("没拉到时不该编一个占用出来：\n%s", fresh.View().Content)
	}
	// 预算为 0（上游没报上下文）⇒ 照实报 0.0%，别算出 NaN/Inf
	m.selectedID, m.context = "c1", &ContextUsage{UsedTokens: 35, BudgetTokens: 0}
	if !strings.Contains(m.View().Content, "35/0 (0.0%)") {
		t.Fatalf("预算为 0 该照实报 0：\n%s", m.View().Content)
	}
}

// 超预算 ⇒ 那一段**红**（别的档照旧）；比例是**自己算的** `used/budget`（契约里的 `ratio` 不是这个）。
func TestContextOverBudgetIsRed(t *testing.T) {
	m := colored(fixture())
	m.context = &ContextUsage{UsedTokens: 140000, BudgetTokens: 131000, OverBudget: true}
	m.contextFor = "c1"
	if !strings.Contains(m.View().Content, "\x1b[31m140k/131k (106.9%)\x1b[0m") {
		t.Fatalf("超预算该整段标红（且照实报 >100%%）：\n%q", m.View().Content)
	}
	// 没超就不红
	m.context.OverBudget, m.context.UsedTokens = false, 12300
	if strings.Contains(m.View().Content, "\x1b[31m12.3k/131k") {
		t.Fatal("没超预算不该红")
	}
}

// 拉取时机：**进会话时**一次、**一轮结束后**一次 —— 不是每 300ms 都拉（占用只在整段落库后才变）。
func TestContextIsFetchedOnEnterAndAfterTurn(t *testing.T) {
	m := fixture()
	if _, cmd := m.Update(messagesMsg{sessionID: "c1", messages: m.messages}); cmd == nil {
		t.Fatal("进会话（拿到消息）该顺手量一次上下文占用")
	}
	// 同一条会话已经量过 ⇒ 不再重复拉
	loaded := m
	loaded.context, loaded.contextFor = &ContextUsage{UsedTokens: 1}, "c1"
	if _, cmd := loaded.Update(messagesMsg{sessionID: "c1", messages: loaded.messages}); cmd != nil {
		t.Fatal("同一条会话不必每拉一次消息就重量一次占用")
	}
	// 一轮结束（idle）⇒ 重拉消息 + 重量占用（同一个 Batch 里）
	live := liveFixture()
	done, cmd := live.Update(turnPollMsg{sessionID: "c1", status: TurnStatus{Phase: "idle"}, slice: StreamSlice{Done: true}})
	if done.(model).turn != nil || cmd == nil {
		t.Fatal("收到 idle 该收干这一轮并重拉")
	}
	// 量回来的数落在状态行上（拉失败就先不显示这一档，且**别去打扰底栏**）
	updated, _ := loaded.Update(contextMsg{sessionID: "c1", usage: ContextUsage{UsedTokens: 12300, BudgetTokens: 131000}})
	if !strings.Contains(updated.(model).View().Content, "12.3k/131k (9.4%)") {
		t.Fatalf("量回来的占用该显示出来：\n%s", updated.(model).View().Content)
	}
	before := updated.(model).lastAction
	failed, _ := updated.(model).Update(contextMsg{sessionID: "c1", err: errTest})
	if strings.Contains(failed.(model).View().Content, "k/") {
		t.Fatal("拉不到就不显示这一档（界面不许编数）")
	}
	if failed.(model).lastAction != before {
		t.Fatalf("量占用失败不该顶掉底栏那句话：%q → %q", before, failed.(model).lastAction)
	}
}

// 失败标红：**只有那句原话还摆在底栏时**才红 —— 别的动作一改底栏，红自己就退了。
func TestFailureIsRedOnlyWhileItIsTheLastAction(t *testing.T) {
	m := colored(fixture())
	m = m.fail("删除失败：404 not_found")
	if !strings.Contains(m.View().Content, "\x1b[31m删除失败：404 not_found\x1b[0m") {
		t.Fatalf("失败该标红：\n%q", m.View().Content)
	}
	m.lastAction = "消息 2 条" // 后来又发生了一件正常的事
	if strings.Contains(m.View().Content, "\x1b[31m") {
		t.Fatalf("红该跟着那句话一起退掉：\n%q", m.View().Content)
	}
}

// 降级三档：`NO_COLOR` / `TERM=dumb` / 非 TTY ⇒ 一律纯文本。
func TestColorDegradesToPlainText(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if !colorEnabled(env(map[string]string{"TERM": "xterm-256color"}), true) {
		t.Fatal("普通终端该上色")
	}
	if colorEnabled(env(map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}), true) {
		t.Fatal("NO_COLOR 该关掉颜色")
	}
	if colorEnabled(env(map[string]string{"TERM": "dumb"}), true) {
		t.Fatal("TERM=dumb 该关掉颜色")
	}
	if colorEnabled(env(map[string]string{"TERM": "xterm-256color"}), false) {
		t.Fatal("非 TTY 该纯文本")
	}
}

// 挑选项：**选中的那条粗体 + 青**（未选中保持默认）—— 挑东西得看出来高亮在哪。
func TestPickerHighlightIsBoldCyan(t *testing.T) {
	m := colored(fixture())
	m.picker, m.pickerIndex = pickerResume, 0
	body := m.View().Content
	if !strings.Contains(body, "\x1b[1;36m> ") {
		t.Fatalf("选中的那条该是粗体 + 青：\n%q", body)
	}
	if strings.Contains(body, "\x1b[1;36m  第二段对话") {
		t.Fatal("未选中的那条不该被高亮")
	}
	// 命令面板一样
	full := colored(typeText(fixture(), "/"))
	if !strings.Contains(full.View().Content, "\x1b[1;36m> /new") {
		t.Fatalf("命令面板该高亮当前那条：\n%q", full.View().Content)
	}
}

// `/outgoing` 查看器：**摘要是压缩过的 ⇒ 黄**、系统提示词 ⇒ 暗、原样消息 ⇒ 默认色。
func TestOutgoingViewerColorsBySource(t *testing.T) {
	m := colored(fixture())
	blocks := int64(4)
	summaryID, messageID := "s1", "m9"
	updated, _ := m.Update(outgoingMsg{items: []Outgoing{
		{Role: "system", Content: "你是主持人", Source: "system"},
		{Role: "assistant", Content: "压缩摘要", Source: "summary", SummaryID: &summaryID, Blocks: &blocks},
		{Role: "user", Content: "原样那句", Source: "message", MessageID: &messageID},
	}})
	body := updated.(model).View().Content
	if !strings.Contains(body, "\x1b[33m摘要 "+shortID(summaryID)) {
		t.Fatalf("摘要那条该是黄：\n%q", body)
	}
	if !strings.Contains(body, "\x1b[2m系统提示词") {
		t.Fatalf("系统提示词该是暗色：\n%q", body)
	}
	if !strings.Contains(body, "消息 "+shortID(messageID)) {
		t.Fatalf("消息那条该列出来：\n%q", body)
	}
	if strings.Contains(body, "\x1b[33m消息 ") || strings.Contains(body, "\x1b[2m消息 ") {
		t.Fatal("原样消息保持默认色（别和压缩出来的混）")
	}
}
