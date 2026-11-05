// 流式打字机：把后端一次到一大块的增量，按“上一包到这一包的实测间隔”摊到多帧里露出。
//
// 2026-11-03 用户定：「不是每隔 0.5s 全冒出来，而是在这 0.5s 里平滑打出来」。
// 两个状态别互相镜像：
//   - 调用方持有 `vueStream`（后端完整目标）；
//   - 这里只持有 `streamDisplay`（当前露出的前缀）。
// 轮询/合并仍只认前者，这里只服务动画。
//
// rAF 在后台标签页可能被冻住 ⇒ `waitTypewriter` 超时兜底 flush，不无限等。
import { ref } from "vue";
import { revealCount, typewriterDuration } from "../utils/typewriter.ts";

interface TypewriterState {
  targetText: string;
  targetReasoning: string;
  fromText: number;
  fromReason: number;
  toText: number;
  toReason: number;
  start: number;
  lastPaint: number;
  duration: number;
}

export function useTypewriter() {
  const streamDisplay = ref({ text: "", reasoning: "" });
  let twState: TypewriterState | null = null;
  let twFrame = 0;
  let twLastAt = 0;
  const twWaiters: Array<() => void> = [];

  const nowMs = (): number => (typeof performance !== "undefined" ? performance.now() : Date.now());
  const requestFrame = (cb: (ts: number) => void): number => (
    typeof requestAnimationFrame === "function"
      ? requestAnimationFrame(cb)
      : window.setTimeout(() => cb(nowMs()), 16)
  );
  const cancelFrame = (id: number): void => {
    if (typeof cancelAnimationFrame === "function") cancelAnimationFrame(id);
    else clearTimeout(id);
  };

  function settleTwWaiters(): void {
    const list = twWaiters.splice(0);
    for (const resolve of list) resolve();
  }

  function resetTypewriter(): void {
    if (twFrame) cancelFrame(twFrame);
    twFrame = 0;
    twState = null;
    twLastAt = 0;
    streamDisplay.value = { text: "", reasoning: "" };
    settleTwWaiters();
  }

  function flushTypewriter(): void {
    if (twFrame) cancelFrame(twFrame);
    twFrame = 0;
    const state = twState;
    twState = null;
    if (state) streamDisplay.value = { text: state.targetText, reasoning: state.targetReasoning };
    settleTwWaiters();
  }

  function twTick(ts?: number): void {
    const s = twState;
    if (!s) { twFrame = 0; return; }
    const at = typeof ts === "number" ? ts : nowMs();
    const p = s.duration > 0 ? Math.min(1, (at - s.start) / s.duration) : 1;
    // 30fps 就够了：每帧都重排 markdown 会把 CPU 吃满，而肉眼也看不出 30 与 60 的区别。
    if (p < 1 && at - (s.lastPaint ?? s.start) < 32) {
      twFrame = requestFrame(twTick);
      return;
    }
    s.lastPaint = at;
    const textEnd = revealCount(s.fromText, s.toText, p * s.duration, s.duration);
    const thinkEnd = revealCount(s.fromReason, s.toReason, p * s.duration, s.duration);
    streamDisplay.value = {
      text: s.targetText.slice(0, textEnd),
      reasoning: s.targetReasoning.slice(0, thinkEnd),
    };
    if (p < 1) {
      twFrame = requestFrame(twTick);
    } else {
      twFrame = 0;
      twState = null;
      settleTwWaiters();
    }
  }

  function setStreamTarget(text?: string | null, reasoning?: string | null): void {
    const targetText = text ?? "";
    const targetReasoning = reasoning ?? "";
    const shown = streamDisplay.value;
    let fromText = shown.text.length;
    let fromReason = shown.reasoning.length;
    // 缓冲被重置 / 换了目标（不是前缀）⇒ 从空白重新开始；否则接着当前前缀往后露。
    if (!targetText.startsWith(shown.text) || !targetReasoning.startsWith(shown.reasoning)) {
      fromText = 0;
      fromReason = 0;
      twLastAt = 0;
      streamDisplay.value = { text: "", reasoning: "" };
    }
    const toText = targetText.length;
    const toReason = targetReasoning.length;
    if (toText <= fromText && toReason <= fromReason) return;
    const start = nowMs();
    // 这一拍的时长 = **上一个数据包到这一拍的实测间隔**；第一拍没有上一拍 ⇒ 默认 300ms。
    const gap = twLastAt ? start - twLastAt : 0;
    twLastAt = start;
    twState = {
      targetText, targetReasoning, fromText, fromReason, toText, toReason,
      start, lastPaint: start, duration: typewriterDuration(gap),
    };
    if (!twFrame) twFrame = requestFrame(twTick);
  }

  // waitTypewriter：一轮收尾时先把最后一块放完再 reloadWindow（顶栏/消息别在最后一跳时换掉）。
  function waitTypewriter(maxMs = 0): Promise<void> {
    if (!twState) return Promise.resolve();
    const limit = maxMs > 0 ? maxMs : Math.min(2000, twState.duration + 100);
    return new Promise<void>((resolve) => {
      let done = false;
      const finish = (): void => { if (done) return; done = true; clearTimeout(timer); resolve(); };
      const timer = window.setTimeout(() => { flushTypewriter(); finish(); }, limit);
      twWaiters.push(finish);
    });
  }

  return { streamDisplay, setStreamTarget, waitTypewriter, resetTypewriter, flushTypewriter };
}
