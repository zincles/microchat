import { strict as assert } from "node:assert";
import { test } from "node:test";
import { createApi, mergeStreamSlice, DEFAULT_API_URL } from "../src/api/client.js";
import { parseCommand, filterCommands, COMMAND_META } from "../src/utils/commands.js";
import { uuidv7 } from "../src/utils/ids.js";
import { buildCompactTree, treeTotals } from "../src/utils/tree.js";
import {
  formatThinkLabel,
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  deletionPlanSummary,
  formatRoutesOutcome,
  buildAbilitiesPatch,
  modelKey,
  parseModelKey,
  modelLabel,
  whoText,
  sessionTitle,
} from "../src/utils/format.js";
import { sendMode, wantsSend, themePref, apiBase, DEFAULT_THEME, DEFAULT_SEND } from "../src/utils/prefs.js";

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
  const cs = filterCommands("c");
  assert.deepEqual(cs.map((x) => x.name), cs.map((x) => x.name).filter((n) => n.startsWith("c")));
  assert.ok(cs.some((x) => x.name === "compact" && x.desc));
  assert.ok(filterCommands("").length === Object.keys(COMMAND_META).length);
});

test("流式合并：正常追加 + 游标推进", () => {
  const a1 = mergeStreamSlice({ text: "你", reasoning: "想" }, { from: 1, thinkFrom: 1 },
    { text: "好", thinking: "了", next: 2, think_next: 2 });
  assert.deepEqual(a1, { text: "你好", reasoning: "想了", cur: { from: 2, thinkFrom: 2 }, reset: false });
});

