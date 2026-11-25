// 一轮生成的流：发送受理、SSE 优先、HTTP 轮询回退、游标合并与 step 切换。
//
// 组件化边界：这里只管“流怎么走完”，落库后的界面刷新交给 App 的 onTurnFinished
//（消息列/状态/占用/任务/预演各自都在 App 的域里）。增量的合并规则仍在 api/client.ts，
// 这里只负责把 status 与 slice 喂给打字机。
import { reactive, type Ref } from "vue";
import type { Accepted, Message, StreamCursor, StreamSlice, TurnPoll, TurnStatus } from "../api/types.ts";
import type { AppRow } from "../types/app.ts";
import { mergeStreamSlice } from "../api/client.ts";
import { toRow } from "./useSessionWindow.ts";
import type { DraftReceipt } from "../utils/sessionDrafts.ts";

interface TurnApi {
  sendMessage(id: string, content: string): Promise<Accepted>;
  resend(id: string): Promise<Accepted>;
  listMessagesLast(id: string, n: number): Promise<Message[]>;
  turnPoll(id: string, from: number, thinkFrom: number): Promise<TurnPoll>;
  turnEventsUrl(id: string, from: number, thinkFrom: number): string;
}

interface ComposerHandle {
  setRunning(running: boolean): void;
}

export interface TurnStreamDeps {
  api: TurnApi;
  getSessionID: () => string | null;
  getEpoch: () => number;
  vueMessages: Ref<AppRow[]>;
  vueStream: Ref<{ id: string | null; text: string; reasoning: string }>;
  addMessage: (who: string, text: string, cls: string) => AppRow;
  settleOptimisticUser: (row: AppRow, user?: import("../api/types.ts").Message | null) => void;
  dropOptimistic: (row: AppRow) => void;
  tmpKey: () => string;
  composerRef: Ref<ComposerHandle | null>;
  resetTypewriter: () => void;
  setStreamTarget: (text: string, reasoning: string) => void;
  refreshStatusline: (phase: string, elapsedMs?: number | null, opts?: { withContext?: boolean }) => void;
  refreshTasks: () => void;
  bumpLocalRev: () => void;
  beginPending: () => void;
  endPending: () => void;
  onTurnFinished: (sid: string, st: TurnStatus) => Promise<void>;
}

export interface TurnStream {
  startSendingBubble: () => AppRow;
  send: (content: string, draft: DraftReceipt) => Promise<void>;
  resend: () => Promise<void>;
  pollTurn: (sid: string, turnID: string | undefined, existing?: AppRow | null) => Promise<void>;
  hasLivePoll: () => boolean;
}

