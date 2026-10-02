package tg

// 这个文件只放**纯函数**：把结构体数据组行文 / 按钮。
//
// 不碰网络、不碰 `*bot.Bot`、不读 Runner 状态 —— 单测直接喂数据断言文案（见 commands_test.go）。
// 口径抄自 TUI 底栏（`internal/tui/model.go` 的 `renderStatus` / `contextLabel` / `formatTokens` /
// `shortID`）：bot 与 TUI 是两套界面、各走各的 HTTP（互不 import），所以那几个小格式化函数
// 照抄一份 —— **形状必须一致**（`12.3k/131k (9.4%)` 这种，用户两边看到同一套说法）。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"microchat/internal/apiclient"
)

// ── 与 TUI 同形状的那几个小格式化（照抄口径，见文件头注释）──

// shortID：会话短 id（前 8 位，够认人）。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// formatTokens：token 数缩写（≥1000 用 k，整千不带小数点）：`12.3k` / `131k`。
func formatTokens(tokens int) string {
	if tokens < 1000 {
		return strconv.Itoa(tokens)
	}
	return strings.Replace(fmt.Sprintf("%.1fk", float64(tokens)/1000), ".0k", "k", 1)
}

// contextLabel：上下文占用那一档（照 Pi 的形状）：`12.3k/131k (9.4%)`。
//
// 百分比 = `used / budget`（**自己算** —— 契约里的 `ratio` 是分词器标定比，不是占用比例）。
// 超预算照实报 >100%（夹到 100% 会把"超了多少"藏起来）。
func contextLabel(usage apiclient.ContextUsage) string {
	percent := 0.0
	if usage.BudgetTokens > 0 {
		percent = float64(usage.UsedTokens) / float64(usage.BudgetTokens) * 100
	}
	return fmt.Sprintf("%s/%s (%.1f%%)",
		formatTokens(usage.UsedTokens), formatTokens(usage.BudgetTokens), percent)
}

// sessionName：会话显示名（没起名就照实说）。
func sessionName(session apiclient.Session) string {
	if title := strings.TrimSpace(session.Title); title != "" {
		return title
	}
	return "（还没起名）"
}

// whereOf：会话绑的渠道 / 模型（还没选就照实说）。
func whereOf(session apiclient.Session) string {
	if session.Provider == "" || session.Model == "" {
		return "（还没选模型）"
	}
	return session.Provider + "/" + session.Model
}

// turnLabel：这一轮在不在跑（"生成中 2.4s" / "空闲"）。
func turnLabel(turn apiclient.TurnStatus) string {
	if turn.Busy() {
		return fmt.Sprintf("生成中 %.1fs", float64(turn.ElapsedMS)/1000)
	}
	return "空闲"
}

// uptimeLabel：bot 在线时长（没起来就照实说）。
func uptimeLabel(uptimeMS *int64) string {
	if uptimeMS == nil {
		return "没在跑"
	}
	duration := time.Duration(*uptimeMS) * time.Millisecond
	return duration.Round(time.Second).String()
}

// ── /status：CLI 底栏那份 ───────────────────────────────────────────────

// statusView：`statusLines` 的全部输入（都是取回来的事实，**不在纯函数里再拉一次**）。
type statusView struct {
	Session   *apiclient.Session
	Context   *apiclient.ContextUsage
	Turn      apiclient.TurnStatus
	Running   bool
	AllowedID int64
	UptimeMS  *int64
	Version   string
	// Problems：哪几块没问到（照实说，别装没事）。
	Problems []string
}

