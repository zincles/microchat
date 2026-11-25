import { strict as assert } from 'node:assert';
import { test, type TestContext } from 'node:test';
import { ref } from 'vue';
import { useTurnStream, type TurnStreamDeps } from '../src/composables/useTurnStream.ts';
import { createSessionDrafts } from '../src/utils/sessionDrafts.ts';
import type { Accepted, Message, TurnPoll } from '../src/api/types.ts';
import type { AppRow } from '../src/types/app.ts';
import { buildChatRows } from '../src/utils/chatrows.ts';

function harness(t: TestContext) {
  const feeds: Feed[] = [];
  class Feed {
    onmessage?: (event: { data: string }) => void;
    constructor() { feeds.push(this); }
    close() {}
    frame(phase: 'streaming' | 'idle', step: number, text: string) {
      const payload: TurnPoll = { status: { phase, step, message_id: 'reply', run_id: 'run', elapsed_ms: 0, chars: text.length, thinking_chars: 0 },
        slice: { text, next: Buffer.byteLength(text), thinking: '', think_next: 0, done: phase === 'idle' } };
      this.onmessage!({ data: JSON.stringify(payload) });
    }
  }
  const original = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: Feed });
  t.after(() => { if (original) Object.defineProperty(globalThis, 'EventSource', original); else Reflect.deleteProperty(globalThis, 'EventSource'); });
  const ui = { sid: 'a', epoch: 1, running: false, text: '', finished: 0 };
  const rows = ref<AppRow[]>([]);
  const history = Promise.withResolvers<Message[]>();
  const receipt = Promise.withResolvers<Accepted>();
  let seq = 0;
  const deps: TurnStreamDeps = {
    api: { listMessagesLast: () => history.promise, sendMessage: () => receipt.promise,
      resend: () => { throw Error('意外重发'); }, turnPoll: () => { throw Error('意外轮询'); }, turnEventsUrl: () => 'fixture' },
    getSessionID: () => ui.sid, getEpoch: () => ui.epoch, vueMessages: rows, vueStream: ref({ id: null, text: '', reasoning: '' }),
    addMessage(who, content, role) { rows.value.push({ id: `tmp${++seq}`, who, content, role }); return rows.value.at(-1)!; },
    dropOptimistic(row) { rows.value = rows.value.filter(x => x !== row); }, settleOptimisticUser() {}, tmpKey: () => `tmp${++seq}`,
    composerRef: ref({ setRunning(on: boolean) { ui.running = on; } }), resetTypewriter() { ui.text = ''; },
    setStreamTarget(text) { ui.text = text; }, refreshStatusline() {}, refreshTasks() {}, bumpLocalRev() {}, beginPending() {}, endPending() {},
    async onTurnFinished() { ui.finished++; },
  };
  return { ui, rows, history, receipt, feeds, stream: useTurnStream(deps) };
}

test('受理回执还没有 idx 时，已归档的工具步骤不能被排序到提问之前', () => {
  const rows = buildChatRows({ windowFrom: 1, messages: [
    { id: 'user', messageId: 'user', role: 'user', run_id: 'user', content: '查资料' },
    { id: 'tool', messageId: 'tool', role: 'assistant', run_id: 'user', idx: 2, kind: 'tool_run' },
    { id: 'live', role: 'assistant', run_id: 'user', stream: true },
  ] });
  assert.deepEqual(rows.map(r => [r.key, r.kind === 'msg' && r.displayIdx]), [['user', 1], ['tool', 2], ['live', 3]]);
});

// 首帧可以直接落在第二个模型轮：第一步不能凭空造，也不能把两次调用算成一步。
test('晚加入的回复以已存工具记录补齐过程，保留正在读的答案', async t => {
  const { stream, feeds, history, rows, ui } = harness(t);
  const done = stream.pollTurn('a', 'reply');
  feeds[0].frame('streaming', 2, '答案');
  history.resolve([{ id: 'step', idx: 2, session_id: 'a', created_at: 0, updated_at: 0, role: 'assistant', run_id: 'run', content: '先查一下',
    kind: 'tool_run', tools: [{ tool_name: 'web_search', result: '资料一' }, { tool_name: 'web_search', result: '资料二' }] }]);
  await history.promise;
  assert.deepEqual(rows.value.map(r => [r.run_id, r.content, r.tools?.length, !!r.stream]),
    [['run', '先查一下', 2, false], ['run', '', undefined, true]]);
  assert.equal(ui.text, '答案');
  feeds[0].frame('idle', 2, '');
  await done;
  assert.equal(ui.finished, 1);
});

test('快速切出再回到同一会话，旧流收尾不能关掉新流', async t => {
  const { stream, feeds, history, rows, ui } = harness(t);
  const old = stream.pollTurn('a', 'reply');
  feeds[0].frame('streaming', 2, '旧');
  ui.epoch += 2;
  rows.value = [];
  const current = stream.pollTurn('a', 'reply');
  feeds[1].frame('streaming', 1, '新');
  feeds[0].frame('idle', 2, '');
  history.resolve([]);
  await old;
  assert.equal(stream.hasLivePoll(), true);
  assert.equal(ui.running, true);
  assert.equal(ui.text, '新');
  assert.equal(ui.finished, 0);
  feeds[1].frame('idle', 1, '');
  await current;
});

test('消息受理失败只退回原会话的输入，别处仍是自己的草稿', async t => {
  const { stream, receipt, rows, ui } = harness(t);
  const drafts = createSessionDrafts();
  drafts.set('a', ' 原话 ');
  const sending = stream.send('原话', drafts.take('a'));
  assert.equal(rows.value[0].content, '原话');
  assert.equal(rows.value[1].phase, 'sending');
  ui.epoch++; ui.sid = 'b'; rows.value = [];
  drafts.set('b', '另一个问题');
  receipt.reject(new Error('拒绝受理'));
  await assert.rejects(sending, /拒绝受理/);
  assert.equal(drafts.get('a'), ' 原话 ');
  assert.equal(drafts.get('b'), '另一个问题');
  assert.equal(rows.value.length, 0);
});
