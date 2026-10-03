package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// ── 小查询（都住在 model 上，免得散落各处算同一件事）──

// isEmptySession：当前会话是不是**还没有消息**（`/new` 的判据）。
//
// 口径（2026-09-30 改）：进 TUI 时就会建一条真会话 ⇒ 正常总是有当前会话；这里的"空"指的是
// **这条会话还没有消息** —— 那它已经就是"新会话"了，再 `/new` 就是造垃圾行。
// **没有会话不算空**（那是个兵灾态）：那时 `/new` 正是出路，真建一条。
func (m model) isEmptySession() bool {
	session := m.currentSession()
	if session == nil {
		return false
	}
	return m.messagesFor == session.ID && len(m.messages) == 0
}

// withSession：把一条会话放进本地列表（在就换掉，不在就放**最前**）—— 别等下一次刷新才有它。
func withSession(sessions []Session, session Session) []Session {
	for index := range sessions {
		if sessions[index].ID == session.ID {
			sessions[index] = session
			return sessions
		}
	}
	return append([]Session{session}, sessions...)
}

// withoutSession：把一条会话从本地列表里拿掉（删除成功后**立刻**生效，刷新只是对账）。
func withoutSession(sessions []Session, id string) []Session {
	kept := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ID != id {
			kept = append(kept, session)
		}
	}
	return kept
}

// whereOf：会话绑的渠道 / 模型（给底栏那句话用；还没选模型时照实说）。
func whereOf(session Session) string {
	if session.Provider == "" || session.Model == "" {
		return "还没选模型"
	}
	return session.Provider + " / " + session.Model
}

// findSession：按完整 id 或**唯一前缀**找（前缀有歧义 ⇒ 当找不到，别猜）。
// 空 id = **没有当前会话**（兵灾态），不是"前缀匹配所有" ⇒ 直接回 -1。
func (m model) findSession(id string) int {
	if id == "" {
		return -1
	}
	found, hits := -1, 0
	for index, session := range m.sessions {
		if session.ID == id {
			return index
		}
		if strings.HasPrefix(session.ID, id) {
			found, hits = index, hits+1
		}
	}
	if hits == 1 {
		return found
	}
	return -1
}

// findMessage：在当前会话的消息里按完整 id 或**唯一前缀**找（前缀有歧义 ⇒ 当找不到，别猜）。
func (m model) findMessage(id string) string {
	found, hits := "", 0
	for _, message := range m.messages {
		if message.ID == id {
			return message.ID
		}
		if strings.HasPrefix(message.ID, id) {
			found, hits = message.ID, hits+1
		}
	}
	if hits == 1 {
		return found
	}
	return ""
}

// reloadMessagesCmd：把当前会话的消息重拉一遍（没有选中会话就什么都不做 —— 空 id 会去查
// 一条不存在的会话，界面上就成了"拉消息失败"）。
func (m model) reloadMessagesCmd() tea.Cmd {
	if session := m.currentSession(); session != nil {
		return loadMessages(m.client, session.ID)
	}
	return nil
}

// indexOfCurrentModel：当前会话用的渠道 / 模型在列表里第几个（挑选项的落点）。
func (m model) indexOfCurrentModel() int {
	provider, model := m.chosenProvider, m.chosenModel
	if session := m.currentSession(); session != nil {
		provider, model = session.Provider, session.Model
	}
	for index, item := range m.models {
		if item.Provider == provider && item.UpstreamID == model {
			return index
		}
	}
	return 0
}

