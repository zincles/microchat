# 工具调用（Tool Calls）—— 形状、开关、实测

> 这份是**专题参考**：工具相关的全部细节。工程规矩与设计取舍在 `AGENTS.md`；
> **行为一律与 Pi（`badlogic/pi-mono`）保持一致**，下面每条都标了出处或实测记录。

## 一、工具在哪：**不在提示词里**

工具**不是**写进系统提示词的文本，而是**请求体顶层的数组**：

```jsonc
// ① 请求体
{
  "model": "…", "messages": [ … ], "stream": true,
  "tools": [ { "type": "function",
               "function": { "name": "roll_dice", "description": "掷骰子",
                             "parameters": { /* JSON Schema */ },
                             "strict": true } } ],   // ← 只在工具自己声明了才发
  "tool_choice": "auto"                              // ← 原样透传
}
```

（Pi 的系统提示词只有 200 token 左右，正因为工具是结构化注入的 —— 提示词里只讲"你是谁、怎么干活"。）

```jsonc
// ② 模型回话：助手消息里的 tool_calls（arguments 是 **JSON 字符串**，不是对象）
{ "role": "assistant", "content": "",
  "tool_calls": [ { "id": "call_00_…", "type": "function",
                    "function": { "name": "roll_dice", "arguments": "{\"sides\":20}" } } ] }

// ③ 我们回填结果
{ "role": "tool", "content": "{\"result\": 17}", "tool_call_id": "call_00_…",
  "name": "roll_dice" }        // ← 只有要求的上游才带（Pi 的 requiresToolResultName）
```

## 二、我们的开关（渠道级，`providers.json`）

| 字段 | 默认 | 作用 | 对应 Pi 的什么 |
|---|---|---|---|
| `allow_tools` | **关** | 开才把 `tools` / `tool_choice` 原样发给上游 | —— |
| `tool_result_name` | 关 | 工具结果消息里带 `name` | `requiresToolResultName` |

**为什么默认关**：不认识的字段，有些上游**直接 400**；宁可少发，让用户显式打开。
**`strict` 同理**：只有工具自己声明了才发（Pi 的注释："Some reject unknown fields"）。

两条与开关**无关**、必须始终成立的规矩：

1. **历史里出现过工具调用/结果 ⇒ `tools` 参数必须在**（哪怕空数组 `[]`）——
   否则 `role:"tool"` 的结果消息没有对应的调用，上游会直接拒。这正是 Pi 那条 `tools: []` 要防的情况
   （`openai-completions.ts`：`else if (hasToolHistory(context.messages)) params.tools = []`）。
2. **历史里的 `tool_calls` / `tool_call_id` 照旧回放** —— 它们是存档的一部分，与开关无关。

## 三、与 Pi 对齐的清单

| 项 | Pi 的做法（源码出处）| 我们 |
|---|---|---|
| 工具声明形状 | `convertTools`：`{type:"function", function:{name,description,parameters}}`；`strict` 只在支持时 | 同 ✓ |
| `tool_choice` | `options.toolChoice` 原样进体 | 同 ✓（原样透传）|
| 工具结果消息 | `{role:"tool", content, tool_call_id}`；`name` 按 `requiresToolResultName` | 同 ✓ |
| 有工具历史但没给 tools | 发 `tools: []` | 同 ✓（**与开关无关**）|
| 思考回传（带工具调用时）| `requiresReasoningContentOnAssistantMessages`（检测到 DeepSeek 就开）：没有思考时补**空字符串** | 同 ✓（模型名含 `deepseek` ⇒ 补 `""`）|
| OpenCode GO 的思考字段名 | 硬写死：`signature === "reasoning"` ⇒ 改成 `reasoning_content` | 同 ✓（预设里就是它）|

## 四、实测记录（2026-09-29，真 key 打 OpenCode GO）

用**我们自己的 `providers` 代码**（不是手搓 JSON）：

```
① 给工具定义（deepseek-v4.1-flash）⇒ 200 ✓ 模型发起 roll_dice ✓
② 回填调用与结果                  ⇒ 200 ✓ 最终答复用了结果：「掷出了 20 面骰子，结果是 17」✓
③ 同两轮改成流式                   ⇒ 200 ✓ SSE 帧正常 ✓
```

**顺带抓到的真 bug**：`stream_options` **只能跟 `stream: true` 一起发** ——
非流式带上它，网关直接 400：*"stream_options should be set along with stream = true"*（已修 + 测试钉住）。
（教训：先前那些**手搓**的探针都没带这个字段，所以一直没踩到；换了我们自己的代码才现形。）

**模型支持**：工具要挑**支持工具**的模型（`deepseek-v4.1-flash` 可以 ✓）。

## 五、还没做（将来看需要）

- **流式里 `tool_calls` 的分片拼装**：`delta.tool_calls[]` 是**按 index 累加**的（arguments 一段一段来），
  Pi 用 `partial-json` 边收边解析；我们目前只在**非流式**下验证过工具，流式要做时照 Pi 的累加逻辑写 ✓；
- **工具的执行**：那是**上层**的事（谁定义工具、谁执行、结果怎么落档），`providers` 只负责"原样透传"；
- 若将来把工具调用**落档**（现在的 `messages` 表没有工具列），需要一次迁移 + 回放时要保证
  `tool_calls` 与结果消息**成对**出现（否则上游会拒）。
