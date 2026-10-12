package chat

import (
	"context"
	"log"

	"microchat/internal/config"
	"microchat/internal/model"
)

// 自动压缩（2026-10-11 用户定）：让对话增长速度靠近 ln(x) 而不是 x。
//
// 策略（用户定的口径，写死在这儿）：
//
//	① 一轮翻 idle 之后看占用（`ContextUsage` 的同一把尺）：到**触发线**（`TriggerTokens`，
//	   缺省 = 预算 × 0.8）就开始；
//	② 一次压 `compact_blocks` 个块（缺省 3 —— 小步快跑，每坨原话只嚼一次）；
//	③ 压完再看：仍 ≥ **退出线**（预算的 50%）就接着压下一批……直到 < 50%、
//	   压不动（无可压缩 / 上游失败）、或这一步没换来任何缩减（防死循环）。
//
// 开/关在 `config.json` 的 `chat.auto_compact`（缺字段 = 开）；关掉就全靠手动 `/compact`。

// autoCompactExitRatio：退出线 = 预算的这个比例（"直到上下文恢复到 50% 以下"）。
const autoCompactExitRatio = 0.5

// MaybeAutoCompact：同步跑一遍自动压缩（调用方决定要不要 `go` —— `run` 里是 go，测试直接调）。
//
// 一轮里最多压多少批？不设数：靠"没缩减就停"+ "无可压缩报错就停"两道闸自然收敛
// （每一批都在啃更老的那一段，啃完自然报无可压缩）。
func (s *Service) MaybeAutoCompact(session model.Session) {
	if s.Compactor == nil {
		return
	}
	cfg, _, _ := s.files()
	if !cfg.AutoCompact {
		return // 默认关（未经验证）：要用的自己在设置里打开
	}
	usage, err := s.ContextUsage(session)
	if err != nil {
		log.Printf("自动压缩：算占用失败，跳过（%v）", err)
		return
	}
	if usage.UsedTokens < usage.TriggerTokens {
		return // 没到触发线：什么都不做
	}
	blocks := cfg.CompactBlocks
	if blocks <= 0 {
		blocks = config.DefaultCompactBlocks
	}
	exit := usage.BudgetTokens / 2
	for usage.UsedTokens >= exit {
		before := usage.UsedTokens
		if err := s.Compactor.Compact(context.Background(), session, blocks); err != nil {
			// 无可压缩 / 另一个压缩在跑 / 上游失败：停 —— **不阻塞任何一轮**，下一轮再来看。
			log.Printf("自动压缩：停了（%d 字时）%v", before, err)
			return
		}
		usage, err = s.ContextUsage(session)
		if err != nil {
			return
		}
		if usage.UsedTokens >= before {
			log.Printf("自动压缩：这一步没有缩减（%d → %d 字），停", before, usage.UsedTokens)
			return
		}
	}
}
