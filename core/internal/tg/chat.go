package tg

// 纯文本消息 = **真发一轮**：发进当前会话 → 轮询生成状态 → 节流 edit 同一条消息把回复"长"出来。
//
// 口径与项目其余部分一致：**202 受理 + 轮询**（增量什么都不算、落库只认整段）；
// 同一会话在跑时再发 ⇒ 后端 409（天然不重入，所以不用额外加锁）。
//
// 这一轮跑在 handler 自己的 goroutine 里，别的事务（/stop、/status、别的命令）照常并发跑；
// ctx 取消（bot 停）就优雅退出，绝不停在那死循环 edit。

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"microchat/internal/apiclient"
)

const (
	turnPollInterval = 700 * time.Millisecond // 轮询间隔（口径：202 受理 + 轮询）
	turnEditThrottle = time.Second            // 同一条消息两次 edit 的最小间隔（别刷屏）
	turnTypingEvery  = 5 * time.Second        // 生成中向 TG 报"正在输入"的间隔
	turnMaxDuration  = 10 * time.Minute       // 轮询上限：超了就摆手，别无限 edit
)

// turnPlaceholder：占位消息的初值（受理成功、正文还没来时先摆这一条，之后原地 edit）。
const turnPlaceholder = "生成中…"

// handleText：非斜杠文本 ⇒ 真发一轮（进当前会话）。替换原先的回声验证。
func (r *Runner) handleText(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	session, err := r.currentSession(ctx)
	if err != nil {
		r.say(ctx, b, update, err.Error())
		return
	}
	r.runTurn(ctx, b, update, session.ID)
}

// runTurn：把 update 里那句话发进 sid，再把生成结果"长"出来。
func (r *Runner) runTurn(ctx context.Context, b *bot.Bot, update *models.Update, sid string) {
	chatID := update.Message.Chat.ID
	accepted, err := r.api.SendMessage(sid, update.Message.Text)
	if err != nil {
		if isConflict(err) {
			_ = sendLong(ctx, b, chatID, "上一轮还在跑（/stop 停，或稍等）")
			return
		}
		_ = sendLong(ctx, b, chatID, "没发出去："+err.Error())
		return
	}
	if mid := accepted.Turn.MessageID; mid != nil {
		log.Printf("TG 发了一轮（会话 %s，回复 id %s）", shortID(sid), *mid)
	}
	// 占位消息：先摆"生成中…"，之后的正文全在原地 edit（超长分段时再补发新消息）。
	placeholder, err := sendMarkdown(ctx, b, chatID, turnPlaceholder)
	if err != nil {
		return
	}
	stream := &turnStream{
		chatID:   chatID,
		ids:      []int{placeholder.ID},
		written:  []string{turnPlaceholder},
		lastEdit: make([]time.Time, 1),
	}
	r.pollTurn(ctx, b, stream, sid, accepted.Turn)
}

