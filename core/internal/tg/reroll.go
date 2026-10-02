package tg

// 每条 bot 回复都挂 [◀] [n/total] [▶] 三个按钮（SillyTavern 的 swipe 那套），
// **只有最近一条回复的按钮有效** —— 新回复落地时旧回复的按钮被就地摘掉，再点只收到"已失效"。
//
// 状态全在内存（`activeReply` 表）：重启就没了 —— 回调认不出就照实说"失效"，
// 不假装还记得（与 pending / 当前会话同一条脾气，不进库）。
//
// 网络调用**一律不持锁**：`replyMu` 只护 map 与字段，Reroll / Switch / Edit 全在锁外跑
// （锁只用来判"这组按钮还作不作数"和翻 busy 旗）。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"microchat/internal/apiclient"
)

const (
	cbReroll = "rr:" // 回复按钮那组回调（prev / noop / next）
	rrPrev   = cbReroll + "prev"
	rrNoop   = cbReroll + "noop"
	rrNext   = cbReroll + "next"

	rerollPollInterval = 700 * time.Millisecond // 重摇轮询间隔（与"真发一轮"同一个口径）
	rerollMaxWait      = 90 * time.Second       // 重摇最长等这么久（超了照实说，不无限转）
)

// replyButtons：**最新一条回复**那组按钮的账 —— 哪条会话、哪条目标回复、挂了哪几段消息、当前第几版。
type replyButtons struct {
	sessionID string
	targetID  string
	segIDs    []int // 这条回复分出来的各段消息 id（每段都挂同一组按钮）
	current   int   // 正在看第几版（1-based）
	total     int   // 一共几版
	busy      bool  // 有一次点击还在处理 —— 防连点把两次网络动作叠在一起
}

// rerollMarkup：一组三个按钮 —— `[◀]` `[n/total]` `[▶]`（callback_data 都是短常量，见 reroll_test.go 钉住）。
func rerollMarkup(current, total int) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "◀", CallbackData: rrPrev},
		{Text: fmt.Sprintf("%d/%d", current, total), CallbackData: rrNoop},
		{Text: "▶", CallbackData: rrNext},
	}}}
}

// attachReplyButtons：一轮回复终稿落地后调 —— 把 `segIDs` 每段都挂上新按钮，并把旧的摘了。
//
//	a. 旧的那组先从登记表摘掉、再逐段清按钮（清不掉只记日志）—— 摘掉之后旧按钮立刻失效；
//	b. 已经在同一个目标的重摇模式里（`Active && TargetMessageID==targetID`）⇒ 用它的位次，
//	   否则 1/1（刚摇完一句、还没重摇过）；
//	c. 每一段都挂同一组按钮。
//
// 全程只记日志、不往回报错 —— 按钮挂不上顶多没得点，正文本身不能受影响。
func (r *Runner) attachReplyButtons(ctx context.Context, chatID int64, sid, targetID string, segIDs []int) {
	if len(segIDs) == 0 {
		return
	}
	// a) 旧的摘下来（灰色按钮不能再点）。
	r.replyMu.Lock()
	if r.activeReply == nil {
		r.activeReply = map[int64]*replyButtons{}
	}
	old := r.activeReply[chatID]
	delete(r.activeReply, chatID)
	r.replyMu.Unlock()
	if old != nil {
		for _, id := range old.segIDs {
			if _, err := r.bot.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
				ChatID: chatID, MessageID: id,
			}); err != nil {
				log.Printf("TG 旧回复按钮没清掉（消息 %d）：%v", id, err)
			}
		}
	}
	// b) 位次：还在这条回复的重摇模式里就照它的，否则 1/1。
	current, total := 1, 1
	if state, err := r.api.RerollStatus(sid); err == nil {
		if state.Active && targetID != "" && state.TargetMessageID == targetID {
			if state.CurrentIdx > 0 {
				current = state.CurrentIdx
			}
			if state.Count > 0 {
				total = state.Count
			}
		}
	} else {
		log.Printf("TG 按钮：重摇状态没问到：%v", err)
	}
	// c) 每段挂上。
	group := &replyButtons{
		sessionID: sid, targetID: targetID,
		segIDs: append([]int{}, segIDs...), current: current, total: total,
	}
	markup := rerollMarkup(current, total)
	r.replyMu.Lock()
	r.activeReply[chatID] = group
	r.replyMu.Unlock()
	for _, id := range segIDs {
		if _, err := r.bot.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID: chatID, MessageID: id, ReplyMarkup: markup,
		}); err != nil {
			log.Printf("TG 回复按钮没挂上（消息 %d）：%v", id, err)
		}
	}
}

