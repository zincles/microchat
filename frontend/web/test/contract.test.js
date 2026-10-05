import { strict as assert } from "node:assert";
import { test } from "node:test";
import { createApi } from "../src/api/client.js";
import { parseCommand, filterCommands } from "../src/utils/commands.js";
import { advanceCursor, buildTurnTextPath } from "../src/utils/cursor.js";
import {
  formatThinkLabel,
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  deletionPlanSummary,
  SETTINGS_TABS,
  switchSettingsTab,
  snapshotSettings,
  settingsDirty,
  formatRoutesOutcome,
  buildAbilitiesPatch,
  renderMessage,
} from "../src/components/MessageBubble.js";

// 契约测试（无浏览器）：只验纯函数与请求形状。`node --test test/`。
// 后端真服务验证另走手工（见 README）。

test("API 基址拼接不丢 /api/v1", () => {
  const API = "http://127.0.0.1:8787/api/v1";
  assert.equal(API + "/sessions", "http://127.0.0.1:8787/api/v1/sessions");
  assert.equal(API + "/health", "http://127.0.0.1:8787/api/v1/health");
});

test("游标读推进：next/think_next 只增不减", () => {
  let from = 0, thinkFrom = 0;
  const slices = [
    { text: "你好", thinking: "", next: 2, think_next: 0 },
    { text: "，世界", thinking: "想", next: 5, think_next: 1 },
  ];
  let text = "";
  for (const s of slices) {
    text += s.text;
    assert.ok(s.next >= from && s.think_next >= thinkFrom);
    from = s.next;
    thinkFrom = s.think_next;
  }
  assert.equal(text, "你好，世界");
});

test("命令解析：/compact 10 → 名+参数；普通文本 → null", () => {
  assert.deepEqual(parseCommand("/compact 10"), { name: "compact", args: ["10"] });
  assert.deepEqual(parseCommand("/resume"), { name: "resume", args: [] });
  assert.equal(parseCommand("你好"), null);
  assert.ok(filterCommands("c").includes("compact"));
  assert.ok(filterCommands("").length >= 8);
});

test("游标回退包丢弃：advanceCursor 标记 stale", () => {
  const cur = { from: 5, thinkFrom: 1 };
  const bad = advanceCursor(cur, { next: 3, think_next: 0 });
  assert.equal(bad.stale, true);
  assert.equal(bad.from, 5);
  const good = advanceCursor(cur, { next: 7, think_next: 2 });
  assert.equal(good.stale, false);
  assert.equal(good.from, 7);
  assert.equal(
    buildTurnTextPath("s1", 7, 2),
    "/sessions/s1/turn/text?from=7&think_from=2",
  );
});

test("思考折叠抬头：reasoning_ms 换算秒；无值回退", () => {
  assert.equal(formatThinkLabel(2100), "思考过程（2.1s）");
  assert.equal(formatThinkLabel(null), "思考过程");
});

test("状态行：used/budget + provider/model + phase/耗时；超预算可判", () => {
  const line = formatStatusLine({
    ctx: { used_tokens: 100, budget_tokens: 1000, over_budget: true },
    phase: "streaming",
    elapsedMs: 300,
    provider: "dummy",
    model: "m",
  });
  assert.ok(line.includes("100/1000"));
  assert.ok(line.includes("超预算"));
  assert.ok(line.includes("dummy/m"));
  assert.ok(line.includes("streaming"));
  assert.equal(statusOverBudget({ over_budget: true }), true);
  assert.equal(statusOverBudget({ over_budget: false }), false);
});

test("模型按 provider 分组；删除预览计数", () => {
  const groups = groupModelsByProvider([
    { provider: "a", upstream_id: "m1" },
    { provider: "a", upstream_id: "m2" },
    { provider: "b", upstream_id: "m3" },
  ]);
  assert.equal(groups.length, 2);
  assert.equal(groups[0].items.length, 2);
  const s = deletionPlanSummary({
    deleted_message_ids: ["x"],
    deleted_summary_ids: [],
    unlinked_message_ids: ["y", "z"],
  });
  assert.ok(s.includes("1 条消息") && s.includes("2 条消息解链"));
});

test("api.call 请求形状：方法/路径/鉴权头/204 空", async () => {
  const seen = [];
  const fetchFn = async (url, opts) => {
    seen.push({ url, opts });
    if (url.endsWith("/gone")) return { status: 204, ok: true, json: async () => null };
    return { status: 200, ok: true, json: async () => ({ ok: true }) };
  };
  const api = createApi({ base: "http://x/api/v1", token: "t", fetchFn });
  await api.call("POST", "/sessions/s1/compact", { blocks: 2 });
  assert.equal(seen[0].url, "http://x/api/v1/sessions/s1/compact");
  assert.equal(seen[0].opts.headers.Authorization, "Bearer t");
  assert.equal(seen[0].opts.body, JSON.stringify({ blocks: 2 }));
  const r = await api.call("GET", "/gone");
  assert.equal(r, null);
  const p = await api.deletionPreview("s1", "m1");
  assert.deepEqual(p, { ok: true });
});