// pollTurn：每 700ms 拉一次状态与增量，把全文同步到消息上；phase idle ⇒ 终稿落定，error ⇒ 明说。
func (r *Runner) pollTurn(ctx context.Context, b *bot.Bot, stream *turnStream, sid string, accepted apiclient.TurnStatus) {
	from, thinkFrom := 0, 0
	var full, thinking strings.Builder
	lastStatus := accepted
	sawBusy := false
	lastTyping := time.Time{}
	deadline := time.Now().Add(turnMaxDuration)
	ticker := time.NewTicker(turnPollInterval)
	defer ticker.Stop()

	for {
		// 状态与增量各拉一次（TurnText 是游标读 —— 正文与思考各一条游标，累加）。
		if status, err := r.api.TurnStatus(sid); err == nil {
			lastStatus = status
			if status.Busy() {
				sawBusy = true
			}
		}
		chunkDone := false
		if chunk, err := r.api.TurnText(sid, from, thinkFrom); err == nil {
			full.WriteString(chunk.Text)
			thinking.WriteString(chunk.Thinking)
			from, thinkFrom = chunk.Next, chunk.ThinkNext
			chunkDone = chunk.Done
		}

		status := lastStatus
		switch {
		case status.Phase == "error":
			stream.replace(ctx, b, 0, "生成失败："+turnFailureReason(status))
			return
		case !status.Busy() && (sawBusy || chunkDone):
			// 终稿再 sync 一遍（把最后一段 edit 到准；force 绕过节流），再给这次回复挂上 [◀ n/total ▶]。
			stream.sync(ctx, b, turnFinal(full.String()), true)
			r.attachReplyButtons(ctx, stream.chatID, sid, turnTargetID(accepted), stream.ids)
			return
		default:
			stream.sync(ctx, b, turnDisplay(full.String(), thinking.String(), status), false)
		}

		if time.Since(lastTyping) >= turnTypingEvery {
			_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: stream.chatID, Action: models.ChatActionTyping})
			lastTyping = time.Now()
		}
		if time.Now().After(deadline) {
			stream.replace(ctx, b, 0, "还在跑，用 /status 看进度")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// turnDisplay：生成途中摆什么 —— 有正文就摆正文（超长就分段）；只有思考就报字数；都还没有就摆计时。
func turnDisplay(full, thinking string, status apiclient.TurnStatus) []string {
	if strings.TrimSpace(full) != "" {
		return splitMessage(full)
	}
	if strings.TrimSpace(thinking) != "" {
		return []string{"思考中… " + strconv.Itoa(len([]rune(thinking))) + " 字"}
	}
	return []string{fmt.Sprintf("生成中… %.1fs", float64(status.ElapsedMS)/1000)}
}

// turnFinal：这轮结束时的终稿（正文照搬；一段都没有就照实说）。
func turnFinal(full string) []string {
	if strings.TrimSpace(full) != "" {
		return splitMessage(full)
	}
	return []string{"（这轮没有正文）"}
}

// turnTargetID：这一轮回复在库里的消息 id（重摇按钮认它 —— 受理时就定好了；没给就空）。
func turnTargetID(turn apiclient.TurnStatus) string {
	if turn.MessageID == nil {
		return ""
	}
	return *turn.MessageID
}

// turnFailureReason：失败原因（后端没说就照实讲"未知"，别装没事）。
func turnFailureReason(status apiclient.TurnStatus) string {
	if reason := strings.TrimSpace(status.Error); reason != "" {
		return reason
	}
	return "未知原因（/status 看状态）"
}

// isConflict：后端错误里那条 409（同一会话还在跑）。apiclient 把状态码拼在错误串里（`conflict（409）：…`）。
func isConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "（409）")
}

// turnStream：一轮生成的渲染状态 —— 占位消息 + 超长时分出来的后续消息。
//
// 第 0 段永远是占位消息；第 i>0 段各发一条新消息（只发一次，之后 edit 它）。
type turnStream struct {
	chatID   int64
	ids      []int       // 段 → 消息 id
	written  []string    // 段 → 最近一次写进去的文本（没变就不 edit）
	lastEdit []time.Time // 段 → 上次 edit 时刻（节流用）
}

// sync：把 parts 落到各条消息上 —— 新段发新消息，已有段**文本变了才 edit**；
// 非 force 时同一段 1s 内不重复 edit。
func (s *turnStream) sync(ctx context.Context, b *bot.Bot, parts []string, force bool) {
	now := time.Now()
	for index, part := range parts {
		if index >= len(s.ids) {
			message, err := sendMarkdown(ctx, b, s.chatID, part)
			if err != nil {
				return
			}
			s.ids = append(s.ids, message.ID)
			s.written = append(s.written, part)
			s.lastEdit = append(s.lastEdit, now)
			continue
		}
		if s.written[index] == part {
			continue
		}
		if !force && now.Sub(s.lastEdit[index]) < turnEditThrottle {
			continue
		}
		if _, err := editMarkdown(ctx, b, s.chatID, s.ids[index], part, nil); err != nil {
			continue
		}
		s.written[index] = part
		s.lastEdit[index] = now
	}
}

// replace：把某一段换成一句终止文案（失败 / 超时）—— 无视节流，一次到位。
func (s *turnStream) replace(ctx context.Context, b *bot.Bot, index int, text string) {
	if index >= len(s.ids) || s.written[index] == text {
		return
	}
	if _, err := editMarkdown(ctx, b, s.chatID, s.ids[index], text, nil); err != nil {
		return
	}
	s.written[index] = text
	s.lastEdit[index] = time.Now()
}
