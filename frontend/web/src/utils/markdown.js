// markdown 渲染（marked：只认成熟库，不手搓解析）+ KaTeX 数学。
// renderMarkdown：正文 → 安全 HTML。
// 顺序：抽数学占位 → 全转义 → marked → KaTeX 填回（display $\$\$/块，inline $/\(\)）。
// XSS：转义先行，marked 只认结构；KaTeX 只吃占位里的原文（trust 关，不产 script）。
import { marked } from "marked";
import katex from "katex";

marked.setOptions({ breaks: true, gfm: true });

// __下划线__ ⇒ <u>…</u>（2026-10-11 用户定）：标准 Markdown 把 `__x__` 当 **bold**，
// 但 RP 模型（Discord 味）惯用它表示**下划线** —— 契约（内置段 `Markdown RolePlay相关`）里
// 明确列的是 `**bold**` 与 `*italics*`，`__x__` 留给下划线。
// 写成 marked 的**行内扩展**：只在"该位置真以 __ 开头"时命中，进了反引号就是 code 的地盘
// ⇒ `__init__` 这类代码段**保持原样**（盲替换正则会把它也吃掉 ✗）。内容不吃下划线、不跨行。
marked.use({
  extensions: [
    {
      name: "underline",
      level: "inline",
      start(src) {
        const i = src.indexOf("__");
        return i < 0 ? undefined : i;
      },
      tokenizer(src) {
        const m = /^__([^_\n]+?)__/.exec(src);
        if (!m) return undefined;
        return { type: "underline", raw: m[0], tokens: this.lexer.inlineTokens(m[1]) };
      },
      renderer(token) {
        return `<u>${this.parser.parseInline(token.tokens)}</u>`;
      },
    },
  ],
});

// 只转义 `&` 与 `<`：没有 `<` 就造不出标签（XSS 的门在这儿）——**`>` 必须留着**，
// 行首的它正是**引述块**的记号（契约里的 `> OOC`）；从前连 `>` 一起转，引述块从来没渲染过（2026-10-11 修）。
function esc(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
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
