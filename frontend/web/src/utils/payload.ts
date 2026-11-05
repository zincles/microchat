// 载荷（wire：{method,url,headers,body}）→ "分块视图"的数据（用户点名 2026-10-11：
// 与其读裸 JSON，不如把 body.messages 一块块画出来）。
//
// **只认形状、不猜语义**：body 不是对象 / 没有 messages 数组 ⇒ null（调用方退回裸 JSON）。
// 字段：role/content 是骨架；name / reasoning_content / tool_call_id / tool_calls 见着就带上
// （工具调用那套形状见 reminder/tool-calls.md —— 一个都不许吞）。

export interface WireLike {
  method?: unknown;
  url?: unknown;
  body?: unknown;
}

export interface ParsedWireMessage {
  role: string;
  name: string;
  content: string;
  chars: number;
  reasoning: string;
  toolCallId: string;
  toolCalls: string;
}

export interface ParsedWire {
  meta: {
    method: string;
    url: string;
    model: string;
    stream: boolean;
    tools: number;
  };
  messages: ParsedWireMessage[];
}

// chars：按 **rune** 数（CJK 一个字算一个 —— 与界面别处的"N 字"同一口径）。
function runeLen(s: string): number {
  return [...s].length;
}

// previewBlock：折叠态的**单行预览**（前 n 个 rune + 省略号；换行压成空格，单行显示）。
export function previewBlock(content: unknown, n = 90): string {
  const flat = String(content ?? "").replace(/\s+/g, " ").trim();
  const runes = [...flat];
  return runes.length > n ? runes.slice(0, n).join("") + "…" : flat;
}

export function parseWireMessages(wire: WireLike | null | undefined): ParsedWire | null {
  const body = wire?.body;
  if (!body || typeof body !== "object") return null;
  const b = body as Record<string, unknown>;
  if (!Array.isArray(b.messages)) return null;
  return {
    meta: {
      method: typeof wire?.method === "string" ? wire.method : "",
      url: typeof wire?.url === "string" ? wire.url : "",
      model: typeof b.model === "string" ? b.model : "",
      stream: b.stream === true,
      tools: Array.isArray(b.tools) ? b.tools.length : 0,
    },
    messages: b.messages.map((raw): ParsedWireMessage => {
      const m: Record<string, unknown> = raw !== null && typeof raw === "object" ? raw : {};
      const rawContent = m.content;
      const content = typeof rawContent === "string" ? rawContent : rawContent == null ? "" : (JSON.stringify(rawContent) ?? "");
      const rawCalls = Array.isArray(m.tool_calls) ? m.tool_calls : [];
      return {
        role: typeof m.role === "string" ? m.role : "?",
        name: typeof m.name === "string" ? m.name : "",
        content,
        chars: runeLen(content),
        reasoning: typeof m.reasoning_content === "string" ? m.reasoning_content : "",
        toolCallId: typeof m.tool_call_id === "string" ? m.tool_call_id : "",
        toolCalls: rawCalls
          .map((t) => {
            const call: Record<string, unknown> = t !== null && typeof t === "object" ? t : {};
            const fn = call.function;
            const name = fn !== null && typeof fn === "object" ? (fn as Record<string, unknown>).name : undefined;
            return typeof name === "string" ? name : "?";
          })
          .join("、"),
      };
    }),
  };
}
