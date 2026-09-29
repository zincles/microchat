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
		lastAction: "消息 3 条",
		width:      60, height: 14, ready: true,
		conversations: []Conversation{
			{ID: "c1", Title: "逆序数该怎么写", Provider: "opencode-go", Model: "deepseek-v4-flash", AgentID: "跑团",
				Turn: TurnStatus{Phase: "streaming", ElapsedMS: 1200, Chars: 8}},
			{ID: "c2", Title: "第二段对话", Provider: "dummy", Model: "dummy", AgentID: "default"},
		},
		messages: []Message{
			{ID: "m1", Role: "user", Content: "在吗"},
			{ID: "m2", Role: "assistant", Content: "在。\n有什么事？", DurationMS: &version},
		},
	}
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

// **CJK 对齐**：中文占两格 —— 侧栏那条竖线必须落在第 28 **列**（不是第 28 个字符）。
func TestLayoutAlignsByDisplayWidth(t *testing.T) {
	m := fixture()
	for _, line := range strings.Split(m.View().Content, "\n") {
		if runewidth.StringWidth(line) > m.width {
			t.Fatalf("这一行超出宽度：%q（宽 %d）", line, runewidth.StringWidth(line))
		}
		if index := strings.Index(line, "|"); index >= 0 {
			if got := runewidth.StringWidth(line[:index]); got != sidebarWidth {
				t.Fatalf("竖线该在第 %d 列，落在第 %d 列：%q", sidebarWidth, got, line)
			}
		}
	}
}

// 生成中的会话在后面挂「 · 生成中…」（界面口径）；选中的那条有游标。
func TestMarksGeneratingAndSelection(t *testing.T) {
	m := fixture()
	body := m.View().Content
	if !strings.Contains(body, "生成中…") {
		t.Fatalf("生成中该有标记：\n%s", body)
	}
	// 标题会被截断以**保住标记**（标记比标题尾巴重要 —— 这是刻意的），所以只断言前缀
	if !strings.Contains(body, "> 逆序数该怎") {
		t.Fatalf("选中的那条该有游标：\n%s", body)
	}
	// 选中那行的「生成中…」必须完整活着（不被截断吃掉）。
	// 只判**侧栏**那一格（竖线之前）—— 底下那行输入也以 "> " 开头，别把它算进来。
	for _, line := range strings.Split(body, "\n") {
		bar := strings.Index(line, "|")
		if bar < 0 {
			continue
		}
		sidebar := line[:bar]
		if strings.Contains(sidebar, "> ") && !strings.Contains(sidebar, "生成中…") {
			t.Fatalf("选中的那条带着生成中，标记不该被截掉：%q", line)
		}
	}
	if strings.Contains(body, "> 第二段对话") {
		t.Fatal("没选中的不该有游标")
	}
	// 助手署名与耗时都该在
	if !strings.Contains(body, "你：") || !strings.Contains(body, "助手") {
		t.Fatalf("消息该带署名：\n%s", body)
	}
	if !strings.Contains(body, "2.1s") {
		t.Fatalf("有耗时数据时该显示：\n%s", body)
	}
}

// 底栏：连接状态与"最近一次动作"**互不顶替**（界面口径）。
func TestFooterShowsBothHalves(t *testing.T) {
	m := fixture()
	footer := strings.Split(m.View().Content, "\n")[m.height-1]
	if !strings.Contains(footer, "已连接 v0.1.0") || !strings.Contains(footer, "消息 3 条") {
		t.Fatalf("底栏 = %q", footer)
	}
	m.version = ""
	if !strings.Contains(m.View().Content, "未连接") {
		t.Fatal("没连上时要如实说")
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

// Update 也能直接测（不必起终端）：上下键换选中、q 退出。
func TestUpdateNavigationAndQuit(t *testing.T) {
	m := fixture()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	next := updated.(model)
	if next.selected != 1 {
		t.Fatalf("下键该换到第二条，得到 %d", next.selected)
	}
	updated, _ = next.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if back := updated.(model); back.selected != 0 {
		t.Fatalf("上键该回到第一条，得到 %d", back.selected)
	}
	// 到底了再按不该越界
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if edge := updated.(model); edge.selected != 0 {
		t.Fatalf("已在第一条，上键不该越界：%d", edge.selected)
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
	// 打了字再回车：还没搬完的功能就如实说，不假装能发
	typed := m
	for _, r := range "你好" {
		updated, _ = typed.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		typed = updated.(model)
	}
	if typed.input != "你好" {
		t.Fatalf("打字该进输入行：%q", typed.input)
	}
	updated, _ = typed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if said := updated.(model).lastAction; !strings.Contains(said, "还没实现") {
		t.Fatalf("回车该如实说：%q", said)
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
	m := model{width: 40, height: 6, ready: true}
	body := m.View().Content
	if !strings.Contains(body, "＋ 新建对话") || !strings.Contains(body, "（左栏选一条会话）") {
		t.Fatalf("空状态 = \n%s", body)
	}
	if !strings.Contains(body, "未连接") {
		t.Fatal("没连上后端要如实说")
	}
}

// 装不下的消息**整块**不显示，并在表头下如实说"上面还有几条"（别让界面骗人）。
func TestDroppedMessagesAreAnnounced(t *testing.T) {
	m := fixture()
	m.height = 7 // 故意很小
	body := m.View().Content
	if !strings.Contains(body, "上面还有") {
		t.Fatalf("挤掉了就该说：\n%s", body)
	}
	// 显示出来的消息块必须**完整**（有署名，不是半截）
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "在吗") && !strings.Contains(body, "你") {
			t.Fatalf("消息块该带署名：\n%s", body)
		}
	}
}

// 初次连接失败后，只要后来刷新成功，底栏就该补回"已连接 vX"（这个 bug 是真机上抓到的）。
func TestReconnectUpdatesFooter(t *testing.T) {
	m := fixture()
	m.version = "" // 假装刚才连不上
	updated, cmd := m.Update(conversationsMsg{conversations: m.conversations})
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
