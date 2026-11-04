package providers

import (
	"context"
	"sync"
)

// endpointSlots：按渠道 id 的全局并发闸门。高级压缩的作业并发只是"本作业最多几个"，
// 这里才是**同一家 endpoint 同时最多几个**（2026-11-05 加）。
//
// 0 = 不限；正数 = 该渠道的同时活跃请求上限。所有走 Client.Chat / Client.Complete 的
// 真网络请求都经过它（dummy/本地假上游不占槽，因为它不联网）。
var (
	slotMu     sync.Mutex
	slotChans  = map[string]chan struct{}{}
	slotLimits = map[string]int{}
)

func acquireEndpoint(ctx context.Context, p Provider) (func(), error) {
	limit := p.MaxConcurrency
	if limit <= 0 {
		return func() {}, nil
	}
	key := p.ID
	if key == "" {
		key = p.BaseURL
	}
	if key == "" {
		return func() {}, nil
	}
	slotMu.Lock()
	if slotLimits[key] != limit {
		slotChans[key] = make(chan struct{}, limit)
		slotLimits[key] = limit
	}
	slots := slotChans[key]
	slotMu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
