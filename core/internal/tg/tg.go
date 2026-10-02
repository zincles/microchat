package tg

import (
	"context"
	"strconv"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// tgMaxLen：TG 单条消息上限 4096 字 —— 按 rune 切（中文一个字算一个，不是字节）。
const tgMaxLen = 4096

// splitMessage：超长按 rune 切段（不断半个字；分段数不封顶 —— 调试输出再长也发得出去）。
func splitMessage(text string) []string {
	if utf8.RuneCountInString(text) <= tgMaxLen {
		return []string{text}
	}
	runes := []rune(text)
	parts := []string{}
	for len(runes) > 0 {
		at := tgMaxLen
		if at > len(runes) {
			at = len(runes)
		}
		parts = append(parts, string(runes[:at]))
		runes = runes[at:]
	}
	return parts
}

// sendLong：分段发出（超长才分两段以上；一段就一次发完）。
func sendLong(ctx context.Context, b *bot.Bot, chatID int64, text string) error {
	for _, part := range splitMessage(text) {
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: part}); err != nil {
			return err
		}
	}
	return nil
}

// gateMessage：白名单门卫 —— 未绑定（allowed==0）或 id 对不上 ⇒ 只回 id 方便绑定，别的什么都不干。
// 返回 true = 放行（继续处理），false = 已回绝。
func gateMessage(ctx context.Context, b *bot.Bot, msg *models.Message, allowed int64) bool {
	if msg == nil || msg.From == nil {
		return false
	}
	if allowed != 0 && msg.From.ID == allowed {
		return true
	}
	hint := "你的 TG id 是 `" + itoa(msg.From.ID) + "`，去终端里 `/telegram-bind " + itoa(msg.From.ID) + "` 绑一下（单账户：绑新的自动顶掉旧的）"
	if allowed == 0 {
		hint = "还没绑定（" + hint + "）"
	}
	_ = sendLong(ctx, b, msg.Chat.ID, hint)
	return false
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
