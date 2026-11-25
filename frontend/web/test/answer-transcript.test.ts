import assert from "node:assert/strict";
import { test } from "node:test";
import type { ChatMessage, ChatRow, ChatRowMessage } from "../src/utils/chatrows.ts";
import { transcriptItems, answerSteps, processLabel } from "../src/utils/answerTranscript.ts";
import { copyMarkdownBody } from "../src/utils/answerCopy.ts";
import { splitState } from "../src/utils/markdown.ts";

function message(idx: number, msg: ChatMessage = {}): ChatRowMessage {
  return { kind: "msg", key: `m${idx}`, displayIdx: idx, msg: { role: "assistant", messageId: `m${idx}`, idx, run_id: "r", status: "done", ...msg } };
}
function group(rows: ChatRow[], from = 1) {
  const items = transcriptItems(rows, from);
  assert.equal(items.length, 1);
  const item = items[0];
  assert.equal(item.kind, "answer");
  return item;
}
const call = { tool_name: "dice", status: "done" };

test("相同 run 只连相邻的助手记录，不跨用户、摘要、旧记录、编号缺口或另一轮", () => {
  const left = message(1, { content: "甲" }), right = message(3, { content: "乙" });
  for (const barrier of [message(2, { role: "user" }), message(2, { run_id: undefined }), message(2, { run_id: "  " }), message(2, { run_id: "other" }),
    { kind: "summary", key: "sum-s", summary: { id: "s" }, fromIdx: 2, toIdx: 2, rows: null, loading: false } satisfies ChatRow]) {
    assert.deepEqual(transcriptItems([left, barrier, right], 1), [left, barrier, right]);
  }
  assert.deepEqual(transcriptItems([left, right], 1), [left, right]);
  const old = message(1, { kind: "tool_run", run_id: undefined, tools: [call] });
  const neighbor = message(2, { kind: "tool_run", run_id: undefined, tools: [call] });
  const archive = transcriptItems([old, neighbor], 1);
  assert.deepEqual(archive.map(item => item.kind === "answer" && item.rows), [[old], [neighbor]]);
});

test("收起时每段正文和状态按原序出现一次，展开回到实际消息，数字按调用而非步骤", () => {
  const rows = [message(1, { tools: [call, call], reasoning: "思考" }), message(2, { content: "中间说明", tools: [call] }),
    message(3, { content: "<state>HP = 1</state>", tools: [call] }), message(4, { content: "最终回答", usage: { output_tokens: 6 } })];
  const item = group(rows);
  assert.equal(processLabel(item), "已完成 4 次工具调用");
  const closed = answerSteps(item, false, false);
  assert.deepEqual(closed.map(step => step.row), rows.slice(1));
  assert.deepEqual(closed.map(step => step.presentation.answerView), ["prose", "prose", "prose-footer"]);
  for (const expanded of [false, true]) {
    assert.deepEqual(answerSteps(item, expanded, false).filter(step => step.presentation.copyAnswer).map(step => step.row), [rows[3]]);
  }
  assert.deepEqual(answerSteps(item, true, false).map(step => step.row), rows);
});

test("单条托管工具保留正文时间线；仅工具或失败结尾仍展示状态与可操作的消息", () => {
  const hosted = message(1, { content: "前\n\n后", kind: "tool_run", tools: [{ ...call, text_offset: 2 }] });
  const item = group([hosted]);
  assert.equal(processLabel(item), "已完成 1 次工具调用");
  assert.equal(answerSteps(item, false, false)[0].presentation.answerView, "prose-footer");
  assert.equal(answerSteps(item, true, false)[0].presentation.answerView, "full");
  for (const [status, label] of [["failed", "失败"], ["interrupted", "已中断"], ["canceled", "已取消"]] as const) {
    const plain = message(1, { status });
    assert.deepEqual(transcriptItems([plain], 1), [plain]); // 无额外过程，不能凭一次取消造出空展开入口
    const item = group([message(1, { kind: "tool_run", tools: [{ ...call, status: "failed" }], status })]);
    assert.ok(processLabel(item).includes(`回答${label}`));
    assert.ok(processLabel(item).includes("工具调用失败"));
    assert.ok(!processLabel(item).includes("已完成"));
    assert.equal(answerSteps(item, false, false)[0].presentation.answerView, "prose-footer");
  }
  const lastTool = message(1, { tools: [call] });
  const error = message(2, { noIdx: true, messageId: undefined, status: "failed" });
  error.displayIdx = null;
  assert.equal(answerSteps(group([lastTool, error]), false, false)[0].presentation.answerView, "prose-footer");
});

test("工具独占一步时，默认阅读视图也保留有位置的服务端工具", () => {
  const search = message(1, { tools: [{ source: "server", tool_name: "web_search", body_offset: 0 }] });
  const reply = message(2, { content: "使用检索结果写的回答" });
  assert.deepEqual(answerSteps(group([search, reply]), false, false).map(({ row }) => row), [search, reply]);
});

test("分页只报可见调用，实时后续步骤无调用数时不臆测；重摇忽略候选的旧工具", () => {
  const prior = message(7, { kind: "tool_run", tools: [call] });
  const partial = group([prior, message(8, { content: "回答" })], 7);
  assert.equal(processLabel(partial), "当前可见 1 次工具调用");
  const live = message(8, { stream: true, step: 4, tool: { kind: "web_search", state: "searching" } });
  assert.equal(processLabel(group([live], 8)), "正在搜索网页…");
  const liveGroup = group([prior, live], 7);
  assert.match(processLabel(liveGroup), /当前可见 1 次工具调用.*正在搜索网页/);
  assert.equal(answerSteps(liveGroup, false, true).at(-1)?.row, live);
  const archived = message(8, { tools: [{ ...call, status: "failed" }], content: "归档文本", status: "failed" });
  const candidate = group([prior, archived], 7);
  assert.equal(processLabel(candidate, "m8"), "当前可见 1 次工具调用 · 正在重新生成回答…");
  assert.equal(answerSteps(candidate, false, false, "m8").at(-1)?.presentation.candidate, true);
});

test("复制只取当前 Markdown 正文，保留链接、代码和原样字符，宏不递归展开", () => {
  const content = "**{{user}}** [资料](https://example.org)\n\n```js\nconst x = '<b>$$</b>'\n```\n<state>HP = 7</state>";
  assert.equal(copyMarkdownBody(splitState(content).cleaned, { user: "甲$&{{user}}" }),
    "**甲$&{{user}}** [资料](https://example.org)\n\n```js\nconst x = '<b>$$</b>'\n```");
});