// statusLines：bot 的 `/status` —— **就是 CLI 底栏那份内容**（用户点名要的）。
//
// 四要素（会话 / 上下文 / 轮次 / TG）+ 后端版本；多段行文、不用颜色（TG 没有底栏）。
// 取不到的那几块如实标注（`—（没问到：…）`），不假装有。
func statusLines(view statusView) []string {
	lines := []string{}
	if view.Session == nil {
		lines = append(lines, "会话：没有（/new 建一条）")
		lines = append(lines, "上下文：—")
	} else {
		lines = append(lines, fmt.Sprintf("会话：%s | %s | %s",
			sessionName(*view.Session), shortID(view.Session.ID), whereOf(*view.Session)))
		if view.Context != nil {
			lines = append(lines, "上下文："+contextLabel(*view.Context))
		} else {
			lines = append(lines, "上下文：—（没问到）")
		}
	}
	lines = append(lines, "轮次："+turnLabel(view.Turn))
	tg := "TG："
	switch {
	case view.Running && view.AllowedID > 0:
		tg += "运行中 · 绑定 " + strconv.FormatInt(view.AllowedID, 10) + " · 在线 " + uptimeLabel(view.UptimeMS)
	case view.Running:
		tg += "运行中 · 还没绑定（去终端里 /telegram-bind <你的 id>）"
	case view.AllowedID > 0:
		tg += "没在跑（绑定 " + strconv.FormatInt(view.AllowedID, 10) + "）"
	default:
		tg += "没在跑 · 还没绑定"
	}
	lines = append(lines, tg)
	if view.Version != "" {
		lines = append(lines, "后端：v"+view.Version)
	} else {
		lines = append(lines, "后端：—（没连上）")
	}
	for _, problem := range view.Problems {
		lines = append(lines, "（"+problem+"）")
	}
	return lines
}

// ── /resume：会话清单 ──────────────────────────────────────────────────

// resumeLimit：清单最多列几条（超出的去打 `/resume` 让用户自己数，越界会说清）。
const resumeLimit = 20

// resumeLines：编号 + 名字 + 短 id（当前那条标出来）—— `/resume N` 的编号就打这里。
func resumeLines(sessions []apiclient.Session, currentID string) []string {
	lines := make([]string, 0, len(sessions)+1)
	lines = append(lines, "会话（/resume N 切过去）：")
	for index, session := range sessions {
		line := fmt.Sprintf("%d. %s (%s)", index+1, sessionName(session), shortID(session.ID))
		if session.ID == currentID {
			line += " ← 当前"
		}
		lines = append(lines, line)
	}
	return lines
}

// parsePick：把 `/resume N` 的 N 解析成 0-based 下标 —— **每条错都说清是哪不对、下一步干嘛**。
func parsePick(arg string, count int) (int, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return 0, fmt.Errorf("得给编号，比如 `/resume 2`")
	}
	number, err := strconv.Atoi(arg)
	if err != nil {
		return 0, fmt.Errorf("`%s` 不是数字 —— 编号是清单里的序号，比如 `/resume 2`", arg)
	}
	if number < 1 || number > count {
		if count == 0 {
			return 0, fmt.Errorf("现在一条会话都没有（/new 建一条）")
		}
		return 0, fmt.Errorf("编号得在 1–%d 之间（你说的是 %d）", count, number)
	}
	return number - 1, nil
}

// ── /model：按钮翻页 ───────────────────────────────────────────────────

// modelPageSize：一页几个模型（按钮一行一个，8 个一屏不挤）。
const modelPageSize = 8

// modelButtons：第 page 页的按钮（0-based）+ 总页数。
//
// 回调两形状（都 ≤ 64 字节，见 commands_test.go 钉住）：
//
//	model:<page>          —— 翻页 / 重画这一页；
//	model:<page>:<index>  —— 选中全局第 index 个（index 是 `/models` 拍平后的下标）。
func modelButtons(items []apiclient.ModelListItem, page int) ([][]models.InlineKeyboardButton, int) {
	if len(items) == 0 {
		return nil, 0
	}
	pages := (len(items) + modelPageSize - 1) / modelPageSize
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * modelPageSize
	end := start + modelPageSize
	if end > len(items) {
		end = len(items)
	}
	rows := make([][]models.InlineKeyboardButton, 0, end-start+1)
	for index := start; index < end; index++ {
		item := items[index]
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         fmt.Sprintf("%d. %s/%s", index+1, item.ProviderLabel(), item.Label()),
			CallbackData: modelPickData(page, index),
		}})
	}
	nav := []models.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, models.InlineKeyboardButton{Text: "← 上一页", CallbackData: modelNavData(page - 1)})
	}
	nav = append(nav, models.InlineKeyboardButton{
		Text: fmt.Sprintf("第 %d/%d 页", page+1, pages), CallbackData: modelNavData(page),
	})
	if page < pages-1 {
		nav = append(nav, models.InlineKeyboardButton{Text: "下一页 →", CallbackData: modelNavData(page + 1)})
	}
	if pages > 1 { // 只有一页就不摆翻页行（一个按不动的按钮只是噪音）
		rows = append(rows, nav)
	}
	return rows, pages
}

