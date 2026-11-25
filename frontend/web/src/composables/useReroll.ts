// 重摇（消息 / 摘要两族）：候选只在内存里，切换就地换正文。
//
// 这一层只碰“候选与位次”：装配材料由后端现算，落库由 switch 接口完成。
// 消息列的渲染仍由 App 拿这里的状态算（isLive / bubbleContent），避免把模板逻辑搬进 composable。
import { onScopeDispose, ref } from "vue";
import type { Ref } from "vue";
import type { RerollAccepted, RerollState, StreamSlice } from "../api/types.ts";
import { mergeStreamSlice } from "../api/client.ts";
import { bubbleText, rerollTargetLive, type ChatSegment } from "../utils/chatrows.ts";
import type { AppRow } from "../types/app.ts";
import { emptyReroll, type RerollView } from "../types/app.ts";

interface RerollApi {
  rerollState(id: string): Promise<RerollState>;
  rerollSummaryState(id: string): Promise<RerollState>;
  rerollEnter(id: string): Promise<RerollAccepted>;
  rerollSwitch(id: string, idx: number): Promise<RerollState>;
  rerollSummaryEnter(id: string, idx: number): Promise<RerollAccepted>;
  rerollSummarySwitch(id: string, idx: number): Promise<RerollState>;
  turnText(id: string, from: number, thinkFrom: number): Promise<StreamSlice>;
}

export interface RerollDeps {
  api: RerollApi;
  toast: (text: string, kind?: string) => void;
  getSessionID: () => string | null;
  getEpoch: () => number;
  canRerollMessage: () => boolean | Promise<boolean>;
  isWindowLoading: () => boolean;
  getLastMsgId: () => string | null;
  vueSegments: Ref<ChatSegment[]>;
  streamDisplay: Ref<{ text: string; reasoning: string }>;
  setStreamTarget: (text: string, reasoning: string) => void;
  resetTypewriter: () => void;
  waitTypewriter: () => Promise<void>;
  refreshStatusline: (phase: string, elapsedMs?: number | null, opts?: { withContext?: boolean }) => void;
  refreshTasks: () => void;
  reloadWindow: () => Promise<boolean>;
  onVersionWritten: (id: string) => void;
  refreshStateView: (id?: string | null) => void;
}

export interface Reroll {
  rerollState: Ref<RerollView>;
  summaryReroll: Ref<RerollView>;
  rerollStarting: Ref<string | null>;
  isLive: (m: AppRow) => boolean;
  bubbleContent: (m: AppRow) => string;
  bubbleReasoning: (m: AppRow) => string;
  rowDuration: (m: AppRow) => number | null;
  refreshRerolls: () => Promise<void>;
  resetRerollView: () => void;
  summaryRefresh: (sumId: string) => Promise<void>;
  summaryVersion: (delta: number) => Promise<void>;
  rerollNow: (targetId?: string | null) => Promise<void>;
  rerollGo: (delta: number) => Promise<void>;
}

type Family = "message" | "summary";
interface PendingCandidate {
  kind: Family;
  target: string;
  idx: number | null; // null = 还在等受理回执
}

