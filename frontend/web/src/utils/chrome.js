// chrome.js —— 滚动隐藏上下栏的**纯决策**（DOM 与事件在 App.vue；这个文件不碰 DOM）。
//
// 用户口径（2026-10-17 定，默认开、只在触屏生效）：
//   往下看（`scrollTop` 增大 = 内容前进）⇒ **顶栏**按位移**逐步**收起，同时把底栏放回来；
//   往回看（`scrollTop` 减小）⇒ **底栏**逐步收起，同时把顶栏放回来。
//   ⇒ 一次只藏一边；进度跟位移**成正比**（不是"过阈值啪一下"，那是开关不是体验）。
// 三条边界：到顶/见底立刻全放回（浏览器也这样）；反向不足 `slop` 像素算抖动、不改方向
// （真手指的滚动是一串正负交替的小步，不滤会把栏抖出重影）；一次跳太远（切会话/贴底）由调用方
// 用 `resetChrome` 处理，不在这里猜。

export const CHROME_SLOP = 6;

// 初始/复位状态。`last` = 上一次看到的 scrollTop（调用方在挂监听时对齐）。
export function resetChrome(scrollTop = 0) {
  return { top: 0, bottom: 0, last: scrollTop, dir: 0, acc: 0 };
}

// stepChrome(state, { dy, scrollTop, headerH, composerH, slop? }) ⇒ 新状态（不修改入参）。
// 返回的 top/bottom 是**已收起的像素**（0 = 完全展开，等于栏高 = 完全收起）。
export function stepChrome(state, { dy, scrollTop, headerH, composerH, slop = CHROME_SLOP }) {
  const next = { ...state, last: scrollTop };
  if (!dy) return next;
  if (scrollTop <= 0) {
    // 到顶：顶栏一定展开；底栏往回让多少就收多少（不是"啪"地全放 —— 那一下很跳）。
    next.top = 0;
    next.bottom = Math.max(0, next.bottom - Math.abs(dy));
    next.dir = 0;
    next.acc = 0;
    return next;
  }
  const dir = dy > 0 ? 1 : -1;
  if (next.dir !== 0 && dir !== next.dir) {
    next.acc += Math.abs(dy);
    if (next.acc < slop) return next; // 反向还没攒够 ⇒ 当抖动
  }
  next.dir = dir;
  next.acc = 0;
  if (dir > 0) {
    // 往下看：顶栏收（最多收到整栏高）、底栏放回
    next.top = Math.min(headerH, next.top + dy);
    next.bottom = Math.max(0, next.bottom - dy);
  } else {
    // 往回看：底栏收、顶栏放回
    next.bottom = Math.min(composerH, next.bottom - dy);
    next.top = Math.max(0, next.top + dy);
  }
  return next;
}