// paletteMatches：输入以 `/` 开头、且还没打空格时的候选（按名字前缀匹配）。
func (m model) paletteMatches() []command {
	if !strings.HasPrefix(m.input, "/") || strings.Contains(m.input, " ") {
		return nil
	}
	prefix := strings.TrimPrefix(m.input, "/")
	matches := make([]command, 0, len(commands))
	for _, candidate := range commands {
		if strings.HasPrefix(candidate.name, prefix) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func (m model) clampPalette() int {
	matches := m.paletteMatches()
	if len(matches) == 0 {
		return 0
	}
	if m.paletteIndex >= len(matches) {
		return len(matches) - 1
	}
	if m.paletteIndex < 0 {
		return 0
	}
	return m.paletteIndex
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func describeSession(session Session) string {
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = "（还没起名）"
	}
	return shortID(session.ID) + " " + title
}

// ── 渲染 ──
//
// 一屏就三块（用户 2026-09-30 定的口径，见 AGENTS.md「界面该长什么样」）：
//
//	消息区：**只显示当前会话的对话**（最近的贴着输入框，从下往上排）
//	> 输入行（命令与消息都从这儿走）
//	------ （ASCII 分隔线：`─` 是模糊宽度字符，算两格 ⇒ 会超宽）
//	会话状态行（1-2 行：会话名 | 短 id | 渠道/模型 | 在不在跑 | 已连接 vX | 最近动作）
//
// **没有左栏**：会话选择只走 `/resume`（挑选项与查看器照旧**铺满消息区**）。

// View：**纯函数**（同样的 model ⇒ 同样的字符串）。
//
// 光标**不是画进正文的字符**：它是**真终端光标**（`View.Cursor`，Bubble Tea v2 的字段）⇒
// `Content` 一个字节都不变（现有那些逐字节断言一个字都不用改）。
// 位置由 render 一并算出来 —— 与正文同一份布局，不会各算各的而漂掉。
func (m model) View() tea.View {
	content, cursor := m.render()
	view := tea.NewView(content)
	view.Cursor = cursor
	return view
}

// render：**纯函数**（同样的 model ⇒ 同样的字符串）；顺带给出光标该摆在第几行第几列。
//
// 返回的 cursor 为 nil = **藏起来**：挑选项（`/resume` `/model`）与查看器（`/outgoing`、
// 确认框）铺满消息区时，输入行不是当前活动面 —— 别让光标留在屏幕上乱闪。
// **生成中照旧显示** ✓（输入行仍然可用，只是提交会被后端 409 顶回来）。
func (m model) render() (string, *tea.Cursor) {
	width, height := max(1, m.width), m.height
	if height < 1 {
		height = 24 // 还没收到 WindowSizeMsg 时的兜底（免得算出 0 行画面）
	}
	status := m.renderStatus(width) // 1-2 行
	palette := m.renderPalette(width)
	// 面板弹在输入行上方：屏幕再矮也要留一行消息区 ⇒ 装不下就只显示高亮那条附近的一段
	if room := height - 2 - len(status) - 1; len(palette) > room {
		palette = paletteWindow(palette, m.clampPalette(), max(room, 0))
	}
	bodyHeight := height - 2 - len(status) - len(palette) // 输入行 + 分隔线 + 状态行
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	lines := m.renderMessages(bodyHeight, width)
	lines = append(lines, palette...)
	inputLine := len(lines)                                           // 输入行在 `lines` 里的下标（消息区 + 面板之后）
	lines = append(lines, truncate("> "+m.input, width))              // 输入行（命令与消息都从这儿走）
	lines = append(lines, m.style.dim(rule(width, m.width, m.ascii))) // 满线（暗色）：窄屏退回 ASCII `-`
	lines = append(lines, status...)
	if len(lines) > height { // 屏幕实在太矮（< 4 行）：宁可切掉上面的消息，也别把输入行挤没
		shift := len(lines) - height
		lines, inputLine = lines[shift:], inputLine-shift
	}
	var cursor *tea.Cursor
	if m.picker == pickerNone && m.viewer == nil && inputLine >= 0 && inputLine < len(lines) {
		// 列 = 前缀 2 格 + 光标**前**那段的**显示宽度**（CJK 一个字两格）；
		// 输入太长被截断时夹到最后一格（终端也就只能摆到那儿）。
		column := m.inputCursorColumn()
		if column > width-1 {
			column = width - 1
		}
		cursor = tea.NewCursor(max(column, 0), inputLine)
	}
	return strings.Join(lines, "\n"), cursor
}

// ── 输入行的光标：两个身份别混 ──
//
// **rune 下标**（model 里存的、增删要用的）≠ **显示列**（摆到终端上的）。
// 一个 rune 可能占**两格**（CJK）⇒ 列只能由 runewidth 现算（见 inputCursorColumn）。

// clampCursor：把光标夹回 [0, rune 数] —— 改动输入之后都过它一次，别让光标停到输入外面。
func clampCursor(cursor, runes int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > runes {
		return runes
	}
	return cursor
}

// inputCursorColumn：光标在输入行里**第几格**（含 `> ` 前缀那两格）—— 按**显示宽度**算。
//
// 这是"光标指到字中间"的唯一防线：`你好|` 的列是 2+4=6 ✗ 不是 2+2=4（那是 rune 数）；
// `a你|` 是 2+1+2=5。
func (m model) inputCursorColumn() int {
	runes := []rune(m.input)
	return 2 + runewidth.StringWidth(string(runes[:clampCursor(m.inputCursor, len(runes))]))
}

// paletteWindow：面板装不下时只显示**高亮那条**附近的一段（不加行，也不让高亮跑出屏幕）。
func paletteWindow(lines []string, current, room int) []string {
	if room >= len(lines) {
		return lines
	}
	if room <= 0 {
		return nil
	}
	start := current - room/2
	if start < 0 {
		start = 0
	}
	if start+room > len(lines) {
		start = len(lines) - room
	}
	return lines[start : start+room]
}

// renderPalette：命令面板（输入以 `/` 开头时**推导**出来的 —— 不是又一个状态）。
func (m model) renderPalette(width int) []string {
	matches := m.paletteMatches()
	if len(matches) == 0 {
		return nil
	}
	current := m.clampPalette()
	lines := make([]string, 0, len(matches))
	for index, candidate := range matches {
		cursor := "  "
		if index == current {
			cursor = "> "
		}
		label := "/" + candidate.name
		if candidate.args != "" {
			label += " " + candidate.args
		}
		lines = append(lines, m.style.selected(truncate(cursor+label+"  "+candidate.help, width), index == current))
	}
	return lines
}

// renderMessages：**只画当前会话的对话**（没有左栏、也没有会话列表）。
//
// 为什么从底部往上整块排：聊天记录里**最新的在下面**（贴着输入行）；而且一块消息
// （署名 + 正文）要么整块出现、要么不出现 —— 从顶部截断会把署名切掉，
// 看起来像"不知道谁说的"（这个 bug 是测试抓出来的）。装不下的在顶部如实报数。
func (m model) renderMessages(height, width int) []string {
	if m.viewer != nil {
		hint := m.viewerHint
		if hint == "" {
			hint = "任意键关掉"
		}
		lines := []string{
			m.style.joined([]segment{{" " + m.viewerTitle, styleBold}, {hint, styleDim}}, width, "   "),
			m.style.dim(truncate(" "+rule(max(1, width-2), m.width, m.ascii), width)),
		}
		for _, line := range m.viewer {
			if len(lines) >= height {
				break
			}
			lines = append(lines, line)
		}
		return fillLines(lines, height)
	}
	switch m.picker {
	case pickerResume:
		return m.renderResumePicker(height, width)
	case pickerModel:
		return m.renderModelPicker(height, width)
	}
	session := m.currentSession()
	if session == nil {
		// **没有可用会话**（正常走不到：进 TUI 就会建一条真的）—— 如实说，别假装在会话里
		return fillLines([]string{
			truncate(" 现在没有可用会话 —— /new 建一条，或 /refresh 重拉会话列表", width),
		}, height)
	}
	busy := m.turn.Busy() && m.turn.sessionID == session.ID
	systemPrompt := m.visibleSystemPrompt(session)
	blocks := make([][]string, 0, len(m.messages)+2)
	// **系统提示词摆在最上面**（它就是"这轮带的底子"）：当成一条 role=system 的消息，
	// 与别的块同一条规矩 —— 块不劈开、装不下就从**顶部**挤掉（fitBlocks 从最新那块往上量）。
	if systemPrompt != nil {
		blocks = append(blocks, m.systemBlock(*systemPrompt, width))
	}
	for _, message := range m.messages {
		blocks = append(blocks, m.messageBlock(message, width))
	}
	if busy { // 生成中那条回复：库里还没有它 ⇒ 界面**合成**一个气泡（落库后同 id 的真消息自然取代它）
		blocks = append(blocks, m.liveBlock(width))
	}
	if m.rerollRunning() { // 重摇中：另起一块（**不假装**那是已定稿的那条回复）
		blocks = append(blocks, m.liveRerollBlock(width))
	}
	return fitBlocksFrom(blocks, m.scrollEnd(len(blocks), systemPrompt != nil), height, width)
}

// scrollEnd：消息区画到第几块（不含）—— scroll 条 message 被翻过去 ⇒ 末尾少画 scroll 块。
// system 提示词那块不算 message（offset=有它就 1）：scroll 只数 m.messages；
// live 气泡是合成块，不占 scroll 名额 —— 往上翻时它们照旧在底下（有活在跑你得看得见）。
func (m model) scrollEnd(total int, hasSystem bool) int {
	offset := 0
	if hasSystem {
		offset = 1
	}
	// scroll 只数 m.messages：live 气泡（生成中/重摇中）是合成块，不占名额 ——
	// 往上翻时它们照旧在底下（有活在跑你得看得见），画到底时自然全在。
	end := offset + len(m.messages) - m.scroll
	if m.turn.Busy() {
		end++
	}
	if m.rerollRunning() {
		end++
	}
	if end < offset {
		end = offset
	}
	if end > total {
		end = total
	}
	return end
}

// scrollBy：消息区往上/下翻几条 message（pgup=+1，pgdn=-1）。
// 上限夹到"最老那条"（再往上没东西了）；下限 0（到底）。viewer/picker 开着时不进这里
// （按键先被它们吃掉 —— 滚动只在纯消息区生效）。
func (m model) scrollBy(delta int) model {
	m.scroll += delta
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll > len(m.messages) {
		m.scroll = len(m.messages)
	}
	return m
}

// visibleSystemPrompt：这条会话**现在该画**的那份生效系统提示词（不画就回 nil）。
//
// 三个条件缺一不可（与上下文占用那条 `contextFor` 同一条规矩）：
//   - 开关开着（`/system`，默认开）；
//   - 已经拉到了（进会话时拉一次）；
//   - 拉回来那份**就是这条会话的**（换会话时旧的还摆着 ⇒ 拿它画就是撒谎）。
func (m model) visibleSystemPrompt(session *Session) *SystemPrompt {
	if !m.showSystemPrompt || session == nil || m.prompt == nil || m.promptFor != session.ID {
		return nil
	}
	return m.prompt
}

// systemBlock：消息区**最上面**那条"系统提示词"（调试用：这轮到底带了什么底子）。
//
// 口径：标签「系统提示词」用调色板里的 `system` 那档 = **暗色**（与 `/outgoing` 里 `type=system` 同一档）；
// 正文保持默认色（内容才是主角，与别的消息一致）。**先排版、后上色**（截断作用在未着色的原文上）。
func (m model) systemBlock(prompt SystemPrompt, width int) []string {
	block := []string{m.style.joined([]segment{{" 系统提示词：", styleDim}}, width, "")}
	text := prompt.Text
	if strings.TrimSpace(text) == "" {
		text = "（空）" // 两级都没写 ⇒ 真会发出去的就是空的，照实说（与 /state 的空态同一个写法）
	}
	for _, line := range strings.Split(text, "\n") {
		block = append(block, truncate("   "+line, width))
	}
	return append(block, "")
}

// messageBlock：一条消息在界面上的样子（署名 + 正文 + 一个空行）—— **整块**是它的最小单位。
//
// 署名上色（口径见 AGENTS.md）：你自己 = 青，助手 = 洋红（**耗时数字用暗色** —— 它是次要信息）。
// 正文保持默认色（内容才是主角）。
func (m model) messageBlock(message Message, width int) []string {
	label := "你"
	kind := styleCyan
	if message.Role == "assistant" {
		label = "助手" // TODO：等 /agents 搬完，改用该会话 agent 的名字（界面口径见 AGENTS.md）
		kind = styleMagenta
	}
	detail := ""
	if message.DurationMS != nil {
		detail = fmt.Sprintf("%.1fs", float64(*message.DurationMS)/1000)
	}
	signature := []segment{{" " + label, kind}}
	if detail != "" { // `助手 2.1s：` —— 耗时**暗色**，署名与冒号还是本色
		signature = append(signature,
			segment{" ", stylePlain}, segment{detail, styleDim}, segment{"：", kind})
	} else {
		signature = append(signature, segment{"：", kind})
	}
	// 重摇模式里的位次标记（`‹ 2/3 ›`）：只在模式内、只给**那条被摇的尾巴**画。
	// 高亮（粗体 + 青）：这条消息就是当前选中那一版 —— 与挑选项的选中口径同一条。
	if marker, ok := m.rerollMarker(message.ID); ok {
		signature = append(signature, segment{" " + marker, styleBoldCyan})
	}
	block := []string{m.style.joined(signature, width, "")}
	// 落库后那条消息：正文**上方**一行**折叠**的「思考（2.1s）」（暗色、不上背景）。
	// 想读全文用 `/think`（查看器铺满消息区）。没有思考的消息不出现这一行。
	if strings.TrimSpace(message.Reasoning) != "" {
		block = append(block, m.style.dim(truncate("   "+thinkingLabel(message), width)))
	}
	for _, line := range strings.Split(message.Content, "\n") {
		block = append(block, truncate("   "+line, width))
	}
	return append(block, "")
}

// thinkingLabel：折叠行的文案 —— 有 `reasoning_ms` 就报「思考（2.1s）」，没有就只说「思考」。
func thinkingLabel(message Message) string {
	if message.ReasoningMS != nil {
		return fmt.Sprintf("思考（%.1fs）", float64(*message.ReasoningMS)/1000)
	}
	return "思考"
}

// fitBlocks：从**最新**那块往上装（整块装、装不下就整块不显示）；
// 顶上被挤掉了几条就如实写一行"（上面还有 N 条）"。scroll语义见 fitBlocksFrom（0 = 到底）。
func fitBlocks(blocks [][]string, height, width int) []string {
	return fitBlocksFrom(blocks, len(blocks), height, width)
}

// fitFromBottom：从最新那块往上量，返回"最上面从第几块开始显示"与"一共用了几行"。
func fitFromBottom(blocks [][]string, height int) (taken, used int) {
	taken = len(blocks)
	for taken > 0 {
		size := len(blocks[taken-1])
		if used+size > height {
			break
		}
		used += size
		taken--
	}
	return taken, used
}

// fitBlocksFrom：从第 end 块（不含）往上装 —— end < len(blocks) 就是"往上翻了"。
// 顶上提示照算（还有几条没显示）；底下被翻过去的，有几条就报"（下面还有 N 条）"。
// 单块超一屏：只露它能露出的**末尾**（块是逻辑单位，屏是物理窗口 —— 长块不断头，只断尾）。
func fitBlocksFrom(blocks [][]string, end, height, width int) []string {
	if end > len(blocks) {
		end = len(blocks)
	}
	if end < 0 {
		end = 0
	}
	visible := blocks[:end]
	taken, used := fitFromBottom(visible, height)
	lines := make([]string, 0, height)
	if taken > 0 {
		// 顶上提示占 1 行 ⇒ 可视区只剩 height-1：塞不下的块要露末尾（块不断头，只断尾）
		budget := height - 1
		kept := [][]string{}
		hidden := 0
		for i := len(visible) - 1; i >= taken; i-- {
			if len(visible[i]) <= budget {
				budget -= len(visible[i])
				kept = append([][]string{visible[i]}, kept...)
			} else {
				hidden += len(visible[i]) - budget
				kept = append([][]string{visible[i][len(visible[i])-budget:]}, kept...)
				budget = 0
				for _, block := range visible[taken:i] {
					hidden += len(block)
				}
				break
			}
		}
		lines = append(lines, truncate(fmt.Sprintf(" （上面还有 %d 条，本条上面还有 %d 行）", taken, hidden), width))
		shown := kept
		used = 0
		for _, block := range shown {
			used += len(block)
		}
		for len(lines)+used < height {
			lines = append(lines, "")
		}
		for _, block := range shown {
			lines = append(lines, block...)
		}
	} else {
		shown := visible[taken:]
		for len(lines)+used < height {
			lines = append(lines, "")
		}
		for _, block := range shown {
			lines = append(lines, block...)
		}
	}
	if end < len(blocks) {
		lines = append(lines, truncate(fmt.Sprintf(" （下面还有 %d 条 · End 回到底）", len(blocks)-end), width))
	}
	return fillLines(lines, height)
}

// fillLines：补空行到 height 行（多了就砍尾巴 —— 每块自己已经按宽度截过了）。
func fillLines(lines []string, height int) []string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// liveBlock：**生成中那一刻**的气泡（库里还没有那条回复）。
//
// 口径照 AGENTS.md：转圈 + "生成中… 12.3s" + 「/stop」；思考只报**字数**
// （token 数流式帧里没有 —— 卡片脚注那儿的 token 是另一回事，别混）。
func (m model) liveBlock(width int) []string {
	// 口径：`生成中…` 那段**黄**（含下面的「/stop 停止」）；署名还是助手本色；思考是次要信息 ⇒ 暗色。
	block := []string{m.style.joined([]segment{
		{" 助手（", styleMagenta},
		{fmt.Sprintf("生成中… %.1fs", float64(m.turn.elapsedMS)/1000), styleYellow},
		{"）：", styleMagenta},
	}, width, "")}
	if m.turn.thinking != "" {
		line := fmt.Sprintf("   [思考中… %d 字]", len([]rune(m.turn.thinking)))
		if m.turn.thinkMS != nil { // 有"受理 → 第一段正文"的用时 ⇒ 一并报出来（口径同落档后的 reasoning_ms）
			line = fmt.Sprintf("   [思考中… %d 字 · %.1fs]", len([]rune(m.turn.thinking)), float64(*m.turn.thinkMS)/1000)
		}
		block = append(block, m.style.dim(truncate(line, width)))
	}
	for _, line := range strings.Split(m.turn.text, "\n") {
		block = append(block, truncate("   "+line, width))
	}
	block = append(block, m.style.yellow(truncate("   （/stop 停止）", width)))
	return append(block, "")
}

// liveRerollBlock：**重摇中**那一刻的合成块（候选还在摇，库里那条消息还是当前那版）。
//
// 口径与 `liveBlock` 同一条：只说真话 —— 说的是"在摇第 N 版 + 耗时"，
// 不假装正文已经在往外蹦（重摇那一趟是非流式的，没有增量可看）。
// 摘要模式还把目标区间带上（消息区没有对应的一条可画标记，信息只能在这儿/底栏说清）。
func (m model) liveRerollBlock(width int) []string {
	state := m.rerollState()
	if state == nil {
		return nil
	}
	command := "/reroll switch"
	hint := ""
	if state.TargetKind == RerollKindSummary {
		command = "/reroll-summary switch"
		hint = "（目标：" + rerollTargetLabel(*state) + "那条摘要）"
	}
	block := []string{m.style.joined([]segment{
		{" 助手（", styleMagenta},
		{fmt.Sprintf("重摇中… %.1fs", float64(state.ElapsedMS)/1000), styleYellow},
		{"）：" + hint, styleMagenta},
	}, width, "")}
	block = append(block, m.style.dim(truncate(
		fmt.Sprintf("   第 %d 版还在摇 —— 摇完用 %s %d 换上去", state.Count, command, state.Count), width)))
	return append(block, "")
}

func (m model) renderResumePicker(height, width int) []string {
	lines := []string{
		m.style.joined([]segment{
			{" 挑一条已有会话", styleBold},
			{"上/下 选 · 回车进 · Esc 取消", styleDim},
		}, width, "   "),
		m.style.dim(truncate(" "+rule(max(1, width-2), m.width, m.ascii), width)),
	}
	for index, session := range m.sessions {
		if len(lines) >= height {
			break
		}
		cursor := "  "
		if index == m.pickerIndex {
			cursor = "> "
		}
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "（还没起名）"
		}
		lines = append(lines, m.pickerLine([]segment{
			{cursor + shortID(session.ID) + " ", styleDim}, // 短 id：次要信息 ⇒ 暗色
			{title, stylePlain},
			{"    " + session.Provider + " / " + session.Model, styleDim},
			// 条数来自 `GET /sessions`（列表项自带）：它可能比"此刻"旧一步（发完一句话要等翻 idle 才刷列表），
			// 当"这条大概多大"够了 ✓ —— 要精确的是启动编排那两条判据，那儿列表刚拉回来 ✓。
			{fmt.Sprintf("  %d 条", session.Messages), styleDim},
		}, width, index == m.pickerIndex))
	}
	return fillLines(lines, height)
}

// renderModelPicker：**按 provider 排列**（用户 2026-09-29 的口径：先不分能力，按渠道分组）。
func (m model) renderModelPicker(height, width int) []string {
	lines := []string{
		m.style.joined([]segment{
			{" 挑渠道 / 模型", styleBold},
			{"上/下 选 · 回车定 · Esc 取消", styleDim},
		}, width, "   "),
		m.style.dim(truncate(" "+rule(max(1, width-2), m.width, m.ascii), width)),
	}
	if len(m.models) == 0 {
		lines = append(lines, truncate(" （还没有发现任何模型 —— 设置里给渠道点一次「获取模型」）", width))
	}
	currentProvider := ""
	for index, item := range m.models {
		if len(lines) >= height {
			break
		}
		if item.Provider != currentProvider {
			currentProvider = item.Provider
			lines = append(lines, m.style.bold(truncate(" -- "+item.ProviderLabel()+" --", width)))
			if len(lines) >= height {
				break
			}
		}
		cursor := "  "
		if index == m.pickerIndex {
			cursor = "> "
		}
		current := ""
		if item.Provider == m.chosenProvider && item.UpstreamID == m.chosenModel {
			current = "（当前）"
		}
		lines = append(lines, m.pickerLine([]segment{
			{cursor + item.Label(), stylePlain},
			{current, styleDim},
		}, width, index == m.pickerIndex))
	}
	return fillLines(lines, height)
}

// pickerLine：挑选项里的一行 —— 选中的那条**粗体 + 青**（其余保持默认）。
//
// 选中时**不再叠暗色** ✗：套两层转义会在行中间插一个复位，把后半行打回默认色（看出来像掉色）。
func (m model) pickerLine(segments []segment, width int, selected bool) string {
	if selected {
		return m.style.boldCyan(truncate(plainOf(segments, ""), width))
	}
	return m.style.joined(segments, width, "")
}

// currentSession：当前会话 —— **按 id 找**（不按下标：列表随时会重排，见 selectedID）。
func (m model) currentSession() *Session {
	if m.selectedID == "" {
		return nil
	}
	index := m.findSession(m.selectedID)
	if index < 0 {
		return nil
	}
	return &m.sessions[index]
}

// tgLine：底栏那段 **Telegram 连通性指示**（没拉过 / 没配 ⇒ 整段不显示）。
//
// 口径（用户 2026-10-02 定）：
//
//	开了 + 跑着        ⇒ `TG ✓` 绿（长轮询真在转）；
//	开了 + 有 token 但没跑 ⇒ `TG ✗` 红（多半启动失败 / 端口冲突，原因在 log 里）；
//	开了 + 没 token     ⇒ `TG 缺token` 红（开着也连不上）；
//	有 token 但没开     ⇒ `TG 关` 暗色（别吓人：这是用户自己的选择）；
//	没拉过 / 没配       ⇒ 不显示（没配 TG 的人底栏不长草）。
func (m model) tgLine() (string, styleKind, bool) {
	if !m.tgLoaded {
		return "", stylePlain, false
	}
	switch {
	case m.tg.Enabled && m.tg.Running:
		return "TG ✓", styleGreen, true
	case m.tg.Enabled && m.tg.HasToken:
		return "TG ✗", styleRed, true
	case m.tg.Enabled:
		return "TG 缺token", styleRed, true
	case m.tg.HasToken:
		return "TG 关", styleDim, true
	}
	return "", stylePlain, false
}

// renderStatus：底下的**会话状态行**（口径见 AGENTS.md「界面该长什么样」）。
//
// 两半**互不顶替**（旧口径就这么定的）：会话那半（名字 | 短 id | 渠道/模型 | 上下文占用 | 在不在跑）
// 与"TG 连通性 | 已连接 vX | 最近一次动作"。一行放得下就一行，放不下就折成两行 —— **最多两行**。
func (m model) renderStatus(width int) []string {
	session := m.currentSession()
	head := []segment{{"没有会话", styleDim}}
	if session != nil {
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "（还没起名）"
		}
		where := session.Provider + "/" + session.Model
		if session.Provider == "" || session.Model == "" {
			where = "（还没选模型）"
		}
		head = []segment{
			{title, styleBold},              // 会话名：粗体（拉层级，不上色）
			{shortID(session.ID), styleDim}, // 短 id / 渠道·模型都是次要信息 ⇒ 暗色
			{where, styleDim},
		}
		if usage := m.contextUsage(session); usage != nil {
			kind := styleDim
			if usage.OverBudget {
				kind = styleRed // **超预算 ⇒ 这一段用红**
			}
			head = append(head, segment{contextLabel(*usage), kind})
		}
	}
	// 在不在跑**比名字重要**（用户口径）⇒ 先给它留位置，再把前面那几档按余量截断
	state := m.turnLabel(session)
	stateKind := styleDim
	if strings.HasPrefix(state, "生成中") || strings.HasPrefix(state, "重摇中") || strings.HasPrefix(state, "⚙") {
		stateKind = styleYellow // 生成中 / 重摇中 / 有活在跑 ⇒ 黄（含耗时）
	}
	room := width - runewidth.StringWidth(state) - len(" | ")
	sessionInfo, infoWidth := m.style.concat(head, max(room, 0), " | ")
	if room < 1 { // 屏幕太窄：只留"在不在跑"
		state = truncate(state, width)
		sessionInfo, infoWidth = m.style.paint(stateKind, state), runewidth.StringWidth(state)
	} else {
		sessionInfo += " | " + m.style.paint(stateKind, state)
		infoWidth += len(" | ") + runewidth.StringWidth(state)
	}
	tail := []segment{}
	// TG 连通性那段插在 `已连接 vX` **之前**（没拉过 / 没配就整段不出现 —— 见 tgLine）。
	if text, kind, show := m.tgLine(); show {
		tail = append(tail, segment{text, kind})
	}
	if m.version == "" {
		tail = append(tail, segment{"未连接", styleDim})
	} else {
		tail = append(tail, segment{"已连接 v" + m.version, styleDim})
	}
	if m.lastAction != "" {
		kind := stylePlain
		if m.lastAction == m.lastFailure { // 还摆着的那句失败 ⇒ 红（见 model.lastFailure）
			kind = styleRed
		}
		tail = append(tail, segment{m.lastAction, kind})
	}
	tailPlain := plainOf(tail, " | ")
	if infoWidth+len(" | ")+runewidth.StringWidth(tailPlain) <= width {
		return []string{sessionInfo + " | " + m.style.joined(tail, width, " | ")}
	}
	return []string{sessionInfo, m.style.joined(tail, width, " | ")}
}

