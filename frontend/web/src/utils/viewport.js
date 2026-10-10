// viewport.js —— 移动端键盘遮挡的规避：**以视觉视口（visualViewport）为准**拟合整个应用盒。
//
// 机理：#app 高 = vv.height、再 translateY(vv.offsetTop)（iOS 把可视窗"推上去"时跟着走）。
// #app 一带 transform，它就成了其内所有 `fixed` 浮层（命令面板/picker/确认框/toast/连接门板）
// 的**包含块** ⇒ 浮层自动钉在**键盘上沿**，一处不用各自再算。
// `interactive-widget=resizes-content`（index.html 的 meta）让 Chromium 系直接缩布局视口，
// 这里是给 iOS Safari 与不支持的浏览器兜底 —— 两边叠着也不打架（都以 vv 为准）。
//
// 捏合缩放（scale > 1.05）时不拟合：那时 vv 是缩放后的视口，再拟合只会把布局搅乱。
//
// ⚠ 必须配合**文档锁死**（main.css 的 `html, body { overflow: hidden }`）：文档可滚时
// "滚动 → offsetTop 变大 → 再平移 → transform 撑大可滚动区域"会正反馈成真机上的
// "页面还能延展、往上滑不完"（2026-10-10 踩过）。这里的平移只该跟 iOS 的键盘上推走。

// computeViewportFit：纯函数（好钉）—— 给 {height, offsetTop, scale} 算 {h, y}；
// 拿不到有效高（或不该拟合）⇒ null（调用方把变量撤掉，CSS 回落 100dvh）。
export function computeViewportFit({ height, offsetTop = 0, scale = 1 } = {}) {
  if (!Number.isFinite(height) || height <= 0) return null;
  if (scale > 1.05) return null;
  return { h: Math.round(height), y: Math.max(0, Math.round(offsetTop || 0)) };
}

// installViewportFit：挂上监听并立即拟合一次；返回卸载函数（App 卸载时调）。
export function installViewportFit() {
  const vv = typeof window !== "undefined" ? window.visualViewport : null;
  if (!vv) return () => {};
  const root = document.documentElement;
  const apply = () => {
    const fit = computeViewportFit({ height: vv.height, offsetTop: vv.offsetTop, scale: vv.scale });
    if (!fit) {
      root.style.removeProperty("--vvh");
      root.style.removeProperty("--vvt");
      return;
    }
    root.style.setProperty("--vvh", `${fit.h}px`);
    root.style.setProperty("--vvt", `${fit.y}px`);
  };
  apply();
  vv.addEventListener("resize", apply);
  vv.addEventListener("scroll", apply);
  return () => {
    vv.removeEventListener("resize", apply);
    vv.removeEventListener("scroll", apply);
  };
}
