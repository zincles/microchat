// typewriter.js —— 流式文字的"平滑显字"节奏（纯函数，App 的 rAF 循环只读它）。
//
// 后端每 ~300ms 才吐一块（一次 100 token 这种），直接整块上屏会像"一段一段跳"。
// 前端把新到的一块摊在一个短窗口里逐字放出来：100 字约 300ms、20 字约 180ms、
// 很大的一块最多 500ms（再慢就追不上下一拍了）。
//
// 不在这里做 DOM / 时间：App 每帧拿 elapsed 调 revealCount，纯函数可测。
export const TYPEWRITER_MIN_MS = 180;
export const TYPEWRITER_MAX_MS = 500;
export const TYPEWRITER_MS_PER_CHAR = 3;

// typewriterDuration：这一次补字该摊多久（取正文/思考两边更长的那个）。
export function typewriterDuration(textChars, reasonChars = 0) {
  const n = Math.max(0, Number(textChars) || 0, Number(reasonChars) || 0);
  if (!n) return 0;
  return Math.min(TYPEWRITER_MAX_MS, Math.max(TYPEWRITER_MIN_MS, n * TYPEWRITER_MS_PER_CHAR));
}

// revealCount：从 from 到 to，在 duration 内线性露出多少个字符（含头不含尾）。
// elapsed 超过 duration ⇒ 直接到 to；不四舍五入成小数（slice 只认整数）。
export function revealCount(from, to, elapsed, duration) {
  const a = Number(from) || 0;
  const b = Number(to) || 0;
  if (b <= a) return b;
  if (!(duration > 0)) return b;
  if (!(elapsed > 0)) return a;
  if (elapsed >= duration) return b;
  return a + Math.floor((b - a) * (elapsed / duration));
}