// contextUsage：当前会话的上下文占用 —— 拉回来的那份**必须属于这条会话**才用
// （换会话时那份还旧着 ⇒ 拿它画就是撒谎）。
func (m model) contextUsage(session *Session) *ContextUsage {
	if m.context == nil || session == nil || m.contextFor != session.ID {
		return nil
	}
	return m.context
}

// rerollState：当前会话的重摇那一份（**只在模式内**；对不上号就 nil）。
//
// 三个条件缺一不可（与 contextUsage / visibleSystemPrompt 同一条规矩）：拉到了、
// 那条会话**现在**在重摇模式里、且这份就是这条会话的。
func (m model) rerollState() *RerollState {
	if m.reroll == nil || !m.reroll.Active || m.selectedID == "" || m.rerollFor != m.selectedID {
		return nil
	}
	return m.reroll
}

// rerollRunning：有一版正在摇（界面据此显示「重摇中… 耗时」，也据此拦住"再发一句"）。
func (m model) rerollRunning() bool {
	state := m.rerollState()
	return state != nil && state.Running
}

// rerollMarker：尾条旁边那个位次标记 —— `‹ 2/3 ›`（**只在消息重摇模式里**、只给它说的那条尾巴）。
//
// 「位次不是身份」：分母是现在共几版、分子是**当前 apply 的那一版**（当前这版高亮 —— 与挑选项同一条口径）。
// 摘要模式**不画**这个标记：摇的是摘要、没有对应的那条消息可挂（信息放底栏 / `/reroll-summary list`）。
// `TERM=dumb` / 非 TTY ⇒ 退回 ASCII `< 2/3 >`（`‹ ›` 也是这台终端不认识的那类字符）。
func (m model) rerollMarker(messageID string) (string, bool) {
	state := m.rerollState()
	if state == nil || state.TargetKind == RerollKindSummary ||
		state.TargetMessageID != messageID || state.Count < 1 || state.CurrentIdx < 1 {
		return "", false
	}
	if m.ascii {
		return fmt.Sprintf("< %d/%d >", state.CurrentIdx, state.Count), true
	}
	return fmt.Sprintf("‹ %d/%d ›", state.CurrentIdx, state.Count), true
}

