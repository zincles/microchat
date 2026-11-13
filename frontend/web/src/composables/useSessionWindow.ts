// 会话消息窗口：取数、分页、摘要展开、回看缓存与悬停预取。
//
// App 只保留“会话状态机”（进哪条、发什么、流怎么走）；这里只回答一件事：
// **当前会话要显示哪一屏**。取数只按消息段拉（被摘要盖住的原文一个字节都不取），
// 摘要卡展开是另一条按需路径。口径与纯算法在 utils/chatrows.ts。
import { nextTick, reactive, ref } from "vue";
import type { Ref } from "vue";
import type { Message, Summary } from "../api/types.ts";
import {
  alignWindowFrom, computeSegments, prevWindowStart, runsToFetch, seededMissingRuns,
  toolSources, windowStart, type ChatSegment, type ChatSummary, type ChatSummaryEntry,
} from "../utils/chatrows.ts";
import { rememberWindow, type WindowSnapshot } from "../utils/windowcache.ts";
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
  scrollBottom: () => void;
  /** 后台对账前再问一次：等待期间人动过消息列吗？ */
  canReconcile?: () => boolean;
  onWindowLoaded?: () => void;
}

export interface SessionWindow {
  vueMessages: Ref<AppRow[]>;
  vueSegments: Ref<ChatSegment[]>;
  loadingSession: Ref<boolean>;
  windowLimit: Ref<number>;
  windowFrom: Ref<number>;
  summaryMsgs: Map<string, ChatSummaryEntry>;
  openSummaries: Set<string>;
  windowCache: Map<string, WindowSnapshot<AppRow, ChatSegment>>;
  toRow: (m: Message) => AppRow;
  fetchWindow: (id: string, opts?: { from?: number | null; limit?: number }) => Promise<WindowSnapshot<AppRow, ChatSegment>>;
  loadWindow: (opts?: { reset?: boolean; scroll?: boolean; onlyIfUnchanged?: boolean }) => Promise<boolean>;
  reloadWindow: (opts?: { scroll?: boolean; onlyIfUnchanged?: boolean }) => Promise<boolean>;
  reloadWindowKeepingScroll: (opts?: { onlyIfUnchanged?: boolean }) => Promise<boolean>;
  loadMore: () => Promise<void>;
  prefetchSession: (id: string) => void;
  refreshSummaryMsgs: (id: string) => Promise<void>;
  onSummaryToggle: (id: string, open: boolean) => void;
}

const PREFETCH_MAX_ACTIVE = 3;
const PREFETCH_FRESH_MS = 30_000;
const SCROLL_STICK_PX = 48;

