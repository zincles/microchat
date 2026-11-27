import assert from "node:assert/strict";
import test, { type TestContext } from "node:test";
import { effectScope, nextTick, shallowRef, watch } from "vue";
import { useSessionWindow, type SessionWindowDeps } from "../src/composables/useSessionWindow.ts";
import type { Message } from "../src/api/types.ts";
import { applyReadingPosition, captureReadingPosition } from "../src/utils/readingPosition.ts";
import { recallWindow } from "../src/utils/windowcache.ts";

// Node 没有布局引擎。给出行在内容中的位置，视口坐标随 scrollTop 变化，与浏览器一致。
class Pane {
  scrollTop = 0;
  clientHeight = 180;
  clientWidth = 400;
  scrollHeight = 1000;
  visible = true;
  rows: Array<[string, number, number]> = [];
  getBoundingClientRect() { return { top: 30, bottom: 30 + this.clientHeight }; }
  checkVisibility() { return this.visible; }
  querySelectorAll(selector: string) {
    assert.equal(selector, "[data-reading-key]");
    return this.rows.map(([key, start, height]) => ({
      dataset: { readingKey: key },
      getBoundingClientRect: () => ({ top: 30 + start - this.scrollTop, bottom: 30 + start + height - this.scrollTop, height }),
    }));
  }
  get element(): HTMLElement { return this as unknown as HTMLElement; }
}

test("阅读锚保留首个可见行及行内偏移；锚消失时钳住像素位置，贴底时追到底", () => {
  const p = new Pane();
  p.scrollTop = 220;
  p.rows = [["old", 0, 200], ["sum-id", 200, 260], ["next", 460, 100]];
  const saved = captureReadingPosition(p.element)!;
  assert.deepEqual(saved, { kind: "row", key: "sum-id", offset: -20, top: 220 });
  p.rows = [["new", 0, 345], ["sum-id", 345, 260]];
  applyReadingPosition(p.element, saved);
  assert.equal(p.scrollTop, 365);
  p.rows = [];
  p.scrollHeight = 250;
  applyReadingPosition(p.element, saved);
  assert.equal(p.scrollTop, 70);
  const bottom = captureReadingPosition(p.element)!;
  p.scrollHeight = 640;
  applyReadingPosition(p.element, bottom);
  assert.equal(p.scrollTop, 460);
});

function fixture(t: TestContext, options: { beforeLast?: () => Promise<void>; onWindowLoaded?: () => void } = {}) {
  const pane = new Pane();
  const element = shallowRef<HTMLElement | null>(null);
  const scope = effectScope();
  t.after(() => scope.stop());
  const session = { id: "A", epoch: 0 };
  const messages: Message[] = Array.from({ length: 12 }, (_, n) => ({
    idx: n + 1, id: `m${n + 1}`, role: "user", content: "正文", session_id: "A", created_at: 0, updated_at: 0,
  }));
  const ranges: Array<[number, number]> = [];
  const revisions: { summaries?: string | null; recent?: string | null; ranges?: string | null } = {
    summaries: "r1", recent: "r1", ranges: "r1",
  };
  const capture = (key: keyof typeof revisions, callback?: (revision: string | null) => void) => {
    if (key in revisions) callback?.(revisions[key] ?? null);
  };
  const api: SessionWindowDeps["api"] = {
    async listSummaries(_id, captureRevision) { capture("summaries", captureRevision); return []; },
    async listMessagesLast(_id, limit, captureRevision) {
      await options.beforeLast?.();
      capture("recent", captureRevision);
      return messages.slice(-limit);
    },
    async messageRanges(_id, wanted, captureRevision) {
      capture("ranges", captureRevision);
      ranges.push(...wanted);
      return messages.filter((m) => wanted.some(([lo, hi]) => m.idx! >= lo && (!hi || m.idx! <= hi)));
    },
    async listMessagesRange() { return []; },
  };
  const view = scope.run(() => useSessionWindow({
    api, getSessionID: () => session.id, getEpoch: () => session.epoch, getLocalRev: () => 0, isDraft: () => false,
    toast: assert.fail, pane: element,
    onWindowLoaded: options.onWindowLoaded,
  }))!;
  view.loadingSession.value = false;
  element.value = pane.element;
  return { view, pane, ranges, revisions, session, messages };
}