test("tab 切换纯函数：合法才切、非法留当前", () => {
  assert.deepEqual(SETTINGS_TABS, ["server", "client", "agent", "provider"]);
  assert.equal(switchSettingsTab("server", "agent"), "agent");
  assert.equal(switchSettingsTab("server", "nope"), "server");
});

test("settings 快照丢弃语义：改表单不碰 snapshot，脏比对只认内容", () => {
  const form = { title_chars: "32", provider: "dummy" };
  const snap = snapshotSettings(form);
  form.title_chars = "64"; // 关 modal 丢弃 ⇒ snap 不动
  assert.equal(snap.title_chars, "32");
  assert.equal(settingsDirty(snap, form), true);
  assert.equal(settingsDirty(snap, snapshotSettings(snap)), false);
});

test("outgoingPreview 空不发：空/空白直接回 null、不调 fetch", async () => {
  let n = 0;
  const api = createApi({ base: "http://x/api/v1", fetchFn: async () => { n++; return { status: 200, ok: true, json: async () => ([]) }; } });
  assert.equal(await api.outgoingPreview("s1", ""), null);
  assert.equal(await api.outgoingPreview("s1", "   "), null);
  assert.equal(n, 0);
  const out = await api.outgoingPreview("s1", "hi");
  assert.deepEqual(out, []);
  assert.equal(n, 1);
});

test("(c) 真请求形状：method/url/headers/体四格齐，且体里无库内账", async () => {
  const wire = {
    method: "POST", url: "https://x/v1/chat/completions",
    headers: { "Content-Type": "application/json" },
    body: { model: "m", stream: true, messages: [{ role: "user", content: "hi" }] },
  };
  const fetchFn = async () => ({ status: 200, ok: true, json: async () => wire });
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  const out = await api.outgoingPreview("s1", "hi");
  assert.equal(out.method, "POST");
  assert.ok(out.url.endsWith("/chat/completions"));
  const raw = JSON.stringify(out.body);
  for (const banned of ["message_id", "pending", '"type"', "from_idx"]) {
    assert.ok(!raw.includes(banned), `体里不许有 ${banned}：${raw}`);
  }
});

test("refresh-routes 回形：{provider, models} 直拼缓存行，error 走失败文案", async () => {
  const fetchFn = async (url) => {
    assert.ok(url.endsWith("/providers/p1/refresh-routes"));
    return { status: 200, ok: true, json: async () => ({ provider: "p1", models: 12 }) };
  };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  const out = await api.refreshRoutes("p1");
  assert.deepEqual(out, { provider: "p1", models: 12 });
  assert.ok(formatRoutesOutcome(out).includes("12"));
  assert.ok(formatRoutesOutcome({ provider: "p1", models: 0, error: "boom" }).includes("boom"));
  assert.deepEqual(buildAbilitiesPatch({ title: { enabled: true, provider: "", model: "m", prompt: "" } }), { title: { enabled: true, model: "m" } });
});

test("编辑端点形状：PATCH 消息带 content/reasoning，PATCH 摘要带 text", async () => {
  const seen = [];
  const fetchFn = async (url, opts) => {
    seen.push([url, opts.method, JSON.parse(opts.body)]);
    return { status: 200, ok: true, json: async () => ({}) };
  };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await api.editMessage("s1", "m1", { reasoning: "想通了" });
  await api.editSummary("s1", "sum1", "新手改");
  assert.deepEqual(seen[0], ["http://x/api/v1/sessions/s1/messages/m1", "PATCH", { reasoning: "想通了" }]);
  assert.deepEqual(seen[1], ["http://x/api/v1/sessions/s1/summaries/sum1", "PATCH", { text: "新手改" }]);
});

