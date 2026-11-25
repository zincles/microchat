import { stashMacros, type MacroMap } from "./macros.ts";

/** 从已拆除世界状态的正文复制 Markdown，而不是从整块 DOM 抓取界面文字。 */
export function copyMarkdownBody(cleaned: string, macros: MacroMap): string {
  const { text, tokens } = stashMacros(cleaned, macros);
  return tokens.length ? text.replace(/\u0000MACRO(\d+)\u0000/g, (raw, index: string) => tokens[Number(index)]?.value ?? raw) : text;
}

export async function writeClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try { await navigator.clipboard.writeText(text); return; } catch { /* 浏览器拒绝时再试同步复制。 */ }
  }
  // 普通 HTTP 没有异步剪贴板。临时选区只包含纯文本，且不会改变编辑器的草稿。
  const previous = document.activeElement;
  const field = document.createElement("textarea");
  field.value = text;
  field.readOnly = true;
  field.style.cssText = "position:fixed;top:0;left:0;opacity:0;pointer-events:none";
  document.body.append(field);
  try {
    field.focus({ preventScroll: true });
    field.select();
    if (!document.execCommand("copy")) throw new Error("浏览器未允许复制");
  } finally {
    field.remove();
    if (previous instanceof HTMLElement) previous.focus({ preventScroll: true });
  }
}
