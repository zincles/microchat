// 会话消息窗口：取数、分页、摘要展开、回看缓存与悬停预取。
//
// App 只保留“会话状态机”（进哪条、发什么、流怎么走）；这里只回答一件事：
// **当前会话要显示哪一屏**。取数只按消息段拉（被摘要盖住的原文一个字节都不取），
// 摘要卡展开是另一条按需路径。口径与纯算法在 utils/chatrows.ts。
import { nextTick, reactive, ref, shallowRef, watch } from "vue";
import type { Ref } from "vue";
import type { Message, Summary } from "../api/types.ts";
import {
  alignWindowFrom, computeSegments, prevWindowStart, runsToFetch, seededMissingRuns,
  toolSources, windowStart, type ChatSegment, type ChatSummary, type ChatSummaryEntry,
} from "../utils/chatrows.ts";
import { rememberWindow, type WindowSnapshot } from "../utils/windowcache.ts";
import { applyReadingPosition, atReadingTail, captureReadingPosition, readingPaneVisible, type ReadingPosition } from "../utils/readingPosition.ts";
import type { AppRow } from "../types/app.ts";

interface WindowApi {
  listSummaries(id: string): Promise<Summary[]>;
  listMessagesLast(id: string, n: number): Promise<Message[]>;
  messageRanges(id: string, ranges: Array<[number, number]>): Promise<Message[]>;
  listMessagesRange(id: string, from: number, to: number): Promise<Message[]>;
}

export interface SessionWindowDeps {
  api: WindowApi;
  toast: (text: string, kind?: string) => void;
  getSessionID: () => string | null;
  getEpoch: () => number;
  isDraft: () => boolean;
  getLocalRev: () => number;
  pane: Readonly<Ref<HTMLElement | null>>;
  /** 后台对账前再问一次：等待期间人动过消息列吗？ */
  canReconcile?: () => boolean;
  onWindowLoaded?: () => void;
}

export interface SessionWindow {
  vueMessages: Ref<AppRow[]>;
  vueSegments: Ref<ChatSegment[]>;
  loadingSession: Ref<boolean>;
  loadingMore: Ref<boolean>;
  windowLimit: Ref<number>;
  windowFrom: Ref<number>;
  summaryMsgs: Map<string, ChatSummaryEntry>;
  summaryErrors: Map<string, string>;
  openSummaries: Set<string>;
  windowCache: Map<string, WindowSnapshot<AppRow, ChatSegment>>;
  toRow: (m: Message) => AppRow;
  fetchWindow: (id: string, opts?: { from?: number | null; limit?: number }) => Promise<WindowSnapshot<AppRow, ChatSegment>>;
  loadWindow: (opts?: { reset?: boolean; resume?: boolean; scroll?: boolean; onlyIfUnchanged?: boolean }) => Promise<boolean>;
  reloadWindow: (opts?: { scroll?: boolean; onlyIfUnchanged?: boolean }) => Promise<boolean>;
  reloadWindowKeepingScroll: (opts?: { onlyIfUnchanged?: boolean }) => Promise<boolean>;
  rememberReadingPosition: () => void;
  restoreReadingPosition: () => void;
  forgetReadingPosition: (id: string) => void;
  awayFromLatest: Readonly<Ref<boolean>>;
  hasNewReply: Readonly<Ref<boolean>>;
  onReadingScroll: () => void;
  jumpToLatest: () => void;
  resetReadingTail: () => void;
  noteNewReply: () => void;
  loadMore: () => Promise<void>;
  resetWindowRequests: () => void;
  prefetchSession: (id: string) => void;
  refreshSummaryMsgs: (id: string) => Promise<void>;
  onSummaryToggle: (id: string, open: boolean) => void;
}

const PREFETCH_MAX_ACTIVE = 3;
const PREFETCH_FRESH_MS = 30_000;

