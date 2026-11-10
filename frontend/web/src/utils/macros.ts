// 只读替换宏的前端展示层。
//
// 存储永远是 `{{user_name}}` 原文；这里只在**渲染**时换成 chip。后端出站时用同语义
// （Go 的 internal/macro）换成纯文本，两边都不回写消息。
//
// 用占位符再还原，而不是直接把宏替换成 HTML：正文必须先过 marked 的转义门，
// 否则用户名称里的 `<` 就是 XSS。占位符也不会被 markdown/KaTeX 吃掉。

export type MacroMap = Record<string, string>;

export interface MacroToken {
  name: string;
  value: string;
}

const simpleMacro = /\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}/g;

export function escapeMacroHtml(value: unknown): string {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// stashMacros：已知宏 → 占位符；未知宏原样留着（后续 marked 会把它当普通文本）。
export function stashMacros(text: string, values?: MacroMap | null): { text: string; tokens: MacroToken[] } {
  const tokens: MacroToken[] = [];
  if (!text || !values) return { text, tokens };
  const next = text.replace(simpleMacro, (match, rawName: string) => {
    const name = String(rawName).trim();
    if (!(name in values)) return match;
    const token = `\u0000MACRO${tokens.length}\u0000`;
    tokens.push({ name, value: values[name] });
    return token;
  });
  return { text: next, tokens };
}

// restoreMacros：占位符 → 安全的 chip；悬停 title 保留原始宏。
export function restoreMacros(html: string, tokens: MacroToken[]): string {
  if (!tokens.length) return html;
  let out = html;
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    const label = escapeMacroHtml(token.value);
    const original = escapeMacroHtml(`{{${token.name}}}`);
    const chip = `<span class="macro-chip" data-macro="${original}" title="原始宏：${original}">${label}</span>`;
    out = out.replace(`\u0000MACRO${i}\u0000`, () => chip);
  }
  return out;
}

// macroMap：把后端/前端当前值摊平成别名表；与 Go `macro.Values` 同口径。
export function macroMap(userName: string, agentName: string, model = ""): MacroMap {
  const user = String(userName ?? "").trim() || "你";
  const agent = String(agentName ?? "").trim() || "助手";
  return {
    user_name: user,
    user,
    char_name: agent,
    char: agent,
    agent_name: agent,
    model: String(model ?? "").trim(),
  };
}