test("加载更早历史后缓存保留整页，缓存失效仍从书签页拉起；隐藏或骨架不覆盖真书签", async (t) => {
  const { view, pane, ranges, session } = fixture(t);
  await view.loadWindow({ reset: true, scroll: false });
  assert.equal(view.windowFrom.value, 8);
  await view.loadMore();
  const cached = recallWindow(view.windowCache, "A")!;
  assert.equal(cached.windowFrom, 3);
  assert.deepEqual(cached.rows.map((r) => r.idx), [3, 4, 5, 6, 7, 8, 9, 10, 11, 12]);
  pane.rows = [["m5", 200, 160]];
  pane.scrollTop = 215;
  view.rememberReadingPosition();
  pane.visible = false;
  pane.scrollTop = 0;
  view.rememberReadingPosition();
  pane.visible = true;
  pane.clientHeight = 0;
  view.rememberReadingPosition();
  pane.clientHeight = 180;
  view.loadingSession.value = true;
  view.rememberReadingPosition();
  view.windowCache.clear();
  session.id = "B"; session.epoch++;
  view.loadingSession.value = false;
  view.restoreReadingPosition();
  await nextTick();
  assert.equal(pane.scrollTop, 820);
  session.id = "A"; session.epoch++;
  view.windowFrom.value = 1;
  ranges.length = 0;
  assert.equal(await view.loadWindow({ resume: true, scroll: false }), true);
  assert.deepEqual(ranges, [[3, 7]]);
  assert.equal(view.windowFrom.value, 3);
  pane.rows = [["m5", 260, 160]];
  view.restoreReadingPosition();
  await nextTick();
  assert.equal(pane.scrollTop, 275);
  view.forgetReadingPosition("A");
  view.rememberReadingPosition();
  session.epoch++;
  view.restoreReadingPosition();
  await nextTick();
  assert.equal(pane.scrollTop, 820);
});

test("后台对账续用响应回来时的可见行，不把等网络时向上滚动的人拽回底部", async (t) => {
  const gate = Promise.withResolvers<void>();
  const { view, pane } = fixture(t, {
    beforeLast: () => gate.promise,
    onWindowLoaded: () => { pane.rows = [["m6", 300, 240]]; },
  });
  pane.scrollTop = 820;
  const loading = view.reloadWindowKeepingScroll({ onlyIfUnchanged: true });
  pane.rows = [["m6", 180, 240]];
  pane.scrollTop = 205;
  gate.resolve();
  assert.equal(await loading, true);
  assert.equal(pane.scrollTop, 325);
});

test("旧请求与尚未执行的回位都不能动新会话；即使又回到相同 ID 也算新世代", async (t) => {
  const gate = Promise.withResolvers<void>();
  const { view, pane, session } = fixture(t, { beforeLast: () => gate.promise });
  pane.scrollTop = 596;
  view.restoreReadingPosition();
  const loading = view.reloadWindowKeepingScroll();
  session.id = "B"; session.epoch++;
  await nextTick();
  assert.equal(pane.scrollTop, 596);
  session.id = "A"; session.epoch++;
  pane.scrollTop = 250;
  gate.resolve();
  assert.equal(await loading, false);
  assert.equal(pane.scrollTop, 250);
  assert.equal(view.vueMessages.value.length, 0);
});

test("对账途中翻页不缩回尾页；新旧工具步骤归入一组而换锚时仍补偿新增高度", async (t) => {
  const gate = Promise.withResolvers<void>();
  let waiting = false;
  const { view, pane } = fixture(t, { beforeLast: async () => { if (waiting) await gate.promise; } });
  await view.loadWindow({ reset: true, scroll: false });
  waiting = true;
  const reconciling = view.reloadWindowKeepingScroll({ onlyIfUnchanged: true });
  pane.rows = [["m8", 100, 200]];
  pane.scrollTop = 135;
  const stop = watch(view.windowFrom, () => {
    pane.rows = [["m4", 240, 440]];
    pane.scrollHeight += 270;
  });
  t.after(stop);
  await view.loadMore();
  assert.equal(pane.scrollTop, 405);
  gate.resolve();
  assert.equal(await reconciling, false);
  assert.equal(view.windowFrom.value, 3);
  assert.equal(pane.scrollTop, 405);
});

