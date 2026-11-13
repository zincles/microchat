// 一轮生成的流：发送受理、SSE 优先、HTTP 轮询回退、游标合并与 step 切换。
//
// 组件化边界：这里只管“流怎么走完”，落库后的界面刷新交给 App 的 onTurnFinished
//（消息列/状态/占用/任务/预演各自都在 App 的域里）。增量的合并规则仍在 api/client.ts，
// 这里只负责把 status 与 slice 喂给打字机。
import type { Ref } from "vue";
import type { Accepted, StreamCursor, StreamSlice, TurnPoll, TurnStatus } from "../api/types.ts";
import type { AppRow } from "../types/app.ts";
import { mergeStreamSlice } from "../api/client.ts";

interface TurnApi {
  sendMessage(id: string, content: string): Promise<Accepted>;
  resend(id: string): Promise<Accepted>;
  turnPoll(id: string, from: number, thinkFrom: number): Promise<TurnPoll>;
  turnEventsUrl(id: string, from: number, thinkFrom: number): string;
}

interface ComposerHandle {
  setRunning(running: boolean): void;
  setText(text: string): void;
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
  send: (content: string) => Promise<void>;
  resend: () => Promise<void>;
  pollTurn: (sid: string, turnID: string | undefined, existing?: AppRow | null) => Promise<void>;
  hasLivePoll: () => boolean;
}

export function useTurnStream({
  api, getSessionID, getEpoch, vueMessages, vueStream, addMessage, settleOptimisticUser, dropOptimistic,
  tmpKey, composerRef, resetTypewriter, setStreamTarget, refreshStatusline, refreshTasks,
  bumpLocalRev, beginPending, endPending, onTurnFinished,
}: TurnStreamDeps): TurnStream {
  // 本地此刻有几条轮询循环真的在跑（0 或 1）。
  // 为什么需要这个数，而不是“看有没有 stream 气泡”：气泡是**一次性的**，循环死掉而气泡还留着
  // —— 那个气泡就是僵住的假象。判“要不要接回来”必须看循环本身在不在。
  let liveTurnPoll = 0;
  // 同一个会话只允许一个轮询循环：前台对齐/重进会话/发送三路都可能喊 pollTurn，
  // 两个循环各带各的游标写同一个 streamDisplay ⇒ 字符会重复/跳序。
  const activeTurnPolls = new Set<string>();

  function startSendingBubble(): AppRow {
    const row = addMessage("助手", "", "assistant");
    row.stream = true;
    row.phase = "sending";
    row.step = 1;
    return row;
  }

  function finishLiveRow(row: AppRow): void {
    row.stream = false;
    row.phase = "";
    row.content = vueStream.value.text ?? "";
    row.reasoning = vueStream.value.reasoning ?? "";
    row.tool = null;
    row.sources = [];
  }

  function startLiveStep(step: number): AppRow {
    const row = addMessage("助手", "", "assistant");
    row.stream = true;
    row.phase = "waiting";
    row.step = step;
    return row;
  }

  function advanceLiveStep(st: TurnStatus, row: AppRow | null): { row: AppRow; cur: StreamCursor } | null {
    const step = Number(st.step || 0);
    if (!step || !row || step === Number(row.step || 0)) return null;
    finishLiveRow(row);
    const next = startLiveStep(step);
    vueStream.value = { id: vueStream.value.id, text: "", reasoning: "" };
    resetTypewriter();
    return { row: next, cur: { from: 0, thinkFrom: 0 } };
  }

  function applyTurnStream(st: TurnStatus, slice: StreamSlice, cur: StreamCursor, row: AppRow): StreamCursor {
    if (st.step) row.step = st.step;
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
    let liveRow = row;
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
          // 迟到的旧连接拿到的是新一轮：立刻收手，别把新的字写进旧的显示前缀。
          if (turnID && st.message_id && st.message_id !== turnID && st.phase !== "idle") {
            source.close(); resolve(null); return;
          }
          const advanced = advanceLiveStep(st, liveRow);
          if (advanced) { liveRow = advanced.row; cur = advanced.cur; }
          cur = applyTurnStream(st, slice, cur, liveRow);
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
    let liveRow = row;
    let misses = 0;
    let delay = 200;
    for (;;) {
      await new Promise((r) => setTimeout(r, delay));
      if (my !== getEpoch()) return;
      let poll: TurnPoll;
      const requestAt = performance.now();
      try {
        poll = await api.turnPoll(sid, cur.from, cur.thinkFrom);
        if (turnID && poll.status.message_id && poll.status.message_id !== turnID && poll.status.phase !== "idle") return;
        misses = 0;
      } catch (err: unknown) {
        if (++misses > 10) throw err;
        delay = 200;
        continue;
      }
      delay = Math.max(80, 300 - (performance.now() - requestAt));
      if (my !== getEpoch()) return;
      const st = poll.status;
      const advanced = advanceLiveStep(st, liveRow);
      if (advanced) { liveRow = advanced.row; cur = advanced.cur; }
      cur = applyTurnStream(st, poll.slice, cur, liveRow);
      if (st.phase === "idle" || st.phase === "error") {
        await onTurnFinished(sid, st);
        return;
      }
    }
  }

  async function pollTurn(sid: string, turnID: string | undefined, existing: AppRow | null = null): Promise<void> {
    if (activeTurnPolls.has(sid)) return;
    activeTurnPolls.add(sid);
    const my = getEpoch();
    liveTurnPoll++;
    const m = existing && vueMessages.value.includes(existing)
      ? existing
      : { who: "助手", role: "assistant", stream: true, content: "", reasoning: "", id: tmpKey() } as AppRow;
    if (!m.stream) m.stream = true;
    m.phase = "waiting";
    m.tool = null;
    m.sources = [];
    m.step = 1;
    if (!vueMessages.value.includes(m)) vueMessages.value.push(m);
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
      activeTurnPolls.delete(sid);
      liveTurnPoll--;
      composerRef.value?.setRunning(false);
    }
  }

  async function send(content: string): Promise<void> {
    const sid = getSessionID();
    if (!sid) throw new Error("还没有会话");
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
      composerRef.value?.setText(content);
      throw err;
    } finally {
      endPending();
    }
    settleOptimisticUser(optimistic, accepted?.user);
    live.phase = "waiting";
    bumpLocalRev();
    refreshTasks();
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
    await pollTurn(sid, accepted.turn.message_id, live);
  }

  return {
    startSendingBubble, send, resend, pollTurn,
    hasLivePoll: () => liveTurnPoll > 0,
  };
}
