// MD3 按下波纹（2026-11-06 用户定：「把按钮波纹做好」）。
//
// 做法是**事件委托**，不给每个按钮挂指令/监听：document 上收 pointerdown，
// 找最近的「可波纹宿主」，在触点位置放一枚 span，动画结束自己删掉。
// 这样新加的按钮/会话行/菜单项自动就有波纹，不必逐个接线。
//
// 三条边界：
//   · 触屏、鼠标左键都算；右键/中键不算；禁用态不算；
//   · `prefers-reduced-motion: reduce` 下**整个不做**（MD3 的 reduce 是不放动画）；
//   · 波纹是 `currentColor` 的半透明圆 —— 主按钮上是 on-primary 的暗波纹，
//     普通按钮上是正文色的淡波纹，不需要每个组件各配一份颜色。
export const RIPPLE_SELECTOR =
  "button, .session-item, .model-item, .agent-item, .palette-item, .picker-item";

// rippleSpec：触点（**视口坐标**）→ 圆的尺寸与相对宿主的左上角。
// 尺寸取宿主长边的 2.2 倍：从触点扩散开能盖满整个控件。
// ⚠ left/top 是**相对宿主**的：x 是 clientX，必须减掉 rect.left，否则圆会画到控件外面
// （被 overflow: hidden 裁掉 ⇒ 看起来像"波纹没生效"）。
export function rippleSpec(rect, x, y) {
  const size = Math.max(rect.width, rect.height) * 2.2 || 40;
  return { size, left: x - rect.left - size / 2, top: y - rect.top - size / 2 };
}

// ensureLayer：给宿主一层**自己的裁剪层**（absolute inset 0 + border-radius: inherit + overflow hidden）。
// 为什么不只靠宿主自己的 overflow？按钮/列表行在部分浏览器或某些样式组合下，绝对定位的
// 子元素会逃出宿主的圆角裁剪（实测用户看到波纹溢到按钮外）；多这一层是确定性的兜底。
function ensureLayer(host) {
  let layer = host.querySelector(":scope > .md-ripple-layer");
  if (!layer) {
    layer = document.createElement("span");
    layer.className = "md-ripple-layer";
    layer.setAttribute("aria-hidden", "true");
    host.appendChild(layer);
  }
  return layer;
}

function spawn(host, clientX, clientY) {
  host.classList.add("md-ripple-host");
  const layer = ensureLayer(host);
  // 坐标以**裁剪层**为准（inset: 0，等于宿主的 padding box）：宿主有边框时也不会偏。
  const rect = layer.getBoundingClientRect();
  if (!rect.width || !rect.height) return;
  // 同一个宿主上只留最新的一枚（快速连点不叠成一坨）。
  const old = layer.querySelector(".md-ripple");
  if (old) old.remove();
  const spec = rippleSpec(rect, clientX, clientY);
  const dot = document.createElement("span");
  dot.className = "md-ripple";
  dot.setAttribute("aria-hidden", "true");
  dot.style.width = `${spec.size}px`;
  dot.style.height = `${spec.size}px`;
  dot.style.left = `${spec.left}px`;
  dot.style.top = `${spec.top}px`;
  dot.addEventListener("animationend", () => dot.remove(), { once: true });
  layer.appendChild(dot);
}

// installRipple：在 main.js 挂载后调用一次。
export function installRipple(root = typeof document !== "undefined" ? document : null) {
  if (!root || root.__mdRippleInstalled) return;
  root.__mdRippleInstalled = true;
  if (typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;

  root.addEventListener("pointerdown", (e) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    const host = e.target?.closest?.(RIPPLE_SELECTOR);
    if (!host || host.disabled || host.getAttribute("aria-disabled") === "true") return;
    if (host.closest("input, textarea, select")) return;
    spawn(host, e.clientX, e.clientY);
  }, { passive: true });

  // 键盘激活（Enter / Space）没有触点：从控件中心放一枚，行为与 MD3 一致。
  root.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const host = e.target?.closest?.(RIPPLE_SELECTOR);
    if (!host || host.disabled) return;
    const rect = host.getBoundingClientRect();
    spawn(host, rect.left + rect.width / 2, rect.top + rect.height / 2);
  });
}