test("尾部随内容和视口变化；向上翻后保留读位，新回复只提示不跳转", async (t) => {
  let resize!: () => void;
  const original = Object.getOwnPropertyDescriptor(globalThis, "ResizeObserver");
  Object.defineProperty(globalThis, "ResizeObserver", { configurable: true, value: class {
    constructor(callback: () => void) { resize = callback; }
    observe() {}
    disconnect() {}
  } });
  t.after(() => {
    if (original) Object.defineProperty(globalThis, "ResizeObserver", original);
    else Reflect.deleteProperty(globalThis, "ResizeObserver");
  });
  const { view, pane } = fixture(t);
  view.jumpToLatest();
  await nextTick();
  pane.scrollHeight = 1170;
  view.onReadingScroll(); // 程序滚动的事件晚于下一批内容，不算主动向上翻。
  resize();
  assert.equal(pane.scrollTop, 990);
  assert.equal(view.awayFromLatest.value, false);

  pane.scrollTop = 790;
  view.onReadingScroll();
  assert.equal(view.awayFromLatest.value, true);
  assert.equal(view.hasNewReply.value, false);
  view.noteNewReply();
  pane.scrollHeight = 1360;
  pane.clientHeight = 90;
  resize();
  assert.equal(pane.scrollTop, 790);
  assert.equal(view.hasNewReply.value, true);

  view.jumpToLatest();
  assert.equal(view.hasNewReply.value, false);
  pane.scrollHeight = 1500;
  resize();
  assert.equal(pane.scrollTop, 1410);
  assert.equal(view.awayFromLatest.value, false);
});

test("切回旧读位时提示离开期间落库的回复，不把其他会话标成未读", async (t) => {
  const { view, pane, session, messages } = fixture(t);
  await view.loadWindow({ reset: true });
  pane.scrollTop = 300;
  pane.rows = [["m8", 280, 220]];
  view.onReadingScroll();
  view.rememberReadingPosition();

  session.id = "B"; session.epoch++;
  view.resetReadingTail();
  view.restoreReadingPosition();
  await nextTick();
  assert.equal(pane.scrollTop, 820);
  assert.equal(view.hasNewReply.value, false);

  session.id = "A"; session.epoch++;
  view.resetReadingTail();
  messages.push({ ...messages[11], id: "m13", idx: 13, role: "assistant" });
  await view.loadWindow({ resume: true, scroll: false });
  pane.scrollHeight = 1200;
  view.restoreReadingPosition();
  await nextTick();
  assert.equal(pane.scrollTop, 300);
  assert.equal(view.hasNewReply.value, true);
  pane.scrollTop = 1020;
  view.onReadingScroll();
  assert.equal(view.hasNewReply.value, false);
});

test("窗口修订只标记一致的已显示快照，缓存恢复可用且会话世代变化后失效", async (t) => {
  const { view, session } = fixture(t);
  await view.loadWindow({ reset: true, scroll: false });
  assert.equal(view.hasHistoryRevision("r1"), true);
  assert.equal(recallWindow(view.windowCache, "A")?.historyRevision, "r1");

  view.resetWindowRequests();
  assert.equal(view.hasHistoryRevision("r1"), false);
  assert.equal(view.restoreCachedWindow("A"), true);
  assert.equal(view.hasHistoryRevision("r1"), true);

  session.id = "B"; session.epoch++;
  session.id = "A"; session.epoch++;
  assert.equal(view.hasHistoryRevision("r1"), false);
  assert.equal(view.restoreCachedWindow("A"), true);
  assert.equal(view.hasHistoryRevision("r1"), true);
});

test("混合、缺失或分页不匹配的修订不会标记窗口", async (t) => {
  const mixed = fixture(t);
  mixed.revisions.recent = "r2";
  await mixed.view.loadWindow({ reset: true, scroll: false });
  assert.equal(mixed.view.hasHistoryRevision("r1"), false);

  const absent = fixture(t);
  delete absent.revisions.recent;
  await absent.view.loadWindow({ reset: true, scroll: false });
  assert.equal(absent.view.hasHistoryRevision("r1"), false);

  const paged = fixture(t);
  await paged.view.loadWindow({ reset: true, scroll: false });
  assert.equal(paged.view.hasHistoryRevision("r1"), true);
  paged.revisions.ranges = "r2";
  await paged.view.loadMore();
  assert.equal(paged.view.hasHistoryRevision("r1"), false);
  assert.equal(recallWindow(paged.view.windowCache, "A")?.historyRevision, undefined);
});
