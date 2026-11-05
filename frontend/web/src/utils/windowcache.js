// 会话窗口缓存（2026-11-05 用户定：「切换不同对话时，最好也预留缓存吧，这样切换不会太慢」）。
//
// 存的是"上一屏"：消息行 + 段结构 + 窗口起点。切回看过的会话时**先把这屏原样画出来**
// （0 RTT），再在后台对一次账 —— 远端 1s+ 时，骨架不会再把熟悉的对话遮住一秒。
//
// 三条纪律：
//   · **只缓存窗口**，不缓存流式内容 / 展开的摘要原文（那些是活的，且可能很大）；
//   · 存/取都做浅拷贝：调用方 push 一条乐观消息不该改到缓存里的快照；
//   · 有上限（LRU 顺序）：会话再多也不把浏览器内存吃穿。命中时"摸一下"让它重新排到最新。
//
// Map 的插入顺序就是 LRU 顺序（ES2015 起 Map 保证按插入遍历）。

export const WINDOW_CACHE_MAX = 8;
// 单条会话最多缓存这么多消息行：翻过上百页的长会话就别缓存了（宁可切回来再拉一次，
// 也不要把几 MB 正文挂在内存里，更不能只存后半段 —— 那会让窗口起点与实际内容对不上）。
export const WINDOW_CACHE_ROWS_MAX = 400;

// rememberWindow：记下一条会话的窗口快照（覆盖旧快照并排到最新）。
export function rememberWindow(cache, id, snapshot) {
  if (!cache || !id || !snapshot) return;
  const rows = Array.isArray(snapshot.rows) ? snapshot.rows : [];
  cache.delete(id);
  if (rows.length > WINDOW_CACHE_ROWS_MAX) return;
  cache.set(id, {
    rows: rows.slice(),
    segments: Array.isArray(snapshot.segments) ? snapshot.segments.slice() : [],
    windowFrom: snapshot.windowFrom ?? 1,
  });
  while (cache.size > WINDOW_CACHE_MAX) {
    const oldest = cache.keys().next().value;
    cache.delete(oldest);
  }
}

// recallWindow：取一条会话的窗口快照；没有 ⇒ null。命中会把它排到最新（LRU 触碰）。
// 返回的是**副本**（rows 再拷一层）—— 调用方拿它当当前消息列用，怎么改都不脏缓存。
export function recallWindow(cache, id) {
  const cached = cache?.get?.(id);
  if (!cached) return null;
  cache.delete(id);
  cache.set(id, cached);
  return {
    rows: cached.rows.slice(),
    segments: cached.segments.slice(),
    windowFrom: cached.windowFrom,
  };
}

// forgetWindow：会话删了就把它那一份丢掉（否则会占着 LRU 名额，直到被挤出去）。
export function forgetWindow(cache, id) {
  if (cache && id) cache.delete(id);
}
