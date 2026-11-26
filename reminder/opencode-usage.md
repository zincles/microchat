# OpenCode GO：套餐余量

套餐余量不是某次生成的 token 用量，也不是 Zen 的按量余额。MicroChat 的 `GET /api/v1/providers/{provider_id}/usage` 只用于 `vendor=opencode-go` 的渠道；`-debug usage [provider_id]` 读同一实现。[providers/opencode_usage.go](../core/internal/providers/opencode_usage.go) 是超时、解析和错误的权威。

## 接口形状

- 上游为 `GET https://opencode.ai/zen/go/v1/usage`，使用渠道的 Bearer 凭据。路径不带尾斜杠；会话头不是前提。
- `usage` 下有 `rolling`、`weekly`、`monthly`，分别对应滚动 5 小时、每周、每月。每个窗口的 `percent` 是该窗口的已用百分比，不是剩余比例；`resetsAt` 是重置时刻。套餐名是本地标签，上游不提供剩余美元金额。
- 三个窗口不齐或字段畸形则报错，不展示猜出的剩余额度。用量接口只读，不修改任务或历史。

## 实测记录：2026-09-30

当时仅带 `Authorization` 返回 200；缺失/错误 key 返回 401，尾斜杠版本也回 401，误拼路径返回站点 HTML 404。观察到有效的 `status: "ok"`、百分比和重置时刻。`rate-limited` 与小数百分比由本地回归覆盖，不是当天实测；未用 Zen 订阅验证其用量接口。

官方窗口说明见 [OpenCode GO](https://opencode.ai/docs/go/)。可重复的本地测试见 [opencode_usage_test.go](../core/internal/providers/opencode_usage_test.go)；其中联网测试需要明确的环境密钥，不在日常文档验收时运行。

