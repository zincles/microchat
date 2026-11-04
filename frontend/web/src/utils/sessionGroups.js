// 会话列表的五档时间分组（2026-11-04 用户定：今天 / 昨天 / 一周内 / 一月内 / 更久）。
//
// 纯函数：时间边界只在这里算一次，组件只按返回的组渲染。`now` 可注入 ⇒ 测试不用等时钟。
// 边界按**本地日历日的零点**算（不是"距现在 24h"）：23:59 与 00:01 只差两分钟，
// 但它们属于两天，分组该跟人看到的日子一致。
export const SESSION_GROUP_LABELS = ["今天", "昨天", "一周内", "一月内", "更久"];

function startOfDay(ms, dayOffset = 0) {
  const date = new Date(ms);
  date.setHours(0, 0, 0, 0);
  date.setDate(date.getDate() + dayOffset);
  return date.getTime();
}

export function groupSessions(sessions, now = Date.now()) {
  const today = startOfDay(now);
  const yesterday = startOfDay(now, -1);
  const week = startOfDay(now, -7);
  const month = startOfDay(now, -30);
  const buckets = SESSION_GROUP_LABELS.map((label, index) => ({
    key: ["today", "yesterday", "week", "month", "older"][index],
    label,
    items: [],
  }));
  for (const session of sessions ?? []) {
    const stamp = Number(session?.updated_at);
    const at = Number.isFinite(stamp) ? stamp : 0;
    const index = at >= today ? 0
      : at >= yesterday ? 1
      : at >= week ? 2
      : at >= month ? 3
      : 4;
    buckets[index].items.push(session);
  }
  return buckets.filter((bucket) => bucket.items.length);
}
