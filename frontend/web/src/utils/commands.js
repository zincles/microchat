// 命令解析（输入框 / 开头用；与网络无关，纯函数）。
// 命令解析："/name arg1 arg2" → {name, args}；非 / 开头 → null。
export function parseCommand(input) {
  if (!input.startsWith("/")) return null;
  const parts = input.slice(1).trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return { name: "", args: [] };
  return { name: parts[0], args: parts.slice(1) };
}

export const COMMANDS = [
  "resume",
  "model",
  "providers",
  "agents",
  "compact",
  "reroll",
  "switch",
  "stop",
  "resend",
  "new",
  "delete",
  "cut",
  "edit",
  "editsum",
];

// 前缀过滤（/ 开头输入用；空名回全部）。
export function filterCommands(name) {
  const q = (name ?? "").toLowerCase();
  return COMMANDS.filter((c) => c.startsWith(q));
}
