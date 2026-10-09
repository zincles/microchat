# 网关（Gateway）—— 客户端指纹、会话头、思考回传：实测与口径

结论先行（大方向，AGENTS.md 也留了一份）：**硬闸只有一个：`x-opencode-session`**（每会话稳定 UUID = `sessions.id`，
裸 UUIDv7 零转换）；缓存按**内容前缀**算，与 session id 无关；思考回传用 `reasoning_content`（网关照收）。

## 一、网关按客户端指纹放行（2026-09-29 读源码 + 真日志）

源码（`deepseek-ai/deepseek-harness` 与 `badlogic/pi-mono`，共用请求层 `llm-pi-ai` / `@earendil-works/pi-ai`）：

| 发什么 | 值 / 条件（源码出处） |
|---|---|
| `User-Agent` | `product/version (+url)`，如 `deepseek-harness/0.1.0 (+https://github.com/…)`。注释明说**不许放密钥/路径/会话 id/提示词**（`llm/src/attribution.ts`）|
| `session_id` / `x-client-request-id` / `x-session-affinity` | 都有值 = 同一个**会话 id**；前一个只在 `sessionAffinityFormat === "openai"` 时发（`api/openai-completions.ts:772`）|
| `x-session-id` | OpenRouter 那种格式只发这一个（同上）|
| 体：`stream: true` | 一直是流式 ✓ |
| 体：`stream_options: {include_usage: true}` | 除非该 provider 明确不支持（默认发）—— 流式下要 `usage` 就得要它 |
| 体：`prompt_cache_key` | **只在 `api.openai.com`（或长缓存场景）**发 —— 乱发可能被别的网关拒 |
| 体：`store: false` / `prompt_cache_retention: "24h"` | 看 provider 能力，能支持才发 |
| SDK 指纹 | 它们用官方 `openai` JS SDK ⇒ 线上还有 `x-stainless-*` 那套。**我们复现不了**（版本漂移），要查就抓一次包 |

同一台机器两个请求的实测（OpenCode GO 网关日志）：

| | 被**拒** ✗ | 被**允许** ✓ |
|---|---|---|
| `user-agent` | `node-fetch` | `pi (linux 7.1.8+deb13-amd64; x64)` |
| `accept` | `*/*` | `application/json` |
| `x-opencode-client` | （无）| `pi` |
| `x-opencode-session` | （无）| `01a0e965-…`（UUIDv7 —— 和 `sessions.id` 同构）|
| `content-type` | `application/json` | `application/json` |

**真 key 打过之后结论收窄 —— 硬闸只有一个：`x-opencode-session`**：

| 变体 | 结果 |
|---|---|
| microchat 默认头（`user-agent: microchat/0.1.0` + `x-opencode-client: microchat` + 会话头） | **200** ✓ |
| 逐字装 Pi（`pi (linux …; x64)` + `x-opencode-client: pi` + 会话头） | 200 ✓ |
| **裸库名**（`user-agent: Go-http-client/1.1` + 会话头） | **200** ✓ ← 库名实测**没被拦** |
| 只留 `user-agent` + 会话头（不带 `x-opencode-client`） | 200 ✓ |
| **缺 `x-opencode-session`** | **400 `MissingSessionID`** |

⇒ 官方文档"别用通用库名"目前是要求而非强制（仍照做 —— 迟早真拦）；`x-opencode-client` 也不是必需。
**`/models` 与 `/chat/completions` 都验过（真回话 + 真 usage）**。
usage 真实形状：`{prompt_tokens, completion_tokens, total_tokens, prompt_tokens_details:{}}`（无缓存命中时该对象为空）。
`user-agent` 里 `(linux <release>; x64)` 是客户端自报环境 —— **写死**即可（不必忠实反映本机）。

## 二、缓存与 session id 无关（2026-09-29 omp 17:30–17:36 真日志）

- 同一 session 两次调用 **0 命中**；另铸 session 的第三次反而命中 **7552** tokens ⇒ 缓存按**内容前缀**算，
  session id 只负责**路由/亲和**。所以"给辅助调用另开会话"对缓存**没有影响**（别为缓存去分子会话）。
  **但会话 id 仍是路由/亲和硬要求** ⇒ 会调模型的 Task 照样骑同一个会话 id。
- 真实客户端确实会中途另铸会话（同会话两 session id、同一秒铸造 `01a0ec83…` ⇒ 开了侧会话）。
  **我们仍照 Pi（共用一个）**：两种网关都收，但"一个会话表现为一个 session"更像人类用法。
