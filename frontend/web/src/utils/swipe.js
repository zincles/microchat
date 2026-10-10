// swipe.js —— 滑动面板（**跟手拖拽**）的纯逻辑；DOM 接线在 App.vue。
//
// 与"甩一下"版不同：跟**手指的移动**走 —— 拖到哪儿面板停哪儿（半开也维持着），
// 松手才按"过半 + 速度"吸附开/关。开合结果仍是 [◧]/[◨] 那一套（同一个 collapsed 类）。
//
// 方向约定：左滑（dx<0）出右栏、右滑（dx>0）出左栏；已有一扇开着 ⇒ **先关后开**：
// 只有"朝它关回去"的方向才认（反方向忽略），关的过程同样跟手。

// classifyAxis：先到先得的轴向锁 —— 越过 slop 后：横占优归 "h"（我们接管），
// 否则归 "v"（放给浏览器滚列表；这一整个手势都不再管）。
export function classifyAxis(dx, dy, slop = 6) {
  if (Math.abs(dx) < slop && Math.abs(dy) < slop) return null;
  return Math.abs(dx) > Math.abs(dy) ? "h" : "v";
}

// pickDrag：这横向手势拖哪扇、从哪个位置起。
//   都没开：朝哪边拖就拖哪扇（base=0，从边缘出来）；
//   有一扇开着：只认"朝它关回去"的方向（base=1，跟手关）；同向忽略（不许两扇同开）。
export function pickDrag({ dx, leftDrawer, rightDrawer, leftOpen, rightOpen }) {
  if (leftOpen) return dx < 0 ? { bar: "left", base: 1 } : null;
  if (rightOpen) return dx > 0 ? { bar: "right", base: 1 } : null;
  if (dx > 0 && leftDrawer) return { bar: "left", base: 0 };
  if (dx < 0 && rightDrawer) return { bar: "right", base: 0 };
  return null;
}

// dragProgress：手指位移 → 0..1 的开度（left 向右为开、right 向左为开）。
export function dragProgress(base, dx, width, bar) {
  const w = width > 0 ? width : 1;
  const p = base + ((bar === "left" ? dx : -dx) / w);
  return Math.min(1, Math.max(0, p));
}

// resolveRelease：松手吸附（三个来源，谁先命中算谁）：
//   ① 快甩（朝开/朝关**都认**）—— flick 小位移也作数；
//   ② 慢拖 —— 过中点算开、没过算关。
//
// ⚠ 两条都是实测修正出来的：
//   · v 的单位是**开度/毫秒**（拖满 335px 的抽屉 ≈ 1 开度；真实手速 0.002–0.01）。
//     曾经写死 0.5（≈2ms 甩完整个抽屉）⇒ "快甩也开"从没生效过，手感发木。
//   · 一开始只有"朝开"的 flick ⇒ **快关手势全落回"必须拖过 60%"**，关闭发涩
//     （2026-10-10 真机反馈："关闭一样的不顺手"）。对称补上"朝关"的 flick。
export function resolveRelease(p, v = 0, { mid = 0.5, flickV = 0.0025, flickAt = 0.06 } = {}) {
  if (v >= flickV && p >= flickAt) return "open";        // 快甩：朝开
  if (v <= -flickV && p <= 1 - flickAt) return "close";  // 快甩：朝关
  return p >= mid ? "open" : "close";                    // 慢拖：中点吸附
}
