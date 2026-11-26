// 上下文工作区（右栏三 tab）的懒加载与刷新。
//
// 这里只回答“当前 tab 要看什么”：载荷预演 / 世界状态 / 压缩树各拉自己那一发；
// 聊天界面不该为看不见的面板付 RTT。树的折叠与渲染仍归 PayloadBar。
import { ref, shallowRef, type Ref } from "vue";
import type { LastPayload, MacroReport, Message, Summary } from "../api/types.ts";
import { buildCompactTree } from "../utils/tree.ts";

interface ContextApi {
  listMessages(id: string): Promise<Message[]>;
  listSummaries(id: string): Promise<Summary[]>;
  outgoingPreview(id: string, content?: string): Promise<LastPayload>;
  sessionMacros(id: string): Promise<MacroReport>;
}

interface PayloadSink {
  setTables?(tables: unknown): void;
  setTree(tree: unknown): void;
}

export interface ContextWorkspaceDeps {
  api: ContextApi;
  toast: (text: string, kind?: string) => void;
  getSessionID: () => string | null;
  getEpoch: () => number;
  isDraft: () => boolean;
  workspace: Ref<string>;
  isFlow: () => boolean;
  payloadbarRef: Ref<PayloadSink | null>;
  setMacros: (report: MacroReport | null) => void;
  isVisible: () => boolean;
  refreshStateView: (id?: string | null, foreground?: boolean) => boolean | void | Promise<boolean | void>;
  getStateError?: () => string;
  reloadWindow: () => Promise<boolean>;
  payloadPreviewOn: () => boolean;
}

const CONTEXT_CACHE_TTL = 30_000;
const CONTEXT_CACHE_LIMIT = 8;
interface TimedValue<T> { value: T; at: number }

function isFresh<T>(entry: TimedValue<T> | undefined): entry is TimedValue<T> {
  return !!entry && Date.now() - entry.at < CONTEXT_CACHE_TTL;
}

function saveCache<T>(cache: Map<string, TimedValue<T>>, sid: string, value: T): void {
  cache.set(sid, { value, at: Date.now() });
  while (cache.size > CONTEXT_CACHE_LIMIT) {
    const oldest = cache.keys().next().value;
    if (oldest == null) break;
    cache.delete(oldest);
  }
}
function saveTimestampCache(cache: Map<string, number>, sid: string): void {
  cache.set(sid, Date.now());
  while (cache.size > CONTEXT_CACHE_LIMIT) {
    const oldest = cache.keys().next().value;
    if (oldest == null) break;
    cache.delete(oldest);
  }
}

