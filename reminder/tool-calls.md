# 工具调用：声明、执行和回放

这是协议专题。项目边界见 [AGENTS.md](../AGENTS.md)，消息与调用记录的区别见 [DEFINE.md](../DEFINE.md)。

## 当前实现

工具以请求体的结构化 `tools` 数组声明，不把签名写进提示词。`model_define_rules` 的 `server_tools` / `local_tools` 是候选；[chat/tool_loop.go](../core/internal/chat/tool_loop.go) 结合实际协议、执行渠道和会话的 `native_tools_disabled` 选择，同一份结果供预演和真发。不要把旧的渠道 `allow_tools` 当成本地工具授权入口。

- **服务端工具**由上游执行。xAI 的 `web_search`、`x_search`、`code_execution` 走 Responses；OpenAI 的通用 `web_search` 声明也限定 Responses，不能照抄到普通 Chat Completions。
- **本地工具**目前是 DeepSeek Search/Fetch 的 `web_search`、`web_fetch`。DeepSeek 自己可执行，其他渠道可通过 `local_tool_provider` 选择 DeepSeek 执行渠道，并按本地工具规则启用。普通对话最多 4 轮工具回填，每轮可有多个调用；执行失败回结构化错误，取消结束本轮。

| 协议 | 函数声明 | 调用与回填 |
|---|---|---|
| Chat Completions | `tools[].function` 下有 `name`、`parameters` | `assistant.tool_calls[].id` 对应 `role:"tool"` 的 `tool_call_id`，arguments 是 JSON 字符串。 |
| Responses | `tools[]` 的 `type:"function"`、`name`、`parameters` 同层 | 按 `output_index` 收集输出；用 `function_call.call_id` 配对 `function_call_output.call_id`，不要用 Responses 项的 `id`。 |

Go SDK 编码与解码在 [sdk_chat.go](../core/internal/providers/sdk_chat.go)、[sdk_responses.go](../core/internal/providers/sdk_responses.go)。Schema 属性按 `json.RawMessage` 保留原值；把 `minimum` / `maximum` 从数值变成字符串曾让 xAI 返回 400。流式与非流式都不能换掉这条规则。

## 留档与界面

`message_tools` 记录真实调用、结果、来源和消息内 `seq`；`exchanges` 保存一次上游请求的有序输出项。历史回放以当前出站正文为准，同渠道、端点、协议、模型才复用可回放的 Responses 原生项（包括受支持的托管工具和加密推理）；跨来源退回通用正文与配对的本地调用/结果。不要从会话今天的模型猜过去的协议。

实时工具位置来自流里实际观察到的正文位置。历史若能确认有序输出的正文与存档一致，可用输出项导出原文 UTF-8 字节偏移；否则使用可靠的已存位置或只在「查看过程」列出。终帧的排序不能把已显示的搜索调用拖到后面。工具不占消息序号，搜索来源随消息保留。

## 验证记录

- **2026-11-09，xAI `grok-4.7`。** Responses 接受 `web_search`、`x_search`、`code_execution`；Chat Completions 对它们回 422，但接受客户端 `function`。已废弃 `live_search` 在 Chat 端返回 410。Responses 不接受 `web_fetch`。X 搜索和附件搜索可在 `custom_tool_call` 下用 `xs_`、`as_` 形式标识托管项，见 [xAI 工具记录](https://docs.x.ai/developers/tools/tool-usage-details#using-responses-api)。
- **2026-11-10，DeepSeek。** 独立 Search/Fetch 返回结果与来源；Responses 原生 `web_search` 当时只在 v4-pro 生效，未返回 sources，flash 可能输出字面工具标签。两者不要混为一套接口。
- **2026-11-25，`f628b46`。** 真实 xAI Grok 流式发起函数调用，收到本地搜索的合成回填后生成最终回复。两段摘要合并后，下一轮能回忆父摘要里的事实，并从原始正文现演世界状态。不是 OpenAI 官方端点的 `store:false` 证明。
- 本地回归：[两种协议工具往返](../core/internal/chat/tool_loop_test.go) `TestSDKToolRoundTrip`，[数值 Schema](../core/internal/providers/sdk_contract_test.go) `TestFunctionSchemaPreservesJSONNumbers`，[工具计量和摘要合并](../core/internal/chat/autocompact_test.go) `TestContextUsageFollowsToolHistoryThroughSummaryMerge`。