/** 库里的消息 → 消息列的一行（展示形状）。 */
export function toRow(m: Message): AppRow {
  return {
    who: m.role === "user" ? "你" : "助手",
    kind: m.kind || "chat",
    status: m.status || "done",
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

function nearBottom(el: HTMLElement | null): boolean {
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight <= SCROLL_STICK_PX;
}

export function useSessionWindow({
  api, toast, getSessionID, getEpoch, getLocalRev, isDraft, scrollBottom, canReconcile, onWindowLoaded,
}: SessionWindowDeps): SessionWindow {
  const vueMessages = ref<AppRow[]>([]);
  const vueSegments = ref<ChatSegment[]>([]);
  const loadingSession = ref(true);
  const windowLimit = ref(5);
  const windowFrom = ref(1);
  const summaryMsgs = reactive<Map<string, ChatSummaryEntry>>(new Map());
  const openSummaries = new Set<string>();
  const windowCache = new Map<string, WindowSnapshot<AppRow, ChatSegment>>();
  const tailInflight = new Map<string, Promise<WindowSnapshot<AppRow, ChatSegment>>>();
  const prefetching = new Set<string>();
  const prefetchedAt = new Map<string, number>();
  let prefetchActive = 0;

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

  async function refreshSummaryMsgs(id: string): Promise<void> {
    const seg = vueSegments.value.find((x): x is Extract<ChatSegment, { kind: "summary" }> => x.kind === "summary" && x.id === id);
    const sid = getSessionID();
    if (!seg || !sid) { summaryMsgs.delete(id); return; }
    const my = getEpoch();
    summaryMsgs.set(id, { state: "loading", rows: null });
    try {
      const msgs = await api.listMessagesRange(sid, seg.b, seg.e);
      if (my !== getEpoch()) return;
      summaryMsgs.set(id, {
        state: "ready",
        rows: msgs.sort((x, y) => (x.idx ?? 0) - (y.idx ?? 0)).map((m) => ({
          kind: "msg" as const, key: m.id, msg: toRow(m), displayIdx: m.idx ?? null,
        })),
      });
    } catch (err: unknown) {
      if (my !== getEpoch()) return;
      summaryMsgs.delete(id);
      toast(`未压缩文本拉取失败：${err instanceof Error ? err.message : String(err)}`, "error");
    }
  }

  function onSummaryToggle(id: string, open: boolean): void {
    if (!open) { openSummaries.delete(id); return; }
    openSummaries.add(id);
    void refreshSummaryMsgs(id);
  }

  async function loadWindow({ reset = false, scroll = true, onlyIfUnchanged = false }: { reset?: boolean; scroll?: boolean; onlyIfUnchanged?: boolean } = {}): Promise<boolean> {
    const sid = getSessionID();
    const my = getEpoch();
    const rev = onlyIfUnchanged ? getLocalRev() : -1;
    if (!sid) return false;
    const snap = await fetchWindow(sid, {
      from: reset ? null : windowFrom.value,
      limit: windowLimit.value,
    });
    if (my !== getEpoch()) return false;
    // 后台对账专用闸：等待期间人已经动了消息列（发送/编辑/删除…）⇒ 这一发是旧快照。
    if (onlyIfUnchanged && (rev !== getLocalRev() || !(canReconcile?.() ?? true))) return false;
    vueSegments.value = snap.segments;
    windowFrom.value = snap.windowFrom;
    vueMessages.value = snap.rows;
    const alive = new Set(snap.segments.filter((seg) => seg.kind === "summary").map((seg) => seg.id));
    for (const key of [...summaryMsgs.keys()]) if (!alive.has(key)) summaryMsgs.delete(key);
    for (const id of openSummaries) if (alive.has(id)) void refreshSummaryMsgs(id);
    rememberWindow(windowCache, sid, { rows: vueMessages.value, segments: snap.segments, windowFrom: windowFrom.value });
    if (scroll) scrollBottom();
    onWindowLoaded?.();
    return true;
  }

  async function reloadWindow(opts: { scroll?: boolean; onlyIfUnchanged?: boolean } = {}): Promise<boolean> {
    return loadWindow({ reset: false, ...opts });
  }

  async function reloadWindowKeepingScroll({ onlyIfUnchanged = false }: { onlyIfUnchanged?: boolean } = {}): Promise<boolean> {
    const el = document.getElementById("messages-vue");
    const stick = nearBottom(el);
    const top = el?.scrollTop ?? 0;
    const ok = await reloadWindow({ scroll: false, onlyIfUnchanged });
    const next = document.getElementById("messages-vue");
    if (next) {
      if (stick) next.scrollTop = next.scrollHeight;
      else next.scrollTop = Math.min(top, Math.max(0, next.scrollHeight - next.clientHeight));
    }
    return ok;
  }

  function prefetchSession(id: string): void {
    if (!id || id === getSessionID() || isDraft()) return;
    if (windowCache.has(id) || prefetching.has(id)) return;
    if (Date.now() - (prefetchedAt.get(id) ?? 0) < PREFETCH_FRESH_MS) return;
    if (prefetchActive >= PREFETCH_MAX_ACTIVE) return;
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
    const sid = getSessionID();
    const my = getEpoch();
    const older = prevWindowStart(vueSegments.value, windowFrom.value, windowLimit.value);
    if (!sid || older >= windowFrom.value) return;
    const runs = runsToFetch(vueSegments.value, older, windowFrom.value - 1);
    const got = runs.length ? await api.messageRanges(sid, batchRanges(runs)) : [];
    if (my !== getEpoch()) return;
    const el = document.getElementById("messages-vue");
    const beforeH = el ? el.scrollHeight : 0;
    const beforeTop = el ? el.scrollTop : 0;
    vueMessages.value = [...got.sort((x, y) => (x.idx ?? 0) - (y.idx ?? 0)).map(toRow), ...vueMessages.value];
    windowFrom.value = older;
    rememberWindow(windowCache, sid, {
      rows: vueMessages.value, segments: vueSegments.value, windowFrom: windowFrom.value,
    });
    await nextTick();
    if (el) el.scrollTop = beforeTop + (el.scrollHeight - beforeH);
  }

  return {
    vueMessages, vueSegments, loadingSession, windowLimit, windowFrom, summaryMsgs, openSummaries, windowCache,
    toRow, fetchWindow, loadWindow, reloadWindow, reloadWindowKeepingScroll, loadMore, prefetchSession,
    refreshSummaryMsgs, onSummaryToggle,
  };
}