// contextLabel：状态行里那一档（照 Pi 的形状）：`12.3k/131k (9.4%)`。
//
// 百分比 = `used / budget`（**自己算** ✗ 别拿契约里的 `ratio` —— 那是分词器标定比，不是占用比例，见 client.go）。
// 超预算就照实报 >100%（夹到 100% 会把"超了多少"藏起来）。
func contextLabel(usage ContextUsage) string {
	percent := 0.0
	if usage.BudgetTokens > 0 {
		percent = float64(usage.UsedTokens) / float64(usage.BudgetTokens) * 100
	}
	return fmt.Sprintf("%s/%s (%.1f%%)",
		formatTokens(usage.UsedTokens), formatTokens(usage.BudgetTokens), percent)
}

// formatTokens：token 数缩写（≥1000 用 k，整千不带小数点）：`12.3k` / `131k`。
func formatTokens(tokens int) string {
	if tokens < 1000 {
		return strconv.Itoa(tokens)
	}
	return strings.Replace(fmt.Sprintf("%.1fk", float64(tokens)/1000), ".0k", "k", 1)
}

// turnLabel：这一轮在不在跑（"生成中 + 耗时" / "空闲"）。
//
// 界面自己那份（`m.turn`）优先：它比会话列表里那份新（列表是上一次刷新时的样子）。
func (m model) turnLabel(session *Session) string {
	if session == nil {
		return "空闲"
	}
	// 重摇在跑 ⇒ 先报它（这一轮没在生成，但**有活在跑** —— 别在界面上撒谎说"空闲"）
	if state := m.rerollState(); state != nil && state.Running {
		return fmt.Sprintf("重摇中 %.1fs", float64(state.ElapsedMS)/1000)
	}
	if m.turn.Busy() && m.turn.sessionID == session.ID {
		return fmt.Sprintf("生成中 %.1fs", float64(m.turn.elapsedMS)/1000)
	}
	if session.Turn.Busy() {
		return fmt.Sprintf("生成中 %.1fs", float64(session.Turn.ElapsedMS)/1000)
	}
	if tasks := m.tasksLine(); tasks != "" {
		return tasks // 本会话空闲，但别处有活（后台压缩/标题/判断在跑）
	}
	return "空闲"
}

