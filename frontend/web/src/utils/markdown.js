// markdown 渲染（marked：只认成熟库，不手搓解析）+ KaTeX 数学。
//
// 2026-11-01 用户定：「增加 markdown 渲染开关 …… 所有的会被 Markdown 解析的
// 非 <div> HTML 块，我们为每个类型都加个开关。不然瞎解析不好。」
//
// 于是这里从"全量 GFM 一直开着"变成**按类型可关**：
//   - 每一类语法都能单独关掉（清单的权威在后端 `config.MarkdownTypes`，前端不另抄）；
//   - **关掉的含义 = 原样显示**（把 `**粗**` 照字面画出来，而不是悄悄变成"粗"、更不是吃掉标记）；
//   - 没有开关的东西（段落、纯文本）不受影响。
//
// 实现要点：用 `new Marked()` **每次渲染建一个实例**，只给"关掉的那几类"挂渲染器覆盖
// （覆盖体 = 把 `token.raw` 转义后原样吐出）；**开着的类型一个都不覆盖** ⇒ 走 marked 自己的
// 默认渲染。这样我们既不重写一遍渲染器（那必然与上游行为分岔），也不必手搓解析。
//
// XSS：转义**先行**（`&` 与 `<`），所以裸 HTML 永远进不来（`<div>` 之类根本到不了 marked）；
// KaTeX 只吃占位里的原文（trust 关，不产 script）。
import { Marked } from "marked";
import katex from "katex";
import { markdownPrefs, markdownTypeOn, markdownTypes } from "./prefs.js";

// 只转义 `&` 与 `<`：没有 `<` 就造不出标签（XSS 的门在这儿）——**`>` 必须留着**，
// 行首的它正是**引述块**的记号；从前连 `>` 一起转，引述块从来没渲染过（2026-10-11 修）。
function esc(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;");
}

// literalBlock：把一段**块级**原文照字面画出来。
//
// 为什么要 `\n` → `<br>`：`.msg .text` 没有 `white-space: pre-wrap`（见 main.css），
// 不转换的话一个列表会挤成一行 —— 那既不是"渲染了"也不是"原样"。
function literalBlock(raw) {
  return `<p>${esc(String(raw).trim()).replace(/\n/g, "<br>")}</p>`;
}

// literalInline：行内原文（不需要 `<br>`：行内 token 本来就不跨行）。
function literalInline(raw) {
  return esc(String(raw));
}

// underline 扩展：__下划线__ ⇒ <u>…</u>（2026-10-11 用户定）。标准 Markdown 把 `__x__`
// 当 **bold**，但 RP 模型（Discord 味）惯用它表示**下划线** —— 契约（内置段
// `Markdown RolePlay相关`）里明确列的是 `**bold**` 与 `*italics*`，`__x__` 留给下划线。
//
// 写成 marked 的**行内扩展**：只在"该位置真以 __ 开头"时命中，进了反引号就是 code 的地盘
// ⇒ `__init__` 这类代码段**保持原样**（盲替换正则会把它也吃掉 ✗）。内容不吃下划线、不跨行。
const underlineExtension = {
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
    // 关掉 underline ⇒ 原样吐回去（`__x__` 就是 `__x__`）。
    if (!on("underline")) return literalInline(token.raw);
    return `<u>${this.parser.parseInline(token.tokens)}</u>`;
  },
};

// on：某类语法这一刻开不开（渲染器里用；读的是 prefs 的内存缓存）。
// 每次渲染现读 ⇒ 改了开关下一次渲染立刻生效，不用重建整个应用。
let on = () => true;

// 每一类"关掉时要原样吐出"的块级语法 → marked 的渲染器名。
//
// ⚠ 这里的 id 必须与后端 `config.MarkdownTypes` 对得上（多一个 = 永远不触发的死代码，
// 少一个 = 那个开关点了没反应）。`contract.test.js` 对着 Go 源码核这张表。
const BLOCK_TYPES = {
  heading: "heading",
  hr: "hr",
  list: "list",
  blockquote: "blockquote",
  code_block: "code",
  table: "table",
};

// 行内语法 → 渲染器名（这几类都只是"包一层"）。
const INLINE_TYPES = {
  bold: "strong",
  italic: "em",
  strike: "del",
  inline_code: "codespan",
  image: "image",
};

