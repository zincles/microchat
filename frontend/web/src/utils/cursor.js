// 游标推进（turn/text 轮询用；回退的包直接丢弃，纯函数）。

// 游标推进：next/think_next 只增不减；回退的包直接丢弃。
export function advanceCursor(state, slice) {
  if (slice.next < state.from || slice.think_next < state.thinkFrom) return { ...state, stale: true };
  return { from: slice.next, thinkFrom: slice.think_next, stale: false };
}

export function buildTurnTextPath(sessionID, from, thinkFrom) {
  return `/sessions/${encodeURIComponent(sessionID)}/turn/text?from=${from}&think_from=${thinkFrom}`;
}
