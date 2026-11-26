// 会话列表的五档时间分组（2026-11-04 用户定：今天 / 昨天 / 一周内 / 一月内 / 更久）。
//
// 纯函数：时间边界只在这里算一次，组件只按返回的组渲染。`now` 可注入 ⇒ 测试不用等时钟。
// 边界按**本地日历日的零点**算（不是"距现在 24h"）：23:59 与 00:01 只差两分钟，
// 但它们属于两天，分组该跟人看到的日子一致。
import { sessionTitle } from "./format.ts";

export const SESSION_GROUP_LABELS = ["收藏", "今天", "昨天", "一周内", "一月内", "更久"];

export interface SessionLike {
  updated_at?: number | string | null;
  favorite?: boolean;
}

export interface SessionGroup<T> {
  key: string;
  label: string;
  items: T[];
}

export function filterSessionTitles<T extends { title?: string | null }>(sessions: T[], query: string): T[] {
  const needle = query.trim().toLowerCase();
  return needle ? sessions.filter((s) => sessionTitle(s.title).toLowerCase().includes(needle)) : sessions;
}

function startOfDay(ms: number, dayOffset = 0): number {
  const date = new Date(ms);
  date.setHours(0, 0, 0, 0);
  date.setDate(date.getDate() + dayOffset);
  return date.getTime();
}

export function groupSessions<T extends SessionLike>(sessions: T[] | null | undefined, now = Date.now()): SessionGroup<T>[] {
  const today = startOfDay(now);
  const yesterday = startOfDay(now, -1);
  const week = startOfDay(now, -7);
  const month = startOfDay(now, -30);
  const favoriteItems: T[] = [];
  const buckets: SessionGroup<T>[] = SESSION_GROUP_LABELS.slice(1).map((label, index) => ({
    key: ["today", "yesterday", "week", "month", "older"][index] ?? "older",
    label,
    items: [],
  }));
  for (const session of sessions ?? []) {
    if (session.favorite) {
      favoriteItems.push(session);
      continue;
    }
    const stamp = Number(session?.updated_at);
    const at = Number.isFinite(stamp) ? stamp : 0;
    const index = at >= today ? 0 : at >= yesterday ? 1 : at >= week ? 2 : at >= month ? 3 : 4;
    const bucket = buckets[index];
    if (bucket) bucket.items.push(session);
  }
  const groups: SessionGroup<T>[] = favoriteItems.length
    ? [{ key: "favorite", label: "收藏", items: favoriteItems }, ...buckets]
    : buckets;
  return groups.filter((bucket) => bucket.items.length);
}