/** 库里的消息 → 消息列的一行（展示形状）。 */
export function toRow(m: Message): AppRow {
  return {
    who: m.role === "user" ? "你" : "助手",
    kind: m.kind || "chat",
    status: m.status || "done",
    run_id: m.run_id,
    tools_snapshot: m.tools_snapshot || [],
    tools: m.tools || [],
    sources: toolSources(m.tools),
    idx: m.idx,
    role: m.role,
    content: m.content,
    reasoning: m.reasoning,
    reasoningMs: m.reasoning_ms,
    durationMs: m.duration_ms ?? null,
    messageId: m.id,
    id: m.id,
    usage: m.usage ?? null,
  };
}

/** runsToFetch 的 Infinity 尾段 → 批量接口的 to=0。 */
function batchRanges(runs: Array<[number, number]>): Array<[number, number]> {
  return runs.map(([a, b]) => [a, b === Infinity ? 0 : b]);
}

function lastReplyID(rows: AppRow[]): string {
  return rows.findLast(row => row.role === "assistant" && row.messageId)?.messageId ?? "";
}

export function useSessionWindow({
  api, toast, getSessionID, getEpoch, getLocalRev, isDraft, pane, canReconcile, onWindowLoaded,
}: SessionWindowDeps): SessionWindow {
  const vueMessages = ref<AppRow[]>([]);
  const vueSegments = ref<ChatSegment[]>([]);
  const loadingSession = ref(true);
  const loadingMore = shallowRef(false);
  let pagingEpoch: number | null = null;
  const windowLimit = ref(5);
  const windowFrom = ref(1);
  const summaryMsgs = reactive<Map<string, ChatSummaryEntry>>(new Map());
  const summaryErrors = reactive(new Map<string, string>());
  const summaryRequests = new Map<string, symbol>();
  const openSummaries = new Set<string>();
  const windowCache = new Map<string, WindowSnapshot<AppRow, ChatSegment>>();
  const tailInflight = new Map<string, Promise<WindowSnapshot<AppRow, ChatSegment>>>();
  const prefetching = new Set<string>();
  const prefetchedAt = new Map<string, number>();
  let prefetchActive = 0;
  // 文本窗口按 LRU 淘汰；轻量书签另存，旧窗口没命中时仍能从读过的那一页取数。
  const bookmarks = new Map<string, { position: ReadingPosition; windowFrom: number; replyID: string; hasNewReply: boolean }>();
  let restoringEpoch: number | null = null;
  let forgottenEpoch: number | null = null;
  const awayFromLatest = shallowRef(false);
  const hasNewReply = shallowRef(false);
  let lastScrollTop = 0;

  function resetWindowRequests(): void {
    pagingEpoch = null;
    loadingMore.value = false;
    summaryErrors.clear();
    summaryRequests.clear();
  }

  function syncTail(el: HTMLElement): void {
    lastScrollTop = el.scrollTop;
    awayFromLatest.value = !atReadingTail(el);
    if (!awayFromLatest.value) hasNewReply.value = false;
  }

  function moveTo(el: HTMLElement | null, position: ReadingPosition): void {
    if (!readingPaneVisible(el)) return;
    applyReadingPosition(el, position);
    syncTail(el);
  }

  function resetReadingTail(): void {
    awayFromLatest.value = false;
    hasNewReply.value = false;
    lastScrollTop = 0;
  }

  function measurePane(): HTMLElement | null {
    const el = pane.value;
    return !loadingSession.value && restoringEpoch !== getEpoch() && readingPaneVisible(el) ? el : null;
  }

  function sampleScroll(el: HTMLElement): void {
    // 自己写的 scrollTop 也会触发 scroll；后续内容增长不能把这一拍误判成向上翻。
    if (el.scrollTop === lastScrollTop) return;
    if (awayFromLatest.value || el.scrollTop < lastScrollTop) syncTail(el);
    else lastScrollTop = el.scrollTop;
  }

  function onReadingScroll(): void {
    const el = measurePane();
    if (el) sampleScroll(el);
  }

  function jumpToLatest(): void {
    moveTo(pane.value, { kind: "bottom" });
  }

  function noteNewReply(): void {
    onReadingScroll();
    if (awayFromLatest.value) hasNewReply.value = true;
  }

  watch(pane, (el, _old, onCleanup) => {
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (measurePane() !== el) return;
      sampleScroll(el);
      // 正文换行、图片加载、输入栏长高及键盘缩窗都走同一条路，不跟每个流式字符抢布局。
      if (!awayFromLatest.value) {
        if (el.scrollHeight - el.clientHeight - el.scrollTop > 1) jumpToLatest();
      } else if (atReadingTail(el)) syncTail(el);
    });
    observer.observe(el);
    if (el.firstElementChild) observer.observe(el.firstElementChild, { box: "border-box" });
    onCleanup(() => observer.disconnect());
  }, { flush: "post" });

  function rememberReadingPosition(): void {
    const sid = getSessionID();
    if (!sid || loadingSession.value || restoringEpoch === getEpoch() || forgottenEpoch === getEpoch()) return;
    const position = captureReadingPosition(pane.value);
    if (position) bookmarks.set(sid, {
      position, windowFrom: windowFrom.value, replyID: lastReplyID(vueMessages.value), hasNewReply: hasNewReply.value,
    });
  }

  function restoreReadingPosition(): void {
    const sid = getSessionID();
    const my = getEpoch();
    if (!sid || loadingSession.value) return;
    restoringEpoch = my;
    void nextTick(() => {
      if (my !== getEpoch() || sid !== getSessionID()) return;
      const saved = bookmarks.get(sid);
      moveTo(pane.value, saved?.position ?? { kind: "bottom" });
      if (saved && (saved.hasNewReply || lastReplyID(vueMessages.value) > saved.replyID) && awayFromLatest.value) {
        hasNewReply.value = true;
      }
      restoringEpoch = null;
    });
  }

  function forgetReadingPosition(id: string): void {
    bookmarks.delete(id);
    // 删除成功后仍可能在等左栏刷新；离开前不要把刚删的书签再存回去。
    if (id === getSessionID()) forgottenEpoch = getEpoch();
  }

  function resumeFrom(id: string): number | null {
    const saved = bookmarks.get(id);
    return saved && saved.position.kind !== "bottom" ? saved.windowFrom : null;
  }

  async function fetchWindowNow(id: string, { from, limit }: { from: number | null; limit: number }): Promise<WindowSnapshot<AppRow, ChatSegment>> {
    const [sums, recent] = await Promise.all([
      api.listSummaries(id),
      // 总数与“窗口可能正好要的那几条”一起拿：`?last=N` 的末条 idx 就是总数。
      api.listMessagesLast(id, limit),
    ]);
    const total = recent.length ? (recent[recent.length - 1].idx ?? 0) : 0;
    const segments = computeSegments(total, sums.map((s) => ({ ...s })) as ChatSummary[]);
    const start = from === null ? windowStart(segments, limit) : alignWindowFrom(segments, from, limit);
    const runs = runsToFetch(segments, start, Infinity);
    const missing = seededMissingRuns(runs, recent.map((m) => ({ idx: m.idx ?? 0 })));
    const rest = missing.length ? await api.messageRanges(id, batchRanges(missing)) : [];
    return {
      rows: [...recent, ...rest].sort((x, y) => (x.idx ?? 0) - (y.idx ?? 0)).map(toRow),
      segments,
      windowFrom: start,
    };
  }

  function fetchWindow(id: string, { from = null, limit = windowLimit.value }: { from?: number | null; limit?: number } = {}): Promise<WindowSnapshot<AppRow, ChatSegment>> {
    if (from !== null) return fetchWindowNow(id, { from, limit });
    const key = `${id}:${limit}`;
    const hit = tailInflight.get(key);
    if (hit) return hit;
    const p = fetchWindowNow(id, { from, limit }).finally(() => tailInflight.delete(key));
    tailInflight.set(key, p);
    return p;
  }

  async function refreshSummaryMsgs(id: string, foreground = true): Promise<void> {
    const seg = vueSegments.value.find((x): x is Extract<ChatSegment, { kind: "summary" }> => x.kind === "summary" && x.id === id);
    const sid = getSessionID();
    if (!seg || !sid) { summaryMsgs.delete(id); return; }
    const my = getEpoch();
    const request = Symbol();
    summaryRequests.set(id, request);
    const current = () => my === getEpoch() && summaryRequests.get(id) === request;
    if (foreground) {
      summaryErrors.delete(id);
      summaryMsgs.set(id, { state: "loading", rows: null });
    }
    try {
      const msgs = await api.listMessagesRange(sid, seg.b, seg.e);
      if (!current()) return;
      summaryErrors.delete(id);
      summaryMsgs.set(id, {
        state: "ready",
        rows: msgs.sort((x, y) => (x.idx ?? 0) - (y.idx ?? 0)).map((m) => ({
          kind: "msg" as const, key: m.id, msg: toRow(m), displayIdx: m.idx ?? null,
        })),
      });
    } catch (err: unknown) {
      if (!current()) return;
      const message = `未压缩文本拉取失败：${err instanceof Error ? err.message : String(err)}`;
      if (foreground || !summaryMsgs.get(id)?.rows) {
        summaryMsgs.delete(id);
        summaryErrors.set(id, message);
      }
      if (openSummaries.has(id)) toast(message, "error");
    } finally {
      if (current()) summaryRequests.delete(id);
    }
  }

  function onSummaryToggle(id: string, open: boolean): void {
    if (!open) { openSummaries.delete(id); return; }
    openSummaries.add(id);
    void refreshSummaryMsgs(id);
  }

  async function loadWindow({ reset = false, resume = false, scroll = true, onlyIfUnchanged = false }: {
    reset?: boolean; resume?: boolean; scroll?: boolean | "keep"; onlyIfUnchanged?: boolean;
  } = {}): Promise<boolean> {
    const sid = getSessionID();
    const my = getEpoch();
    const rev = onlyIfUnchanged ? getLocalRev() : -1;
    if (!sid) return false;
    const previousFrom = windowFrom.value;
    const snap = await fetchWindow(sid, {
      from: resume ? resumeFrom(sid) : reset ? null : previousFrom,
      limit: windowLimit.value,
    });
    if (my !== getEpoch() || sid !== getSessionID()) return false;
    if (previousFrom !== windowFrom.value) {
      // 后台刷新可以让位；生成收尾必须按已经扩大了的窗口重新取，不能留下合成消息。
      return !onlyIfUnchanged && scroll === "keep" && loadWindow({ scroll });
    }
    // 后台对账专用闸：等待期间人已经动了消息列（发送/编辑/删除…）⇒ 这一发是旧快照。
    if (onlyIfUnchanged && (rev !== getLocalRev() || !(canReconcile?.() ?? true))) return false;
    // 等网络期间可能已经翻页或滚到另一条消息；只取落屏这一刻的锚，不把人拉回请求发起处。
    const position = scroll === "keep" ? captureReadingPosition(pane.value) : null;
    const newReply = scroll === "keep" && lastReplyID(snap.rows) > lastReplyID(vueMessages.value);
    vueSegments.value = snap.segments;
    windowFrom.value = snap.windowFrom;
    vueMessages.value = snap.rows;
    const alive = new Set(snap.segments.filter((seg) => seg.kind === "summary").map((seg) => seg.id));
    for (const key of [...summaryMsgs.keys()]) if (!alive.has(key)) summaryMsgs.delete(key);
    for (const key of summaryErrors.keys()) if (!alive.has(key)) summaryErrors.delete(key);
    for (const key of summaryRequests.keys()) if (!alive.has(key)) summaryRequests.delete(key);
    for (const id of openSummaries) if (alive.has(id)) void refreshSummaryMsgs(id, false);
    rememberWindow(windowCache, sid, { rows: vueMessages.value, segments: snap.segments, windowFrom: windowFrom.value });
    onWindowLoaded?.();
    if (position || scroll === true) {
      await nextTick();
      if (my !== getEpoch() || sid !== getSessionID()) return false;
      moveTo(pane.value, position ?? { kind: "bottom" });
    }
    if (newReply) noteNewReply();
    return true;
  }

  async function reloadWindow(opts: { scroll?: boolean; onlyIfUnchanged?: boolean } = {}): Promise<boolean> {
    return loadWindow({ reset: false, ...opts });
  }

  async function reloadWindowKeepingScroll({ onlyIfUnchanged = false }: { onlyIfUnchanged?: boolean } = {}): Promise<boolean> {
    return loadWindow({ scroll: "keep", onlyIfUnchanged });
  }

  function prefetchSession(id: string): void {
    if (!id || id === getSessionID() || isDraft()) return;
    if (windowCache.has(id) || prefetching.has(id)) return;
    if (Date.now() - (prefetchedAt.get(id) ?? 0) < PREFETCH_FRESH_MS) return;
    if (prefetchActive >= PREFETCH_MAX_ACTIVE) return;
    // 老窗口已被淘汰：尾页预取既缺锚也可能压掉翻过的页；只有真切回来才拉这一大片历史。
    if (resumeFrom(id) !== null) return;
    prefetchActive++;
    prefetching.add(id);
    fetchWindow(id).then((snap) => {
      if (!windowCache.has(id)) rememberWindow(windowCache, id, snap);
      prefetchedAt.set(id, Date.now());
    }).catch(() => {}).finally(() => {
      prefetchActive--;
      prefetching.delete(id);
    });
  }

  async function loadMore(): Promise<void> {
    const my = getEpoch();
    if (pagingEpoch === my) return;
    pagingEpoch = my;
    try {
      await loadOlderWindow();
    } catch (err: unknown) {
      if (my === getEpoch()) toast(`加载更多失败：${err instanceof Error ? err.message : String(err)}`, "error");
    } finally {
      if (pagingEpoch === my) {
        pagingEpoch = null;
        loadingMore.value = false;
      }
    }
  }

  async function loadOlderWindow(): Promise<void> {
    const sid = getSessionID();
    const my = getEpoch();
    const from = windowFrom.value;
    const segments = vueSegments.value;
    const older = prevWindowStart(segments, from, windowLimit.value);
    if (!sid || older >= from) return;
    const runs = runsToFetch(segments, older, from - 1);
    if (runs.length) loadingMore.value = true;
    const got = runs.length ? await api.messageRanges(sid, batchRanges(runs)) : [];
    if (my !== getEpoch() || sid !== getSessionID() || from !== windowFrom.value) return;
    // 等分页时前台对账可能已经替换了段结构。按新结构兑现这次翻页，不往新窗口拼旧数据。
    if (segments !== vueSegments.value) return loadOlderWindow();
    const el = pane.value;
    const position = captureReadingPosition(el);
    const beforeH = el?.scrollHeight ?? 0;
    const beforeTop = el?.scrollTop ?? 0;
    vueMessages.value = [...got.sort((x, y) => (x.idx ?? 0) - (y.idx ?? 0)).map(toRow), ...vueMessages.value];
    windowFrom.value = older;
    rememberWindow(windowCache, sid, {
      rows: vueMessages.value.filter(row => row.messageId), segments: vueSegments.value, windowFrom: windowFrom.value,
    });
    await nextTick();
    if (position && el && my === getEpoch() && sid === getSessionID() && el === pane.value) {
      // 翻页要把新内容加在视口上方，即使原来离底部只差几十像素也不应吸到底。
      const top = beforeTop + el.scrollHeight - beforeH;
      // 新页可能并进同一条回复，换掉组首的锚；找不到它时仍按新增高度补偿。
      moveTo(el, position.kind === "row" ? { ...position, top } : { kind: "offset", top });
    }
  }

  return {
    vueMessages, vueSegments, loadingSession, loadingMore, windowLimit, windowFrom, summaryMsgs, summaryErrors, openSummaries, windowCache,
    toRow, fetchWindow, loadWindow, reloadWindow, reloadWindowKeepingScroll, loadMore, resetWindowRequests, prefetchSession,
    rememberReadingPosition, restoreReadingPosition, forgetReadingPosition,
    awayFromLatest, hasNewReply, onReadingScroll, jumpToLatest, resetReadingTail, noteNewReply,
    refreshSummaryMsgs, onSummaryToggle,
  };
}