// buildMarked：按当前开关造一个实例。
function buildMarked() {
  const instance = new Marked({
    // `breaks`：单个换行要不要断行。默认开（保持 2026-11-01 之前的观感）。
    breaks: on("breaks"),
    gfm: true,
  });
  instance.use({ extensions: [underlineExtension] });

  const renderer = {};

  // 块级：关掉 ⇒ 整块原文。
  for (const [id, name] of Object.entries(BLOCK_TYPES)) {
    if (on(id)) continue;
    renderer[name] = (token) => literalBlock(token.raw ?? "");
  }

  // 行内：关掉 ⇒ 那几个标记照字面显示。
  for (const [id, name] of Object.entries(INLINE_TYPES)) {
    if (on(id)) continue;
    renderer[name] = (token) => literalInline(token.raw ?? "");
  }

  // 链接要分两族（它们是同一个渲染器）：
  //   - `[名](址)` = 显式链接，归 `link`；
  //   - `<https://…>` 与裸 URL = 自动链接，归 `autolink`。
  // 判据看 `token.raw` 是不是以 `[` 开头 —— marked 对这两族的 token 类型都是 `link`。
  if (!on("link") || !on("autolink")) {
    renderer.link = (token) => {
      const explicit = String(token.raw ?? "").startsWith("[");
      if (explicit ? on("link") : on("autolink")) {
        // 开着的这一族走默认渲染（与不覆盖时同一套行为）。
        const text = token.text ?? "";
        const title = token.title ? ` title="${esc(token.title)}"` : "";
        return `<a href="${esc(token.href ?? "")}"${title}>${text}</a>`;
      }
      return literalInline(token.raw ?? "");
    };
  }

  instance.use({ renderer });
  return instance;
}

// renderMarkdown：正文 → 安全 HTML。
//
// 顺序：抽数学占位 → 全转义 → marked → KaTeX 填回（display `$$…$$` 块，inline `$…$` / `\(…\)`）。
//
// `options.typeOn` 可覆盖"某一类开不开"（测试用；不传就读 prefs）。
// `options.enabled` 可覆盖总开关（同上）。
export function renderMarkdown(text, options = {}) {
  const src = text ?? "";
  const typeOn = options.typeOn ?? markdownTypeOn;
  const masterOn = options.enabled ?? markdownEnabled();
  on = (id) => (masterOn ? typeOn(id) : false);

  // 总开关关着 ⇒ 什么都不解析，整段原文（但换行要留住 —— 见 `literalBlock` 的注释）。
  if (!masterOn) return literalBlock(src);

  // 先抽块级 $$…$$，再抽行内 \(…\) 与 $…$（单 $ 要两边非空，避开 $5 这种）。
  let tmp = src;
  const stash = [];
  // 每条都记下**原始定界符**：关掉数学时要原样吐回去，而 `$a+b$` 与 `\(a+b\)`
  // 是两种写法 —— 换掉形态就不叫"原样"了（一个用户写什么就该看到什么）。
  tmp = tmp.replace(/\$\$([\s\S]+?)\$\$/g, (m, body) => {
    stash.push({ body, display: true, open: "$$", close: "$$" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  tmp = tmp.replace(/\\\((.+?)\\\)/g, (m, body) => {
    stash.push({ body, display: false, open: "\\(", close: "\\)" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  tmp = tmp.replace(/(?<!\S)\$(?!\s)(.+?)(?<!\s)\$(?!\S)/g, (m, body) => {
    stash.push({ body, display: false, open: "$", close: "$" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });

  let html = buildMarked().parse(esc(tmp));

  // KaTeX：`katex` 那一类关掉 ⇒ 原文照字面（连同 `$` 一起显示）。
  const mathOn = on("katex");
  for (let i = 0; i < stash.length; i++) {
    const { body, display, open, close } = stash[i];
    let rendered;
    if (!mathOn) {
      // 原样显示：用它写进来的那一对定界符（`$a$` 别变成 `\(a\)`）。
      rendered = esc(open + body + close);
    } else {
      try {
        rendered = katex.renderToString(body, { displayMode: display, throwOnError: false });
      } catch {
        rendered = esc(body);
      }
    }
    // ⚠ 必须用**函数**替换：`String.replace` 的替换串里 `$$` 是转义序列（代表一个字面 `$`），
    // 直接传字符串会把 `$$x^2$$` 吃成 `$x^2$` —— 不报错，只是内容悄悄少了一半。
    const needle = `\u0000MATH${i}\u0000`;
    html = html.replace(needle, () => rendered);
  }
  return html;
}

// markdownEnabled：总开关（单独抽出来，是为了让 `renderMarkdown` 的缺省值读得到）。
function markdownEnabled() {
  const md = markdownPrefs();
  return md.enabled === undefined || md.enabled === null ? true : !!md.enabled;
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

// markdownTypeIDs：当前后端认识的类型 id（测试与设置界面用来核对）。
export function markdownTypeIDs() {
  return markdownTypes().map((t) => t.id);
}
