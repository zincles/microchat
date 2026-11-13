// 重摇（消息 / 摘要两族）：候选只在内存里，切换就地换正文。
//
// 这一层只碰“候选与位次”：装配材料由后端现算，落库由 switch 接口完成。
// 消息列的渲染仍由 App 拿这里的状态算（isLive / bubbleContent），避免把模板逻辑搬进 composable。
import { ref } from "vue";
import type { Ref } from "vue";
import type { RerollState, StreamSlice } from "../api/types.ts";
import { mergeStreamSlice } from "../api/client.ts";
import { bubbleText, rerollTargetLive, type ChatSegment } from "../utils/chatrows.ts";
import type { AppRow } from "../types/app.ts";
import { emptyReroll, type RerollView } from "../types/app.ts";

interface RerollApi {
  rerollState(id: string): Promise<RerollState>;
  rerollSummaryState(id: string): Promise<RerollState>;
  rerollEnter(id: string): Promise<unknown>;
  rerollSwitch(id: string, idx: number): Promise<unknown>;
  rerollSummaryEnter(id: string, idx: number): Promise<unknown>;
  rerollSummarySwitch(id: string, idx: number): Promise<unknown>;
  turnText(id: string, from: number, thinkFrom: number): Promise<StreamSlice>;
}

export interface RerollDeps {
  api: RerollApi;
  toast: (text: string, kind?: string) => void;
  getSessionID: () => string | null;
  getEpoch: () => number;
  getLastMsgId: () => string | null;
  vueSegments: Ref<ChatSegment[]>;
  streamDisplay: Ref<{ text: string; reasoning: string }>;
  setStreamTarget: (text: string, reasoning: string) => void;
  resetTypewriter: () => void;
  waitTypewriter: () => Promise<void>;
  refreshStatusline: (phase: string, elapsedMs?: number | null, opts?: { withContext?: boolean }) => void;
  refreshTasks: () => void;
  reloadWindow: () => Promise<boolean>;
  refreshStateView: (id?: string | null) => void;
}

export interface Reroll {
  rerollState: Ref<RerollView>;
  summaryReroll: Ref<RerollView>;
  rerollPreview: Ref<{ text: string; reasoning: string }>;
  rerollStarting: Ref<string | null>;
  isLive: (m: AppRow) => boolean;
  bubbleContent: (m: AppRow) => string;
  bubbleReasoning: (m: AppRow) => string;
  rowDuration: (m: AppRow) => number | null;
  refreshReroll: () => Promise<void>;
  refreshRerollSummary: () => Promise<void>;
  refreshRerolls: () => Promise<unknown>;
  summaryRefresh: (sumId: string) => Promise<void>;
  summarySwitch: (idx: number) => Promise<void>;
  summaryVersion: (delta: number) => Promise<void>;
  pollReroll: (sid: string) => Promise<RerollView | null>;
  rerollNow: (targetId?: string | null) => Promise<void>;
  rerollGo: (delta: number) => Promise<void>;
  switchReroll: (sid: string, idx: number) => Promise<void>;
  resumeRerollIfLive: () => Promise<void>;
}

