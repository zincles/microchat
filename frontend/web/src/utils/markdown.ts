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
import type { RendererObject, TokenizerAndRendererExtension, Tokens } from "marked";
import katex from "katex";
import { markdownPrefs, markdownTypeOn, markdownTypes } from "./prefs.ts";
import { restoreMacros, stashMacros, type MacroMap } from "./macros.ts";

// 只转义 `&` 与 `<`：没有 `<` 就造不出标签（XSS 的门在这儿）——**`>` 必须留着**，
// 行首的它正是**引述块**的记号；从前连 `>` 一起转，引述块从来没渲染过（2026-10-11 修）。
function esc(s: unknown): string {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;");
}

// literalBlock：把一段**块级**原文照字面画出来。
//
// 为什么要 `\n` → `<br>`：`.msg .text` 没有 `white-space: pre-wrap`（见 main.css），
// 不转换的话一个列表会挤成一行 —— 那既不是"渲染了"也不是"原样"。
function literalBlock(raw: unknown): string {
  return `<p>${esc(String(raw).trim()).replace(/\n/g, "<br>")}</p>`;
}

// literalInline：行内原文（不需要 `<br>`：行内 token 本来就不跨行）。
function literalInline(raw: unknown): string {
  return esc(String(raw));
}

// fencedCode：普通围栏代码块（Mermaid 关掉 / 代码块关掉时的兜底）。
function fencedCode(code: string, lang = "mermaid"): string {
  return `<pre><code class="language-${esc(lang)}">${esc(code)}</code></pre>`;
}

// mermaidFigure：Mermaid 的**延迟渲染占位**。正文先输出源码（没有 JS / 正在流式时也能读），
// MessageBubble 挂载/收流后由 utils/mermaid.ts 把 `.mermaid-diagram` 换成 SVG，再把源码收起来。
// 源码在 HTML 里已经 `esc` 过；Mermaid 只在浏览器里从 `textContent` 取回原文。
function mermaidFigure(code: string): string {
  return `<div class="mermaid-figure" data-mermaid>`
    + `<div class="mermaid-head"><span class="mermaid-label">Mermaid</span><span class="mermaid-status">待渲染</span></div>`
    + `<div class="mermaid-diagram"></div>`
    + `<pre class="mermaid-source"><code>${esc(code)}</code></pre>`
    + `<div class="mermaid-error" role="alert"></div>`
    + `</div>`;
}

// underline 扩展：__下划线__ ⇒ <u>…</u>（2026-10-11 用户定）。标准 Markdown 把 `__x__`
// 当 **bold**，但 RP 模型（Discord 味）惯用它表示**下划线** —— 契约（内置段
// `Markdown RolePlay相关`）里明确列的是 `**bold**` 与 `*italics*`，`__x__` 留给下划线。
//
// 写成 marked 的**行内扩展**：只在"该位置真以 __ 开头"时命中，进了反引号就是 code 的地盘
// ⇒ `__init__` 这类代码段**保持原样**（盲替换正则会把它也吃掉 ✗）。内容不吃下划线、不跨行。
// strongCJK：CommonMark 的 flanking 规则会让「都**“引号”**」解析不出粗体
// （`**` 前是汉字、后是标点，既不算左 flanking 也不算右 flanking）——中文写作里很常见。
// 只接住「开 ** 紧跟着中文引号/括号」这一种形状，其余 bold 仍交给 marked 默认解析。
const strongCJKExtension: TokenizerAndRendererExtension = {
  name: "strongCJK",
  level: "inline",
  start(src: string): number | undefined {
    const m = /\*\*(?=[“”‘’「」『』《》〈〉（）【】])/.exec(src);
    return m ? m.index : undefined;
  },
  tokenizer(src: string) {
    const m = /^\*\*(?=[“”‘’「」『』《》〈〉（）【】])([^\n]*?[^\s])\*\*/.exec(src);
    if (!m) return undefined;
    return { type: "strongCJK", raw: m[0], tokens: this.lexer.inlineTokens(m[1]) };
  },
  renderer(token) {
    if (!on("bold")) return literalInline(token.raw);
    return `<strong>${this.parser.parseInline(token.tokens ?? [])}</strong>`;
  },
};

const underlineExtension: TokenizerAndRendererExtension = {
  name: "underline",
  level: "inline",
  start(src: string): number | undefined {
    const i = src.indexOf("__");
    return i < 0 ? undefined : i;
  },
  tokenizer(src: string) {
    const m = /^__([^_\n]+?)__/.exec(src);
    if (!m) return undefined;
    return { type: "underline", raw: m[0], tokens: this.lexer.inlineTokens(m[1]) };
  },
  renderer(token) {
    // 关掉 underline ⇒ 原样吐回去（`__x__` 就是 `__x__`）。
    if (!on("underline")) return literalInline(token.raw);
    return `<u>${this.parser.parseInline(token.tokens ?? [])}</u>`;
  },
};

// on：某类语法这一刻开不开（渲染器里用；读的是 prefs 的内存缓存）。
// 每次渲染现读 ⇒ 改了开关下一次渲染立刻生效，不用重建整个应用。
type TypeOn = (id: string) => boolean;
let on: TypeOn = () => true;