// tasksLine：底栏的 Tasks 指示器 —— 有在跑的活才亮（`⚙ 压缩中 · 起标题中`）。
// 只报 running 的（已结束的不占地方）；本会话的生成/重摇已有专属档，不重复报。
func (m model) tasksLine() string {
	names := []string{}
	seen := map[string]bool{}
	for _, record := range m.tasks.Tasks {
		if record.FinishedAt != nil {
			continue
		}
		label := taskKindLabel(record.Kind)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		names = append(names, label+"中")
	}
	if len(names) == 0 {
		return ""
	}
	return "⚙ " + strings.Join(names, " · ")
}

// taskKindLabel：Task 种类的中文名（与后端 `Kind.Label` 同一套词，界面侧照抄一份
// —— TUI 不 import 后端内部包，走 HTTP 契约）。
func taskKindLabel(kind string) string {
	switch kind {
	case "turn":
		return "生成"
	case "reroll":
		return "重摇"
	case "compact":
		return "压缩"
	case "title":
		return "起标题"
	case "judgement":
		return "判断"
	case "refresh_models":
		return "刷新模型"
	default:
		return kind
	}
}

// ── 小工具（宽度一律按**显示宽度**算：中文占两格，所以必须用 runewidth）──

// truncate：按**显示宽度**截断到 width 格以内（中文占两格 ⇒ 不能按字符数截）；短了不补空格。
//
// 两个坑：
//  1. 不用 `runewidth.Truncate`：它把省略号算进宽度却**少填一格**（"abcdefghij",5 ⇒ "abc…" ✗）；
//  2. 省略号用 **ASCII `...`** ✗ 不用 `…`：`…` 是**模糊宽度**字符（East Asian Ambiguous），
//     runewidth 当一格、CJK 终端可能当两格 ⇒ 宽度账与实际差一格。满线 `─` 是同一个坑
//     （所以它只在**宽屏**上用，窄屏退回 ASCII —— 见 rule），结构性字符（提示符 / 竖线）则一律 ASCII。
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width <= len(ellipsis) { // 连省略号都放不下 ⇒ 只给三点里放得下的那几格（ASCII ⇒ 一格一个）
		return ellipsis[:width]
	}
	budget := width - len(ellipsis)
	var builder strings.Builder
	used := 0
	for _, r := range text {
		cell := runewidth.RuneWidth(r)
		if used+cell > budget {
			break
		}
		builder.WriteRune(r)
		used += cell
	}
	return builder.String() + ellipsis
}