export function useReroll({
  api, toast, getSessionID, getEpoch, canRerollMessage, isWindowLoading, getLastMsgId, vueSegments, streamDisplay,
  setStreamTarget, resetTypewriter, waitTypewriter, refreshStatusline, refreshTasks, reloadWindow,
  onVersionWritten, refreshStateView,
}: RerollDeps): Reroll {
  const rerollState = ref<RerollView>(emptyReroll());
  const summaryReroll = ref<RerollView>(emptyReroll());
  const rerollStarting = ref<string | null>(null);

  // 候选属于会话；前台观察者只属于当前这次进入。切走时不取消后台任务，也不在别处轮询。
  const pending = new Map<string, PendingCandidate>();
  const switching = new Set<string>();
  let observer: { epoch: number } | null = null;

  function resetRerollView(): void {
    observer = null;
    rerollState.value = emptyReroll();
    summaryReroll.value = emptyReroll();
    rerollStarting.value = null;
  }
  onScopeDispose(resetRerollView);

  function targetOf(state: RerollState): string | undefined {
    return state.target_kind === "message" ? state.target_message_id : state.target_summary_id;
  }

  function paintState(state: RerollState): void {
    rerollState.value = state.active && state.target_kind === "message" ? state : emptyReroll();
    summaryReroll.value = state.active && state.target_kind === "summary" ? state : emptyReroll();
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

  async function applyVersion(sid: string, kind: Family, idx: number): Promise<boolean> {
    if (switching.has(sid)) return false;
    switching.add(sid);
    let written = false;
    try {
      const state = await (kind === "message" ? api.rerollSwitch(sid, idx) : api.rerollSummarySwitch(sid, idx));
      written = true;
      pending.delete(sid);
      onVersionWritten(sid);
      if (getSessionID() !== sid || isWindowLoading()) return true;
      const epoch = getEpoch();
      const loaded = await reloadWindow();
      if (getEpoch() === epoch && getSessionID() === sid && loaded) {
        paintState(state);
        rerollStarting.value = null;
        refreshStateView(sid);
        refreshStatusline("idle");
      }
      return loaded;
    } catch (err) {
      if (getSessionID() === sid) toast(`${written ? "候选已切换，载入正文失败" : "切版失败"}：${err instanceof Error ? err.message : String(err)}`, "error");
      return false;
    } finally {
      switching.delete(sid);
    }
  }

  async function observeReroll(initial?: RerollState): Promise<void> {
    const sid = getSessionID();
    const epoch = getEpoch();
    if (!sid || isWindowLoading() || observer?.epoch === epoch || switching.has(sid)) return;
    const known = pending.get(sid);
    if (known?.kind === "message") rerollStarting.value = known.target;
    if (known?.idx === null) return;

    const watching = { epoch };
    observer = watching;
    const owns = () => observer === watching && getEpoch() === epoch && getSessionID() === sid;
    let kind: Family = known?.kind ?? "message";
    try {
      let state = initial;
      if (!state?.active) {
        const [message, summary] = await Promise.all([
          Promise.resolve(canRerollMessage()).then((allowed) =>
            allowed && owns() ? api.rerollState(sid) : emptyReroll()),
          api.rerollSummaryState(sid),
        ]);
        state = message.running ? message : summary.running ? summary : message.active ? message : summary;
      }
      if (!owns()) return;
      kind = state.target_kind ?? kind;
      const target = targetOf(state);
      let intent = known?.kind === kind && known.target === target ? known : undefined;
      if (known && !intent) pending.delete(sid);
      let cur = { from: 0, thinkFrom: 0 };
      let text = { text: "", reasoning: "" };
      const read = kind === "message" ? api.rerollState : api.rerollSummaryState;
      while (state.active && targetOf(state) === target && state.running) {
        const idx = state.items.find((item) => item.pending)?.idx;
        if (idx !== undefined && target) {
          intent = { kind, target, idx };
          pending.set(sid, intent);
        }
        paintState(state);
        if (kind === "message") {
          const slice = await api.turnText(sid, cur.from, cur.thinkFrom).catch(() => null);
          if (!owns()) return;
          if (slice) {
            const merged = mergeStreamSlice(text, cur, slice);
            text = { text: merged.text, reasoning: merged.reasoning };
            setStreamTarget(merged.text, merged.reasoning);
            cur = merged.cur;
          }
        }
        refreshStatusline(kind === "message" ? "重摇中" : "摘要刷新中", undefined, { withContext: false });
        await new Promise((resolve) => setTimeout(resolve, 400));
        if (!owns()) return;
        state = await read(sid);
        if (!owns()) return;
      }

      // count 含正在生成的占位。必须确认同一目标的这个位次已经有正文，取消不能切到旧候选。
      const idx = intent?.idx;
      if (idx && state.active && targetOf(state) === intent?.target && !state.error && state.current_idx !== idx &&
          state.items.some((item) => item.idx === idx && !item.pending)) {
        if (kind === "message") await waitTypewriter();
        if (!owns()) return;
        const applied = await applyVersion(sid, kind, idx);
        if (!owns() || applied) return;
      }
      pending.delete(sid);
      paintState(state);
      rerollStarting.value = null;
      if (intent) refreshStatusline("idle");
      if (state.error && intent) toast(`重摇失败：${state.error}`, "error");
    } catch (err) {
      if (!owns()) return;
      const running = pending.has(sid);
      resetRerollView();
      if (running) toast(`读取重摇进度失败：${err instanceof Error ? err.message : String(err)}`, "error");
    } finally {
      if (observer === watching) observer = null;
    }
  }

  async function begin(sid: string, kind: Family, target: string, accept: () => Promise<RerollAccepted>): Promise<void> {
    if (switching.has(sid) || rerollState.value.running || summaryReroll.value.running) return;
    if (pending.has(sid)) { await observeReroll(); return; }
    observer = null;
    const intent: PendingCandidate = { kind, target, idx: null };
    pending.set(sid, intent);
    if (kind === "message") {
      resetTypewriter();
      rerollStarting.value = target;
    }
    let response: RerollAccepted;
    try { response = await accept(); }
    catch (err) {
      pending.delete(sid);
      if (getSessionID() === sid) {
        rerollStarting.value = null;
        toast(`重摇失败：${err instanceof Error ? err.message : String(err)}`, "error");
      }
      return;
    }
    refreshTasks();
    intent.target = targetOf(response.state) ?? target;
    intent.idx = response.state.items.find((item) => item.pending)?.idx ?? 0;
    if (getSessionID() === sid) await observeReroll(response.state);
  }

  async function rerollNow(targetId: string | null = null): Promise<void> {
    const sid = getSessionID();
    const epoch = getEpoch();
    const target = targetId || getLastMsgId();
    if (!sid || !target) return;
    const allowed = canRerollMessage();
    if ((typeof allowed === "boolean" ? allowed : await allowed) && getEpoch() === epoch) {
      await begin(sid, "message", target, () => api.rerollEnter(sid));
    }
  }

  async function summaryRefresh(sumId: string): Promise<void> {
    const sid = getSessionID();
    const seg = vueSegments.value.find((x): x is Extract<ChatSegment, { kind: "summary" }> => x.kind === "summary" && x.id === sumId);
    if (sid && seg) await begin(sid, "summary", sumId, () => api.rerollSummaryEnter(sid, seg.b));
  }

  async function move(kind: Family, delta: number): Promise<void> {
    const sid = getSessionID();
    const state = (kind === "message" ? rerollState : summaryReroll).value;
    if (!sid || !state.active || state.running || pending.has(sid) || switching.has(sid)) return;
    const idx = state.current_idx + delta;
    if (idx < 1) { toast("已经是第一版了"); return; }
    if (idx > state.count) {
      if (kind === "message") await rerollNow();
      else await summaryRefresh(state.target_summary_id ?? "");
      return;
    }
    observer = null;
    await applyVersion(sid, kind, idx);
  }

  return {
    rerollState, summaryReroll, rerollStarting,
    isLive, bubbleContent, bubbleReasoning, rowDuration,
    refreshRerolls: () => observeReroll(), resetRerollView,
    summaryRefresh, summaryVersion: (delta) => move("summary", delta),
    rerollNow, rerollGo: (delta) => move("message", delta),
  };
}
