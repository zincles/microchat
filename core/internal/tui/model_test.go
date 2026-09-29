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
		sessions: []Session{
			{ID: "c1", Title: "逆序数该怎么写", Provider: "opencode-go", Model: "deepseek-v4-flash", AgentID: "跑团",
				Turn: TurnStatus{Phase: "streaming", ElapsedMS: 1200, Chars: 8}},
			{ID: "c2", Title: "第二段对话", Provider: "dummy", Model: "dummy", AgentID: "default"},
		},
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
	// 打了字再回车：**真发**（不再说"还没实现"）—— 空会话下也一样（sendCmd 顺手建会话）
	typed := m
	for _, r := range "你好" {
		updated, _ = typed.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		typed = updated.(model)
	}
	if typed.input != "你好" {
		t.Fatalf("打字该进输入行：%q", typed.input)
	}
	updated, cmd := typed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	// 空会话（还没进任何会话）⇒ **只留底部**：输入行 + 分隔线 + 底栏。
	// 不显示左侧列表与右边空白（那是噪音；列表走 /resume）。
	m := model{width: 40, height: 6, ready: true}
	body := m.View().Content
	if strings.Contains(body, "＋ 新建对话") || strings.Contains(body, "|") {
		t.Fatalf("空会话不该铺开侧栏与主区：\n%s", body)
	}
	lines := strings.Split(body, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "> ") {
		t.Fatalf("该只有三行（输入行/分隔线/底栏）：\n%s", body)
	}
	if !strings.Contains(body, "未连接") {
		t.Fatal("没连上后端要如实说")
	}
	// 挑选项开着（/resume）就铺开界面 —— 那时确实有东西要看
	picking := model{width: 40, height: 6, ready: true, picker: pickerResume}
	if !strings.Contains(picking.View().Content, "挑一条") { // 窄宽度下标题会被截断，断言前缀
		t.Fatalf("挑选项要看得见：\n%s", picking.View().Content)
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

// 方向键在面板里走（不给侧栏）；Tab 补全；回车跑**高亮**那条。
func TestPaletteNavigationAndRun(t *testing.T) {
	m := fixture()
	m = typeText(m, "/")
	// 下键两次 ⇒ 高亮第 3 条（`/cut`），侧栏选中**不动**
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	walked := updated.(model)
	if walked.paletteIndex != 2 {
		t.Fatalf("下键该走面板：%d", walked.paletteIndex)
	}
	if walked.selected != 0 {
		t.Fatalf("面板开着时方向键不该动侧栏：%d", walked.selected)
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
	empty.selected, empty.messages, empty.messagesFor = -1, nil, ""
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
	if next.selected != -1 || len(next.messages) != 0 {
		t.Fatalf("删完该回到空会话：selected=%d", next.selected)
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
	empty.selected, empty.messages, empty.messagesFor = -1, nil, ""
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
	// 复制回来：选中新会话（列表刷新回来才选得上），并重拉列表
	updated, cmd = m.Update(copiedMsg{session: Session{ID: "c9", Title: "副本"}})
	next := updated.(model)
	if cmd == nil {
		t.Fatal("复制完该重拉列表")
	}
	if next.pendingSelect != "c9" {
		t.Fatalf("该盯着新会话：%q", next.pendingSelect)
	}
	if said := next.lastAction; !strings.Contains(said, "已复制成") {
		t.Fatalf("该报新会话：%q", said)
	}
	// 空会话：没有可复制的
	empty := fixture()
	empty.selected, empty.messages, empty.messagesFor = -1, nil, ""
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
	if cmd == nil || updated.(model).selected != 1 {
		t.Fatalf("给 uuid 该直接进：selected=%d", updated.(model).selected)
	}
	// 找不到 ⇒ 报错，**什么都不发生**
	before, beforeMessages := m.selected, len(m.messages)
	updated, cmd = m.runCommand("/resume 00000000-0000-0000-0000-000000000000")
	after := updated.(model)
	if cmd != nil || after.selected != before || len(after.messages) != beforeMessages || after.picker != pickerNone {
		t.Fatalf("找不到就不许动：selected=%d cmd=%v picker=%d", after.selected, cmd != nil, after.picker)
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
	body := m.View().Content
	_ = body
	if !strings.Contains(opened.View().Content, "挑一条已有会话") {
		t.Fatalf("挑选项该有标题：\n%s", opened.View().Content)
	}
	// 下键 + 回车 ⇒ 进第二条
	updated, _ = opened.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, cmd = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := updated.(model)
	if next.picker != pickerNone || next.selected != 1 || cmd == nil {
		t.Fatalf("回车该进第二条：picker=%d selected=%d", next.picker, next.selected)
	}
	// Esc 取消
	updated, _ = opened.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cancelled := updated.(model); cancelled.picker != pickerNone || cancelled.selected != m.selected {
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
	empty.selected, empty.messages, empty.messagesFor = -1, nil, ""
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
	if m.selected != -1 {
		t.Fatalf("该停在空会话：selected=%d", m.selected)
	}
	// 拿到会话列表也不自动选（用户没点就不动）
	updated, _ := m.Update(sessionsMsg{sessions: fixture().sessions})
	if after := updated.(model); after.selected != -1 {
		t.Fatalf("列表回来也不该自动选：selected=%d", after.selected)
	}
	// 空会话的界面形状 = **只有底部**（输入行/分隔线/底栏），不铺侧栏与主区
	if body := updated.(model).View().Content; len(strings.Split(body, "\n")) != 3 || strings.Contains(body, "|") {
		t.Fatalf("空会话该只留底部：\n%s", body)
	}
}

// ── 生成那一轮：合成气泡 + 300ms 轮询 + /stop ──

// liveFixture：一条正在生成的会话（界面按状态**合成**气泡；库里还没有它）。
func liveFixture() model {
	m := fixture()
	m.selected = 0
	m.turn = &liveTurn{
		sessionID: "c1", messageID: "m3", phase: "streaming", elapsedMS: 12300,
		text: "说到一半", thinking: "嗯……",
	}
	return m
}

// 生成中：气泡上有耗时与「/stop」，思考只报字数（token 数流式帧里没有）。
func TestGeneratingBubbleShowsElapsedAndStop(t *testing.T) {
	m := liveFixture()
	view := m.View().Content
	for _, want := range []string{"生成中", "12.3s", "说到一半", "/stop", "思考中… 3 字"} {
		if !strings.Contains(view, want) {
			t.Fatalf("生成中的气泡该有 %q：\n%s", want, view)
		}
	}
	// 不是这条会话的轮次 ⇒ 不该在这条会话里画那个气泡（侧栏的"生成中…"是列表口径，另说）
	other := liveFixture()
	other.turn.sessionID = "c2"
	if view := other.View().Content; strings.Contains(view, "生成中… 12.3s") || strings.Contains(view, "/stop") {
		t.Fatalf("别条会话的轮次不该画在这儿：\n%s", view)
	}
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
	idle.selected = 0
	// 空会话（没有任何会话）时连发都不用发
	if _, cmd := (model{client: NewClient("http://127.0.0.1:1", ""), selected: -1}).runCommand("/stop"); cmd != nil {
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

// 202 回来：进列表（空会话里冒出的新会话）、重置游标、开始看进度；库里的用户那句靠重拉消息拿到。
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
	if next.pendingSelect != "c9" {
		t.Fatalf("新建的会话该被选中：%q", next.pendingSelect)
	}
	if cmd == nil {
		t.Fatal("该去重拉会话列表与消息、并开始轮询")
	}
	if said := next.lastAction; !strings.Contains(said, "dummy") {
		t.Fatalf("底栏该报是哪条后端答的：%q", said)
	}
}