// ellipsis：ASCII 三点的省略号（见 truncate 的注释：模糊宽度字符会算错一格）。
const ellipsis = "..."

// ── 画线：**重复**，不是"填充" ──

// ruleMinWidth：满线用 `─` 的最小屏幕宽度。`─` 是**模糊宽度**字符（East Asian Ambiguous）：
// 宽度账上按一格算（见 init），但 CJK 终端可能按两格排 ⇒ 窄屏上会溢出换行。
// 所以窄屏退回 ASCII `-`（横线是界面骨架，骨架歪了整屏都歪）。
const ruleMinWidth = 60

// rule：画一条 `cells` 格的满线。**绝不用模糊宽度字符去"填充"对齐** —— 只用重复（`strings.Repeat`）。
//
// 两个条件任一成立就退回 ASCII `-`：**屏幕太窄**（见 ruleMinWidth）或**这台终端不认 Unicode**
// （`ascii` = `TERM=dumb` / 非 TTY，见「降级两档」）。
func rule(cells, screen int, ascii bool) string {
	cells = max(1, cells)
	if ascii || screen < ruleMinWidth {
		return strings.Repeat("-", cells)
	}
	return strings.Repeat("─", cells)
}

// ── 着色：**只有前景色**，绝不给背景上色（用户明确要求）──

