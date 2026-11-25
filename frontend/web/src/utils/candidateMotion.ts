// 只动画已换版的正文；行锚、按钮焦点与摘要展开态不重建。
export function animateCandidate(el: HTMLElement | null, next?: number, previous?: number): void {
  if (!el?.animate || !next || !previous || next === previous ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  const offset = next > previous ? 8 : -8;
  el.animate([{ opacity: 0.65, transform: `translateX(${offset}px)` }, { opacity: 1, transform: "none" }],
    { duration: 160, easing: "ease-out" });
}
