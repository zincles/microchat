package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
				Messages: 2, Turn: TurnStatus{Phase: "idle"}},
			{ID: "c2", Title: "第二段对话", Provider: "dummy", Model: "dummy", AgentID: "default", Messages: 7},
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

// emptyFixture：**还没有可用会话**的样子（兵灾态：启动编排"建那一条"还没回来 / 失败了）。
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
	if lines[input+1] != rule(m.width, m.width, m.ascii) {
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

// **没有可用会话**（兵灾态）也别只剩输入行：消息区还得在（如实提示 + /new 是出路），状态行照旧。
func TestNoSessionStillHasMessageArea(t *testing.T) {
	m := emptyFixture()
	lines := viewLines(m)
	if len(lines) != m.height {
		t.Fatalf("该正好 %d 行：%d", m.height, len(lines))
	}
	input := inputLineIndex(lines)
	if body := strings.Join(lines[:input], "\n"); !strings.Contains(body, "没有可用会话") {
		t.Fatalf("没有会话时消息区该有话说：\n%s", m.View().Content)
	}
	if !strings.Contains(strings.Join(lines[input+2:], "\n"), "未连接") {
		t.Fatalf("状态行该如实说没连上：\n%s", m.View().Content)
	}
	// 兵灾态**不是**会话列表
	if strings.Contains(m.View().Content, "挑一条已有会话") {
		t.Fatalf("没有会话时不该铺开挑选项：\n%s", m.View().Content)
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

// ── 输入行的光标（可见 + 可移动）──
//
// 两条纪律在这儿交汇：光标位置是 **rune 下标**（model），摆到终端上是**显示列**（runewidth）；
// 而它**不画进正文** ✗ —— 走的是 Bubble Tea v2 的 `View.Cursor`（真终端光标）。

// press：喂一个特殊键（←/→/Home/End/Delete/… 没有 Text，只靠 Code 认）。
func press(m model, key tea.KeyPressMsg) model {
	updated, _ := m.Update(key)
	return updated.(model)
}

// **光标列必须按显示宽度算**（本任务最容易歪的地方）：`> ` 前缀两格 + 光标**前**那段的显示宽度。
// 按 rune 数算，`你好|` 会算成 4 格而实际该是 6 ⇒ 光标指到字中间。
func TestInputCursorColumnUsesDisplayWidth(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		cursor int
	}{
		{name: "全 ASCII 中间", input: "abcd", cursor: 2},  // 2 + 2 = 4（按 rune 数也一样 —— 这种行看不出歪）
		{name: "CJK 行首", input: "你好", cursor: 0},        // 2 + 0 = 2
		{name: "CJK 行尾", input: "你好", cursor: 2},        // 2 + 4 = 6
		{name: "CJK 中间", input: "你好", cursor: 1},        // 2 + 2 = 4（与行尾不同 ⇒ 证明是按字宽走的）
		{name: "ASCII 接 CJK", input: "a你", cursor: 2},   // 2 + 1 + 2 = 5
		{name: "CJK 尾接 ASCII", input: "你好a", cursor: 3}, // 2 + 4 + 1 = 7
	}
	for _, c := range cases {
		m := fixture()
		m.input, m.inputCursor = c.input, c.cursor
		want := 2 + runewidth.StringWidth(string([]rune(c.input)[:c.cursor]))
		cursor := m.View().Cursor
		if cursor == nil {
			t.Fatalf("%s：普通界面上该看得见光标", c.name)
		}
		if cursor.X != want {
			t.Fatalf("%s：光标列 = %d，该是 %d（2 格前缀 + %q 的显示宽度）", c.name, cursor.X, want, string([]rune(c.input)[:c.cursor]))
		}
		// 行也得对：就是输入行那一行（`> ` 提示符那行）
		if want := inputLineIndex(viewLines(m)); cursor.Y != want {
			t.Fatalf("%s：光标行 = %d，该在输入行第 %d 行", c.name, cursor.Y, want)
		}
	}

	// 中文这条必须**真的**与"按 rune 数算"不同（否则上面那几条断言等于没测）
	m := fixture()
	m.input, m.inputCursor = "你好", 2
	if m.inputCursorColumn() == 2+m.inputCursor {
		t.Fatal("CJK 下光标列不该等于 2+rune 数 —— 那正是「指到字中间」的算法")
	}
}

// 编辑键：插入在**光标处**、退格删**光标前**一个 rune、Delete 删**光标处**那个 rune，光标跟着走。
func TestInputCursorEditingKeys(t *testing.T) {
	left := tea.KeyPressMsg{Code: tea.KeyLeft}
	right := tea.KeyPressMsg{Code: tea.KeyRight}
	home := tea.KeyPressMsg{Code: tea.KeyHome}
	end := tea.KeyPressMsg{Code: tea.KeyEnd}
	ctrlA := tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
	ctrlE := tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}
	check := func(m model, input string, cursor int, what string) model {
		if m.input != input || m.inputCursor != cursor {
			t.Fatalf("%s：input=%q cursor=%d，该是 %q / %d", what, m.input, m.inputCursor, input, cursor)
		}
		return m
	}

	m := check(typeText(fixture(), "abcd"), "abcd", 4, "打字落在末尾")
	m = check(press(press(m, left), left), "abcd", 2, "← 一次一格")
	m = check(typeText(m, "X"), "abXcd", 3, "插在**光标处**（不是追加）")
	m = check(press(m, tea.KeyPressMsg{Code: tea.KeyDelete}), "abXd", 3, "Delete 删光标处那个")
	m = check(press(m, tea.KeyPressMsg{Code: tea.KeyBackspace}), "abd", 2, "退格删光标前那个")
	m = check(press(m, right), "abd", 3, "→ 一次一格")
	m = check(press(m, right), "abd", 3, "末尾再 → 不动")
	m = check(press(m, tea.KeyPressMsg{Code: tea.KeyDelete}), "abd", 3, "末尾 Delete 不动")
	m = check(press(m, home), "abd", 0, "Home 到首")
	m = check(press(m, left), "abd", 0, "行首再 ← 不动")
	m = check(press(m, tea.KeyPressMsg{Code: tea.KeyBackspace}), "abd", 0, "行首退格不动")
	m = check(typeText(m, "Z"), "Zabd", 1, "行首打字插在最前")
	m = check(press(press(m, end), end), "Zabd", 4, "End 到尾")
	m = check(typeText(m, "!"), "Zabd!", 5, "尾部追加")
	m = check(press(m, ctrlA), "Zabd!", 0, "Ctrl+A = 到首")
	m = check(press(m, ctrlE), "Zabd!", 5, "Ctrl+E = 到尾")

	// CJK 也要按 **rune** 走（一个汉字一次删掉，不劈成半个字节）
	cjk := fixture()
	cjk.input, cjk.inputCursor = "你好", 1
	cjk = check(press(cjk, tea.KeyPressMsg{Code: tea.KeyBackspace}), "好", 0, "中文退格删一个 rune")
	cjk = check(press(cjk, tea.KeyPressMsg{Code: tea.KeyDelete}), "", 0, "中文 Delete 删一个 rune")
	cjk = check(typeText(cjk, "你好"), "你好", 2, "中文按 rune 计数")
	cjk = check(press(press(cjk, left), left), "你好", 0, "中文 ← 一次一格（列走两格）")

	// Esc 清输入 ⇒ 光标归零；回车提交后也归零（且输入行清空）
	check(press(cjk, tea.KeyPressMsg{Code: tea.KeyEscape}), "", 0, "Esc 清输入")
	// （回车会派生一个"真发"的 Cmd，测试不执行它 —— 这里只验光标与输入行）
	check(press(typeText(fixture(), "在吗"), tea.KeyPressMsg{Code: tea.KeyEnter}), "", 0, "回车后输入行清空、光标归零")

	// 面板开着时：←/→ 仍是移光标（↑/↓ 才管面板 —— 两者不冲突）
	panel := typeText(fixture(), "/resu")
	if len(panel.paletteMatches()) == 0 {
		t.Fatal("`/resu` 该弹出面板")
	}
	panel = check(press(panel, left), "/resu", 4, "面板开着时 ← 仍移光标")
	if len(panel.paletteMatches()) == 0 {
		t.Fatal("移光标不该把面板弄没（面板是**推导**出来的，与光标无关）")
	}
	panel = check(press(panel, tea.KeyPressMsg{Code: tea.KeyUp}), "/resu", 4, "↑ 只管面板，不动光标")
	panel = check(press(panel, tea.KeyPressMsg{Code: tea.KeyTab}), "/resume ", 8, "Tab 补全后光标跟到末尾")
}

// 光标走的是**终端光标**（`View.Cursor`），不是画进正文的字符 ⇒ 正文一个字节都不该变。
// （现有那些逐字节断言一个字都不改，靠的就是这条。）
func TestCursorDoesNotTouchContent(t *testing.T) {
	m := fixture()
	m.input = "你好ab"
	head, tail := m, m
	head.inputCursor, tail.inputCursor = 0, len([]rune(m.input))
	if head.View().Content != tail.View().Content {
		t.Fatalf("光标位置不该改正文（改了就是把它画进正文了）：\n--- 行首 ---\n%s\n--- 行尾 ---\n%s", head.View().Content, tail.View().Content)
	}
	if strings.Contains(head.View().Content, "\x1b[7m") {
		t.Fatal("光标不该是画出来的反色方块")
	}
	// 着色打开也一样：正文里不该多出任何东西（颜色是最后一步，零宽）
	if colored(head).View().Content != colored(tail).View().Content {
		t.Fatal("着色版里光标位置同样不该改正文")
	}
	// 而且输入行本身就长这样（`> ` + 原文）—— 正文里不该有别的光标痕迹
	if !strings.Contains(tail.View().Content, "> 你好ab") {
		t.Fatalf("输入行该是 `> ` + 原文：\n%s", tail.View().Content)
	}
}

// 输入行不是当前活动面时（挑选项 / 查看器 / 确认框铺满消息区）⇒ 把光标**藏掉**（别在屏幕上乱闪）；
// 生成中**照旧显示**（输入行仍然可用，只是提交会被后端 409 顶回来）。
func TestCursorHiddenWhenInputLineIsNotActive(t *testing.T) {
	if fixture().View().Cursor == nil {
		t.Fatal("普通界面上该看得见光标")
	}
	picking := fixture()
	picking.picker, picking.pickerIndex = pickerResume, 0
	if picking.View().Cursor != nil {
		t.Fatal("挑选项（/resume）开着时该把光标藏掉")
	}
	modeling := fixture()
	modeling.picker = pickerModel
	if modeling.View().Cursor != nil {
		t.Fatal("挑选项（/model）开着时该把光标藏掉")
	}
	viewing := fixture()
	viewing.viewer, viewing.viewerTitle = []string{" system", " 消息"}, "/outgoing"
	if viewing.View().Cursor != nil {
		t.Fatal("查看器（/outgoing）开着时该把光标藏掉")
	}
	confirming := fixture()
	confirming.viewer, confirming.viewerTitle, confirming.viewerHint = []string{" 会删掉 2 条"}, "确认删除", "回车 / y 执行"
	confirming.confirm = &confirm{kind: "confirmDeleteSession", sessionID: "c1"}
	if confirming.View().Cursor != nil {
		t.Fatal("确认框（要先看清楚再点头）里也不该有光标")
	}
	if liveFixture().View().Cursor == nil {
		t.Fatal("生成中也该留着光标（输入行还能用）")
	}
}

// 面板弹在输入行**上方** ⇒ 输入行被顶下去，光标的行也得跟着走（否则光标会落在面板上）。
func TestCursorRowFollowsInputLine(t *testing.T) {
	m := typeText(fixture(), "/res")
	m.inputCursor = 0
	if len(m.paletteMatches()) == 0 {
		t.Fatal("`/res` 该弹出面板")
	}
	cursor := m.View().Cursor
	if cursor == nil {
		t.Fatal("面板开着（命令面板不是挑选项）时该有光标")
	}
	if want := inputLineIndex(viewLines(m)); cursor.Y != want {
		t.Fatalf("面板把输入行顶下去了：光标行 = %d，该是 %d", cursor.Y, want)
	}
}

// 离线/空数据时也要有像样的画面（**不 panic**、有话可说）。
func TestEmptyState(t *testing.T) {
	// 还没有可用会话（连后端都没连上）：**消息区照旧在**（有话可说）+ 输入行 + 分隔线 + 状态行。
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
	if !strings.HasPrefix(strings.Join(lines[:input], "\n"), " 现在没有可用会话") {
		t.Fatalf("消息区该给一句「没有可用会话」的话：\n%s", body)
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
	// 退一格 ⇒ `/re`：比 `/res` 松、比 `/` 紧（**别钉死条数** —— 命令表会加东西，条数是它的影子）
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if back := updated.(model); len(back.paletteMatches()) <= 1 || len(back.paletteMatches()) >= len(commands) {
		t.Fatalf("`/re` 该比 `/res` 松、又比 `/` 紧：%d", len(back.paletteMatches()))
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
	// 半截名字 + 回车 ⇒ 跑高亮那条（`/resu` ⇒ resume ⇒ 打开挑选项）
	half := typeText(fixture(), "/resu")
	updated, _ = half.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opened := updated.(model); opened.picker != pickerResume {
		t.Fatalf("`/resu` 回车该跑 /resume（打开挑选项），得到 picker=%d", opened.picker)
	}
	// Esc 清输入（面板是推导出来的，输入空了面板自然没了）
	updated, _ = typeText(fixture(), "/re").Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cleared := updated.(model); cleared.input != "" || len(cleared.paletteMatches()) != 0 {
		t.Fatalf("Esc 该清输入：%q", cleared.input)
	}
}

// ── /new：当前会话**还没有消息**时无效（你已经在一条新会话里了）──

func TestNewIsInvalidOnEmptySession(t *testing.T) {
	// 选中的会话确实没有消息（拉过了）⇒ 那就是"新会话"，/new 无效
	messageless := fixture()
	messageless.messages, messageless.messagesFor = nil, "c1"
	updated, cmd := messageless.runCommand("/new")
	if cmd != nil {
		t.Fatal("没有消息的会话上 /new 不该发请求")
	}
	if said := updated.(model).lastAction; !strings.Contains(said, "无效") {
		t.Fatalf("该如实说无效：%q", said)
	}
	// **没有会话**（启动编排建失败了那种兵灾态）**不算空** ⇒ /new 正是出路，真建一条
	noSession := fixture()
	noSession.selectedID, noSession.messages, noSession.messagesFor = "", nil, ""
	if _, cmd := noSession.runCommand("/new"); cmd == nil {
		t.Fatal("没有会话时 /new 该真建一条（那是出路）")
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
	if next.findSession("c1") >= 0 {
		t.Fatalf("删掉的会话该立刻从列表里拿掉：%+v", next.sessions)
	}
	if next.selectedID != "" {
		t.Fatalf("等新建那条回来之前先落回没有会话：selectedID=%q", next.selectedID)
	}
	if cmd == nil {
		t.Fatal("删完该**立刻再建一条**（没有'没有会话'这个状态）")
	}
	// 新建那条回来 ⇒ 认它当当前会话，并拉它的消息
	updated, cmd = next.Update(createdMsg{session: Session{ID: "c9", Provider: "dummy", Model: "dummy"}})
	after := updated.(model)
	if after.selectedID != "c9" {
		t.Fatalf("删完该立刻进那条新建的会话：selectedID=%q", after.selectedID)
	}
	if cmd == nil {
		t.Fatal("新会话该重拉列表与消息")
	}
	// 失败要如实说
	updated, _ = m.Update(deletedMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "删除失败") {
		t.Fatalf("失败该如实说：%q", said)
	}
	// 新建失败也要如实说（并给出路）—— 那才是"没有会话"这个兵灾态
	updated, _ = next.Update(createdMsg{err: errTest})
	if said := updated.(model).lastAction; !strings.Contains(said, "新建失败") || !strings.Contains(said, "/new") {
		t.Fatalf("新建失败该如实说并给出路：%q", said)
	}
}

// `/delete` 删完**立刻**真发一次 `POST /sessions`（建那条新的）—— 真跑一次请求来证。
func TestDeleteImmediatelyCreatesAFreshSession(t *testing.T) {
	created := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			created++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Session{ID: "c9", Provider: "dummy", Model: "dummy", AgentID: "default"})
		case r.URL.Path == "/api/v1/sessions":
			_ = json.NewEncoder(w).Encode([]Session{{ID: "c9", Provider: "dummy", Model: "dummy"}})
		case r.URL.Path == "/api/v1/sessions/c9/messages":
			_ = json.NewEncoder(w).Encode([]Message{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	m := fixture()
	m.client = NewClient(backend.URL, "")
	updated, cmd := m.Update(deletedMsg{id: "c1"})
	if cmd == nil {
		t.Fatal("删完该立刻建一条新的")
	}
	// 把新命令真跑一次（它会 POST /sessions）⇒ 界面随即进那条新会话
	updated, _ = updated.(model).Update(cmd())
	after := updated.(model)
	if created != 1 {
		t.Fatalf("该真发一次 POST /sessions，实际 %d 次", created)
	}
	if after.selectedID != "c9" {
		t.Fatalf("该立刻进新建那条：selectedID=%q", after.selectedID)
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
	// 没有可用会话（兵灾态）：没有可删的
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	if _, cmd := empty.runCommand("/cut"); cmd != nil {
		t.Fatal("没有会话时不该发请求")
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
	// 没有可用会话（兵灾态）：没有可复制的
	empty := fixture()
	empty.selectedID, empty.messages, empty.messagesFor = "", nil, ""
	if _, cmd := empty.runCommand("/copy"); cmd != nil {
		t.Fatal("没有会话时不该发请求")
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
	// 没有会话时 ⇒ 只记下来，不发请求（留给下一条新建的会话）
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

// 打开 TUI 先做**启动编排**（先清空会话、再建一条真会话，顺序不能反）——
// `initialModel` 只置位"还没编排过"，真正动手在连上后端那一下（`healthMsg`）。
func TestInitialModelAwaitsStartupBootstrap(t *testing.T) {
	m := initialModel(NewClient("http://127.0.0.1:8787", ""))
	if !m.startup {
		t.Fatal("刚打开该等着做启动编排")
	}
	if m.selectedID != "" {
		t.Fatalf("还没连上后端时不该自己选中谁：selectedID=%q", m.selectedID)
	}
	// 连上 ⇒ 交出去编排，而且只编排一次
	updated, cmd := m.Update(healthMsg{health: Health{Status: "ok", Version: "0.1.0"}})
	next := updated.(model)
	if cmd == nil {
		t.Fatal("连上该去清空会话 / 建一条真会话")
	}
	if next.startup {
		t.Fatal("编排只该发起一次")
	}
	// 编排结果回来之前，列表就算先到了也不自动选（等 bootMsg 说了算）
	listed, _ := next.Update(sessionsMsg{sessions: fixture().sessions})
	if after := listed.(model); after.selectedID != "" {
		t.Fatalf("编排结果回来之前不该自动选：selectedID=%q", after.selectedID)
	}
	// 界面照旧像样（消息区在，只是没有会话列表）
	if body := listed.(model).View().Content; !strings.Contains(body, "没有可用会话") || strings.Contains(body, "＋ 新建对话") {
		t.Fatalf("没有会话该是消息区 + 底部，不是会话列表：\n%s", body)
	}
}

// fakeBootstrap：**假客户端** —— 只记调用顺序（真 `Client` 走 HTTP，"先删后建"这种顺序断言测不出来）。
// 列表项自带 `Messages`（条数）⇒ 编排**只发一次 `GET /sessions`**（见 bootstrapAPI）。
type fakeBootstrap struct {
	sessions  []Session
	delErr    map[string]error
	create    Session
	createErr error
	listErr   error
	calls     []string
}

func (f *fakeBootstrap) Sessions() ([]Session, error) {
	f.calls = append(f.calls, "sessions")
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.sessions, nil
}

func (f *fakeBootstrap) DeleteSession(id string) error {
	f.calls = append(f.calls, "delete:"+id)
	return f.delErr[id]
}

func (f *fakeBootstrap) CreateSession(provider, model string) (Session, error) {
	f.calls = append(f.calls, "create:"+provider+"|"+model)
	if f.createErr != nil {
		return Session{}, f.createErr
	}
	return f.create, nil
}

// ── 启动编排：**先清空会话、再建一条真会话**（顺序不能反）──

// 该清的只有【0 条消息 且 标题为空】那些；**带标题的空会话留着**（用户 /rename 过）；
// 有消息的留着；**两次删除都排在创建之前**（顺序反过来这条断言就该红）。
//
// ★ 而且**一次 `GET /messages` 都不发**：条数由 `GET /sessions` 的列表项自带
// （以前是每条无标题会话问一次 ⇒ 会话一多就是 N 次请求）。
func TestStartupCleansEmptySessionsThenCreatesOne(t *testing.T) {
	fake := &fakeBootstrap{
		sessions: []Session{
			{ID: "空1", Title: ""},
			{ID: "空2", Title: "   "}, // 只有空白 ⇒ 等于没起名
			{ID: "有名字", Title: "我改过名"},
			{ID: "有消息", Title: "", Messages: 1},
		},
		create: Session{ID: "新会话", Provider: "dummy", Model: "dummy", AgentID: "default"},
	}
	msg, ok := bootstrapCmd(fake, "dummy", "dummy")().(bootMsg)
	if !ok {
		t.Fatal("该回 bootMsg")
	}
	// ★ 调用顺序：**删（该清的）都在建之前**；只发一次列表请求，不看任何会话的消息
	want := []string{
		"sessions",
		"delete:空1",
		"delete:空2",
		"create:dummy|dummy",
	}
	if got := strings.Join(fake.calls, " "); got != strings.Join(want, " ") {
		t.Fatalf("编排的调用顺序不对：\n得到 %s\n该是 %s", got, strings.Join(want, " "))
	}
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "messages:") {
			t.Fatalf("编排不该逐条去拉消息（条数就在列表里）：%v", fake.calls)
		}
	}
	if msg.err != nil || msg.created == nil || msg.created.ID != "新会话" {
		t.Fatalf("该建出那一条：%+v（err=%v）", msg.created, msg.err)
	}
	// 列表 = 新建的那条（最前）+ 留下的两条
	ids := make([]string, 0, len(msg.sessions))
	for _, session := range msg.sessions {
		ids = append(ids, session.ID)
	}
	if got := strings.Join(ids, ","); got != "新会话,有名字,有消息" {
		t.Fatalf("清理后的列表不对：%s", got)
	}
	// 界面：认新建那条当当前会话，并去拉它的消息
	m := fixture()
	updated, cmd := m.Update(msg)
	after := updated.(model)
	if after.selectedID != "新会话" {
		t.Fatalf("该选中新建那条：selectedID=%q", after.selectedID)
	}
	if cmd == nil {
		t.Fatal("该去拉新会话的消息")
	}
	if after.findSession("空1") >= 0 || after.findSession("空2") >= 0 {
		t.Fatalf("该清的空会话没清掉：%+v", after.sessions)
	}
	if !strings.Contains(after.lastAction, "已新建会话") {
		t.Fatalf("底栏该报新建了哪条：%q", after.lastAction)
	}
}

// 标题空、但**列表里报着有消息**的会话**不删**（判据完全来自 `GET /sessions` 的 `messages`）。
func TestStartupKeepsUnnamedSessionsThatHaveMessages(t *testing.T) {
	fake := &fakeBootstrap{
		sessions: []Session{{ID: "没名字但有内容", Messages: 3}},
		create:   Session{ID: "新会话"},
	}
	msg := bootstrapCmd(fake, "", "")().(bootMsg)
	if got := strings.Join(fake.calls, " "); got != "sessions create:|" {
		t.Fatalf("只该发列表 + 创建：%v", fake.calls)
	}
	if len(msg.sessions) != 2 || msg.sessions[1].ID != "没名字但有内容" {
		t.Fatalf("那条该留着：%+v", msg.sessions)
	}
}

// 没清掉（DELETE 失败）⇒ 如实说一句，但**别把编排整个算失败**（新会话照建）。
func TestStartupReportsUndeletedSessions(t *testing.T) {
	fake := &fakeBootstrap{
		sessions: []Session{{ID: "删不掉"}},
		delErr:   map[string]error{"删不掉": errTest},
		create:   Session{ID: "新会话"},
	}
	msg := bootstrapCmd(fake, "", "")().(bootMsg)
	if msg.created == nil || msg.created.ID != "新会话" {
		t.Fatalf("新会话照建（没清掉不该拖着它一起失败）：%+v", msg.created)
	}
	if len(msg.failed) != 1 || msg.failed[0] != shortID("删不掉") {
		t.Fatalf("该如实报没清掉的：%+v", msg.failed)
	}
	m := fixture()
	updated, _ := m.Update(msg)
	after := updated.(model)
	if after.selectedID != "新会话" {
		t.Fatalf("该照样进新会话：%q", after.selectedID)
	}
	if said := after.lastAction; !strings.Contains(said, "没清掉") {
		t.Fatalf("底栏该报一句：%q", said)
	}
}

// 建那条失败 ⇒ **如实说 + 给出路**（那才是"没有会话"这个兵灾态），别 panic、也别假装建好了。
func TestStartupReportsCreateFailure(t *testing.T) {
	fake := &fakeBootstrap{sessions: []Session{{ID: "空1"}}, createErr: errTest}
	msg := bootstrapCmd(fake, "", "")().(bootMsg)
	if msg.created != nil {
		t.Fatal("建失败不该假装建好了")
	}
	if msg.err == nil {
		t.Fatal("建失败该把原因带回来")
	}
	m := fixture()
	updated, _ := m.Update(msg)
	after := updated.(model)
	if after.selectedID != "" {
		t.Fatalf("建失败该落回没有会话：selectedID=%q", after.selectedID)
	}
	if said := after.lastAction; !strings.Contains(said, "没有可用会话") || !strings.Contains(said, "/new") {
		t.Fatalf("该如实说并给出路：%q", said)
	}
	// 那种状态下命令照旧不 panic、如实提示
	if _, cmd := after.runCommand("/outgoing"); cmd != nil {
		t.Fatal("没有会话时 /outgoing 不该发请求")
	}
	if said := after.lastAction; !strings.Contains(said, "没有可用会话") {
		t.Fatalf("/outgoing 该如实说：%q", said)
	}
}

// 列表都拉不到（后端没起）⇒ 如实说，**别建一条假的**。
func TestStartupReportsListFailure(t *testing.T) {
	fake := &fakeBootstrap{listErr: errTest}
	msg := bootstrapCmd(fake, "", "")().(bootMsg)
	if msg.err == nil || msg.created != nil {
		t.Fatalf("列表都拉不到就不该往下走：%+v", msg)
	}
	if strings.Contains(strings.Join(fake.calls, " "), "create:") {
		t.Fatalf("拉不到列表时不该去建会话：%v", fake.calls)
	}
	m := fixture()
	updated, _ := m.Update(msg)
	if said := updated.(model).lastAction; !strings.Contains(said, "没有可用会话") {
		t.Fatalf("该如实说：%q", said)
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

// runCmds：替测试跑一趟 Cmd（界面测试平时不跑 Cmd —— 这一条专门要看"那一批请求回来说了什么"）。
//
// 只跑**一层**：Batch 里那几个 Cmd 各执行一次，回来的消息喂回 model；
// 喂完又派生的 Cmd（比如"进会话顺手量占用"）**不追**（免得绕进网络重试）。
// 一律配 `httptest` 的假后端 —— 别拿真端口（真发请求就成了"测试打真服务"）。
func runCmds(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok { // 一批（只有一条时 Batch 会直接给那一条）
		for _, sub := range batch {
			m = runCmds(t, m, sub)
		}
		return m
	}
	if msg == nil {
		return m
	}
	updated, _ := m.Update(msg)
	return updated.(model)
}

// ── 用户实跑抓到的 bug：**第一条消息之后标题不出现，发第二条才出现** ──
//
// 后端**是对的**（先复现确认过）：标题在翻 idle **之前**就落库了 ——
// `chat.run` 里 `Titles.Auto` 排在 `Finish` 之前，`title.Auto` 只挑空标题、写完就走
// （直操模式 `-debug send` 跑完一轮 `sessions.title` 就有名字，HTTP 也一样）。
//
// 错在界面这一路：收到 idle 只重拉消息与占用，**不重拉会话列表**；而发消息那一刻
// （`sentMsg`）拉的那份列表里标题还是空的 ⇒ 左栏与状态行一直停在「（还没起名）」，
// 要等下一次列表刷新（再发一句就有一次）才补上 —— 看着就像"第二条才起标题"。
func TestAutoTitleLandsAfterFirstTurn(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/sessions":
			// 后端那侧的事实：这一轮跑完（idle）时标题已经在了
			_ = json.NewEncoder(w).Encode([]Session{
				{ID: "c1", Title: "逆序数该怎么写", Provider: "dummy", Model: "dummy"},
			})
		case "/api/v1/sessions/c1/messages":
			_ = json.NewEncoder(w).Encode([]Message{{ID: "m2", Role: "assistant", Content: "这样写。"}})
		case "/api/v1/sessions/c1/context":
			_ = json.NewEncoder(w).Encode(ContextUsage{UsedTokens: 100, BudgetTokens: 1000})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	m := fixture()
	m.client = NewClient(backend.URL, "")
	// 起跑线：刚发完第一句话，列表里这条会话**还没有名字**（发消息那一刻后端确实还没起）
	m.sessions = []Session{{ID: "c1", Title: "", Provider: "dummy", Model: "dummy", Turn: TurnStatus{Phase: "streaming"}}}
	m.selectedID, m.messagesFor, m.messages = "c1", "c1", nil
	m.turn = &liveTurn{sessionID: "c1", messageID: "m2", phase: "streaming"}
	if body := m.View().Content; !strings.Contains(body, "（还没起名）") {
		t.Fatalf("起跑线该是「还没起名」：\n%s", body)
	}

	updated, cmd := m.Update(turnPollMsg{sessionID: "c1", status: TurnStatus{Phase: "idle"}, slice: StreamSlice{Done: true}})
	next := runCmds(t, updated.(model), cmd)
	if next.turn != nil {
		t.Fatal("收到 idle 该收干这一轮（气泡消失，真消息接手）")
	}
	if body := next.View().Content; !strings.Contains(body, "逆序数该怎么写") {
		t.Fatalf("第一条消息跑完，标题就该显示出来（不许等到第二条）：\n%s", body)
	}
	if body := next.View().Content; strings.Contains(body, "（还没起名）") {
		t.Fatalf("名字已经有了，就不该再显示「还没起名」：\n%s", body)
	}
}

// 静默那一趟（跑完去接标题）**不碰底栏** —— 底栏那句话是这一轮的结局，
// 不能被"会话 N 条"顶掉（`settleTurn` 的那条口径）；拉不到也当没发生。
func TestQuietSessionReloadKeepsTheBottomLine(t *testing.T) {
	m := fixture()
	m.lastAction = "生成完成" // 跑完那一刻底栏上摆着的就是它
	fresh := []Session{{ID: "c1", Title: "新起的名字", Provider: "opencode-go", Model: "deepseek-v4-flash"}}

	quiet, _ := m.Update(sessionsMsg{sessions: fresh, quiet: true})
	next := quiet.(model)
	if said := next.lastAction; said != "生成完成" {
		t.Fatalf("静默重拉不该动底栏：%q", said)
	}
	if next.sessions[0].Title != "新起的名字" {
		t.Fatalf("但列表要真更新：%+v", next.sessions[0])
	}
	// 拉不到（后端一时抽风）⇒ 一个字都不动，也别把失败摆到底栏
	failed, _ := next.Update(sessionsMsg{quiet: true, err: errTest})
	if after := failed.(model); after.lastAction != "生成完成" || after.sessions[0].Title != "新起的名字" {
		t.Fatalf("静默重拉失败该当没发生：%q %+v", after.lastAction, after.sessions[0])
	}
	// 正常那一趟（`/refresh`、发消息…）照旧要说话
	loud, _ := next.Update(sessionsMsg{sessions: fresh})
	if said := loud.(model).lastAction; !strings.Contains(said, "会话 1 条") {
		t.Fatalf("非静默重拉该照旧报数：%q", said)
	}
}

// 生成中按停那趟也要**静默刷一次列表**：与"第一条消息之后标题不出现"是**同一类时差** ——
//
//	按停时后端那半程可能**刚好**把标题写好（`title.Auto`）⇒ 不补这一趟，左栏/状态行就停在
//	「（还没起名）」，要等下一次列表刷新才补上。
//
// 走**静默**那一档（`loadSessionsQuiet`）⇒ 更新列表但**不动底栏**，刚写上的「已停止」不被顶掉。
func TestStopAlsoQuietlyReloadsSessions(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions" {
			http.NotFound(w, r)
			return
		}
		// 后端那侧的事实：按停那一刻标题已经写好了（或者压根没有，这一趟都是"对账"）
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Session{
			{ID: "c1", Title: "刚起好的名字", Provider: "dummy", Model: "dummy"},
		})
	}))
	defer backend.Close()

	m := liveFixture()
	m.client = NewClient(backend.URL, "")
	m.sessions = []Session{{ID: "c1", Title: "", Provider: "dummy", Model: "dummy",
		Turn: TurnStatus{Phase: "streaming"}}}
	m.selectedID, m.messagesFor = "c1", "c1"
	m.turn = &liveTurn{sessionID: "c1", messageID: "m3", phase: "streaming"}
	m.lastAction = "停止…"

	updated, cmd := m.Update(stoppedMsg{sessionID: "c1", stopped: true})
	next := updated.(model)
	if next.turn != nil {
		t.Fatal("停下来了 ⇒ 气泡该消失")
	}
	if cmd == nil {
		t.Fatal("按停那趟该补一次**静默**刷列表（标题可能刚写好）")
	}
	if said := next.lastAction; said != "已停止这一轮（这条回复没有落库）" {
		t.Fatalf("按停该在底栏留下「已停止」那句：%q", said)
	}
	// 真把那趟 Cmd 跑一趟（假后端接住）⇒ 列表更新了，而底栏那句**一个字都没被顶掉**
	after := runCmds(t, next, cmd)
	if after.sessions[0].Title != "刚起好的名字" {
		t.Fatalf("静默那一趟该把列表接上：%+v", after.sessions[0])
	}
	if said := after.lastAction; said != "已停止这一轮（这条回复没有落库）" {
		t.Fatalf("静默刷列表**不许**动底栏那句「已停止」：%q", said)
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
	if got := rule(10, ruleMinWidth-1, false); got != strings.Repeat("-", 10) {
		t.Fatalf("窄屏该退回 ASCII：%q", got)
	}
	if got := rule(10, ruleMinWidth, false); got != strings.Repeat("─", 10) {
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

// 字符集降级：`TERM=dumb` / 非 TTY ⇒ **跟窄屏同一档**（满线 ASCII、位次标记 ASCII），
// 而且**与屏幕多宽无关**（判定与宽度是两件事：一个说终端不认 Unicode，一个说放不下）。
func TestDumbTerminalFallsBackToASCII(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	ascii := asciiEnabled
	if !ascii(env(map[string]string{"TERM": "dumb"}), true) {
		t.Fatal("TERM=dumb 该退 ASCII")
	}
	if !ascii(env(map[string]string{"TERM": "xterm-256color"}), false) {
		t.Fatal("非 TTY 该退 ASCII")
	}
	if ascii(env(map[string]string{"TERM": "xterm-256color"}), true) {
		t.Fatal("正常终端不该退")
	}
	// NO_COLOR 只关颜色，**不动字符集**（"别上色" ≠ "不认 Unicode"）
	if colorEnabled(env(map[string]string{"NO_COLOR": "1"}), true) {
		t.Fatal("NO_COLOR 该关颜色")
	}
	if ascii(env(map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}), true) {
		t.Fatal("NO_COLOR 不该影响字符集降级")
	}

	// 整屏：宽屏 + 退 ASCII ⇒ 一个 `─` 都不许有（不是窄屏，是终端不认）
	dumb := fixture()
	dumb.ascii = true
	for _, line := range viewLines(dumb) {
		if strings.Contains(line, "─") {
			t.Fatalf("TERM=dumb 下不该出现 `─`：%q", line)
		}
	}
	if !strings.Contains(strings.Join(viewLines(dumb), "\n"), strings.Repeat("-", dumb.width)) {
		t.Fatal("TERM=dumb 下该画 ASCII 满线")
	}

	// 位次标记也退：`‹ 2/3 ›` → `< 2/3 >`
	reroll := fixture()
	reroll.reroll = &RerollState{Active: true, TargetMessageID: "m2", Count: 3, CurrentIdx: 2}
	reroll.rerollFor = "c1"
	if marker, ok := reroll.rerollMarker("m2"); !ok || marker != "‹ 2/3 ›" {
		t.Fatalf("宽屏该用 `‹ 2/3 ›`：%q（%v）", marker, ok)
	}
	reroll.ascii = true
	if marker, ok := reroll.rerollMarker("m2"); !ok || marker != "< 2/3 >" {
		t.Fatalf("退 ASCII 该用 `< 2/3 >`：%q（%v）", marker, ok)
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
	// 同一条会话已经量过 / 已经拉过 ⇒ 不再重复拉
	loaded := m
	loaded.context, loaded.contextFor = &ContextUsage{UsedTokens: 1}, "c1"
	loaded.prompt, loaded.promptFor = &SystemPrompt{Text: "底子"}, "c1"
	// 重摇那一份同理：候选只在内存里 ⇒ 进会话时问一次，之后不重复问
	loaded.reroll, loaded.rerollFor = &RerollState{Active: true, Count: 2, CurrentIdx: 1}, "c1"
	if _, cmd := loaded.Update(messagesMsg{sessionID: "c1", messages: loaded.messages}); cmd != nil {
		t.Fatal("同一条会话不必每拉一次消息就重量一次占用 / 重拉一次提示词")
	}
	// 进会话（还没问过重摇）⇒ 顺手问一次这条会话在不在重摇模式里
	asked := fixture()
	if _, cmd := asked.Update(messagesMsg{sessionID: "c1", messages: asked.messages}); cmd == nil {
		t.Fatal("进会话该顺手问一次重摇状态（候选只在内存里，换会话/重启后要重新问）")
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

// `/outgoing` 查看器：**摘要是压缩过的 ⇒ 黄**、系统提示词 ⇒ 暗、原样消息 ⇒ 默认色；
// 左列是**压缩前的消息序号**（摘要写它涵盖的区间）。
func TestOutgoingViewerColorsBySource(t *testing.T) {
	m := colored(fixture())
	blocks := int64(4)
	summaryID, messageID := "s1", "m9"
	zero, nine, from, to := 0, 9, 3, 5
	updated, _ := m.Update(outgoingMsg{items: []Outgoing{
		{Role: "system", Content: "你是主持人", Source: "system", Idx: &zero},
		{Role: "assistant", Content: "压缩摘要", Source: "summary", SummaryID: &summaryID, Blocks: &blocks, FromIdx: &from, ToIdx: &to},
		{Role: "user", Content: "原样那句", Source: "message", MessageID: &messageID, Idx: &nine},
	}})
	body := updated.(model).View().Content
	lines := viewLines(updated.(model))
	// 按行比对：左列标签 + 角色 + 着色后的说明（行内不再有 `#9` / `第 3-5 条` 那种冗余后缀）
	want := []string{
		fmt.Sprintf("%-7s %-9s %-8s", "0", "system", "") + "\x1b[2m系统提示词\x1b[0m",
		fmt.Sprintf("%-7s %-9s %-8s", "3-5", "assistant", "") + "\x1b[33m摘要 " + shortID(summaryID) + "（覆盖 4 块）\x1b[0m",
		fmt.Sprintf("%-7s %-9s %-8s", "9", "user", "") + "消息 " + shortID(messageID),
	}
	for _, line := range want {
		found := false
		for _, got := range lines {
			if got == line {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("该有这一行：\n%q\n整屏：\n%q", line, body)
		}
	}
	if strings.Contains(body, "第 3-5 条") || strings.Contains(body, "#9") || strings.Contains(body, "#0") {
		t.Fatalf("行内冗余后缀该删干净：\n%q", body)
	}
	if strings.Contains(body, "\x1b[33m消息 ") || strings.Contains(body, "\x1b[2m消息 ") {
		t.Fatal("原样消息保持默认色（别和压缩出来的混）")
	}
}

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
	m.prompt = &SystemPrompt{Text: "你是跑团主持人。", Source: "agent"}
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
	updated, _ := m.Update(promptMsg{sessionID: "c1", prompt: SystemPrompt{Text: "底子", Source: "builtin"}})
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
		Active: true, TargetMessageID: "m2", Count: 3, CurrentIdx: 2,
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
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/sessions/c1/reroll" {
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
		t.Fatalf("该真发一次 DELETE /reroll，实际 %d 次", hits)
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
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/c1/reroll":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(RerollAccepted{
				TargetMessageID: "m2", TaskID: "t1",
				State: RerollState{Active: true, TargetMessageID: "m2", Count: 2, CurrentIdx: 1, Running: true,
					Items: []RerollItem{{Idx: 1, Preview: "在。", Current: true}, {Idx: 2, Pending: true}}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/c1/reroll":
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetMessageID: "m2", Count: 2, CurrentIdx: 1,
				Items: []RerollItem{{Idx: 1, Preview: "在。", Current: true}, {Idx: 2, Preview: "在的。"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/c1/reroll/switch":
			var body struct {
				Idx int `json:"idx"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switchIdx = body.Idx
			_ = json.NewEncoder(w).Encode(RerollState{
				Active: true, TargetMessageID: "m2", Count: 2, CurrentIdx: 2,
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
	polled := runCmds(t, entered, pollRerollCmd(entered.client, "c1"))
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