// 每一类"关掉时要原样吐出"的块级语法 → marked 的渲染器名。
//
// ⚠ 这里的 id 必须与后端 `config.MarkdownTypes` 对得上（多一个 = 永远不触发的死代码，
// 少一个 = 那个开关点了没反应）。`contract.test.js` 对着 Go 源码核这张表。
const BLOCK_TYPES: Record<string, string> = {
  heading: "heading",
  hr: "hr",
  list: "list",
  blockquote: "blockquote",
  code_block: "code",
  table: "table",
};

// 行内语法 → 渲染器名（这几类都只是"包一层"）。
const INLINE_TYPES: Record<string, string> = {
  bold: "strong",
  italic: "em",
  strike: "del",
  inline_code: "codespan",
  image: "image",
};

// buildMarked：按当前开关造一个实例。
function buildMarked(): Marked {
  const instance = new Marked({
    // `breaks`：单个换行要不要断行。默认开（保持 2026-11-01 之前的观感）。
    breaks: on("breaks"),
    gfm: true,
  });
  instance.use({ extensions: [underlineExtension, strongCJKExtension] });

  type AnyRenderer = (token: Tokens.Generic) => string;
  const renderer: Record<string, AnyRenderer> = {};

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
      const link = token as unknown as Tokens.Link;
      const explicit = String(link.raw ?? "").startsWith("[");
      if (explicit ? on("link") : on("autolink")) {
        // 开着的这一族走默认渲染（与不覆盖时同一套行为）。
        const text = link.text ?? "";
        const title = link.title ? ` title="${esc(link.title)}"` : "";
        return `<a href="${esc(link.href ?? "")}"${title}>${text}</a>`;
      }
      return literalInline(link.raw ?? "");
    };
  }

  instance.use({ renderer: renderer as RendererObject });
  return instance;
}

export interface RenderOptions {
  typeOn?: TypeOn;
  enabled?: boolean;
  // 只读替换宏；不给就不替换（编辑器/内部调试）。
  macros?: MacroMap | null;
}

