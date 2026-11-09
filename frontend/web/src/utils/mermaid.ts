// Mermaid 延迟渲染（2026-11-09 用户定：正文里的 ```mermaid 围栏要画出来）。
//
// 分工：
//   - `utils/markdown.ts` 只输出 `.mermaid-figure` 占位（源码可见）；
//   - 这里在 MessageBubble 挂载 / 收流 / 主题变化后，把 `.mermaid-diagram` 换成 SVG。
//
// 两条纪律：
//   · **动态 import**：mermaid 很大，不能压进首屏 bundle；没有 Mermaid 的消息永远不下载它。
//   · **securityLevel: "strict" + htmlLabels: false**：Mermaid 文本来自模型/用户，不能让它
//     产出可执行 HTML；源码在 Markdown 阶段已经转义，这里只从 textContent 取回原文。
type Mermaid = (typeof import("mermaid"))["default"];

let loader: Promise<Mermaid> | null = null;
let configuredTheme = "";
let seq = 0;
const done = new WeakMap<HTMLElement, string>();
const pending = new WeakMap<HTMLElement, string>();

function themeName(): "dark" | "default" {
  const declared = document.documentElement.getAttribute("data-theme");
  if (declared === "dark") return "dark";
  if (declared === "light") return "default";
  // `auto`：Mermaid 没有"跟随 CSS 变量"的现成模式，按系统深浅选一套。
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "default";
}

async function ensureMermaid(theme: "dark" | "default"): Promise<Mermaid> {
  if (!loader) loader = import("mermaid").then((m) => m.default);
  const mermaid = await loader;
  if (configuredTheme !== theme) {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      theme,
      fontFamily: getComputedStyle(document.body).fontFamily,
      flowchart: { htmlLabels: false },
    });
    configuredTheme = theme;
  }
  return mermaid;
}

function errorText(e: unknown): string {
  const text = e instanceof Error ? e.message : String(e);
  return text.split("\n").slice(0, 8).join("\n").slice(0, 500);
}

// renderMermaidIn：只处理 `root` 下还没渲染过的 `.mermaid-figure`。
// `done` 以「主题 + 源码」为键：主题切了要重画，正文没变则重复调用是空转。
export async function renderMermaidIn(root: HTMLElement | null): Promise<void> {
  if (!root) return;
  const figures = Array.from(root.querySelectorAll<HTMLElement>(".mermaid-figure[data-mermaid]"));
  if (!figures.length) return;

  const theme = themeName();
  for (const figure of figures) {
    const sourceEl = figure.querySelector<HTMLElement>(".mermaid-source");
    const diagram = figure.querySelector<HTMLElement>(".mermaid-diagram");
    const status = figure.querySelector<HTMLElement>(".mermaid-status");
    const errorEl = figure.querySelector<HTMLElement>(".mermaid-error");
    const code = sourceEl?.textContent ?? "";
    if (!diagram || !code.trim()) continue;

    const key = `${theme}\u0000${code}`;
    if (done.get(figure) === key || pending.get(figure) === key) continue;
    pending.set(figure, key);
    figure.classList.remove("mermaid-error", "mermaid-rendered");
    if (errorEl) errorEl.textContent = "";
    if (status) status.textContent = "渲染中…";

    try {
      const mermaid = await ensureMermaid(theme);
      const { svg } = await mermaid.render(`mermaid-${Date.now().toString(36)}-${++seq}`, code);
      // 流式期间可能已经换了一版正文；旧结果不要盖到新源码上。
      if (!figure.isConnected || (sourceEl?.textContent ?? "") !== code) continue;
      diagram.innerHTML = svg;
      if (sourceEl) sourceEl.hidden = true;
      if (status) status.textContent = "";
      figure.classList.add("mermaid-rendered");
      done.set(figure, key);
    } catch (e) {
      if (!figure.isConnected || (sourceEl?.textContent ?? "") !== code) continue;
      figure.classList.add("mermaid-error");
      if (status) status.textContent = "渲染失败";
      if (sourceEl) sourceEl.hidden = false;
      if (errorEl) errorEl.textContent = errorText(e);
      done.set(figure, key);
    } finally {
      if (pending.get(figure) === key) pending.delete(figure);
    }
  }
}