export function useReroll({
  api, toast, getSessionID, getEpoch, getLastMsgId, vueSegments, streamDisplay,
  setStreamTarget, resetTypewriter, waitTypewriter, refreshStatusline, refreshTasks, reloadWindow,
  refreshStateView,
}: RerollDeps): Reroll {
  const rerollState = ref<RerollView>(emptyReroll());
  const summaryReroll = ref<RerollView>(emptyReroll());
  const rerollPreview = ref({ text: "", reasoning: "" });
  const rerollStarting = ref<string | null>(null);

  function clearRerollPreview(): void {
    rerollPreview.value = { text: "", reasoning: "" };
  }

  const isLive = (m: AppRow): boolean =>
    rerollTargetLive(m, { startingId: rerollStarting.value, state: rerollState.value });

  function bubbleContent(m: AppRow): string {
    return bubbleText(m, {
      field: "content", live: isLive(m),
      streamText: streamDisplay.value.text, previewText: streamDisplay.value.text,
    });
  }

  function bubbleReasoning(m: AppRow): string {
    return bubbleText(m, {
      field: "reasoning", live: isLive(m),
      streamText: streamDisplay.value.reasoning, previewText: streamDisplay.value.reasoning,
    });
  }

  function rowDuration(m: AppRow): number | null {
    const n = Number(m?.durationMs);
    return Number.isFinite(n) && n > 0 ? n : null;
  }

  async function refreshReroll(): Promise<void> {
    const sid = getSessionID();
    if (!sid) { rerollState.value = emptyReroll(); clearRerollPreview(); return; }
    const my = getEpoch();
    try {
      const st = await api.rerollState(sid);
      if (my !== getEpoch()) return;
      const active = st?.active && st.target_kind === "message";
      rerollState.value = active ? st : emptyReroll();
      if (!active) clearRerollPreview();
    } catch {
      if (my !== getEpoch()) return;
      rerollState.value = emptyReroll();
      clearRerollPreview();
    }
  }

  async function refreshRerollSummary(): Promise<void> {
    const sid = getSessionID();
    if (!sid) { summaryReroll.value = emptyReroll(); return; }
    const my = getEpoch();
    try {
      const st = await api.rerollSummaryState(sid);
      if (my !== getEpoch()) return;
      summaryReroll.value = st?.active && st.target_kind === "summary" ? st : emptyReroll();
    } catch {
      if (my === getEpoch()) summaryReroll.value = emptyReroll();
    }
  }

  function refreshRerolls(): Promise<unknown> {
    return Promise.all([refreshReroll(), refreshRerollSummary()]).catch(() => {});
  }

  async function summaryRefresh(sumId: string): Promise<void> {
    const sid = getSessionID();
    const seg = vueSegments.value.find((x): x is Extract<ChatSegment, { kind: "summary" }> => x.kind === "summary" && x.id === sumId);
    if (!sid || !seg) return;
    if (summaryReroll.value.active && summaryReroll.value.running) { toast("摘要还在刷新"); return; }
    // 与 rerollNow 同一个口径：只有“真的多出一版”才切（被按停 ⇒ 不切）。
    const versionsBefore = summaryReroll.value.count;
    try {
      await api.rerollSummaryEnter(sid, seg.b);
      refreshTasks();
    } catch (err: unknown) {
      toast(`刷新失败：${err instanceof Error ? err.message : String(err)}`, "error");
      return;
    }
    const my = getEpoch();
    const st = await pollRerollSummary(sid);
    if (my !== getEpoch()) return;
    if (st?.error) { toast(`刷新失败：${st.error}`, "error"); return; }
    if (!st?.active) return;
    if (st.count <= versionsBefore) { await refreshRerollSummary(); return; }
    await summarySwitch(st.count);
  }

  async function pollRerollSummary(sid: string): Promise<RerollView | null> {
    const my = getEpoch();
    let st: RerollView | null = null;
    for (let i = 0; i < 450; i++) {
      await new Promise((r) => setTimeout(r, 400));
      if (my !== getEpoch()) return null;
      st = await api.rerollSummaryState(sid).catch(() => null);
      if (!st || my !== getEpoch()) return null;
      summaryReroll.value = st.active && st.target_kind === "summary" ? st : emptyReroll();
      refreshStatusline(st.running ? "摘要刷新中" : "idle", undefined, { withContext: !st.running });
      if (!st.running) return st;
    }
    return st;
  }

  async function summarySwitch(idx: number): Promise<void> {
    const sid = getSessionID();
    if (!sid) return;
    const my = getEpoch();
    try {
      await api.rerollSummarySwitch(sid, idx);
    } catch (err: unknown) {
      toast(`切版失败：${err instanceof Error ? err.message : String(err)}`, "error");
      return;
    }
    if (my !== getEpoch()) return;
    await reloadWindow();
    refreshStateView();
    await refreshRerollSummary();
  }

  async function summaryVersion(delta: number): Promise<void> {
    const st = summaryReroll.value;
    if (!st.active || st.running) return;
    const to = st.current_idx + delta;
    if (delta > 0 && to > st.count) { await summaryRefresh(st.target_summary_id ?? ""); return; }
    if (delta < 0 && to < 1) { toast("已经是第一版了"); return; }
    await summarySwitch(to);
  }

  async function pollReroll(sid: string): Promise<RerollView | null> {
    const my = getEpoch();
    let st: RerollView | null = null;
    let cur = { from: 0, thinkFrom: 0 };
    for (let i = 0; i < 450; i++) {
      await new Promise((r) => setTimeout(r, 400));
      if (my !== getEpoch()) return null;
      st = await api.rerollState(sid).catch(() => null);
      if (!st || my !== getEpoch()) return null;
      // ⚠ 无条件写回，不是“活着才写”（2026-11-01 修的 bug：按停之后界面一直等）。
      rerollState.value = st.active && st.target_kind === "message" ? st : emptyReroll();
      if (st.running) {
        const slice = await api.turnText(sid, cur.from, cur.thinkFrom).catch(() => null);
        if (slice && my === getEpoch()) {
          const merged = mergeStreamSlice(
            { text: rerollPreview.value.text, reasoning: rerollPreview.value.reasoning }, cur, slice);
          rerollPreview.value = { text: merged.text, reasoning: merged.reasoning };
          setStreamTarget(merged.text, merged.reasoning);
          cur = merged.cur;
        }
      }
      refreshStatusline(st.running ? "重摇中" : "idle", undefined, { withContext: !st.running });
      if (!st.running) return st;
    }
    return st;
  }

  async function rerollNow(targetId: string | null = null): Promise<void> {
    const sid = getSessionID();
    if (!sid) return;
    const my = getEpoch();
    clearRerollPreview();
    resetTypewriter();
    const target = targetId || getLastMsgId();
    if (!target) return;
    rerollStarting.value = target;
    const versionsBefore = rerollState.value.count;
    try {
      try {
        await api.rerollEnter(sid);
        refreshTasks();
      } catch (err: unknown) {
        toast(`重摇失败：${err instanceof Error ? err.message : String(err)}`, "error");
        return;
      }
      const st = await pollReroll(sid);
      if (my !== getEpoch()) return;
      if (st?.error) { toast(`重摇失败：${st.error}`, "error"); clearRerollPreview(); return; }
      if (!st?.active) return;
      if (st.count <= versionsBefore) { await refreshReroll(); return; }
      await waitTypewriter();
      await switchReroll(sid, st.count);
    } finally {
      rerollStarting.value = null;
    }
  }

  async function rerollGo(delta: number): Promise<void> {
    const sid = getSessionID();
    const st = rerollState.value;
    if (!sid || !st.active || st.running) return;
    const to = st.current_idx + delta;
    if (delta > 0 && to > st.count) { await rerollNow(); return; }
    if (delta < 0 && to < 1) { toast("已经是第一版了"); return; }
    await switchReroll(sid, to);
  }

  async function switchReroll(sid: string, idx: number): Promise<void> {
    const my = getEpoch();
    try {
      await api.rerollSwitch(sid, idx);
    } catch (err: unknown) {
      toast(`切版失败：${err instanceof Error ? err.message : String(err)}`, "error");
      return;
    }
    if (my !== getEpoch()) return;
    await reloadWindow();
    // 预览与库内正文必须同一拍换过去，否则会闪一帧空白。
    clearRerollPreview();
    rerollStarting.value = null;
    refreshStateView(sid);
    refreshStatusline("idle");
    await refreshRerolls();
  }

  async function resumeRerollIfLive(): Promise<void> {
    const sid = getSessionID();
    if (!sid) return;
    const messageLive = rerollState.value.active && rerollState.value.running;
    const summaryLive = summaryReroll.value.active && summaryReroll.value.running;
    if (!messageLive && !summaryLive) return;
    const my = getEpoch();
    if (messageLive) {
      const before = rerollState.value.count;
      const st = await pollReroll(sid).catch(() => null);
      if (my !== getEpoch() || !st) return;
      if (st.active && st.count > before) await switchReroll(sid, st.count);
      else await refreshReroll();
      return;
    }
    const before = summaryReroll.value.count;
    const st = await pollRerollSummary(sid).catch(() => null);
    if (my !== getEpoch() || !st) return;
    if (st.active && st.count > before) await summarySwitch(st.count);
    else await refreshRerollSummary();
  }

  return {
    rerollState, summaryReroll, rerollPreview, rerollStarting,
    isLive, bubbleContent, bubbleReasoning, rowDuration,
    refreshReroll, refreshRerollSummary, refreshRerolls,
    summaryRefresh, summarySwitch, summaryVersion, pollReroll, rerollNow, rerollGo, switchReroll,
    resumeRerollIfLive,
  };
}