test("消息编辑按钮：头行右挂；点开是行内框（textarea+保存/取消），保存带新值", () => {
  const calls = [];
  function makeEl(tag) {
    const el = {
      tag, children: [],
      className: "", textContent: "", type: "", value: "", rows: 0, title: "",
      attrs: {},
      style: {},
      appendChild(c) { this.children.push(c); return c; },
      after(c) { this._after = c; },
      remove() { this._removed = true; },
      focus() {},
      setAttribute(k, v) { this.attrs[k] = v; },
      set onclick(fn) { this._click = fn; },
      get onclick() { return this._click; },
    };
    return el;
  }
  const doc = {
    createElement: (tag) => makeEl(tag),
  };
  const onEdit = (mid, kind, value) => calls.push([mid, kind, value]);
  const barOf = (node) => node.children.find((c) => c.className === "msg-head").children.find((c) => c.className === "edit-bar");
  // 无 id ⇒ 头行只有署名，不挂按钮
  const bare = renderMessage(doc, { who: "系", role: "assistant", content: "x" });
  assert.ok(!barOf(bare));
  // user：只有"改"；点了 ⇒ 行内框出现（textarea 初值 = 旧正文），保存带新值
  const u = renderMessage(doc, { who: "你", role: "user", content: "旧正文", messageId: "m1", onEdit });
  const ubar = barOf(u);
  assert.equal(ubar.children.length, 1);
  assert.equal(ubar.children[0].textContent, "✎");
  assert.equal(ubar.children[0].title, "改正文");
  const textEl = makeEl("div");
  textEl.className = "text";
  const ubox = { box: null };
  u.querySelector = (sel) => (sel === ".text" ? textEl : null);
  textEl.after = (c) => { ubox.box = c; };
  ubar.children[0].onclick();
  assert.ok(ubox.box && ubox.box.className === "inline-edit");
  const uarea = ubox.box.children[0];
  assert.equal(uarea.value, "旧正文");
  uarea.value = "新手改";
  ubox.box.children[1].children[0].onclick(); // 保存
  // assistant 改思考：初值 = 旧思考
  const a = renderMessage(doc, { who: "助", role: "assistant", content: "x", reasoning: "旧思考", messageId: "m2", onEdit });
  const abar = barOf(a);
  assert.equal(abar.children.length, 2);
  assert.equal(abar.children[1].textContent, "✎⋯");
  assert.equal(abar.children[1].title, "改思考");
  const thinkEl = makeEl("div");
  const abox = { box: null };
  const thinkDet = makeEl("details");
  a.querySelector = (sel) => (sel === ".think" ? thinkDet : sel === ".think-body" ? thinkEl : null);
  thinkEl.after = (c) => { abox.box = c; };
  abar.children[1].onclick();
  assert.equal(abox.box.children[0].value, "旧思考");
  assert.deepEqual(calls, [["m1", "content", "新手改"]]);
});

test("无思考也可改思考：没 .think 段 ⇒ 现建空段再挂框（换模型不断档）", () => {
  const calls = [];
  function makeEl(tag) {
    const el = {
      tag, children: [],
      className: "", textContent: "", type: "", value: "", rows: 0, title: "",
      attrs: {}, style: {}, open: false,
      appendChild(c) { this.children.push(c); return c; },
      insertBefore(c, ref) { this.children.unshift(c); return c; },
      after(c) { this._after = c; },
      remove() { this._removed = true; },
      focus() {},
      setAttribute(k, v) { this.attrs[k] = v; },
      set onclick(fn) { this._click = fn; },
      get onclick() { return this._click; },
    };
    return el;
  }
  const doc = { createElement: (tag) => makeEl(tag) };
  const onEdit = (mid, kind, value) => calls.push([mid, kind, value]);
  // muse spark 那种：没 reasoning ⇒ 画出来就没有 .think 段，但改思考按钮照挂
  const a = renderMessage(doc, { who: "助", role: "assistant", content: "Hello", messageId: "m3", onEdit });
  const bar = a.children.find((c) => c.className === "msg-head").children.find((c) => c.className === "edit-bar");
  assert.equal(bar.children.length, 2);
  const box = { box: null };
  const textEl = makeEl("div");
  let built = null;
  a.querySelector = (sel) => {
    if (sel === ".text") return textEl;
    if (sel === ".think") return built;
    if (sel === ".think-body") return built?.children.find((c) => c.className === "think-body") ?? null;
    if (sel === ".inline-edit") return box.box;
    return null;
  };
  // 新段自带 after（实现里显式补的）⇒ 框挂在新 think-body 后面，从它身上认。
  let seg = null;
  const origAppend = a.appendChild.bind(a);
  a.appendChild = (c) => { if (c.className === "think") { built = c; seg = c.children.find((x) => x.className === "think-body"); seg.after = (node) => { box.box = node; }; } return origAppend(c); };
  bar.children[1].onclick(); // 改思考
  assert.ok(built, "该现建一段空 .think");
  assert.equal(box.box.children[0].value, "");
  box.box.children[0].value = "后补的思考";
  box.box.children[1].children[0].onclick(); // 保存
  assert.deepEqual(calls, [["m3", "reasoning", "后补的思考"]]);
});

test("消息正文节点永在：.text 带原文；有思考 ⇒ .think-body 带思考（丢了就是上次的事故）", () => {
  const doc = { createElement: (tag) => ({
    tag, children: [], className: "", textContent: "",
    appendChild(c) { this.children.push(c); return c; },
  }) };
  const find = (node, cls) => {
    if (node.className === cls) return node;
    for (const c of node.children ?? []) {
      const hit = find(c, cls);
      if (hit) return hit;
    }
    return null;
  };
  const u = renderMessage(doc, { who: "你", role: "user", content: "你好。" });
  assert.equal(find(u, "text")?.textContent, "你好。");
  assert.equal(find(u, "think-body"), null);
  const a = renderMessage(doc, { who: "助", role: "assistant", content: "Hello", reasoning: "想了想" });
  assert.equal(find(a, "text")?.textContent, "Hello");
  assert.equal(find(a, "think-body")?.textContent, "想了想");
});