export function useContextWorkspace({
  api, toast, getSessionID, getEpoch, isDraft, workspace, isFlow, payloadbarRef, setMacros,
  isVisible, refreshStateView, getStateError, reloadWindow, payloadPreviewOn,
}: ContextWorkspaceDeps) {
  const treeVisible = ref(false);
  const payloadTab = ref("payload");
  const historyWire = shallowRef<LastPayload | null>(null);
  const historyLoading = shallowRef(false);
  const historyError = shallowRef("");
  const treeLoading = shallowRef(false);
  const treeError = shallowRef("");
  const macrosLoading = shallowRef(false);
  const macrosError = shallowRef("");
  let treeSeq = 0;
  let stateSeq = 0;
  let macrosSeq = 0;
  let macrosLoaded = false;
  let historyPreviewSeq = 0;
  const historyCache = new Map<string, TimedValue<LastPayload>>();
  const treeCache = new Map<string, TimedValue<unknown>>();
  const stateCache = new Map<string, number>();
  const historyRequests = new Map<string, Promise<LastPayload>>();
  const treeRequests = new Map<string, Promise<unknown>>();
  const stateRequests = new Map<string, Promise<boolean | void>>();

  function resetContextRequests(): void {
    historyPreviewSeq++;
    treeSeq++;
    stateSeq++;
    historyRequests.clear();
    treeRequests.clear();
    stateRequests.clear();
    stateCache.clear();
    historyWire.value = null;
    historyLoading.value = false;
    historyError.value = "";
    treeLoading.value = false;
    treeError.value = "";
    payloadbarRef.value?.setTables?.({});
    payloadbarRef.value?.setTree(null);
    macrosSeq++;
    macrosLoaded = false;
    macrosLoading.value = false;
    macrosError.value = "";
    setMacros(null);
  }

  function treeRequest(sid: string, force = false): Promise<unknown> {
    const pending = treeRequests.get(sid);
    if (pending && !force) return pending;
    const request = Promise.all([api.listMessages(sid), api.listSummaries(sid)]).then(([msgs, sums]) =>
      buildCompactTree(msgs.map((m) => ({ ...m, idx: m.idx ?? 0 })), sums));
    treeRequests.set(sid, request);
    void request.then(
      () => { if (treeRequests.get(sid) === request) treeRequests.delete(sid); },
      () => { if (treeRequests.get(sid) === request) treeRequests.delete(sid); },
    );
    return request;
  }

  async function loadTree(foreground = true, force = false): Promise<void> {
    const sid = getSessionID();
    if (!sid || !treeVisible.value || !isVisible() || isFlow()) return;
    const cached = treeCache.get(sid);
    if (!force && isFresh(cached)) {
      payloadbarRef.value?.setTree(cached.value);
      treeError.value = "";
      treeLoading.value = false;
      return;
    }
    const my = getEpoch();
    const seq = ++treeSeq;
    const current = () => my === getEpoch() && sid === getSessionID() && seq === treeSeq;
    treeLoading.value = true;
    treeError.value = "";
    try {
      const tree = await treeRequest(sid, force);
      if (!current()) return;
      saveCache(treeCache, sid, tree);
      payloadbarRef.value?.setTree(tree);
    } catch (err: unknown) {
      if (!current()) return;
      if (force) treeCache.delete(sid);
      treeError.value = `压缩结构读取失败：${err instanceof Error ? err.message : String(err)}`;
    } finally {
      if (current()) treeLoading.value = false;
    }
  }

  async function refreshTree(foreground = true): Promise<void> {
    await loadTree(foreground, true);
  }

  function historyRequest(sid: string, force = false): Promise<LastPayload> {
    const pending = historyRequests.get(sid);
    if (pending && !force) return pending;
    const request = api.outgoingPreview(sid, "");
    historyRequests.set(sid, request);
    void request.then(
      () => { if (historyRequests.get(sid) === request) historyRequests.delete(sid); },
      () => { if (historyRequests.get(sid) === request) historyRequests.delete(sid); },
    );
    return request;
  }

  // 历史预演（载荷 tab 的“这一发会发什么”）：只在真的看载荷时才问。
  async function loadHistoryPreview(foreground = true, force = false): Promise<void> {
    const sid = getSessionID();
    if (isDraft() || !sid || !payloadPreviewOn()) return;
    const cached = historyCache.get(sid);
    if (!force && isFresh(cached)) {
      historyWire.value = cached.value;
      historyError.value = "";
      historyLoading.value = false;
      return;
    }
    const my = getEpoch();
    const seq = ++historyPreviewSeq;
    const current = () => my === getEpoch() && sid === getSessionID() && seq === historyPreviewSeq && payloadPreviewOn();
    historyLoading.value = true;
    historyError.value = "";
    try {
      const wire = await historyRequest(sid, force);
      if (!current()) return;
      saveCache(historyCache, sid, wire);
      historyWire.value = wire;
    } catch (err: unknown) {
      if (!current()) return;
      if (force) historyCache.delete(sid);
      const message = `预演失败：${err instanceof Error ? err.message : String(err)}`;
      historyError.value = message;
      if (isVisible() && payloadTab.value === "payload") toast(message, "error");
    } finally {
      if (current()) historyLoading.value = false;
    }
  }

  async function refreshHistoryPreview(foreground = true): Promise<void> {
    await loadHistoryPreview(foreground, true);
  }

  async function loadState(foreground = true, force = false): Promise<void> {
    const sid = getSessionID();
    if (isDraft() || !sid || isFlow()) return;
    const cachedAt = stateCache.get(sid);
    if (!force && cachedAt != null && Date.now() - cachedAt < CONTEXT_CACHE_TTL && !getStateError?.()) return;
    const pending = stateRequests.get(sid);
    if (!force && pending) {
      await pending;
      return;
    }
    const my = getEpoch();
    const seq = ++stateSeq;
    const current = () => my === getEpoch() && sid === getSessionID() && seq === stateSeq;
    const request = Promise.resolve().then(() => refreshStateView(sid, true));
    stateRequests.set(sid, request);
    void request.then(
      () => { if (stateRequests.get(sid) === request) stateRequests.delete(sid); },
      () => { if (stateRequests.get(sid) === request) stateRequests.delete(sid); },
    );
    try {
      const succeeded = await request;
      if (current()) {
        if (succeeded === true || (succeeded === undefined && !getStateError?.())) saveTimestampCache(stateCache, sid);
        else stateCache.delete(sid);
      }
    } catch {
      if (current()) stateCache.delete(sid);
      // The callback owns the visible error state. Failed callbacks are never cached.
    }
  }

  async function refreshMacros(): Promise<void> {
    const sid = getSessionID();
    if (!sid) { setMacros(null); return; }
    const my = getEpoch();
    const seq = ++macrosSeq;
    const current = () => my === getEpoch() && seq === macrosSeq;
    if (!macrosLoaded && !macrosError.value) macrosLoading.value = true;
    try {
      const report = await api.sessionMacros(sid);
      if (!current()) return;
      macrosLoaded = true;
      macrosError.value = "";
      setMacros(report);
    } catch (err: unknown) {
      if (!current() || macrosLoaded) return;
      macrosError.value = `已注册宏读取失败：${err instanceof Error ? err.message : String(err)}`;
      setMacros(null);
    } finally {
      if (current()) macrosLoading.value = false;
    }
  }

  function refreshContextLazy(foreground = true): void {
    if (!isVisible()) return;
    // FlowAgent 暂时没有普通 Agent 的上下文面板 ⇒ 不拉这些请求。
    if (isFlow()) return;
    if (payloadTab.value === "tree") void loadTree(foreground, true);
    else if (payloadTab.value === "state") void loadState(foreground, true);
    else if (payloadTab.value === "payload") void loadHistoryPreview(foreground, true);
  }

  function onPayloadTab(tab: string): void {
    payloadTab.value = tab;
    treeVisible.value = tab === "tree";
    if (tab === "tree") void loadTree(true, false);
    else if (tab === "state") void loadState(true, false);
    else if (tab === "payload") void loadHistoryPreview(true, false);
  }

  function onPayloadPreview(on: unknown): void {
    if (!on) {
      historyPreviewSeq++;
      historyCache.clear();
      historyRequests.clear();
      historyWire.value = null;
      historyLoading.value = false;
      historyError.value = "";
      return;
    }
    if (workspace.value === "context" && payloadTab.value === "payload") void loadHistoryPreview(true, true);
  }

  async function onAdvancedDone(): Promise<void> {
    const sid = getSessionID();
    if (!sid) return;
    const my = getEpoch();
    await reloadWindow();
    if (my !== getEpoch()) return;
    stateCache.delete(sid);
    await loadState(true, true);
    if (treeVisible.value) await refreshTree(false);
  }

  return {
    treeVisible, payloadTab,
    historyWire, historyLoading, historyError, treeLoading, treeError, macrosLoading, macrosError,
    resetContextRequests,
    onPayloadTab, onPayloadPreview, onAdvancedDone,
    refreshTree, refreshHistoryPreview, refreshMacros, refreshContextLazy,
  };
}
