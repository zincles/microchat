package tg

// 服务端 Markdown 渲染：bot 带 parse_mode 发，对端 TG 客户端画富文本。
//
// 口径：我们自己不主动加任何语法糖 —— 模型吐什么样就原样转义发出（渲染 ≠ 排版）；
// 转义后 TG 仍报 `can't parse entities` ⇒ 同一文本无 parse_mode 纯文本重发一次。
// 回退只认 `can't parse entities` 这个串 —— 429 限流等别的错必须冒泡给上层。

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// escapeMarkdownV2：Telegram MarkdownV2 转义 —— 官方表里的每个特殊字符前加 `\`
// （反斜杠本身也转义，不然原文里的 `\` 会把后面一个字"吃"掉）。
func escapeMarkdownV2(text string) string {
	var out strings.Builder
	out.Grow(len(text) + len(text)/8)
	for _, r := range text {
		switch r {
		case '\\', '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!':
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

// isParseError：是不是 TG 的 entities 解析失败 —— 只认这个串，别的原因不回退。
func isParseError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "can't parse entities")
}

// sendMarkdownWithMarkup：转义后 MarkdownV2 发；parse 失败 ⇒ 原文（不转义）无 parse_mode
// 带原 markup 重发一次。markup 原样透传（按钮不断）。
func sendMarkdownWithMarkup(ctx context.Context, b *bot.Bot, chatID int64, text string, markup models.ReplyMarkup) (*models.Message, error) {
	message, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: escapeMarkdownV2(text), ParseMode: models.ParseModeMarkdown, ReplyMarkup: markup,
	})
	if err != nil && isParseError(err) {
		return b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID, Text: text, ReplyMarkup: markup,
		})
	}
	return message, err
}

// sendMarkdown：无按钮版（分段纯文本 / 占位消息都走这儿）。
func sendMarkdown(ctx context.Context, b *bot.Bot, chatID int64, text string) (*models.Message, error) {
	return sendMarkdownWithMarkup(ctx, b, chatID, text, nil)
}

// editMarkdown：edit 版同 preludes —— 先 MarkdownV2 + markup，
// parse 失败 ⇒ 原文无 parse_mode + markup 重发一次。
func editMarkdown(ctx context.Context, b *bot.Bot, chatID int64, messageID int, text string, markup models.ReplyMarkup) (*models.Message, error) {
	message, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: chatID, MessageID: messageID, Text: escapeMarkdownV2(text),
		ParseMode: models.ParseModeMarkdown, ReplyMarkup: markup,
	})
	if err != nil && isParseError(err) {
		return b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: messageID, Text: text, ReplyMarkup: markup,
		})
	}
	return message, err
}
