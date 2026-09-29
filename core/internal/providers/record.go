package providers

import "sync"

// 最近一发（内存一份，**覆盖式**）—— `/debug/last-payload` 唯一的证据来源。
//
// 为什么要有它：网关为什么 403 只能靠猜，而拒的往往是**头**不是体（见 AGENTS.md 的实测表）。
var (
	payloadMu   sync.Mutex
	payloadLast *LastPayload
)

// RecordLastPayload：把拼好的那一发记下来（用 `Build` 拼完之后、真发之前调用）。
func RecordLastPayload(payload LastPayload) {
	payloadMu.Lock()
	defer payloadMu.Unlock()
	payloadLast = &payload
}

// LastSent：取最近一发；`ok=false` ⇒ 还没发过（调试页据实回 null，别编）。
func LastSent() (LastPayload, bool) {
	payloadMu.Lock()
	defer payloadMu.Unlock()
	if payloadLast == nil {
		return LastPayload{}, false
	}
	return *payloadLast, true
}
