// markdown 渲染（marked：只认成熟库，不手搓解析）+ KaTeX 数学。
// renderMarkdown：正文 → 安全 HTML。
// 顺序：抽数学占位 → 全转义 → marked → KaTeX 填回（display $\$\$/块，inline $/\(\)）。
// XSS：转义先行，marked 只认结构；KaTeX 只吃占位里的原文（trust 关，不产 script）。
import { marked } from "marked";
import katex from "katex";

marked.setOptions({ breaks: true, gfm: true });

function esc(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export function renderMarkdown(text) {
  const src = text ?? "";
  // 先抽块级 $$…$$，再抽行内 \(…\) 与 $…$（单 $ 要两边非空，避开 $5 这种）。
  let tmp = src;
  const stash = [];
  tmp = tmp.replace(/\$\$([\s\S]+?)\$\$/g, (m, body) => {
    stash.push({ body, display: true });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  tmp = tmp.replace(/\\\((.+?)\\\)/g, (m, body) => {
    stash.push({ body, display: false });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  tmp = tmp.replace(/(?<!\S)\$(?!\s)(.+?)(?<!\s)\$(?!\S)/g, (m, body) => {
    stash.push({ body, display: false });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  let html = marked.parse(esc(tmp));
  for (let i = 0; i < stash.length; i++) {
    const { body, display } = stash[i];
    let rendered;
    try {
      rendered = katex.renderToString(body, { displayMode: display, throwOnError: false });
    } catch {
      rendered = esc(body);
    }
    html = html.replace(`\u0000MATH${i}\u0000`, rendered);
  }
  return html;
}

// splitState：把 <state…>…</state> / <current_state…>…</current_state> 摘出来，
// 回 [{table, current, lines}]；说话部分原样回（markdown 走它）。
// 表名：无名 ⇒ global（与后端 DefaultTable 同口径）。
export function splitState(text) {
  const blocks = [];
  const re = /<(state|current_state)([^>]*)>([\s\S]*?)<\/\1\s*>/gi;
  let m;
  let cleaned = text ?? "";
  while ((m = re.exec(text ?? "")) !== null) {
    const tag = m[1].toLowerCase();
    const table = (m[2] ?? "").trim() || "global";
    const lines = m[3]
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean);
    blocks.push({ table, current: tag === "current_state", lines });
  }
  cleaned = (text ?? "").replace(re, "").trim();
  return { cleaned, blocks };
}