// onRerollButton：`rr:` 回调 —— 失效检查 → busy → noop / prev / next。
//
// 只有**最新那条回复**的任一段能点到；旧回复的按钮在新回复落地时就被摘了，点也只收到一句"失效"。
func (r *Runner) onRerollButton(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !r.gateCallback(ctx, b, update) {
		return
	}
	message := callbackMessage(update)
	if message == nil {
		r.answerCallback(ctx, b, update, "这条回调没有消息（旧按钮已失效）", false)
		return
	}
	chatID, messageID := message.Chat.ID, message.ID
	action := strings.TrimPrefix(update.CallbackQuery.Data, cbReroll)

	r.replyMu.Lock()
	group := r.activeReply[chatID]
	if group == nil || !containsInt(group.segIDs, messageID) {
		r.replyMu.Unlock()
		r.answerCallback(ctx, b, update, "旧按钮已失效（只服务最新那条回复）", false)
		return
	}
	if group.busy {
		r.replyMu.Unlock()
		r.answerCallback(ctx, b, update, "上一条点击还在处理", false)
		return
	}
	if action == "noop" {
		r.replyMu.Unlock()
		r.answerCallback(ctx, b, update, "", false) // 静默：中间的 n/total 只是个计数牌
		return
	}
	// 占住 busy 再放锁 —— 后面的网络调用一律不持锁。
	group.busy = true
	sid, current, total := group.sessionID, group.current, group.total
	r.replyMu.Unlock()
	defer func() {
		r.replyMu.Lock()
		group.busy = false
		r.replyMu.Unlock()
	}()

	switch action {
	case "prev":
		if current <= 1 {
			r.answerCallback(ctx, b, update, "当前是第一个备选回复", false)
			return
		}
		if _, err := r.api.RerollSwitch(sid, current-1); err != nil {
			r.answerCallback(ctx, b, update, "切换失败："+err.Error(), false)
			return
		}
		now, all, err := r.refreshReplyButtons(ctx, b, chatID, group)
		if err != nil {
			r.answerCallback(ctx, b, update, err.Error(), false)
			return
		}
		r.answerCallback(ctx, b, update, fmt.Sprintf("已切到第 %d/%d 版", now, all), false)
	case "next":
		if current < total {
			if _, err := r.api.RerollSwitch(sid, current+1); err != nil {
				r.answerCallback(ctx, b, update, "切换失败："+err.Error(), false)
				return
			}
			now, all, err := r.refreshReplyButtons(ctx, b, chatID, group)
			if err != nil {
				r.answerCallback(ctx, b, update, err.Error(), false)
				return
			}
			r.answerCallback(ctx, b, update, fmt.Sprintf("已切到第 %d/%d 版", now, all), false)
			return
		}
		r.rerollNext(ctx, b, update, chatID, sid, group)
	default:
		r.answerCallback(ctx, b, update, "不认识的按钮（"+action+"）", false)
	}
}

// rerollNext：`▶` 已经顶到最右 ⇒ 再摇一版：受理（202）→ 轮询到摇完 → 切到最新那版 → 重画。
func (r *Runner) rerollNext(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64, sid string, group *replyButtons) {
	if _, err := r.api.Reroll(sid); err != nil {
		r.answerCallback(ctx, b, update, err.Error(), false) // 409 / 400 原样说
		return
	}
	state, err := r.waitRerollDone(ctx, sid)
	if err != nil {
		r.answerCallback(ctx, b, update, err.Error(), false)
		return
	}
	latest := state.Count
	if latest < 1 {
		latest = 1
	}
	if _, err := r.api.RerollSwitch(sid, latest); err != nil {
		r.answerCallback(ctx, b, update, "切到新版失败："+err.Error(), false)
		return
	}
	if _, _, err := r.refreshReplyButtons(ctx, b, chatID, group); err != nil {
		r.answerCallback(ctx, b, update, err.Error(), false)
		return
	}
	r.answerCallback(ctx, b, update, fmt.Sprintf("重摇好了（第 %d 版）", latest), false)
}

