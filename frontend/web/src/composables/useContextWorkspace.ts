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
  refreshStateView: (id?: string | null, foreground?: boolean) => void;
  reloadWindow: () => Promise<boolean>;
  payloadPreviewOn: () => boolean;
}

export function useContextWorkspace({
  api, toast, getSessionID, getEpoch, isDraft, workspace, isFlow, payloadbarRef, setMacros,
  isVisible, refreshStateView, reloadWindow, payloadPreviewOn,
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
  let macrosSeq = 0;
  let macrosLoaded = false;
  let historyPreviewSeq = 0;

  function resetContextRequests(): void {
    historyPreviewSeq++;
    historyWire.value = null;
    historyLoading.value = false;
    historyError.value = "";
    treeLoading.value = false;
    treeError.value = "";
    macrosLoaded = false;
    macrosLoading.value = false;
    macrosError.value = "";
    setMacros(null);
  }

  async function refreshTree(foreground = true): Promise<void> {
    const sid = getSessionID();
    if (!sid || !treeVisible.value || !isVisible() || isFlow()) return;
    const my = getEpoch();
    const seq = ++treeSeq;
    const current = () => my === getEpoch() && seq === treeSeq;
    if (foreground) treeLoading.value = true;
    try {
      const [msgs, sums] = await Promise.all([api.listMessages(sid), api.listSummaries(sid)]);
      if (!current()) return;
      payloadbarRef.value?.setTree(buildCompactTree(
        msgs.map((m) => ({ ...m, idx: m.idx ?? 0 })), sums));
      treeError.value = "";
    } catch (err: unknown) {
      if (!current() || (!foreground && !treeLoading.value)) return;
      payloadbarRef.value?.setTree(null);
      treeError.value = `压缩结构读取失败：${err instanceof Error ? err.message : String(err)}`;
    } finally {
      if (current()) treeLoading.value = false;
    }
  }

  // 历史预演（载荷 tab 的“这一发会发什么”）：只在真的看载荷时才问。
  async function refreshHistoryPreview(foreground = true): Promise<void> {
    const sid = getSessionID();
    if (isDraft() || !sid || !payloadPreviewOn()) return;
    const my = getEpoch();
    const seq = ++historyPreviewSeq;
    const current = () => my === getEpoch() && seq === historyPreviewSeq && payloadPreviewOn();
    if (foreground) historyLoading.value = true;
    try {
      const wire = await api.outgoingPreview(sid, "");
      if (!current()) return;
      historyError.value = "";
      historyWire.value = wire;
    } catch (err: unknown) {
      if (!current()) return;
      const message = `预演失败：${err instanceof Error ? err.message : String(err)}`;
      if (foreground || historyLoading.value || !historyWire.value) historyError.value = message;
      if (isVisible() && payloadTab.value === "payload") toast(message, "error");
    } finally {
      if (current()) historyLoading.value = false;
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
    if (payloadTab.value === "tree") void refreshTree(foreground);
    else if (payloadTab.value === "state") refreshStateView(getSessionID(), foreground);
    else if (payloadTab.value === "payload") void refreshHistoryPreview(foreground);
  }

  function onPayloadTab(tab: string): void {
    payloadTab.value = tab;
    treeVisible.value = tab === "tree";
    refreshContextLazy();
  }

  function onPayloadPreview(on: unknown): void {
    if (!on) {
      historyPreviewSeq++;
      historyWire.value = null;
      historyLoading.value = false;
      historyError.value = "";
    }
    if (workspace.value === "context" && payloadTab.value === "payload") void refreshHistoryPreview();
  }

  async function onAdvancedDone(): Promise<void> {
    if (!getSessionID()) return;
    const my = getEpoch();
    await reloadWindow();
    if (my !== getEpoch()) return;
    refreshStateView(getSessionID());
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