test("流式合并：游标回退（缓冲换了一轮）⇒ 清零重建，不把旧尾巴接上新开头", () => {
  // 已在旧一轮读到 from=9；后端缓冲被新轮重置（next=2）⇒ 不许追加，下一拍从 0 重读。
  const r = mergeStreamSlice({ text: "旧一轮的九", reasoning: "旧" }, { from: 9, thinkFrom: 1 },
    { text: "", thinking: "", next: 2, think_next: 0 });
  assert.deepEqual(r, { text: "", reasoning: "", cur: { from: 0, thinkFrom: 0 }, reset: true });
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

test("uuidv7：与后端同形（version 7 / variant / 毫秒序）", () => {
  const shape = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
  const a = uuidv7(1000);
  const b = uuidv7(2000);
  assert.match(a, shape);
  assert.match(b, shape);
  assert.ok(a < b, "毫秒不同 ⇒ 字符串序仍按时间（v7 的全部意义）");
  assert.notEqual(uuidv7(7), uuidv7(7), "同毫秒靠随机位分开");
});

test("draftOutgoing 请求形状：POST /outgoing，体 = id + 缺省三件 + content", async () => {
  const seen = [];
  const api = createApi({ base: "http://x/api/v1", fetchFn: async (url, opts) => { seen.push({ url, opts }); return { status: 200, ok: true, json: async () => ({ ok: true }) }; } });
  const id = "0198ba3c-1234-7abc-8def-0123456789ab";
  await api.draftOutgoing({ id, provider: "dummy", model: "dummy", agent_id: "default", content: "hi" });
  assert.equal(seen[0].url, "http://x/api/v1/outgoing");
  assert.equal(seen[0].opts.method, "POST");
  assert.deepEqual(JSON.parse(seen[0].opts.body), { id, provider: "dummy", model: "dummy", agent_id: "default", content: "hi" });
});

test("压缩树：区间成目录、嵌套成子目录、used 标出装配真跳的那层", () => {
  // 库内真形状：消息的 summary_id 指**直接**盖它的那个（合并后仍指孩子）；合并回填孩子的 parent。
  const msgs = [
    { id: "m1", idx: 1, role: "user", content: "a".repeat(130), summary_id: "s1" },
    { id: "m2", idx: 2, role: "assistant", content: "b".repeat(130), summary_id: "s1" },
    { id: "m3", idx: 3, role: "user", content: "c".repeat(65), summary_id: "s2" },
    { id: "m4", idx: 4, role: "assistant", content: "d".repeat(65), summary_id: "s2" },
    { id: "m5", idx: 5, role: "user", content: "e".repeat(26) },
  ];
  const s1 = { id: "s1", parent_summary_id: "s3", type: "message", begin_message_id: "m1", end_message_id: "m2", blocks: 1, tokens: 40, dirty: false };
  const s2 = { id: "s2", parent_summary_id: "s3", type: "message", begin_message_id: "m3", end_message_id: "m4", blocks: 1, tokens: 30, dirty: false };
  const s3 = { id: "s3", parent_summary_id: null, type: "summary", begin_message_id: "m1", end_message_id: "m4", blocks: 2, tokens: 50, dirty: false };
  // 只有 s3（最粗同左端）该被标 used；s1/s2 是它孩子
  const tree = buildCompactTree(msgs, [s1, s2, s3]);
  assert.equal(tree.length, 2, "顶层 = s3 + 尾条原文");
  const [top, leaf] = tree;
  assert.equal(top.kind, "summary");
  assert.equal(top.id, "s3");
  assert.equal(top.used, true, "装配跳到最粗同左端祖先 s3");
  assert.deepEqual(top.children.map((c) => c.id), ["s1", "s2"]);
  assert.deepEqual(top.children.map((c) => c.used), [false, false]);
  assert.equal(top.children[0].children.length, 2, "消息级摘要的孩子 = 区间里的消息");
  assert.equal(leaf.kind, "message");
  assert.equal(leaf.idx, 5);
  // 体积：rawChars 是子树和；tokens 各报各的（≈ 归一：130 字 → ceil(130/1.3)=100）
  assert.equal(top.rawChars, 130 + 130 + 65 + 65);
  assert.equal(top.children[0].rawChars, 260);
  assert.equal(top.children[0].children[0].tokens, 100);
  const totals = treeTotals(tree);
  assert.deepEqual(totals, { rawChars: 130 + 130 + 65 + 65 + 26, sent: 50 + 20 });
});

test("压缩树：左端对不上就不算 used（照原文发那条）", () => {
  const msgs = [
    { id: "m1", idx: 1, role: "user", content: "x" },
    { id: "m2", idx: 2, role: "assistant", content: "y" },
  ];
  // m1 身上挂着 s1，但 s1 的区间不从 m1 起 ⇒ 不跳，m1 照原文
  const s1 = { id: "s1", parent_summary_id: null, type: "message", begin_message_id: "m2", end_message_id: "m2", blocks: 1, tokens: 5, dirty: true };
  const tree = buildCompactTree(msgs.map((m, i) => (i === 0 ? { ...m, summary_id: "s1" } : m)), [s1]);
  assert.equal(tree.length, 2);
  assert.equal(tree[0].kind, "message", "左端对不上 → 原文");
  assert.equal(tree[1].kind, "summary");
  assert.equal(tree[1].used, false);
  assert.equal(tree[1].dirty, true);
});

test("outgoingPreview 空字问历史：空/空白也发包，与空发 resend 同段", async () => {
  const seen = [];
  const api = createApi({ base: "http://x/api/v1", fetchFn: async (url, opts) => { seen.push(JSON.parse(opts.body)); return { status: 200, ok: true, json: async () => ([]) }; } });
  await api.outgoingPreview("s1", "");
  await api.outgoingPreview("s1", "   ");
  await api.outgoingPreview("s1", null);
  assert.deepEqual(seen, [{ content: "" }, { content: "   " }, { content: "" }]);
  const out = await api.outgoingPreview("s1", "hi");
  assert.deepEqual(out, []);
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

test("resend 端点形状：POST /resend 无体", async () => {
  let seen = null;
  const fetchFn = async (url, opts) => {
    seen = [url, opts.method, opts.body];
    return { status: 202, ok: true, json: async () => ({ turn: {} }) };
  };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await api.resend("s1");
  assert.deepEqual(seen, ["http://x/api/v1/sessions/s1/resend", "POST", undefined]);
});




test("importSt 端点形状：POST /sessions/import-st 原文体 + ?title=", async () => {
  let seen;
  const fetchFn = async (url, opts) => {
    seen = [url, opts.method, opts.headers["Content-Type"], opts.body];
    return { status: 201, ok: true, json: async () => ({ session: {}, messages: 1, skipped: 0 }) };
  };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await api.importSt('{"mes":"hi"}\n', "ST");
  assert.deepEqual(seen, ["http://x/api/v1/sessions/import-st?title=ST", "POST", "text/plain", '{"mes":"hi"}\n']);
});


// —— 背景图（utils/background.js）：存储层的健壮性 —— 坏值当没设、往返一字不差。
import { loadBackground, saveBackground, describeBackground } from "../src/utils/background.js";

function stubStorage() {
  const m = new Map();
  globalThis.localStorage = {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
}

test("背景图存储：坏 JSON / 缺字段 / 空串都当没设", () => {
  stubStorage();
  assert.equal(loadBackground(), null);
  localStorage.setItem("mc_bg", "{oops");
  assert.equal(loadBackground(), null);
  localStorage.setItem("mc_bg", JSON.stringify({ kind: "nope", value: "x" }));
  assert.equal(loadBackground(), null);
  localStorage.setItem("mc_bg", JSON.stringify({ kind: "url", value: "" }));
  assert.equal(loadBackground(), null);
});

test("背景图存储：save → load 往返；清除后回 null", () => {
  stubStorage();
  const bg = { kind: "url", value: "https://example.com/a.png" };
  saveBackground(bg);
  assert.deepEqual(loadBackground(), bg);
  saveBackground(null);
  assert.equal(loadBackground(), null);
});

test("背景图标注：未设 / URL 截断 / 上传带尺寸", () => {
  assert.equal(describeBackground(null), "未设（纯平底）");
  const long = "https://example.com/" + "x".repeat(80);
  assert.ok(describeBackground({ kind: "url", value: long }).includes("…"));
  const txt = describeBackground({ kind: "file", value: "d".repeat(1365), width: 1920, height: 1080 });
  assert.ok(txt.includes("1920×1080") && txt.includes("1 KB"), txt);
});

// —— 共享层新增（2026-10-10 审计整改） ——

test("API 错误：code/status 原样带上（调用方按 code 分支，别猜文案）", async () => {
  const fetchFn = async () => ({ status: 409, ok: false, json: async () => ({ error: { code: "conflict", message: "末尾变了" } }) });
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await assert.rejects(() => api.stop("s1"), (e) => e.code === "conflict" && e.status === 409 && e.message === "末尾变了");
});

test("API 错误：非 JSON 错误体（框架 405 的 text/plain）不炸 SyntaxError，回退 HTTP <status>", async () => {
  const fetchFn = async () => ({ status: 405, ok: false, json: async () => { throw new SyntaxError("Unexpected token"); } });
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await assert.rejects(() => api.stop("s1"), (e) => e.message === "HTTP 405" && e.code === null && e.status === 405);
});

test("模型键往返：provider 里带 / 也不歧义；无分隔符 → null", () => {
  const k = modelKey("自建/代理", "deepseek/chat");
  assert.equal(k, "自建/代理|||deepseek/chat");
  assert.deepEqual(parseModelKey(k), { provider: "自建/代理", model: "deepseek/chat" });
  assert.equal(parseModelKey("没有分隔符"), null);
});

test("modelLabel 三级回退：用户覆盖 → 上游名 → 上游 id", () => {
  assert.equal(modelLabel({ display_name: "我的名字", name: "上游名", upstream_id: "id" }), "我的名字");
  assert.equal(modelLabel({ display_name: null, name: "上游名", upstream_id: "id" }), "上游名");
  assert.equal(modelLabel({ name: "", upstream_id: "id" }), "id");
});

test("whoText / sessionTitle：两处回退档各就各位", () => {
  assert.equal(whoText("p", "m"), "p/m");
  assert.equal(whoText("", ""), "未知模型");
  assert.equal(whoText("", "", "未选模型"), "未选模型");
  assert.equal(sessionTitle("", true), "新对话");
  assert.equal(sessionTitle("", false), "新会话");
  assert.equal(sessionTitle("  标题  "), "标题");
});

test("偏好读取：默认与空串语义（mc_theme 空串=跟随系统，不许被吞）", () => {
  stubStorage();
  assert.equal(apiBase(), DEFAULT_API_URL);
  assert.equal(themePref(), DEFAULT_THEME);
  assert.equal(sendMode(), DEFAULT_SEND);
  localStorage.setItem("mc_theme", "");
  assert.equal(themePref(), "");
  localStorage.setItem("mc_api", "http://elsewhere/api/v1");
  assert.equal(apiBase(), "http://elsewhere/api/v1");
});

test("发送键判定：三档 + 修饰键（App 与 Composer 同一把尺）", () => {
  stubStorage();
  const e = (o = {}) => ({ shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, ...o });
  assert.equal(wantsSend(e()), true);
  assert.equal(wantsSend(e({ shiftKey: true })), false);
  assert.equal(wantsSend(e({ shiftKey: true }), "shift-enter"), true);
  assert.equal(wantsSend(e(), "shift-enter"), false);
  assert.equal(wantsSend(e(), "button"), false);
});