// modelPageText：那一页的行文（编号与按钮上的编号对齐 —— 同一套数）。
func modelPageText(items []apiclient.ModelListItem, page, pages int) string {
	if len(items) == 0 {
		return "没有可用模型（渠道还没配 / 模型列表还没刷出来）"
	}
	start := page * modelPageSize
	end := start + modelPageSize
	if end > len(items) {
		end = len(items)
	}
	lines := []string{fmt.Sprintf("模型（共 %d 个，第 %d/%d 页）—— 点一个就切当前会话：", len(items), page+1, pages)}
	for index := start; index < end; index++ {
		item := items[index]
		lines = append(lines, fmt.Sprintf("%d. %s/%s", index+1, item.ProviderLabel(), item.Label()))
	}
	return strings.Join(lines, "\n")
}

// ── callback 数据的形状（前缀 + 编解码）───────────────────────────────
//
// Telegram 的 callback_data 硬上限 64 字节 —— 这里全用短 key / 小整数，
// 长东西（预览文本、会话 id）一律留在内存 pending 表里（见 runner.go）。

const (
	cbConfirm = "confirm:" // 确认一次待办（删会话 / 删消息）
	cbCancel  = "cancel:"  // 撤掉待办
	cbModel   = "model:"   // /model 翻页与选择
)

func confirmData(key string) string { return cbConfirm + key }

func cancelData(key string) string { return cbCancel + key }

// deletionPreviewText：`/cut` 的预览 —— **确认前必须看到的**：会没掉多少、哪条是末尾。
func deletionPreviewText(plan apiclient.DeletionPlan, target string) string {
	text := fmt.Sprintf("删「%s」及之后的全部：\n消息 %d 条、摘要 %d 条",
		target, len(plan.DeletedMessageIDs), len(plan.DeletedSummaryIDs))
	if len(plan.UnlinkedMessageIDs) > 0 || len(plan.UnlinkedSummaryIDs) > 0 {
		text += fmt.Sprintf("\n另有 %d 条消息 / %d 条摘要只被解链（不删）",
			len(plan.UnlinkedMessageIDs), len(plan.UnlinkedSummaryIDs))
	}
	if plan.LastDeletedMessageID != "" {
		text += "\n末尾那条：" + shortID(plan.LastDeletedMessageID)
	}
	return text + "\n\n确认后不可逆。"
}

func modelNavData(page int) string { return cbModel + strconv.Itoa(page) }

func modelPickData(page, index int) string {
	return cbModel + strconv.Itoa(page) + ":" + strconv.Itoa(index)
}

// parseModelData：拆 `model:` 回调 —— 返回页码、全局下标（`index < 0` 表示这是翻页）。
func parseModelData(data string) (page, index int, err error) {
	rest := strings.TrimPrefix(data, cbModel)
	parts := strings.Split(rest, ":")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("model 回调形状不对：%q", data)
	}
	page, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("model 回调页码不是数字：%q", data)
	}
	index = -1
	if len(parts) == 2 {
		index, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("model 回调下标不是数字：%q", data)
		}
	}
	return page, index, nil
}

// clip：按 rune 截短（回调回执 / 按钮文案用，TG 有硬上限）。
func clip(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
