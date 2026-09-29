package providers

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

// sessionNamespace：microchat 的固定命名空间（UUIDv5 用）。**永不改** ——
// 改了它，所有派生的会话 id 都会变，网关那边的路由/亲和就全断了。
var sessionNamespace = uuid.MustParse("6f1c6a58-3a1e-4a53-9d3f-2b8f4c1d7e90")

// SessionIDFor：**这条请求该带哪个会话 id** —— 一条规则，覆盖将来所有新玩法。
//
// 规则（用户 2026-09-29 定）：
//
//	**系统提示词一致的，在同一个会话里共用一个 session id。**
//	· 与主提示词一致（主聊天、以及任何同提示词的扩展）⇒ 就用 `conversations.id`（与 Pi 同构：一个会话一个 id）；
//	· 提示词不同（摘要/标题/判断/子 Agent…，将来任何新玩意）⇒ **由 `(会话 id, 提示词)` 确定性派生**一个 UUIDv5。
//
// 为什么不许随机铸 id（Pi 在"没有会话上下文"时就是这么干的，我们不学这一步）：
// 网关拿会话 id 做**路由与提示词缓存亲和**；每次换一个新 id ⇒ 那份亲和完全没用，
// 而且一个会话对网关表现成"每次都是新会话"，正是他们滥用监控盯的形状。
//
// 派生是**无状态、可重现**的：后端重启、换机器、重放同一段文本，算出来都是同一个 id。
func SessionIDFor(conversationID, systemPrompt, mainSystemPrompt string) string {
	if systemPrompt == mainSystemPrompt {
		return conversationID // 与主线同提示词 ⇒ 共用会话 id（Pi 的形状）
	}
	material := conversationID + "\x00" + systemPrompt
	digest := sha256.Sum256([]byte(material))
	// UUIDv5（name-based）：同样的 (会话, 提示词) 永远得到同一个 id。
	return uuid.NewSHA1(sessionNamespace, []byte(hex.EncodeToString(digest[:16]))).String()
}
