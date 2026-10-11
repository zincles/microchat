// 载荷（wire：{method,url,headers,body}）→ "分块视图"的数据（用户点名 2026-10-11：
// 与其读裸 JSON，不如把 body.messages 一块块画出来）。
//
// **只认形状、不猜语义**：body 不是对象 / 没有 messages 数组 ⇒ null（调用方退回裸 JSON）。
// 字段：role/content 是骨架；name / reasoning_content / tool_call_id / tool_calls 见着就带上
// （工具调用那套形状见 reminder/tool-calls.md —— 一个都不许吞）。

// chars：按 **rune** 数（CJK 一个字算一个 —— 与界面别处的"N 字"同一口径）。
function runeLen(s) {
  return [...s].length;
}

export function parseWireMessages(wire) {
  const body = wire?.body;
  if (!body || typeof body !== "object") return null;
  if (!Array.isArray(body.messages)) return null;
  return {
    meta: {
      method: typeof wire.method === "string" ? wire.method : "",
      url: typeof wire.url === "string" ? wire.url : "",
      model: typeof body.model === "string" ? body.model : "",
      stream: body.stream === true,
      tools: Array.isArray(body.tools) ? body.tools.length : 0,
    },
    messages: body.messages.map((m) => {
      const raw = m?.content;
      const content = typeof raw === "string" ? raw : raw == null ? "" : JSON.stringify(raw);
      return {
        role: typeof m?.role === "string" ? m.role : "?",
        name: typeof m?.name === "string" ? m.name : "",
        content,
        chars: runeLen(content),
        reasoning: typeof m?.reasoning_content === "string" ? m.reasoning_content : "",
        toolCallId: typeof m?.tool_call_id === "string" ? m.tool_call_id : "",
        toolCalls: Array.isArray(m?.tool_calls)
          ? m.tool_calls.map((t) => t?.function?.name ?? "?").join("、")
          : "",
      };
    }),
  };
}
