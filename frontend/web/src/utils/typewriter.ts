// typewriter.js —— 流式文字的"平滑显字"节奏（纯函数，App 的 rAF 循环只读它）。
//
// 节奏由**前端实际观察到的到达间隔**决定（2026-11-03 用户定）：
// 0s 到 100token、0.5s 到 120token、1.2s 到 80token ⇒ 第二块摊 0.5s、第三块摊 0.7s。
// 不按字数猜速度：同一块字数多就打得快、少就打得慢，始终用 `内容 / 到达间隔` 的节奏。
// 第一块没有"上一块" ⇒ 用默认 300ms；间隔异常大时封顶 1200ms（否则一块要打几秒）。
//
// 不在这里做 DOM / 时间：App 每帧拿 elapsed 调 revealCount，纯函数可测。
export const TYPEWRITER_DEFAULT_MS = 300;
export const TYPEWRITER_MIN_MS = 80;
export const TYPEWRITER_MAX_MS = 1200;

// typewriterDuration：这一拍从上一个包到下一个包之间该摊多久（= 实测到达间隔）。
export function typewriterDuration(gapMs?: number): number {
  const n = Number(gapMs);
  if (!Number.isFinite(n) || n <= 0) return TYPEWRITER_DEFAULT_MS;
  return Math.min(TYPEWRITER_MAX_MS, Math.max(TYPEWRITER_MIN_MS, n));
}

// revealCount：从 from 到 to，在 duration 内线性露出多少个字符（含头不含尾）。
// elapsed 超过 duration ⇒ 直接到 to；不四舍五入成小数（slice 只认整数）。
export function revealCount(from: number, to: number, elapsed: number, duration: number): number {
  const a = Number(from) || 0;
  const b = Number(to) || 0;
  if (b <= a) return b;
  if (!(duration > 0)) return b;
  if (!(elapsed > 0)) return a;
  if (elapsed >= duration) return b;
  return a + Math.floor((b - a) * (elapsed / duration));
}