export function useTurnStream({
  api, getSessionID, getEpoch, vueMessages, vueStream, addMessage, settleOptimisticUser, dropOptimistic,
  tmpKey, composerRef, resetTypewriter, setStreamTarget, refreshStatusline, refreshTasks,
  bumpLocalRev, beginPending, endPending, onTurnFinished,
}: TurnStreamDeps): TurnStream {
  // 离开再回来是新的一次订阅；旧连接的 finally 不能解开新订阅或改另一条会话的发送键。
  const activeTurnPolls = new Map<string, number>();

  function startSendingBubble(): AppRow {
    const row = addMessage("助手", "", "assistant");
    row.stream = true;
    row.phase = "sending";
    return row;
  }

  // NextStep 只在前一步的消息和工具结果提交之后发出。只在换步时补读这些真记录，
  // 避免短于 SSE 节拍的搜索悄悄消失，也不把一次模型调用误报成一次工具调用。
  async function readCommittedSteps(sid: string, my: number, st: TurnStatus, live: AppRow): Promise<void> {
    const { run_id: run, step } = st;
    if (!run || !step || step < 2) return;
    let recent: Message[];
    try { recent = await api.listMessagesLast(sid, step); }
    catch { return; } // 网络中断时留着已经见到的片段，最终仍会从归档对齐。
    if (my !== getEpoch() || live.step !== step || !live.stream || !vueMessages.value.includes(live)) return;
    const steps = recent.filter(m => m.role === "assistant" && m.run_id === run && m.id !== st.message_id);
    if (steps.length !== step - 1) return; // 查询期间上游又走了一步，下一步的快照才有完整前缀。
    const ids = new Set(steps.map(m => m.id));
    const kept = vueMessages.value.filter(m => m === live || !(m.role === "assistant" && m.run_id === run && (!m.messageId || ids.has(m.messageId))));
    kept.splice(kept.indexOf(live), 0, ...steps.map(toRow));
    vueMessages.value = kept;
  }

  function advanceLiveStep(sid: string, my: number, st: TurnStatus, row: AppRow): boolean {
    if (st.run_id) row.run_id = st.run_id;
    const step = st.step;
    if (!step || step === row.step) return false;
    const previous = vueStream.value;
    const at = vueMessages.value.indexOf(row);
    if (at >= 0 && row.step && (previous.text || previous.reasoning || row.tool || row.sources?.length)) {
      vueMessages.value.splice(at, 0, { id: tmpKey(), who: "助手", role: "assistant", kind: "tool_run",
        run_id: row.run_id, step: row.step, content: previous.text, reasoning: previous.reasoning,
        tool: row.tool, sources: row.sources });
    }
    row.step = step;
    row.kind = "chat";
    row.reasoning = "";
    row.sources = [];
    row.tool = null;
    vueStream.value = { id: previous.id, text: "", reasoning: "" };
    resetTypewriter();
    void readCommittedSteps(sid, my, st, row);
    return true;
  }

  function applyTurnStream(st: TurnStatus, slice: StreamSlice, cur: StreamCursor, row: AppRow): StreamCursor {
    row.tool = st.tool ?? null;
    row.sources = st.sources ?? [];
    if (row.tool || row.sources.length) row.kind = "tool_run";
    const merged = mergeStreamSlice(
      { text: vueStream.value.text ?? "", reasoning: vueStream.value.reasoning ?? "" }, cur, slice);
    vueStream.value.text = merged.text;
    vueStream.value.reasoning = merged.reasoning;
    setStreamTarget(merged.text, merged.reasoning);
    row.reasoning = merged.reasoning;
    refreshStatusline(st.phase, st.elapsed_ms, { withContext: false });
    return merged.cur;
  }

  async function pollTurnEvents(sid: string, turnID: string | undefined, my: number, row: AppRow): Promise<TurnStatus | null | undefined> {
    let cur: StreamCursor = { from: 0, thinkFrom: 0 };
    const sources: EventSource[] = [];
    try {
      return await new Promise((resolve, reject) => {
        let got = false;
        const watchdog = setTimeout(() => {
          if (got) return;
          sources.forEach((source) => source.close());
          reject(new Error("SSE 连接超时"));
        }, 8000);
        const source = new EventSource(api.turnEventsUrl(sid, 0, 0));
        sources.push(source);
        source.onmessage = (event) => {
          got = true;
          clearTimeout(watchdog);
          if (my !== getEpoch()) { source.close(); resolve(null); return; }
          let poll: TurnPoll;
          try { poll = JSON.parse(event.data) as TurnPoll; } catch { return; }
          const st = poll.status;
          const slice = poll.slice;
          if (!st || !slice) return;
          if (turnID && st.message_id && st.message_id !== turnID) { source.close(); resolve(null); return; }
          if (advanceLiveStep(sid, my, st, row)) cur = { from: 0, thinkFrom: 0 };
          cur = applyTurnStream(st, slice, cur, row);
          if (st.phase === "idle" || st.phase === "error") {
            source.close(); resolve(st);
          }
        };
        source.onerror = () => {
          // EventSource 自己会带 Last-Event-ID 重连；这里不关它。
          // 如果从未收到过事件，watchdog 会兜底切 HTTP。
        };
      });
    } finally {
      sources.forEach((source) => source.close());
    }
  }

  async function pollTurnHTTP(sid: string, turnID: string | undefined, my: number, row: AppRow): Promise<void> {
    let cur: StreamCursor = { from: 0, thinkFrom: 0 };
    let misses = 0;
    let delay = 200;
    for (;;) {
      await new Promise((r) => setTimeout(r, delay));
      if (my !== getEpoch()) return;
      let poll: TurnPoll;
      const requestAt = performance.now();
      try {
        poll = await api.turnPoll(sid, cur.from, cur.thinkFrom);
        if (turnID && poll.status.message_id && poll.status.message_id !== turnID) return;
        misses = 0;
      } catch (err: unknown) {
        if (++misses > 10) throw err;
        delay = 200;
        continue;
      }
      delay = Math.max(80, 300 - (performance.now() - requestAt));
      if (my !== getEpoch()) return;
      const st = poll.status;
      if (advanceLiveStep(sid, my, st, row)) {
        const fromPrevious = cur.from !== 0 || cur.thinkFrom !== 0;
        cur = { from: 0, thinkFrom: 0 };
        // HTTP 的这一拍是按旧游标取的；不要把新步的半截尾巴当成全文。
        if (fromPrevious && (st.phase === "pending" || st.phase === "streaming")) continue;
      }
      cur = applyTurnStream(st, poll.slice, cur, row);
      if (st.phase === "idle" || st.phase === "error") {
        await onTurnFinished(sid, st);
        return;
      }
    }
  }

  async function pollTurn(sid: string, turnID: string | undefined, existing: AppRow | null = null): Promise<void> {
    const my = getEpoch();
    if (sid !== getSessionID() || activeTurnPolls.get(sid) === my) return;
    activeTurnPolls.set(sid, my);
    const m = existing && vueMessages.value.includes(existing)
      ? existing
      : reactive<AppRow>({ who: "助手", role: "assistant", stream: true, content: "", reasoning: "", id: tmpKey() });
    if (!m.stream) m.stream = true;
    m.phase = "waiting";
    m.tool = null;
    m.sources = [];
    // 起标题还在跑时，最终正文可能已经入库；继续等终态，但不要再画一份合成回复。
    if (!vueMessages.value.includes(m) && !vueMessages.value.some(r => r.messageId && r.messageId === turnID)) vueMessages.value.push(m);
    vueStream.value = { id: turnID ?? null, text: "", reasoning: "" };
    resetTypewriter();
    composerRef.value?.setRunning(true);
    refreshStatusline("pending");
    try {
      let finalStatus: TurnStatus | null | undefined;
      if (typeof EventSource === "function") {
        try {
          finalStatus = await pollTurnEvents(sid, turnID, my, m);
        } catch {
          finalStatus = undefined; // SSE 没起来，走下面的 HTTP 回退
        }
        if (finalStatus === null) return; // 世代/旧轮次：已收手，不回退
      }
      if (finalStatus) {
        await onTurnFinished(sid, finalStatus);
      } else {
        await pollTurnHTTP(sid, turnID, my, m);
      }
    } finally {
      if (activeTurnPolls.get(sid) === my) activeTurnPolls.delete(sid);
      if (my === getEpoch()) composerRef.value?.setRunning(false);
    }
  }

  async function send(content: string, draft: DraftReceipt): Promise<void> {
    const sid = getSessionID();
    if (!sid) throw new Error("还没有会话");
    const my = getEpoch();
    const optimistic = addMessage("你", content, "user");
    const live = startSendingBubble();
    bumpLocalRev();
    let accepted: Accepted;
    beginPending();
    try {
      accepted = await api.sendMessage(sid, content);
    } catch (err: unknown) {
      dropOptimistic(optimistic);
      dropOptimistic(live);
      draft.reject();
      throw err;
    } finally {
      endPending();
    }
    draft.accept();
    refreshTasks();
    if (sid !== getSessionID()) return;
    if (my !== getEpoch()) { await pollTurn(sid, accepted.turn.message_id); return; }
    settleOptimisticUser(optimistic, accepted?.user);
    live.phase = "waiting";
    bumpLocalRev();
    live.run_id = accepted.turn.run_id;
    await pollTurn(sid, accepted.turn.message_id, live);
  }

  async function resend(): Promise<void> {
    const sid = getSessionID();
    if (!sid) throw new Error("还没有会话");
    const live = startSendingBubble();
    let accepted: Accepted;
    try {
      accepted = await api.resend(sid);
    } catch (err: unknown) {
      dropOptimistic(live);
      throw err;
    }
    live.phase = "waiting";
    refreshTasks();
    if (sid !== getSessionID()) return;
    live.run_id = accepted.turn.run_id;
    await pollTurn(sid, accepted.turn.message_id, live);
  }

  return {
    hasLivePoll: () => activeTurnPolls.get(getSessionID() ?? "") === getEpoch(),
    startSendingBubble, send, resend, pollTurn,
  };
}