// renderMarkdown：正文 → 安全 HTML。
//
// 顺序：抽数学占位 → 全转义 → marked → KaTeX 填回（display `$$…$$` 块，inline `$…$` / `\(…\)`）。
//
// `options.typeOn` 可覆盖"某一类开不开"（测试用；不传就读 prefs）。
// `options.enabled` 可覆盖总开关（同上）。
export function renderMarkdown(text?: string | null, options: RenderOptions = {}): string {
  const src = text ?? "";
  const macroStash = stashMacros(src, options.macros);
  const source = macroStash.text;
  const typeOn = options.typeOn ?? markdownTypeOn;
  const masterOn = options.enabled ?? markdownEnabled();
  on = (id) => (masterOn ? typeOn(id) : false);

  // 总开关关着 ⇒ 什么都不解析，整段原文（但换行要留住 —— 见 `literalBlock` 的注释）。
  if (!masterOn) return restoreMacros(literalBlock(source), macroStash.tokens);

  // **先把代码保护起来再找数学定界符**（2026-11-03）：模型会写「用 `$$` 包裹」这种说明，
  // 其中反引号里的 `$$` 不是块级公式的开头。不保护的话它会把后续所有 `$$` 的配对整体错位，
  // 最后一大段 Markdown/正文被当成一条畸形的 KaTeX 公式 ⇒ 整块 `.katex-error` 红字。
  // 代码块/行内代码换成占位符，抽完数学后原样还原，再交给 marked 解析。
  let tmp = source;

  // Mermaid 围栏**先摘出来**：它不该参与 KaTeX 的 `$` 配对，也不该被 marked 当普通代码块；
  // 解析完再替换成延迟渲染的占位。关闭 mermaid / code_block 时在最后退化成普通代码块或原文。
  const mermaidStash: Array<{ code: string; fence: string }> = [];
  tmp = tmp.replace(/(^|\n)(```|~~~)[ \t]*mermaid[^\n]*\r?\n([\s\S]*?)\r?\n\2[ \t]*(?=\n|$)/gi,
    (_m: string, lead: string, fence: string, body: string) => {
      mermaidStash.push({ code: body.replace(/\r\n/g, "\n").trim(), fence });
      return `${lead}\u0000MERMAID${mermaidStash.length - 1}\u0000`;
    });

  const codeStash: string[] = [];
  const protectCode = (raw: string): string => {
    codeStash.push(raw);
    return `\u0000CODE${codeStash.length - 1}\u0000`;
  };
  tmp = tmp.replace(/(^|\n)(```[\s\S]*?```|~~~[\s\S]*?~~~)(?=\n|$)/g,
    (_m: string, lead: string, block: string) => lead + protectCode(block));
  tmp = tmp.replace(/(`+)([^`]*?)\1/g, (m: string) => protectCode(m));
  // ⚠ **不把"缩进 4 格"当代码块**（2026-11-07 用户报）：列表项里的续行也常是 4 格缩进，
  // 把它们先当代码 stash 会把列表里的 $$…$$ 整块吞进 <pre><code>，KaTeX 永远收不到。
  // 缩进代码块交给 marked 自己判断；围栏代码块与行内代码已在上面保护。
  interface MathStash {
    body: string;
    display: boolean;
    open: string;
    close: string;
  }
  const stash: MathStash[] = [];
  // 每条都记下**原始定界符**：关掉数学时要原样吐回去，而 `$a+b$` 与 `\(a+b\)`
  // 是两种写法 —— 换掉形态就不叫"原样"了（一个用户写什么就该看到什么）。
  tmp = tmp.replace(/\$\$([\s\S]+?)\$\$/g, (_m: string, body: string) => {
    stash.push({ body, display: true, open: "$$", close: "$$" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  // LaTeX 的 display 定界符 `\[...\]`：DeepSeek 等模型默认就写这个，不是 `$$`。
  // 它和 `$$…$$` 等价（都是 displayMode），必须一起抽，否则整段会以字面 `\[` 露出来。
  tmp = tmp.replace(/\\\[([\s\S]+?)\\\]/g, (_m: string, body: string) => {
    stash.push({ body, display: true, open: "\\[", close: "\\]" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  tmp = tmp.replace(/\\\((.+?)\\\)/g, (_m: string, body: string) => {
    stash.push({ body, display: false, open: "\\(", close: "\\)" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  // 行内 $…$：**闭合 `$` 后面允许紧跟中文标点**（`$u(x)\to 0$，` / `$A-B$）`）。
  // 旧规则要求闭合后是空白，于是中文里的 `$…$，` 永远配不上，解析器会一路吃到下一个 `$`，
  // 把两句话之间的正文当成一条畸形公式涂红（2026-11-03 用户报的红色块）。
  tmp = tmp.replace(/(?<![\\$])\$(?!\$|\s)([^$\n]+?)(?<!\s)\$(?!\$)/g, (_m: string, body: string) => {
    stash.push({ body, display: false, open: "$", close: "$" });
    return `\u0000MATH${stash.length - 1}\u0000`;
  });
  // 数学抽完再把代码原文放回去，交给 marked 按正常代码块/行内代码解析。
  tmp = tmp.replace(/\u0000CODE(\d+)\u0000/g, (_m: string, index: string) => codeStash[Number(index)] ?? "");

  let html = buildMarked().parse(esc(tmp), { async: false });

  // KaTeX：`katex` 那一类关掉 ⇒ 原文照字面（连同 `$` 一起显示）。
  const mathOn = on("katex");
  for (let i = 0; i < stash.length; i++) {
    const { body, display, open, close } = stash[i];
    let rendered: string;
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

  // Mermaid 占位：段首的 `<p>\0MERMAIDn\0</p>` 换成块级图；嵌在别处的退化成原位替换。
  const mermaidOn = on("mermaid");
  const codeBlockOn = on("code_block");
  for (let i = 0; i < mermaidStash.length; i++) {
    const { code, fence } = mermaidStash[i];
    let rendered: string;
    if (!codeBlockOn) rendered = literalBlock(`${fence}mermaid\n${code}\n${fence}`);
    else if (!mermaidOn) rendered = fencedCode(code);
    else rendered = mermaidFigure(code);
    const needle = `\u0000MERMAID${i}\u0000`;
    const wrapped = `<p>${needle}</p>`;
    html = html.includes(wrapped)
      ? html.replace(wrapped, () => rendered)
      : html.replace(needle, () => rendered);
  }
  return restoreMacros(html, macroStash.tokens);
}

// markdownEnabled：总开关（单独抽出来，是为了让 `renderMarkdown` 的缺省值读得到）。
function markdownEnabled(): boolean {
  const md = markdownPrefs();
  return md.enabled === undefined || md.enabled === null ? true : !!md.enabled;
}

export interface StateBlock {
  table: string;
  current: boolean;
  lines: string[];
}

// splitState：把 <state…>…</state> / <current_state…>…</current_state> 摘出来，
// 回 [{table, current, lines}]；说话部分原样回（markdown 走它）。
// 表名：无名 ⇒ global（与后端 DefaultTable 同口径）。
export function splitState(text?: string | null): { cleaned: string; blocks: StateBlock[] } {
  const source = text ?? "";
  const blocks: StateBlock[] = [];
  const re = /<(state|current_state)([^>]*)>([\s\S]*?)<\/\1\s*>/gi;
  let m: RegExpExecArray | null;
  while ((m = re.exec(source)) !== null) {
    const tag = m[1].toLowerCase();
    const table = (m[2] ?? "").trim() || "global";
    const lines = m[3]
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean);
    blocks.push({ table, current: tag === "current_state", lines });
  }
  const cleaned = source.replace(re, "").trim();
  return { cleaned, blocks };
}

// markdownTypeIDs：当前后端认识的类型 id（测试与设置界面用来核对）。
export function markdownTypeIDs(): string[] {
  return markdownTypes().map((t) => t.id);
}