// waitRerollDone：每 700ms 拉一次状态，摇完（`!Running`）就回最后那份；超时 / 出错把话说清。
func (r *Runner) waitRerollDone(ctx context.Context, sid string) (apiclient.RerollState, error) {
	deadline := time.Now().Add(rerollMaxWait)
	ticker := time.NewTicker(rerollPollInterval)
	defer ticker.Stop()
	for {
		state, err := r.api.RerollStatus(sid)
		if err != nil {
			return apiclient.RerollState{}, fmt.Errorf("重摇状态没问到：%w", err)
		}
		if !state.Running {
			if reason := strings.TrimSpace(state.Error); reason != "" {
				return state, fmt.Errorf("重摇失败：%s", reason)
			}
			return state, nil
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("重摇还没摇完（超过 %s）—— 稍后点 ▶ 再看，或 /status 看进度", rerollMaxWait)
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-ticker.C:
		}
	}
}

// refreshReplyButtons：把当前版次的正文重新落到各段消息上（文本 + 新按钮），段数变了就补发 / 删多余。
//
// 只有 refresh 才 edit（不是每秒刷）：切版 / 摇完各一次，别刷屏。返回新的 位次 / 总数。
func (r *Runner) refreshReplyButtons(ctx context.Context, b *bot.Bot, chatID int64, group *replyButtons) (int, int, error) {
	state, err := r.api.RerollStatus(group.sessionID)
	if err != nil {
		return 0, 0, fmt.Errorf("重摇状态没问到：%w", err)
	}
	if !state.Active || state.TargetMessageID == "" {
		return 0, 0, fmt.Errorf("重摇模式已经退出了（重新发一句话再摇）")
	}
	messages, err := r.api.Messages(group.sessionID)
	if err != nil {
		return 0, 0, fmt.Errorf("消息没取到：%w", err)
	}
	content, found := "", false
	for _, message := range messages {
		if message.ID == state.TargetMessageID {
			content, found = message.Content, true
			break
		}
	}
	if !found {
		return 0, 0, fmt.Errorf("目标消息 %s 在库里找不到（会话变了？）", shortID(state.TargetMessageID))
	}
	current, total := state.CurrentIdx, state.Count
	if current < 1 {
		current = 1
	}
	if total < 1 {
		total = 1
	}
	parts := turnFinal(content)
	markup := rerollMarkup(current, total)

	// 快照段 id（别持锁做网络），顺手把位次写回。
	r.replyMu.Lock()
	if r.activeReply[chatID] != group {
		r.replyMu.Unlock()
		return 0, 0, fmt.Errorf("这条回复已经不在服务里了（只服务最新那条）")
	}
	segIDs := append([]int{}, group.segIDs...)
	group.current, group.total = current, total
	group.targetID = state.TargetMessageID
	r.replyMu.Unlock()

	// 逐段 edit（文本 + 新按钮）；在段数范围内的先原地改。
	for index := 0; index < len(parts) && index < len(segIDs); index++ {
		if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: segIDs[index], Text: parts[index], ReplyMarkup: markup,
		}); err != nil {
			log.Printf("TG 重摇后第 %d 段没 edit 上（消息 %d）：%v", index+1, segIDs[index], err)
		}
	}
	switch {
	case len(parts) < len(segIDs): // 变短：多余的段删掉
		for _, id := range segIDs[len(parts):] {
			if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: id}); err != nil {
				log.Printf("TG 重摇后多余的段没删掉（消息 %d）：%v", id, err)
			}
		}
		segIDs = segIDs[:len(parts)]
	case len(parts) > len(segIDs): // 变长：补发新段（也挂上按钮）
		for _, part := range parts[len(segIDs):] {
			message, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, Text: part, ReplyMarkup: markup,
			})
			if err != nil {
				log.Printf("TG 重摇后补发的段没发出去：%v", err)
				break
			}
			segIDs = append(segIDs, message.ID)
		}
	}

	r.replyMu.Lock()
	if r.activeReply[chatID] == group {
		group.segIDs = segIDs
	}
	r.replyMu.Unlock()
	return current, total, nil
}

// containsInt：小工具（段 id 表里有没有它）。
func containsInt(items []int, want int) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