- **辅助调用（标题/摘要/压缩）用同一个会话 id**（Pi 原话 routing session ID "forwarded **without enabling
  prompt caching**"）。**不要为能力造子 session id**：网关的会话 id 是**路由/亲和键**不是缓存键；
  反过来"一个会话表现为 N 个 session"才像异常流量。
- `"子调用拒绝缓存"` = 请求选项 **`cacheRetention: "none"`**（**不是 header**，Pi 源码 `compaction.ts` 原话
  "Avoid cache writes for one-off summaries"）。实际效果只在体里（且只对支持的 provider）：
  不发 `prompt_cache_key`、不发 `prompt_cache_retention: "24h"`、Anthropic 系 `cache_control` 断点不生成。
  对 OpenCode GO 是 **no-op**（那几个参数本就只对 api.openai.com / Anthropic 系存在）—— **别为此发明 header**。
  **但会话 id 必填**：会调模型的 Task 缺了在 `task.Begin()` 当场报错（**不学 Pi"没有上下文就铸新的"**）；
  只有**不调模型**的 Task（`refresh_models`）才允许没有会话。

## 三、会话头的生命周期（`deepseek-harness` discussion #5495，OpenCode 员工开，含 09/05 硬期限）

- 没带 `x-opencode-session` 直接报错（不是警告）；要求每会话一个稳定 UUID；线上值 = 裸 UUID（我们零转换）。
- 生命周期：跨轮次 / 恢复 / **压缩** / 重试**都不变**；**新会话、Copy** 才换新 id。
  对照我们：重发、编辑、删消息 = 同一会话 ⇒ 同一 id（**我们不做"给子任务另开会话"**：会调模型的 Task 一律骑 `sessions.id`）。
- 归口：**provider 层按 vendor 自动加**（"该由 pi-ai 归一化各家的特殊需求"）；动态会话头**压过**静态同名头；`opencode*` 之外不发。
- 出处：Pi 的 UA（`packages/ai/src/utils/pi-user-agent.ts:18`）、会话头（`providers/opencode-headers.ts`）、
  归因头（`coding-agent/src/core/provider-attribution.ts`）、会话 id（`agent/src/harness/session/session.ts:237`）。
- Pi 的会话 id 模式：`createSessionId() = uuidv7()` 裸、无前缀；只允许 `[A-Za-z0-9._-]` 首尾字母数字；
  载入沿用文件头、fork/分支铸新。⇒ `sessions.id` 逐字节同形，**不用改**。
- 早先 `providers.SessionIDFor(会话 id, 提示词)` 那个 UUIDv5 方案**作废**："一个会话表现为 N 个 session"
  正是滥用监控盯的形状。
- 一个真实分歧：OpenCode GO 上有的模型走 **Anthropic messages** 协议（不是 OpenAI 兼容）——
  我们只做 OpenAI 兼容的话那些模型用不了；真要用得在 `providers` 里再加一种协议适配。

## 四、思考（reasoning）两面（照 Pi；OpenCode GO 已实测字段名）

- **往下（给前端）**：独立增量（`turn.AppendReasoning` ⇒ 「思考中…」动画），落档进 `messages.reasoning`。
- **往上（回传上游）**：assistant 消息带着当时思考一起回传（Pi 原话 `reasoning_details` 是 *replay metadata*，
  不这么做多轮推理就断）。字段名认三种（`reasoning` / `reasoning_content` / `reasoning_text`）；
  **OpenCode GO 实测用 `reasoning_content`**（非流式 `message` 里有、流式 delta 键也是它、历史里带它网关照收 ⇒ 回传安全）。
- DeepSeek 官方"带工具调用必须回传思维链"在这网关上**没被强制**（不回传 / 回传 / 回传空串都是 200）。
  但保险照做：**模型名含 `deepseek` 时** assistant 没思考就补 `reasoning_content: ""`；
  非 deepseek 系**一个字段都不塞**（有些上游见不认识字段 400）。整块关闭：渠道配 `reasoning_field: ""`。
- `reasoning_effort` 下限是 `low`（设 off 也只到 low，仍花 46–198 reasoning tokens ⇒ 关不掉）；
  我们暴露 low/medium/high/max 即可，别假装能关。
- usage 真实三段：`Input` + `Cache read`（prompt 子集）+ `Output`（含 `Reasoning` 子集）⇒ 归一化与卡片脚注按此口径。
