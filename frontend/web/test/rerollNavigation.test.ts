import assert from "node:assert/strict";
import { test } from "node:test";
import { effectScope, ref } from "vue";
import { useReroll } from "../src/composables/useReroll.ts";
import type { RerollAccepted, RerollState } from "../src/api/types.ts";
import { emptyReroll, type AppRow } from "../src/types/app.ts";

function candidateState(count: number, current: number, running = false): RerollState {
  return {
    active: true, target_kind: "message", target_message_id: "reply", count, current_idx: current, running, elapsed_ms: 0,
    items: Array.from({ length: count }, (_, i) => ({ idx: i + 1, pending: running && i + 1 === count,
      at: i, current: i + 1 === current, preview: `第 ${i + 1} 版` })),
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

function harness(canRerollMessage: (id: string) => boolean | Promise<boolean> = () => true) {
  let sid = "a", epoch = 0;
  let state: RerollState = emptyReroll();
  let archived = "原文";
  let output = deferred<void>();
  const accepted = deferred<RerollAccepted>();
  const versionWrites: Array<[string, number]> = [];
  const stateReads: Array<[string, string]> = [];
  const messageEntries: string[] = [];
  const streamDisplay = ref({ text: "", reasoning: "" });
  const scope = effectScope();
  const view = scope.run(() => useReroll({
    api: {
      rerollState: async (id) => { stateReads.push(["message", id]); return id === "a" ? state : emptyReroll(); },
      rerollSummaryState: async (id) => { stateReads.push(["summary", id]); return emptyReroll(); },
      rerollEnter: (id) => { messageEntries.push(id); return accepted.promise; },
      rerollSummaryEnter: async () => { throw new Error("不是摘要重摇"); },
      rerollSummarySwitch: async () => { throw new Error("不是摘要切版"); },
      rerollSwitch: async (id, idx) => { versionWrites.push([id, idx]); archived = `第 ${idx} 版的归档`; return { ...state, current_idx: idx }; },
      turnText: async () => ({ text: "还在生成的字", from: 0, next: 18, thinking: "", think_from: 0, think_next: 0, done: false }),
    },
    toast: (message) => assert.fail(message),
    getSessionID: () => sid, getEpoch: () => epoch, getLastMsgId: () => "reply", canRerollMessage: () => canRerollMessage(sid), isWindowLoading: () => false,
    vueSegments: ref([]), streamDisplay,
    setStreamTarget: (text, reasoning) => { streamDisplay.value = { text, reasoning }; output.resolve(); },
    resetTypewriter: () => { streamDisplay.value = { text: "", reasoning: "" }; },
    waitTypewriter: async () => {}, refreshStatusline: () => {}, refreshTasks: () => {},
    reloadWindow: async () => true, onVersionWritten: () => {}, refreshStateView: () => {},
  }))!;
  const changeView = (id: string) => {
    sid = id; epoch++; output = deferred<void>(); streamDisplay.value = { text: "", reasoning: "" }; view.resetRerollView();
    return view.refreshRerolls();
  };
  const row = (): AppRow => ({ id: "reply", messageId: "reply", role: "assistant", content: archived });
  return { view, accepted, versionWrites, stateReads, messageEntries, row, changeView, setState: (s: RerollState) => { state = s; }, waitForText: () => output.promise, close: () => scope.stop() };
}

test("重摇切出再切回：恢复流文字，只应用原会话新生成的那一版", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = harness();
  t.after(h.close);
  const pending = candidateState(2, 1, true);
  h.accepted.resolve({ state: pending });
  h.setState(pending);
  const initial = h.view.rerollNow("reply");
  await h.waitForText();
  assert.equal(h.view.bubbleContent(h.row()), "还在生成的字");
  await h.changeView("b");
  assert.equal(h.view.isLive(h.row()), false);
  assert.deepEqual(h.versionWrites, []);
  t.mock.timers.tick(400);
  await initial;
  const returned = h.changeView("a");
  await h.waitForText();
  assert.equal(h.view.bubbleContent(h.row()), "还在生成的字");
  h.setState(candidateState(2, 1));
  t.mock.timers.tick(400);
  await returned;
  assert.deepEqual(h.versionWrites, [["a", 2]]);
  assert.equal(h.view.bubbleContent(h.row()), "第 2 版的归档");
  assert.equal(h.view.rerollState.value.current_idx, 2);
});

test("切出时才收到重摇受理，但后台取消：回来后不换到已有的另一版", async (t) => {
  const h = harness();
  t.after(h.close);
  h.setState(candidateState(2, 1));
  await h.view.refreshRerolls();
  const initial = h.view.rerollNow("reply");
  await h.changeView("b");
  h.accepted.resolve({ state: candidateState(3, 1, true) });
  await initial;
  assert.equal(h.view.isLive(h.row()), false);
  await h.changeView("a");
  assert.deepEqual(h.versionWrites, []);
  assert.equal(h.view.rerollState.value.current_idx, 1);
  assert.equal(h.view.bubbleContent(h.row()), "原文");
});

test("助手类型未加载时只问摘要；认出 FlowAgent 后不请求消息重摇", async (t) => {
  const agent = deferred<boolean>();
  const h = harness((id) => id === "a" ? agent.promise : true);
  t.after(h.close);
  const refresh = h.view.refreshRerolls();
  const enter = h.view.rerollNow("reply");
  assert.deepEqual(h.stateReads, [["summary", "a"]]);
  assert.deepEqual(h.messageEntries, []);
  agent.resolve(false);
  await Promise.all([refresh, enter]);
  assert.deepEqual(h.stateReads, [["summary", "a"]]);
  assert.deepEqual(h.messageEntries, []);
  await h.changeView("b");
  assert.deepEqual(h.stateReads, [["summary", "a"], ["summary", "b"], ["message", "b"]]);
});

test("等待助手类型时切走，迟到的传统助手判定不请求旧会话", async (t) => {
  const agent = deferred<boolean>();
  const h = harness((id) => id === "a" ? agent.promise : false);
  t.after(h.close);
  const refresh = h.view.refreshRerolls();
  const enter = h.view.rerollNow("reply");
  await h.changeView("b");
  agent.resolve(true);
  await Promise.all([refresh, enter]);
  assert.deepEqual(h.stateReads, [["summary", "a"], ["summary", "b"]]);
  assert.deepEqual(h.messageEntries, []);
  assert.equal(h.view.bubbleContent(h.row()), "原文");
});
