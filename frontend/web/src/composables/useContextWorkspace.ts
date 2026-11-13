// 上下文工作区（右栏三 tab）的懒加载与刷新。
//
// 这里只回答“当前 tab 要看什么”：载荷预演 / 世界状态 / 压缩树各拉自己那一发；
// 聊天界面不该为看不见的面板付 RTT。树的折叠与渲染仍归 PayloadBar。
import type { Ref } from "vue";
import { ref } from "vue";
import type { MacroReport, Summary } from "../api/types.ts";
import { buildCompactTree } from "../utils/tree.ts";
import type { Message } from "../api/types.ts";

interface ContextApi {
  listMessages(id: string): Promise<Message[]>;
  listSummaries(id: string): Promise<Summary[]>;
  outgoingPreview(id: string, content?: string): Promise<unknown>;
  sessionMacros(id: string): Promise<MacroReport>;
}

interface PayloadSink {
  setWire(wire: unknown): void;
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
  refreshStateView: (id?: string | null) => void;
  reloadWindow: () => Promise<boolean>;
  payloadPreviewOn: () => boolean;
}

export function useContextWorkspace({
  api, toast, getSessionID, getEpoch, isDraft, workspace, isFlow, payloadbarRef, setMacros,
  refreshStateView, reloadWindow, payloadPreviewOn,
}: ContextWorkspaceDeps) {
  const treeVisible = ref(false);
  const payloadTab = ref("payload");
  let historyPreviewSeq = 0;

  async function refreshTree(): Promise<void> {
    const sid = getSessionID();
    if (!sid || !treeVisible.value) return;
    const my = getEpoch();
    try {
      const [msgs, sums] = await Promise.all([api.listMessages(sid), api.listSummaries(sid)]);
      if (my !== getEpoch()) return;
      payloadbarRef.value?.setTree(buildCompactTree(
        msgs.map((m) => ({ ...m, idx: m.idx ?? 0 })), sums));
    } catch {
      if (my === getEpoch()) payloadbarRef.value?.setTree(null);
    }
  }

  // 历史预演（载荷 tab 的“这一发会发什么”）：只在真的看载荷时才问。
  async function refreshHistoryPreview(): Promise<void> {
    const sid = getSessionID();
    if (isDraft() || !sid || !payloadPreviewOn()) { payloadbarRef.value?.setWire(null); return; }
    const my = getEpoch();
    const seq = ++historyPreviewSeq;
    try {
      const wire = await api.outgoingPreview(sid, "");
      if (my === getEpoch() && seq === historyPreviewSeq) payloadbarRef.value?.setWire(wire);
    } catch (err: unknown) {
      if (my === getEpoch() && seq === historyPreviewSeq) {
        toast(`预演失败：${err instanceof Error ? err.message : String(err)}`, "error");
      }
    }
  }

  async function refreshMacros(): Promise<void> {
    const sid = getSessionID();
    if (!sid) { setMacros(null); return; }
    try { setMacros(await api.sessionMacros(sid)); }
    catch { setMacros(null); }
  }

  function refreshContextLazy(): void {
    if (workspace.value !== "context") return;
    // FlowAgent 暂时没有普通 Agent 的上下文面板 ⇒ 不拉这些请求。
    if (isFlow()) return;
    if (payloadTab.value === "tree") void refreshTree();
    else if (payloadTab.value === "state") refreshStateView(getSessionID());
    else if (payloadTab.value === "payload") void refreshHistoryPreview();
  }

  function onPayloadTab(tab: string): void {
    payloadTab.value = tab;
    treeVisible.value = tab === "tree";
    if (workspace.value !== "context") return;
    if (treeVisible.value) void refreshTree();
    else if (tab === "state") refreshStateView(getSessionID());
    else if (tab === "payload") void refreshHistoryPreview();
  }

  function onPayloadPreview(on: unknown): void {
    if (!on) payloadbarRef.value?.setWire(null);
    if (workspace.value === "context" && payloadTab.value === "payload") void refreshHistoryPreview();
  }

  async function onAdvancedDone(): Promise<void> {
    if (!getSessionID()) return;
    await reloadWindow();
    refreshStateView(getSessionID());
    if (treeVisible.value) await refreshTree();
  }

  return {
    treeVisible, payloadTab,
    onPayloadTab, onPayloadPreview, onAdvancedDone,
    refreshTree, refreshHistoryPreview, refreshMacros, refreshContextLazy,
  };
}