// styler：极小的着色器。三条硬规矩（见 AGENTS.md「踩过的坑」）：
//
//  1. **只前景**：16 色基础 ANSI（30–37 / 90–97）+ 粗体 / 暗色；不用 256 色 / truecolor，不用背景色；
//  2. **先排版、后上色**：转义序列零宽，而 truncate / 对齐是按格算的 ⇒
//     截断一律作用在**未着色**的原文上（落点是 concat / plainOf），颜色最后才包上去；
//  3. **可关**：零值 = 关 ⇒ 测试拿到的 View() 是纯文本（逐字节断言靠这个）。
//
// 生产路径的开关在 detectTerminal（`NO_COLOR` / `TERM=dumb` / 非 TTY 三档降级）。
type styler struct{ enabled bool }

// styleKind：要包哪一层前景色（零值 = 不上色）。
type styleKind int

const (
	stylePlain styleKind = iota
	styleBold
	styleDim
	styleCyan
	styleMagenta
	styleYellow
	styleRed
	styleGreen
	styleBoldCyan
)

func (s styler) paint(kind styleKind, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	code := ""
	switch kind {
	case styleBold:
		code = "1"
	case styleDim:
		code = "2"
	case styleCyan:
		code = "36"
	case styleMagenta:
		code = "35"
	case styleYellow:
		code = "33"
	case styleRed:
		code = "31"
	case styleGreen:
		code = "32"
	case styleBoldCyan:
		code = "1;36"
	default:
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s styler) bold(text string) string     { return s.paint(styleBold, text) }
func (s styler) dim(text string) string      { return s.paint(styleDim, text) }
func (s styler) yellow(text string) string   { return s.paint(styleYellow, text) }
func (s styler) red(text string) string      { return s.paint(styleRed, text) }
func (s styler) boldCyan(text string) string { return s.paint(styleBoldCyan, text) }

// selected：挑选项里**选中**的那一行 —— 粗体 + 青（没选中就原样）。
func (s styler) selected(text string, on bool) string {
	if !on {
		return text
	}
	return s.boldCyan(text)
}

// segment：一段"先排版、后上色"的文本：text 是**未着色**的原文（宽度账只算它），kind 决定包什么色。
//
// ⚠ 别在同一段里嵌套两种颜色：行中间那个复位会把后半段打回默认色（宁可分两段）。
type segment struct {
	text string
	kind styleKind
}

// concat：把若干段按 sep 拼进 limit 格，返回拼好的字符串与它**未着色**的宽度。
//
// 装不下的段**整段丢掉**（含它前面的分隔符）；最后那一段太长就**截断**（带省略号）后收工 ——
// 截断发生在 paint 之前，所以宽度账与终端实际排出来的格数一致（**先排版、后上色**）。
func (s styler) concat(segments []segment, limit int, sep string) (string, int) {
	var builder strings.Builder
	used := 0
	for _, seg := range segments {
		if seg.text == "" {
			continue
		}
		gap := 0
		if used > 0 {
			gap = runewidth.StringWidth(sep)
		}
		room := limit - used - gap
		if room <= 0 {
			break
		}
		text := seg.text
		clipped := runewidth.StringWidth(text) > room
		if clipped {
			if room <= len(ellipsis) { // 连省略号都放不下 ⇒ 这一段不显示（宁可空着，也不超宽）
				break
			}
			text = truncate(text, room)
		}
		if used > 0 {
			builder.WriteString(sep)
		}
		builder.WriteString(s.paint(seg.kind, text))
		used += gap + runewidth.StringWidth(text)
		if clipped {
			break
		}
	}
	return builder.String(), used
}

// joined：只要拼好的字符串（不关心它占了几格）。
func (s styler) joined(segments []segment, limit int, sep string) string {
	text, _ := s.concat(segments, limit, sep)
	return text
}

// plainOf：把几段按 sep 拼成**未着色**的原文 —— 宽度账与"一行放不放得下"都只算它。
func plainOf(segments []segment, sep string) string {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg.text != "" {
			parts = append(parts, seg.text)
		}
	}
	return strings.Join(parts, sep)
}

// 宽度账与终端自洽：模糊宽度（East Asian Ambiguous，如 `│` `▶` `·` `…`）**按一格**算 ——
// CJK 宽字（`你` 这类 Wide）不受影响，仍是两格 ✓。
// **结构性字符**（提示符、竖线、省略号）一律 ASCII ✗ 例外只有一个：满线 `─` 在**宽屏**上用
// （窄屏退回 ASCII，见 rule —— 模糊宽度字符在 CJK 终端可能按两格排）。
func init() { runewidth.DefaultCondition.EastAsianWidth = false }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
