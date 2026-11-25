import assert from "node:assert/strict";
import test, { type TestContext } from "node:test";
import { nextTick, watch } from "vue";
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
  const old = Object.getOwnPropertyDescriptor(globalThis, "document");
  Object.defineProperty(globalThis, "document", { configurable: true, value: { getElementById: () => pane.element } });
  t.after(() => {
    if (old) Object.defineProperty(globalThis, "document", old);
    else Reflect.deleteProperty(globalThis, "document");
  });
  const session = { id: "A", epoch: 0 };
  const messages: Message[] = Array.from({ length: 12 }, (_, n) => ({
    idx: n + 1, id: `m${n + 1}`, role: "user", content: "正文", session_id: "A", created_at: 0, updated_at: 0,
  }));
  const ranges: Array<[number, number]> = [];
  const api: SessionWindowDeps["api"] = {
    async listSummaries() { return []; },
    async listMessagesLast(_id, limit) { await options.beforeLast?.(); return messages.slice(-limit); },
    async messageRanges(_id, wanted) {
      ranges.push(...wanted);
      return messages.filter((m) => wanted.some(([lo, hi]) => m.idx! >= lo && (!hi || m.idx! <= hi)));
    },
    async listMessagesRange() { return []; },
  };
  const view = useSessionWindow({
    api, getSessionID: () => session.id, getEpoch: () => session.epoch, getLocalRev: () => 0, isDraft: () => false,
    toast: assert.fail, scrollBottom: () => { pane.scrollTop = pane.scrollHeight - pane.clientHeight; },
    onWindowLoaded: options.onWindowLoaded,
  });
  view.loadingSession.value = false;
  return { view, pane, ranges, session };
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
