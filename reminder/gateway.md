# OpenCode GO 网关：会话头与缓存

## 当前接法

渠道的 endpoint、UA、会话头和思考回传字段由 [providers](../core/internal/providers) 统一处理。对于 OpenCode GO，默认会话头是 `x-opencode-session`；值为当前会话的裸 UUIDv7，聊天、标题、压缩和重摇不另派生。`identity` 可设 `pi`、`microchat` 或 `bare`，渠道覆写优先。请求配置示例不要含真实 key、主机名或会话记录。

`opencode-go` 等渠道仍按实际模型路由选择 Chat Completions / Responses；不把某条探针的字段推广到所有模型。思考留档、文本回放分别受 `store_reasoning`、`chat.replay_reasoning` 控制；Responses 原生推理项还要匹配来源。工具配对见 [工具调用](tool-calls.md)，套餐余量见 [用量](opencode-usage.md)。

## 实测记录：2026-09-29

在当时的 OpenCode GO `/chat/completions` 上，以下结果支持把会话头当作路由要求，而不是把 UA 当作鉴权。这里只记录当时的组合，不保证上游矩阵永远不变。

| 变体 | 结果 |
|---|---|
| MicroChat 或 Pi 身份，带 `x-opencode-session` | 200 |
| `Go-http-client/1.1`，带会话头 | 200 |
| 不带 `x-opencode-client`，仍带会话头 | 200 |
| 不带 `x-opencode-session` | 400 `MissingSessionID` |

同一会话前两次探针缓存命中均为 0；另一会话复用长前缀时命中 7552 tokens。该观察支持使用稳定文本前缀，不给辅助调用造子会话 ID，也不为关闭缓存发明请求头。

当时流式和非流式 Chat 返回的可见思考字段为 `reasoning_content`，历史带该字段请求被接受；在未显式调高档位的探针中，低档仍产生推理 token。历史兼容性不能代替今天的端点实测。


