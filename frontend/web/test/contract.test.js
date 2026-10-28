import { strict as assert } from "node:assert";
import { test } from "node:test";
import { createApi, mergeStreamSlice, DEFAULT_API_URL, normalizeApiBase, probeAuthStatus, authLogin } from "../src/api/client.js";
import { parseWireMessages, shouldCollapse, previewBlock, LONG_BLOCK_CHARS } from "../src/utils/payload.js";
import { assemblePrompt, abilityPrompt, missingSegments } from "../src/utils/prompt.js";
import { renderMarkdown } from "../src/utils/markdown.js";
import { buildCartridge, cartridgeAgent } from "../src/utils/cartridge.js";
import { parseCommand, filterCommands, COMMAND_META } from "../src/utils/commands.js";
import { uuidv7 } from "../src/utils/ids.js";
import { buildCompactTree, treeTotals } from "../src/utils/tree.js";
import { buildChatRows, rerollTargetLive, bubbleText } from "../src/utils/chatrows.js";
import { resetChrome, stepChrome } from "../src/utils/chrome.js";
import { computeViewportFit } from "../src/utils/viewport.js";
import { classifyAxis, pickDrag, dragProgress, resolveRelease } from "../src/utils/swipe.js";
import {
  formatThinkLabel,
  formatStatusLine,
  formatTokens,
  contextTitle,
  contextRatio,
  contextPercentText,
  contextArc,
  CONTEXT_MIN_ARC,
  contextOverBudget,
  formatCost,
  usageFoot,
  usageFootTitle,
  groupModelsByProvider,
  providerChoices,
  modelChoicesFor,
  SETTINGS_TABS,
  providerIdForNextVendor,
  filterModelGroups,
  curModelLabelOf,
  modelMatches,
  ORPHAN_MODEL_GROUP,
  deletionPlanSummary,
  formatRoutesOutcome,
  ABILITY_IDS,
  ABILITY_LABELS,
  ABILITY_TEMPLATE_SEGMENTS,
  buildAbilitiesPatch,
  taskLineText,
  modelKey,
  parseModelKey,
  modelLabel,
  whoText,
  sessionTitle,
} from "../src/utils/format.js";
import { sendMode, wantsSend, themePref, normalizeTheme, apiBase, defaultApiBase, apiBaseCandidates, THEME_IDS, DEFAULT_THEME, DEFAULT_SEND, composerStyle, applyComposerStyle, saveComposerStyle, DEFAULT_COMPOSER, normalizeTextScale, textScale, applyTextScale, saveTextScale, TEXT_SCALE_MIN, TEXT_SCALE_MAX, DEFAULT_TEXT_SCALE } from "../src/utils/prefs.js";

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

// 上下文数字的人话格式（2026-10-17 用户点名）：向下取整、K/M 两档、去掉多余的 0。
// 证伪点：四舍五入（995904 ⇒ 996K）、.0 尾巴（1000 ⇒ 1.0K）、M 档不封顶（1e9 还写 K）。
test("formatTokens：9.8K / 995K / 1.04M（向下取整，别把用量说大）", () => {
  assert.equal(formatTokens(0), "0");
  assert.equal(formatTokens(999), "999");
  assert.equal(formatTokens(1000), "1K");
  assert.equal(formatTokens(4096), "4K");
  assert.equal(formatTokens(9857), "9.8K");
  assert.equal(formatTokens(9999), "9.9K");
  assert.equal(formatTokens(10000), "10K");
  assert.equal(formatTokens(131072), "131K");
  assert.equal(formatTokens(995904), "995K"); // 不四舍五入成 996K
  assert.equal(formatTokens(1000000), "1M");
  assert.equal(formatTokens(1048576), "1.04M");
  assert.equal(formatTokens(null), "?");
  assert.equal(formatTokens(undefined), "?");
});

// 2026-10-28：这串解释跟着上下文占用一起从状态行搬到右上角那枚环（悬停 title + 浮板底部同一句话）。
test("上下文环的悬停解释：分母是输入预算 = 上下文 − 输出预留", () => {
  const t = contextTitle({ ctx_len: 1000000, max_output: 4096, budget_tokens: 995904 });
  assert.ok(t.includes("995,904"));
  assert.ok(t.includes("1,000,000"));
  assert.ok(t.includes("4,096"));
  // 没有会话时也要给出一句人话（从前回空串 ⇒ 环的 title 是空的，等于没解释）
  assert.equal(contextTitle(null), "上下文占用（还没有会话）");
});

// 环要能把"几乎没占"和"快满了"分开画：0.03% 那种小占比不能显示成 0%（看着像环坏了），
// 也不能因为它小而把弧画到画不出来（TopBar 里另有 2% 的最小可见弧）。
test("上下文占比：夹到 0..1、小占比不显示成 0%、向下取整（不把用量说大）", () => {
  assert.equal(contextRatio({ used_tokens: 344, budget_tokens: 1044480 }), 344 / 1044480);
  assert.equal(contextRatio({ used_tokens: 2000, budget_tokens: 1000 }), 1); // 超预算也夹到 1
  assert.equal(contextRatio({ used_tokens: 100, budget_tokens: 0 }), 0);    // 分母缺失 ⇒ 0（不能 NaN）
  assert.equal(contextRatio(null), 0);
  assert.equal(contextRatio({ used_tokens: null, budget_tokens: 1000 }), 0);

  assert.equal(contextPercentText({ used_tokens: 0, budget_tokens: 1000 }), "0%");
  assert.equal(contextPercentText({ used_tokens: 344, budget_tokens: 1044480 }), "<0.1%");
  assert.equal(contextPercentText({ used_tokens: 34, budget_tokens: 1000 }), "3.4%");
  assert.equal(contextPercentText({ used_tokens: 99, budget_tokens: 100 }), "99%");
  // 向下取整：999/1000 = 99.9% ⇒ 报 99%（向上取整会把用量说大，与 formatTokens 同一个理由）
  assert.equal(contextPercentText({ used_tokens: 999, budget_tokens: 1000 }), "99%");

  // 弧长：**用了就至少画一道**（344/1.04M 取整成 0 会让环消失，看起来像坏了），
  // 没用就不画；满/超都封顶 100。
  assert.equal(contextArc({ used_tokens: 344, budget_tokens: 1044480 }), CONTEXT_MIN_ARC);
  assert.equal(contextArc({ used_tokens: 0, budget_tokens: 1000 }), 0);
  assert.equal(contextArc({ used_tokens: 500, budget_tokens: 1000 }), 50);
  assert.equal(contextArc({ used_tokens: 2000, budget_tokens: 1000 }), 100);
  assert.equal(contextArc(null), 0);
});

// 成本与气泡脚注（2026-10-20）：OpenRouter 在 `usage.cost` 里报实扣金额，直连渠道不报。
// 证伪点：① 把"没数据"显示成 $0.00（说谎，直连渠道全中）；② 便宜模型的单发被两位小数抹成 $0.00
// （deepseek-v4.1-flash 一发就是 0.0004 量级）；③ 把 reasoning 当额外开销加上去（它是 completion 的子集）。
test("formatCost：便宜模型给 4 位小数；没数据回空串（不是 $0.00）", () => {
  assert.equal(formatCost(0), "$0");              // 免费模型：真的 0
  assert.equal(formatCost(0.0004), "$0.0004");    // 便宜模型单发
  assert.equal(formatCost(0.001234), "$0.0012");
  assert.equal(formatCost(0.0123), "$0.01");      // ≥1 分 ⇒ 两位
  assert.equal(formatCost(1.5), "$1.50");
  assert.equal(formatCost(null), "");             // 直连渠道不报 ⇒ 什么都不显示
  assert.equal(formatCost(undefined), "");
  assert.equal(formatCost("0.5"), "");            // 不是数字 ⇒ 当没有
});

test("usageFoot：token 用量 + 成本；没用量整行为空", () => {
  const foot = usageFoot({ prompt_tokens: 6585, completion_tokens: 945, cached_tokens: 0, reasoning_tokens: 333, cost_usd: 0.0012 });
  assert.ok(foot.includes("6.5K→945"));           // 向下取整：6585 ⇒ 6.5K
  assert.ok(foot.includes("思考 333"));
  assert.ok(foot.includes("$0.0012"));
  assert.ok(!foot.includes("缓存命中"));          // 0 命中不啰嗦

  const withCache = usageFoot({ prompt_tokens: 100000, completion_tokens: 50, cached_tokens: 96000 });
  assert.ok(withCache.includes("缓存命中 96K"));
  assert.ok(!withCache.includes("$"));            // 没 cost ⇒ 没有价格那一格

  assert.equal(usageFoot(null), "");              // 生成中 / 库外那条
  assert.equal(usageFoot({}), "");                // 上游没报
  assert.equal(usageFootTitle(null), "");
});

test("usageFoot 的悬停说明：点明思考是输出的子集、成本是上游报的", () => {
  const title = usageFootTitle({ prompt_tokens: 100, completion_tokens: 40, cached_tokens: 80, reasoning_tokens: 30, cost_usd: 0.0004 });
  assert.ok(title.includes("输入 100 tok（其中缓存命中 80）"));
  assert.ok(title.includes("子集"));              // 别让人把 reasoning 再加一遍
  assert.ok(title.includes("$0.0004"));
  assert.ok(title.includes("上游报的"));
});

test("状态行：助手 + phase/耗时；上下文占用不在这行（2026-10-28 挪去右上角的环）", () => {
  const line = formatStatusLine({ phase: "streaming", elapsedMs: 300, agent: "默认助手" });
  assert.ok(line.includes("默认助手"));
  assert.ok(line.includes("streaming"));
  assert.ok(line.includes("300ms"));
  assert.ok(!line.includes("dummy/m")); // 模型不再出现在状态行（单处归属：顶栏左上）
  // 上下文占用整块搬走了：这行里不许再出现 used/budget/超预算（两处都显示同一个数字，
  // 只会让两处都可能过期）。
  assert.ok(!line.includes("上下文"));
  assert.ok(!line.includes("超预算"));
  assert.ok(!formatStatusLine({ phase: "idle" }).includes("上下文"));
  // 超预算的信号还在（环变红用它判），只是不再走状态行
  assert.equal(contextOverBudget({ over_budget: true }), true);
  assert.equal(contextOverBudget({ over_budget: false }), false);
  assert.equal(contextOverBudget(null), false);
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

test("Markdown 渲染：加粗/斜体/下划线/行内码/删除线/引述；代码段里不转换", () => {
  const h = renderMarkdown("**粗** 与 *斜* 与 _斜_ 与 __下划线__ 与 `code __x__` 与 ~~删~~");
  assert.match(h, /<strong>粗<\/strong>/);
  assert.match(h, /<em>斜<\/em>/);
  assert.match(h, /<u>下划线<\/u>/); // __x__ = 下划线（不是 bold）
  assert.match(h, /<code>code __x__<\/code>/); // 代码段里原样（`__init__` 那类）
  assert.match(h, /<del>删<\/del>/);
  assert.match(renderMarkdown("> OOC：题外话"), /<blockquote>/);
});

test("Markdown 渲染：HTML 一律转义、数学走 KaTeX 占位", () => {
  const h = renderMarkdown("<script>alert(1)</script>");
  assert.ok(!h.includes("<script>"));
  assert.ok(h.includes("&lt;script&gt;"));
  assert.ok(renderMarkdown("$x^2$").includes("katex"));
});

test("载荷长块折叠：>500 字默认折叠；预览单行截断（换行压空格）", () => {
  assert.equal(LONG_BLOCK_CHARS, 500);
  assert.equal(shouldCollapse("短句"), false);
  assert.equal(shouldCollapse("x".repeat(500)), false);
  assert.equal(shouldCollapse("x".repeat(501)), true);
  assert.equal(previewBlock("你好\n世界"), "你好 世界");
  const long = "字".repeat(120);
  const pv = previewBlock(long, 90);
  assert.equal([...pv].length, 91); // 90 + 省略号
  assert.ok(pv.endsWith("…"));
  assert.equal(previewBlock("短", 90), "短");
});

test("系统提示词拼装：base + 追加段（顺序、空行、缺段跳过）—— 与后端同规则", () => {
  const lib = [
    { id: "katex", text: "数学用 $…$ 写。" },
    { id: "empty", text: "  " },
    { id: "mcp", text: "需要联网时调用搜索工具。" },
  ];
  assert.equal(
    assemblePrompt("你是助手。", ["katex", "empty", "mcp"], lib),
    "你是助手。\n\n数学用 $…$ 写。\n\n需要联网时调用搜索工具。",
  );
  assert.equal(assemblePrompt("", ["mcp"], lib), "需要联网时调用搜索工具。");
  assert.equal(assemblePrompt("base", ["nope"], lib), "base");
  assert.equal(assemblePrompt("  ", [], lib), "");
  assert.deepEqual(missingSegments(["a", "katex"], lib), ["a"]);
});

test("agent 卡带 v2：带引用段（内置不带）、结构不拼平；v1 老卡带照收；导入侧校验", () => {
  const lib = [
    { id: "katex", text: "数学用 $…$ 写。" },
    { id: "mcp", text: "需要联网时调用搜索。" },
    { id: "合理的RolePlay", text: "内置的", builtin: true },
  ];
  const agent = {
    id: "rp-agent-simple", name: "RP Agent", system_prompt: "你是主持人。",
    segments: ["katex", "合理的RolePlay", "mcp"], first_message: "开场",
    // 能力也引用段（2026-10-28）：`mcp` 与 agent 自己那份**重叠** ⇒ 卡带里只出现一次
    abilities: { title: { enabled: false }, compact: { segments: ["mcp", "katex"] } }, prepend_state: true,
  };
  const card = buildCartridge(agent, lib);
  assert.equal(card.version, 2);
  assert.equal(card.agent.system_prompt, "你是主持人。"); // base 原文，**不拼平**
  assert.deepEqual(card.agent.segments, ["katex", "合理的RolePlay", "mcp"]); // 引用照旧
  assert.deepEqual(card.segments, [ // 只带引用到的**用户段**；内置不带
    { id: "katex", text: "数学用 $…$ 写。" },
    { id: "mcp", text: "需要联网时调用搜索。" },
  ]);
  assert.equal(card.agent.id, undefined); // 导入方现铸
  // ⚠ `display_mode` 2026-10-27 已删（显示只有一种形态）⇒ 卡带里不该再有这一格。
  assert.equal(card.agent.display_mode, undefined);
  // 能力原样带（含 2026-10-28 的 `segments`）—— 卡带不解释能力，只搬运
  assert.deepEqual(card.agent.abilities, { title: { enabled: false }, compact: { segments: ["mcp", "katex"] } });
  // 导入侧：形状不对 ⇒ 说清；对了 ⇒ {agent, segments} 归一化
  assert.throws(() => cartridgeAgent({}), /不是 agent 卡带/);
  assert.throws(() => cartridgeAgent({ agent: { name: "  " } }), /没有名字/);
  assert.throws(() => cartridgeAgent({ agent: { name: "x" }, segments: [{ id: "  " }] }), /空 id/);
  const back = cartridgeAgent(card);
  assert.equal(back.agent.name, "RP Agent");
  assert.deepEqual(back.agent.segments, ["katex", "合理的RolePlay", "mcp"]);
  assert.equal(back.segments.length, 2);
  assert.equal(back.agent.prepend_state, true);
  // 老卡带（还带着 display_mode）照样能导：那个键没人看，归一化后自然不在结果里。
  const legacy = cartridgeAgent({ agent: { name: "x", display_mode: "roleplay" } });
  assert.equal(legacy.agent.display_mode, undefined);
  assert.equal(legacy.agent.name, "x");
  // v1 老卡带（提示词已拼平、无 segments 数组）照样能导
  const v1 = cartridgeAgent({ version: 1, agent: { name: "老卡", system_prompt: "拼平过的" } });
  assert.deepEqual(v1.segments, []);
  assert.equal(v1.agent.system_prompt, "拼平过的");
});

test("对话导出/导入接口形状：路径 / 方法 / 体", async () => {
  const seen = [];
  const api = createApi({ base: "http://x/api/v1", token: "t", fetchFn: async (url, opts) => { seen.push({ url, opts }); return { status: 200, ok: true, json: async () => ({}) }; } });
  await api.exportSession("s1");
  await api.importJson({ version: 1, title: "x", messages: [{ role: "user", content: "a" }] });
  assert.deepEqual(seen.map((s) => [s.opts.method, s.url.replace("http://x/api/v1", "")]), [
    ["GET", "/sessions/s1/export"],
    ["POST", "/sessions/import-json"],
  ]);
  assert.deepEqual(JSON.parse(seen[1].opts.body).messages[0], { role: "user", content: "a" });
});

test("提示词段接口形状：路径 / 方法", async () => {
  const seen = [];
  const api = createApi({ base: "http://x/api/v1", token: "t", fetchFn: async (url, opts) => { seen.push({ url, opts }); return { status: 200, ok: true, json: async () => ({}) }; } });
  await api.createSegment({ id: "katex", text: "x" });
  await api.patchSegment("katex", { text: "y" });
  await api.deleteSegment("katex");
  assert.deepEqual(seen.map((s) => [s.opts.method, s.url.replace("http://x/api/v1", "")]), [
    ["POST", "/segments"],
    ["PATCH", "/segments/katex"],
    ["DELETE", "/segments/katex"],
  ]);
});

test("用户管理接口形状：路径 / 方法 / sessions 查询参数", async () => {
  const seen = [];
  const api = createApi({ base: "http://x/api/v1", token: "t", fetchFn: async (url, opts) => { seen.push({ url, opts }); return { status: 200, ok: true, json: async () => ({}) }; } });
  await api.users();
  await api.createUser({ name: "alice", password: "hunter22" });
  await api.patchUser(1000, { name: "alice2" });
  await api.setUserPassword(1000, "hunter33");
  await api.deleteUser(1000, "transfer");
  assert.deepEqual(seen.map(s => [s.opts.method, s.url.replace("http://x/api/v1", "")]), [
    ["GET", "/users"],
    ["POST", "/users"],
    ["PATCH", "/users/1000"],
    ["POST", "/users/1000/password"],
    ["DELETE", "/users/1000?sessions=transfer"],
  ]);
  assert.deepEqual(JSON.parse(seen[1].opts.body), { name: "alice", password: "hunter22" });
  assert.deepEqual(JSON.parse(seen[3].opts.body), { password: "hunter33" });
});

test("载荷分块：parseWireMessages 只认形状（messages 数组），边角字段一个不吞", () => {
  const wire = {
    method: "POST",
    url: "https://x/v1/chat/completions",
    headers: {},
    body: {
      model: "m1",
      stream: true,
      tools: [{}, {}],
      messages: [
        { role: "system", content: "规则" },
        { role: "user", content: "你好" },
        { role: "assistant", content: "答", reasoning_content: "想了一下" },
        { role: "assistant", content: null, tool_calls: [{ function: { name: "get_weather" } }] },
        { role: "tool", content: "晴", tool_call_id: "call_1", name: "get_weather" },
      ],
    },
  };
  const p = parseWireMessages(wire);
  assert.equal(p.meta.model, "m1");
  assert.equal(p.meta.stream, true);
  assert.equal(p.meta.tools, 2);
  assert.deepEqual(p.messages.map((m) => m.role), ["system", "user", "assistant", "assistant", "tool"]);
  assert.equal(p.messages[1].content, "你好");
  assert.equal(p.messages[1].chars, 2); // rune 数（CJK 一字算一）
  assert.equal(p.messages[2].reasoning, "想了一下");
  assert.equal(p.messages[3].content, ""); // content 为 null ⇒ 空串（靠 tool_calls 行交代）
  assert.equal(p.messages[3].toolCalls, "get_weather");
  assert.equal(p.messages[4].toolCallId, "call_1");
  assert.equal(p.messages[4].name, "get_weather");
  // 不是这个形状 ⇒ null（调用方退回裸 JSON）
  assert.equal(parseWireMessages(null), null);
  assert.equal(parseWireMessages({ body: "字符串" }), null);
  assert.equal(parseWireMessages({ body: { messages: "nope" } }), null);
  // content 不是字符串（多模态数组）⇒ JSON 化，不吞
  const mm = parseWireMessages({ body: { messages: [{ role: "user", content: [{ type: "text", text: "hi" }] }] } });
  assert.match(mm.messages[0].content, /"text"/);
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
  // 能力的追加段（2026-10-28）：空清单**不发**（省略 = 没有段），顺序照抄（顺序就是语义）
  assert.deepEqual(
    buildAbilitiesPatch({ compact: { enabled: true, segments: ["b", "", "a", "b"] } }),
    { compact: { enabled: true, segments: ["b", "a", "b"] } },
  );
});

// 提示词段的编辑器（agent 级 + 能力级）：`.vue` 里的接线**只能读源码验** —— 少接一环不会报错，
// 只会让"我加了段、它没生效"变成一场排查（后端那份由 `compact_segments_test.go` 用真载荷钉住）。
//
// 2026-10-28 用户：「不协调。总觉得和编辑系统提示词的有区别。」⇒ 两处**共用同一个组件**。
// 这条守卫要的正是这个：挑 / 排 / 删 / 缺段警告 / 拼装预览**只有一份实现**，
// 谁要是又在某一个地方手写一套，这里就该炸。
test("提示词段的编辑器：agent 级与能力级用的是同一个组件", () => {
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  const list = readFileSync(join(SRC, "components", "SegmentList.vue"), "utf8");
  // 两处都调它，而且各自绑对了自己的清单（agent 的 `segs` / 能力的 `rows[id].segments`）
  assert.equal(editor.match(/<SegmentList/g)?.length, 2,
    "AgentEditor 该在两处（agent 级、能力级）都用 SegmentList —— 各写一套必然长得不一样");
  assert.match(editor, /v-model:ids="segs"/, "agent 级没绑自己的段清单");
  assert.match(editor, /v-model:ids="rows\[id\]\.segments"/, "能力级没绑自己的段清单");
  // 组件本身：挑 / 排 / 删 / 缺段警告 / 拼装预览，五样都在
  assert.match(list, /function add\(/, "没有「从段库挑一个加进来」");
  assert.match(list, /function move\(/, "没有上下移（顺序就是语义）");
  assert.match(list, /function drop\(/, "没有移除");
  assert.match(list, /引用了库里没有的段/, "缺段的汇总警告没了");
  assert.match(list, /拼装预览（\{\{ assembled\.length \}\} 字）/, "拼装预览没了");
  // 编辑器的读回 / 写回仍然在 AgentEditor（那是配置形状，不是编辑器）
  assert.match(editor, /segments: \[\.\.\.\(one\.segments \?\? \[\]\)\]/, "fillRows 没把能力的段读回来");
  assert.match(editor, /segments: \[\.\.\.\(r\.segments \?\? \[\]\)\]/, "toPatch 没把段发出去");
  // 能力级的预览必须是"这一发真会发出去的提示词"（模板 + 段），不是只把段拼起来
  assert.match(editor, /abilityPrompt\(r\.prompt, r\.segments/, "预览没走能力的完整拼装");
  assert.match(editor, /ABILITY_TEMPLATE_SEGMENTS\[id\]/, "预览不知道这能力的模板住在哪几条内置段");
});

// 能力的模板 2026-10-28 起都住在段库（用户：「把默认压缩载荷挑出来，制作成单独的内置 segment」
// + 起标题那条同一个道理；后来又加了「适用于 RP Agent 的压缩用提示词」）。
// 前端要 preview 它，就得知道那几条内置段的 id —— 而**真正的模板仍由后端拼**。
// 这些 id 在两处各写一遍（Go 常量 / JS 常量），写错**不会报错**，只会让预览少一段。
//
// 两条都要钉住：① 数组第一条 = 后端那个 `*TemplateSegmentID`（默认）；
// ② 每一条候选都**真的在内置段清单里** —— 列表里挂着一条库里没有的 id，
// 挑它的时候后端会静默跳过（`JoinPrompt` 只是跳过 + 打日志），界面照挑不误。
test("能力的模板段 id：前端常量与后端 const / 内置段清单都对得上", () => {
  const go = readFileSync(join(REPO, "core", "internal", "config", "config.go"), "utf8");
  const constValue = (name) => {
    const found = go.match(new RegExp(`${name}\\s*=\\s*"([^"]+)"`));
    assert.ok(found, `core/internal/config/config.go 里找不到 ${name}`);
    return found[1];
  };
  // ① 第一条是默认
  const defaults = [
    ["compact", "CompactTemplateSegmentID"],
    ["title", "TitleTemplateSegmentID"],
  ];
  for (const [ability, constName] of defaults) {
    assert.equal(ABILITY_TEMPLATE_SEGMENTS[ability][0], constValue(constName),
      `前后端的 ${ability} 默认模板段 id 对不上`);
  }
  // ② 每条候选都在内置段清单里（清单里那一行写的是 Go 常量名或字面量 id）
  const listed = [...go.matchAll(/\{ID: ([^,]+), File: "segments\/[^"]+\.md"\}/g)].map((m) => m[1].trim());
  assert.ok(listed.length >= 7, `内置段清单只解析出 ${listed.length} 条`);
  const listedIDs = listed.map((name) => (go.match(new RegExp(`${name}\\s*=\\s*"([^"]+)"`))?.[1] ?? name.replace(/^"|"$/g, "")));
  for (const [ability, ids] of Object.entries(ABILITY_TEMPLATE_SEGMENTS)) {
    for (const id of ids) {
      assert.ok(listedIDs.includes(id), `${ability} 的模板段「${id}」不在内置段清单里 —— 挑它等于什么都没挑`);
    }
  }
  // 退役的 judge 不该在这张表里：它连流程都没有，登记进来等于预览里凭空多一段。
  assert.equal(ABILITY_TEMPLATE_SEGMENTS.judge, undefined, "judge 已退役，不该登记模板段");
});

// 能力 id 也是"两处各写一遍"的那种：前端 `ABILITY_IDS` 决定编辑器画出几块，
// 后端 `abilities.definitions` 决定校验认几个 —— 少同步一处**不会报错**，
// 只是界面多出一块点了没反应的开关（`judge` 那块正是这么来的）。
test("能力 id：前端 ABILITY_IDS 与后端 abilities.definitions 一致", () => {
  const go = readFileSync(join(REPO, "core", "internal", "abilities", "abilities.go"), "utf8");
  const block = go.match(/var definitions = \[\]Definition\{([\s\S]*?)\n\}/);
  assert.ok(block, "core/internal/abilities/abilities.go 里找不到 definitions 表");
  // definitions 里写的是 Go 常量名（Title / Compact）⇒ 转成 id 再比
  const goIDs = [...block[1].matchAll(/\{ID: (\w+),/g)].map((m) => m[1].toLowerCase());
  assert.deepEqual(ABILITY_IDS, goIDs, "前端 ABILITY_IDS 与后端 definitions 对不上");

  // 退役的**不在** definitions 里 —— 否则老配置会读不进来
  assert.ok(!goIDs.includes("judge"), "judge 已退役（2026-10-28 用户：「暂时砍掉 agent 的 judge 功能」），别加回 definitions");
  // 但必须在 config 的退役表里：读文件时靠它把这条历史摘掉（摘不掉就一直躺在 agents.json 里）
  const cfg = readFileSync(join(REPO, "core", "internal", "config", "config.go"), "utf8");
  assert.ok(/var RetiredAbilityIDs = map\[string\]bool\{\s*"judge": true,/.test(cfg),
    "config.RetiredAbilityIDs 里该有 judge（读入时摘掉它，否则它会一直躺在老文件里）");
  assert.ok(cfg.includes("agents.dropRetiredAbilities()"),
    "config.LoadAgents 该调 dropRetiredAbilities —— 否则只有启动时才摘、问一次接口又冒回来");
});

test("能力提示词拼装：覆盖打头 / 空则垫默认模板段 / 挑了它就替换", () => {
  const comp = ["压缩提示词", "RP压缩提示词"];
  const segs = [
    { id: "压缩提示词", text: "【模板】照我说的压。", builtin: true },
    { id: "RP压缩提示词", text: "【RP模板】讲处境与停点。", builtin: true },
    { id: "风格", text: "【风格】用第二人称。" },
  ];
  // 什么都没填 ⇒ 就是默认模板段正文
  assert.equal(abilityPrompt("", [], segs, comp), "【模板】照我说的压。");
  // 加了普通段 ⇒ 模板在前
  assert.equal(abilityPrompt("", ["风格"], segs, comp), "【模板】照我说的压。\n\n【风格】用第二人称。");
  // 把默认模板段自己挑进来 ⇒ 按列表位置出现，**不重复**
  assert.equal(abilityPrompt("", ["风格", "压缩提示词"], segs, comp), "【风格】用第二人称。\n\n【模板】照我说的压。");
  // 挑**候选**（RP 版）⇒ 整份替换默认，不夹通用版（两份模板会互相打架）
  assert.equal(abilityPrompt("", ["RP压缩提示词"], segs, comp), "【RP模板】讲处境与停点。");
  assert.equal(abilityPrompt("", ["风格", "RP压缩提示词"], segs, comp), "【风格】用第二人称。\n\n【RP模板】讲处境与停点。");
  // 填了覆盖 ⇒ 覆盖打头，不再垫默认
  assert.equal(abilityPrompt("【我的】短一点。", ["风格"], segs, comp), "【我的】短一点。\n\n【风格】用第二人称。");
  // 没有模板段的能力（退役的 judge 那种）⇒ 行为就是普通的 base + 段
  assert.equal(abilityPrompt("base", ["风格"], segs, undefined), "base\n\n【风格】用第二人称。");
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

// 输入栏形态（2026-10-20 用户定：**主题项** —— 要效率还是要美观）。
// 证伪点：① 默认不是浮空（老用户开机被改成实心）；② 空串/坏值被当成 solid（应回落 float）；
// ③ 切回 float 时属性没摘掉（CSS 还在按 solid 画，界面"切了没反应"）；④ 值不是 solid 也挂属性。
test("输入栏形态：默认浮空、只认 solid、切回来要摘掉属性", () => {
  stubStorage();
  const root = { attrs: {}, setAttribute(k, v) { this.attrs[k] = v; }, removeAttribute(k) { delete this.attrs[k]; } };
  const originalDoc = globalThis.document;
  globalThis.document = { documentElement: root };
  try {
    assert.equal(DEFAULT_COMPOSER, "float");
    assert.equal(composerStyle(), "float");            // 没设过 ⇒ 浮空（既有观感）
    assert.deepEqual(root.attrs, {});                  // 没调 apply 之前什么都不挂

    applyComposerStyle();                              // 缺省：属性不该出现
    assert.equal(root.attrs["data-composer"], undefined);

    saveComposerStyle("solid");
    assert.equal(composerStyle(), "solid");
    assert.equal(root.attrs["data-composer"], "solid");

    saveComposerStyle("float");                        // 切回来 ⇒ 属性摘掉（CSS 才知道回浮空）
    assert.equal(composerStyle(), "float");
    assert.equal(root.attrs["data-composer"], undefined);

    localStorage.setItem("mc_composer_style", "nonsense"); // 坏值 ⇒ 回落默认，不许当 solid
    assert.equal(composerStyle(), "float");
    localStorage.setItem("mc_composer_style", "");         // 空串 = 没设过
    assert.equal(composerStyle(), "float");
  } finally {
    globalThis.document = originalDoc;
  }
});

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

test("偏好读取：默认与空串语义（mc_theme 空串 / 认不出的 id 都归默认主题）", () => {
  stubStorage();
  assert.equal(apiBase(), DEFAULT_API_URL);
  assert.equal(themePref(), DEFAULT_THEME);
  assert.equal(sendMode(), DEFAULT_SEND);
  // 2026-10-28：`""` 以前是"跟随系统"，那个选项删了 ⇒ 现在它就是"没设过"，落到默认主题。
  localStorage.setItem("mc_theme", "");
  assert.equal(themePref(), DEFAULT_THEME);
  // 删主题留下的老 id 不许让页面"没有主题"：themePref 读的时候就归一化了。
  localStorage.setItem("mc_theme", "midnight");
  assert.equal(themePref(), "dark");
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


// —— 消息列渲染行 v2（2026-10-10：窗口/分段/摘要折叠，摘要原文展开才拉） ——
import {
  computeSegments, windowStart, prevWindowStart, alignWindowFrom, olderCount, runsToFetch,
} from "../src/utils/chatrows.js";

const sum = (id, b, e, parent = null, dirty = false) => ({
  id, parent_summary_id: parent, begin_idx: b, end_idx: e, text: `S${id}`, dirty,
});

test("分段：顶层摘要吃一段（端点序号来自接口）；孩子不另露；空档按消息段走", () => {
  const segs = computeSegments(6, [sum("c", 2, 3, "s"), sum("s", 2, 4)]);
  assert.deepEqual(segs.map((x) => x.kind), ["msg", "summary", "msg", "msg"]);
  assert.deepEqual(segs.map((x) => x.b ?? x.idx), [1, 2, 5, 6]);
  // 端点缺（悬空）= 不折；span 越界（快照滞后）= 按 total 夹住
  assert.deepEqual(computeSegments(3, [{ id: "x", begin_idx: undefined, end_idx: undefined }]).map((x) => x.idx), [1, 2, 3]);
  const clamped = computeSegments(3, [sum("y", 2, 9)]);
  assert.equal(clamped[1].kind, "summary");
  assert.equal(clamped[1].e, 3);
});

test("窗口：首屏取最后 N 段；往回一页 N 段；结构变了对回段边界", () => {
  const segs = computeSegments(6, [sum("s", 2, 4)]); // [m1, sum2-4, m5, m6]
  assert.equal(windowStart(segs, 2), 5);
  assert.equal(windowStart(segs, 4), 1);
  assert.equal(prevWindowStart(segs, 5, 2), 1);
  assert.equal(prevWindowStart(segs, 1, 2), 1); // 已到最老
  assert.equal(olderCount(segs, 5), 2);
  assert.equal(olderCount(segs, 1), 0);
  // 压缩把它并成 s(1-6)：原窗口起点 5 落在卡里 ⇒ 提到 1
  assert.equal(alignWindowFrom(computeSegments(6, [sum("s", 1, 6)]), 5, 2), 1);
  // 删除砍到 2 条：起点 5 越过末尾 ⇒ 回到最后一页
  assert.equal(alignWindowFrom(computeSegments(2, []), 5, 2), 1);
});

test("要拉的区间：摘要段跳过；尾巴开区间（吸收总数快照滞后）", () => {
  const segs = computeSegments(8, [sum("s", 3, 5)]); // [m1, m2, sum3-5, m6, m7, m8]
  assert.deepEqual(runsToFetch(segs, 1, Infinity), [[1, 2], [6, Infinity]]);
  assert.deepEqual(runsToFetch(segs, 4, 7), [[6, 7]]); // 窗口起点落在摘要里：只取消息段
  const noSum = computeSegments(5, []);
  assert.deepEqual(runsToFetch(noSum, 3, 4), [[3, 4]]);
  // 最后一段是摘要（理论上不该有）：尾巴不开区间，别把被盖的原文顺手拉了
  const tailSum = computeSegments(3, [sum("t", 2, 3)]);
  assert.deepEqual(runsToFetch(tailSum, 1, Infinity), [[1, 1]]);
});

test("渲染行：卡（展开才带原文）+ 编号 + 库外尾巴顺延", () => {
  const segs = computeSegments(4, [sum("s", 2, 3)]);
  const messages = [
    { id: "m1", idx: 1, content: "一" },
    { id: "m4", idx: 4, content: "四" },
    { id: "tmp", content: "刚发的" },
    { id: "stream", content: "", stream: true },
    { id: "err", content: "失败：x", noIdx: true },
  ];
  const rows = buildChatRows({ segments: segs, windowFrom: 1, messages, summaryMsgs: new Map() });
  assert.deepEqual(rows.map((r) => r.kind), ["msg", "summary", "msg", "msg", "msg", "msg"]);
  assert.equal(rows[1].key, "sum-s");
  assert.equal(rows[1].fromIdx, 2);
  assert.equal(rows[1].toIdx, 3);
  assert.equal(rows[1].rows, null); // 没展开 ⇒ 没有原文
  assert.equal(rows[1].loading, false);
  assert.deepEqual(rows.filter((r) => r.kind === "msg").map((r) => r.displayIdx), [1, 4, 5, 6, null]);
  // 展开后（ready）带原文；loading 态也标出来
  const loaded = new Map([["s", { state: "ready", rows: [{ msg: { id: "m2", idx: 2 }, displayIdx: 2 }] }]]);
  const rows2 = buildChatRows({ segments: segs, windowFrom: 1, messages, summaryMsgs: loaded });
  assert.equal(rows2[1].rows.length, 1);
  const loading = new Map([["s", { state: "loading" }]]);
  assert.equal(buildChatRows({ segments: segs, windowFrom: 1, messages, summaryMsgs: loading })[1].loading, true);
  // 窗口起点在卡后面：卡不进渲染行；窗口外的老消息也不许冒出来
  const rows3 = buildChatRows({ segments: segs, windowFrom: 4, messages, summaryMsgs: new Map() });
  assert.deepEqual(rows3.map((r) => r.kind), ["msg", "msg", "msg", "msg"]);
  assert.deepEqual(rows3.map((r) => r.displayIdx), [4, 5, 6, null]);
});

test("摘要重摇家族：三个方法的形状（enter 带消息 idx；switch 带位次）", async () => {
  const seen = [];
  const fetchFn = async (url, opts) => { seen.push([url, opts.method, opts.body]); return { status: 200, ok: true, json: async () => ({}) }; };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await api.rerollSummaryEnter("s1", 3);
  await api.rerollSummaryState("s1");
  await api.rerollSummarySwitch("s1", 2);
  assert.deepEqual(seen[0], ["http://x/api/v1/sessions/s1/reroll-summary", "POST", '{"idx":3}']);
  assert.deepEqual(seen[1], ["http://x/api/v1/sessions/s1/reroll-summary", "GET", undefined]);
  assert.deepEqual(seen[2], ["http://x/api/v1/sessions/s1/reroll-summary/switch", "POST", '{"idx":2}']);
});


test("任务行文案：只数在跑的、kind 去重、0 个 ⇒ 空串", () => {
  assert.equal(taskLineText(null), "");
  assert.equal(taskLineText({ running: 0, tasks: [{ kind: "turn", finished_at: 123 }] }), "");
  assert.equal(
    taskLineText({ running: 2, tasks: [{ kind: "turn" }, { kind: "compact" }, { kind: "turn" }] }),
    "⟳ 后台任务 2：turn · compact",
  );
  assert.equal(taskLineText({ running: 1, tasks: [{ kind: "" }] }), "⟳ 后台任务 1");
});

test("/tasks 端点形状：GET，回 running + tasks（轮询用）", async () => {
  let seen = null;
  const fetchFn = async (url, opts) => { seen = [url, opts.method]; return { status: 200, ok: true, json: async () => ({ running: 1, tasks: [] }) }; };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await api.tasks();
  assert.deepEqual(seen, ["http://x/api/v1/tasks", "GET"]);
});


test("normalizeApiBase：补协议、补 /api/v1、去尾斜杠、空串回空", () => {
  assert.equal(normalizeApiBase("192.168.0.110:8787"), "http://192.168.0.110:8787/api/v1");
  assert.equal(normalizeApiBase("http://x:8787"), "http://x:8787/api/v1");
  assert.equal(normalizeApiBase("http://x:8787/api/v1/"), "http://x:8787/api/v1");
  assert.equal(normalizeApiBase("  https://box/api/v1  "), "https://box/api/v1");
  assert.equal(normalizeApiBase(""), "");
});

test("probeAuthStatus：先规范化再探 /auth/status；非 microchat 的 200 拒绝；authLogin 形状", async () => {
  const ok = await probeAuthStatus("x:8787", { fetchFn: async (url) => {
    assert.equal(url, "http://x:8787/api/v1/auth/status"); // 裸地址 ⇒ 补协议、补 /api/v1
    return { ok: true, status: 200, json: async () => ({ status: "ok", version: "1", needs_setup: false }) };
  } });
  assert.equal(ok.needs_setup, false);
  await assert.rejects(
    () => probeAuthStatus("http://x:8787", { fetchFn: async () => ({ ok: true, status: 200, json: async () => ({ hello: "world" }) }) }),
    (e) => /不是 microchat/.test(e.message),
  );
  const login = await authLogin("x:8787", "alice", "pw", { fetchFn: async (url, opts) => {
    assert.equal(url, "http://x:8787/api/v1/auth/login");
    assert.equal(opts.method, "POST");
    assert.deepEqual(JSON.parse(opts.body), { user: "alice", password: "pw" });
    return { ok: false, status: 401, json: async () => ({ error: { code: "unauthorized", message: "用户名或口令不对" } }) };
  } }).catch((e) => e);
  assert.equal(login.status, 401);
  assert.equal(login.message, "用户名或口令不对");
});

test("默认后端地址跟着页面走：hostname:8787；IPv6 套方括号；存过的最优先；候选同源在前", () => {
  stubStorage();
  assert.equal(defaultApiBase(), DEFAULT_API_URL); // node 里没有 location ⇒ 退回默认
  globalThis.location = { hostname: "192.168.0.110", origin: "http://192.168.0.110:8788" };
  assert.equal(defaultApiBase(), "http://192.168.0.110:8787/api/v1");
  assert.equal(apiBase(), "http://192.168.0.110:8787/api/v1"); // 没存过 ⇒ 用推导值
  // 候选：**同源第一**（vite 代理把 /api 转给后端 ⇒ 单容器部署零配置直进），推导值兜底。
  assert.deepEqual(apiBaseCandidates(), ["http://192.168.0.110:8788/api/v1", "http://192.168.0.110:8787/api/v1"]);
  globalThis.location = { hostname: "::1", origin: "http://[::1]:8788" };
  assert.equal(defaultApiBase(), "http://[::1]:8787/api/v1");
  assert.deepEqual(apiBaseCandidates(), ["http://[::1]:8788/api/v1", "http://[::1]:8787/api/v1"]);
  localStorage.setItem("mc_api", "http://box:9999/api/v1");
  assert.equal(apiBase(), "http://box:9999/api/v1"); // 存过 ⇒ 只认存的
  assert.deepEqual(apiBaseCandidates(), ["http://box:9999/api/v1"]);
  delete globalThis.location;
});


test("键盘拟合：高度取整、平移跟走；拿不到高或捏合缩放 ⇒ 不拟合（null）", () => {
  assert.deepEqual(computeViewportFit({ height: 512.7, offsetTop: 0, scale: 1 }), { h: 513, y: 0 });
  assert.deepEqual(computeViewportFit({ height: 512, offsetTop: 118.4, scale: 1 }), { h: 512, y: 118 });
  assert.deepEqual(computeViewportFit({ height: 512, offsetTop: -3, scale: 1 }), { h: 512, y: 0 });
  assert.equal(computeViewportFit({ height: 0 }), null);
  assert.equal(computeViewportFit({ height: 512, scale: 2 }), null);
  assert.equal(computeViewportFit({}), null);
});


test("跟手拖拽：轴向锁、拖哪扇、开度换算、松手吸附", () => {
  // 轴向锁：先到先得；纵向的手势整个放给浏览器
  assert.equal(classifyAxis(3, 2), null);
  assert.equal(classifyAxis(20, 5), "h");
  assert.equal(classifyAxis(5, 30), "v");
  // 拖哪扇：都没开朝哪拖哪扇；左开着只认往左（关回去）、右开着只认往右
  assert.deepEqual(pickDrag({ dx: 40, leftDrawer: true, rightDrawer: true, leftOpen: false, rightOpen: false }), { bar: "left", base: 0 });
  assert.deepEqual(pickDrag({ dx: -40, leftDrawer: true, rightDrawer: true, leftOpen: false, rightOpen: false }), { bar: "right", base: 0 });
  assert.deepEqual(pickDrag({ dx: -40, leftDrawer: true, rightDrawer: true, leftOpen: true, rightOpen: false }), { bar: "left", base: 1 });
  assert.equal(pickDrag({ dx: 40, leftDrawer: true, rightDrawer: true, leftOpen: true, rightOpen: false }), null);
  assert.deepEqual(pickDrag({ dx: 40, leftDrawer: true, rightDrawer: true, leftOpen: false, rightOpen: true }), { bar: "right", base: 1 });
  assert.equal(pickDrag({ dx: -40, leftDrawer: true, rightDrawer: true, leftOpen: false, rightOpen: true }), null);
  assert.equal(pickDrag({ dx: -40, leftDrawer: false, rightDrawer: false, leftOpen: false, rightOpen: false }), null); // 平板：不是抽屉就不接管
  // 开度：换算 + 夹在 0..1（跟手关时用 base=1 反向算）
  assert.equal(Math.round(dragProgress(0, 150, 300, "left") * 100), 50);
  assert.equal(Math.round(dragProgress(0, -150, 300, "right") * 100), 50);
  assert.equal(dragProgress(0, -50, 300, "left"), 0);
  assert.equal(dragProgress(0, 400, 300, "left"), 1);
  assert.equal(Math.round(dragProgress(1, -150, 300, "left") * 100), 50);
  assert.equal(dragProgress(1, 150, 300, "left"), 1);
  // 松手吸附：中点 + 两个方向的快甩（v 单位 = 开度/ms，真实手速量级 ~0.002-0.01）
  assert.equal(resolveRelease(0.55, 0), "open");      // 慢拖过中点
  assert.equal(resolveRelease(0.45, 0), "close");     // 慢拖不过中点
  assert.equal(resolveRelease(0.2, 0.004), "open");   // 快甩朝开（60px/30ms 量级）
  assert.equal(resolveRelease(0.8, -0.004), "close"); // 快甩朝关（对称规则；此前缺了它 ⇒ 关手势发涩）
  assert.equal(resolveRelease(0.2, 0.0005), "close"); // 慢慢挪到 20%：还不算开
  assert.equal(resolveRelease(0.97, -0.006), "open"); // 几乎没拖就恨恨一甩：按兵不动（p>1-flickAt，位置也没过中点）
  assert.equal(resolveRelease(0.05, 0.006), "close"); // 没动几步的光速点击式滑动不算
  assert.equal(classifyAxis(5, 2), null);             // 轴向锁现在 6px
  assert.equal(classifyAxis(7, 2), "h");
});

// 重摇期间的显示（2026-10-17 用户定）：点下 ⟳ 就**立刻**切空白 + 「正在输出…」，
// 旧正文一直挂到第一帧才被换掉"有点丑"。证伪点：live 时回落 msg.content（又把旧正文挂回去）、
// startingId 对不上也当 live（清错了别的消息）、摇完了还判 live（该显示库内正文/候选）。
test("重摇：正在摇的那条 = 刚落手（startingId）或后端说 running", () => {
  const msg = { messageId: "m1", content: "旧正文" };
  assert.equal(rerollTargetLive(msg, {}), false);
  assert.equal(rerollTargetLive(msg, { startingId: "m1" }), true);          // 刚点下、状态还没回来
  assert.equal(rerollTargetLive(msg, { startingId: "m2" }), false);        // 别的消息不受影响
  assert.equal(rerollTargetLive(msg, { state: { active: true, running: true, target_message_id: "m1" } }), true);
  assert.equal(rerollTargetLive(msg, { state: { active: true, running: false, target_message_id: "m1" } }), false); // 摇完了
  assert.equal(rerollTargetLive(msg, { state: { active: false, running: true, target_message_id: "m1" } }), false);
  assert.equal(rerollTargetLive({ messageId: null, content: "x" }, { startingId: null }), false); // 库外那条（还没 id）
});

test("气泡正文：合成流 > 正在摇的那条（空白起步）> 库内字段", () => {
  const stored = { messageId: "m1", content: "旧正文", reasoning: "旧思考" };
  assert.equal(bubbleText(stored, { live: true, previewText: "" }), "");   // 摇的过程中还没有字 ⇒ 空（不是旧正文）
  assert.equal(bubbleText(stored, { live: true, previewText: null }), "");
  assert.equal(bubbleText(stored, { live: true, previewText: "新的一版" }), "新的一版");
  assert.equal(bubbleText(stored, { live: false }), "旧正文");              // 不摇 ⇒ 库内正文
  assert.equal(bubbleText(stored, { field: "reasoning", live: false }), "旧思考"); // 思考各读各的字段
  const streamRow = { stream: true, content: "", reasoning: "" };
  assert.equal(bubbleText(streamRow, { streamText: "正在吐" }), "正在吐"); // 一轮的合成流优先
  assert.equal(bubbleText(streamRow, { streamText: null }), "");
});

// 滚动隐藏上下栏（2026-10-17 用户定，触屏默认开）：进度与位移**成正比**、一次只藏一边、
// 反向要攒够 slop 才算换向（真手指的滚动是一串正负小步，不滤就抖出重影）。
// 证伪点：变成阈值开关（dy 小就不动）、两边一起藏、不夹上限（收成负数/超过整栏高）、到顶不展开。
test("滚动隐藏：往下看收顶栏、往回看收底栏，比例 1:1 且夹在 0..栏高", () => {
  const H = { headerH: 40, composerH: 64 };
  let s = resetChrome(500);
  // 往下看 12px ⇒ 顶栏收 12、底栏不动（本来就没收）
  s = stepChrome(s, { dy: 12, scrollTop: 512, ...H });
  assert.equal(s.top, 12);
  assert.equal(s.bottom, 0);
  // 再往下 100 ⇒ 顶栏到顶（40）、底栏仍是 0
  s = stepChrome(s, { dy: 100, scrollTop: 612, ...H });
  assert.equal(s.top, 40);
  assert.equal(s.bottom, 0);
  // 往回看 30 ⇒ 底栏收 30、顶栏退 30
  s = stepChrome(s, { dy: -30, scrollTop: 582, ...H });
  assert.equal(s.bottom, 30);
  assert.equal(s.top, 10);
  // 再往回 200 ⇒ 底栏封顶在栏高、顶栏退到 0
  s = stepChrome(s, { dy: -200, scrollTop: 382, ...H });
  assert.equal(s.bottom, 64);
  assert.equal(s.top, 0);
});

test("滚动隐藏：反向不足 slop 当抖动；上滑到顶全放回顶栏", () => {
  const H = { headerH: 40, composerH: 64 };
  let s = stepChrome(resetChrome(1000), { dy: 20, scrollTop: 1020, ...H }); // 往下看，收 20
  assert.equal(s.top, 20);
  // 反向 3px（< slop=6）⇒ 什么都不变（方向没换、量也没动）
  const jitter = stepChrome(s, { dy: -3, scrollTop: 1017, ...H });
  assert.equal(jitter.top, 20);
  assert.equal(jitter.bottom, 0);
  // 反向再来 5px（累计 8 ≥ slop）⇒ 真换向：底栏收、顶栏退
  const turned = stepChrome(jitter, { dy: -5, scrollTop: 1012, ...H });
  assert.equal(turned.dir, -1);
  assert.equal(turned.bottom, 5);
  assert.equal(turned.top, 15);
  // 滚回顶部 ⇒ 顶栏立刻展开（底栏按位移逐步退，不"啪"）
  const atTop = stepChrome(turned, { dy: -1012, scrollTop: 0, ...H });
  assert.equal(atTop.top, 0);
  assert.equal(atTop.bottom, 0);
});

// —— 主题 id 的一致性守卫（2026-10-28 加，同日随「只留三套」收紧）——
// 主题 id 在**三处**各写一遍：`src/utils/prefs.js` 的 `THEME_IDS`、`src/assets/theme.css` 的
// `html[data-theme="…"]` 规则、`UserView.vue` 那个下拉的 option。改了一处忘了另一处**不会报错**
// —— 页面只是安静地退回默认配色（或者让人选到一个没有规则的 id）。
// 所以把三张表钉死：THEME_IDS 是唯一权威，另外两处必须**正好**是它（顺序也一样）。
// 读文件比对，不引 CSS/Vue 解析器。
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const SRC = join(dirname(fileURLToPath(import.meta.url)), "..", "src");
// 仓库根（契约测试偶尔要读后端源码/内置段文件来对口径 —— 两处各写一遍的常量最容易漂）
const REPO = join(SRC, "..", "..", "..");

function themeCssIds() {
  const css = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  return new Set([...css.matchAll(/html\[data-theme="([^"]+)"\]/g)].map((m) => m[1]));
}

test("主题：theme.css 与下拉必须正好是 THEME_IDS 这三套", () => {
  // theme.css：一条不多、一条不少（多出来 = 有人偷偷加了主题；少了 = 那份 id 没人给规则）
  assert.deepEqual([...themeCssIds()].sort(), [...THEME_IDS].sort(),
    "theme.css 的 data-theme 规则与 prefs.js 的 THEME_IDS 对不上");
  assert.ok(THEME_IDS.includes(DEFAULT_THEME), `DEFAULT_THEME="${DEFAULT_THEME}" 不在 THEME_IDS 里`);

  // 下拉：顺序也照 THEME_IDS（"亮色/暗色/Breeze" 是用户点名的顺序，别在界面上乱换）
  const vue = readFileSync(join(SRC, "components", "UserView.vue"), "utf8");
  // ⚠ 必须**只取主题那个 select**：UserView 里还有别的下拉（输入栏形态之类），
  // 广撒网会把 "float" 这种非主题值捞进来（第一版就是这么挂的）。
  const sel = vue.match(/<select v-model="theme">([\s\S]*?)<\/select>/);
  assert.ok(sel, '没找到 v-model="theme" 那个下拉');
  const chosen = [...sel[1].matchAll(/<option value="([^"]*)">/g)].map((m) => m[1]);
  assert.deepEqual(chosen, THEME_IDS, "主题下拉的 option 与 THEME_IDS 对不上");
});

test("主题：删掉的老 id 都能搬到现存的一档（不许让人主题凭空变样）", () => {
  // 这些 id 曾经写进过别人的 localStorage。删主题时如果只删 CSS 规则，他们匹配不到任何规则
  // ⇒ 页面退回默认配色，用户看到的是"我的主题自己变了"。
  const LEGACY = ["graphite", "deepseek", "midnight", "wine", "forest", "paper", "breeze-dark"];
  for (const id of LEGACY) {
    const moved = normalizeTheme(id);
    assert.ok(THEME_IDS.includes(moved), `老主题「${id}」被搬到了「${moved}」，那不是一个现存的主题`);
    assert.notEqual(moved, undefined);
  }
  // 深夜蓝/酒红/森绿都是深色 ⇒ 归暗色；日间白 ⇒ 亮色；Breeze Dark ⇒ Breeze。钉住这几条映射。
  assert.equal(normalizeTheme("midnight"), "dark");
  assert.equal(normalizeTheme("paper"), "light");
  assert.equal(normalizeTheme("breeze-dark"), "breeze");
  // 认不出的（含老版本的"跟随系统"空串）⇒ 默认，不再摘掉 data-theme。
  for (const junk of ["", null, undefined, "随便填的", "Dark"]) {
    assert.equal(normalizeTheme(junk), DEFAULT_THEME);
  }
});

// —— 界面缩放（2026-10-28，照 OpenWebUI 的 UI Scale）——
// 证伪点：① 认不出的值把字号弄成 0 / NaN（整页缩没了）；② 超出范围的滑杆值原样落盘
// （下次开机字号离谱，用户不知道怎么退回去）；③ 浮点尾数让同一档存成两个值；
// ④ 滑杆范围与 CSS 兜底对不上（拖到 85% 却按 100% 画）。
test("界面缩放：夹到范围内、对齐 5%、坏值回落 100%", () => {
  assert.equal(DEFAULT_TEXT_SCALE, 1);
  // 没设过 / 认不出的 ⇒ 100%（不是 0，也不能是 NaN —— 那会把 body 字号算成 0px）
  for (const junk of [null, undefined, "", "abc", 0, -1, NaN, Infinity]) {
    assert.equal(normalizeTextScale(junk), 1, `坏值 ${String(junk)} 该回落 100%`);
  }
  // 超范围往回收：滑杆给不出 5 倍，但 localStorage 什么都能塞
  assert.equal(normalizeTextScale(9), TEXT_SCALE_MAX);
  assert.equal(normalizeTextScale(0.1), TEXT_SCALE_MIN);
  // 对齐到 5%：0.87 不是滑杆上的格 ⇒ 落到最近的 0.85
  assert.equal(normalizeTextScale(0.87), 0.85);
  assert.equal(normalizeTextScale(1.02), 1);
  // 二进制尾数（17 * 0.05 = 0.8500000000000001）不许漏到 localStorage 里
  const s = String(normalizeTextScale(TEXT_SCALE_MIN));
  assert.ok(s.length <= 4, `存下来的值太长：${s}`);
});

test("界面缩放：落盘、读回、挂到根节点的 --app-text-scale", () => {
  stubStorage();
  const vars = {};
  const originalDoc = globalThis.document;
  globalThis.document = { documentElement: { style: { setProperty: (k, v) => { vars[k] = v; } } } };
  try {
    assert.equal(textScale(), 1);                 // 没设过 ⇒ 100%
    saveTextScale(1.15);
    assert.equal(localStorage.getItem("mc_text_scale"), "1.15");
    assert.equal(vars["--app-text-scale"], "1.15"); // 存完当场生效（不是等刷新）
    assert.equal(textScale(), 1.15);
    localStorage.setItem("mc_text_scale", "3");   // 手改过
    assert.equal(textScale(), TEXT_SCALE_MAX);    // 读的时候也要夹
    applyTextScale(0.9);
    assert.equal(vars["--app-text-scale"], "0.9");
  } finally {
    globalThis.document = originalDoc;
  }
});

// —— 外壳色（`<meta name="theme-color">` 与 PWA manifest）——
// 这两处是**静态**的，而主题是三套运行时切换的 ⇒ 只能钉"默认（暗色）那套的底色"。
// 证伪点：改了配色却忘了改它们 —— 2026-10-28 就真踩过：主题换成中性灰，
// 手机状态栏和 PWA 启动画面还是旧的 TG 深夜蓝 #0e1621，装成 PWA 的人看到的是割裂感。
test("外壳色：index.html 与 PWA manifest 跟暗色主题的底色一致", () => {
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  const dark = theme.match(/html\[data-theme="dark"\]\s*\{([\s\S]*?)\}/);
  assert.ok(dark, 'theme.css 里没找到 html[data-theme="dark"] 那一块');
  const bg = dark[1].match(/--tg-bg:\s*(#[0-9a-fA-F]{6})/);
  assert.ok(bg, "暗色主题里没读到 --tg-bg");
  const want = bg[1].toLowerCase();

  const html = readFileSync(join(SRC, "..", "index.html"), "utf8");
  const meta = html.match(/<meta name="theme-color" content="([^"]*)"/);
  assert.ok(meta, "index.html 里没有 theme-color meta");
  assert.equal(meta[1].toLowerCase(), want, "index.html 的 theme-color 与暗色主题底色对不上");

  const vite = readFileSync(join(SRC, "..", "vite.config.js"), "utf8");
  for (const key of ["background_color", "theme_color"]) {
    const m = vite.match(new RegExp(`${key}:\\s*"([^"]*)"`));
    assert.ok(m, `vite.config.js 里没有 ${key}`);
    assert.equal(m[1].toLowerCase(), want, `PWA manifest 的 ${key} 与暗色主题底色对不上`);
  }
});

// —— 压缩载荷预演（右栏「看载荷」）——
// 这条链是"四处各写一遍"的典型：按钮 → emit → App 的 @ 监听 → api 方法 → 渲染。
// 少接一环**不会报错**，只是按钮点了没反应（或点了报 undefined）—— 2026-10-28 之前
// 就踩过同类（`sessions-changed` 挂错了事件，刷的是会话左栏不是模型下拉）。
// 所以这里不测行为、只钉"四处都还在、并且名字逐个对得上"。
test("压缩载荷预演：按钮 / 事件 / App 监听 / 接口四处都接着", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  assert.match(bar, /"compact-payload"/, "PayloadBar 的 defineEmits 里没有 compact-payload");
  assert.match(bar, /@click="previewCompact"/, "压缩面板上没有「看载荷」按钮");
  assert.match(bar, /emit\("compact-payload"/, "按钮没有 emit 出 compact-payload");
  assert.match(bar, /setCompactWire/, "PayloadBar 没有暴露 setCompactWire");

  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /@compact-payload="previewCompactPayload"/, "App 没有监听 compact-payload");
  assert.match(app, /function previewCompactPayload/, "App 里没有 previewCompactPayload");
  assert.match(app, /api\.compactPayload\(/, "App 没有调 api.compactPayload");

  const client = readFileSync(join(SRC, "api", "client.js"), "utf8");
  assert.match(client, /compactPayload:\s*\(id, blocks\)/, "api 里没有 compactPayload");
  assert.match(client, /\/compact\/payload/, "api 打错了路径（该是 /compact/payload）");
});

// —— 上下文占比环（右上角，2026-10-28）——
// 这条链又是"每一环各写一遍"：App 算 ctx → 喂给 TopBar → 环画弧 → 点开浮板。
// 少接一环**不会报错**：App 不调 setContext ⇒ 环永远是空的（看着像"没占用"）。
// 另钉一条**反向**的：状态行不许再把上下文报回来 —— 同一个数字住两处，
// 只会让两处都可能过期（这正是这次搬家的理由）。
test("上下文环：App 喂 ctx / 环在顶栏画 / 状态行不再报占用", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /topbarRef\.value\?\.setContext\(ctx\)/, "App 没有把 ctx 喂给顶栏");
  assert.match(app, /topbarRef\.value\?\.setContext\(null\)/, "草稿态没有清掉环（会挂着上一条会话的数字）");
  assert.ok(!app.includes("statusOverBudget"), "App 还在用状态行的超预算（已经搬去环上了）");

  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  assert.match(top, /id="ctx-ring"/, "顶栏里没有那枚环");
  assert.match(top, /pathLength="100"/, "弧长没走 pathLength 归一化（dasharray 会被当成绝对长度）");
  assert.match(top, /setContext/, "TopBar 没有暴露 setContext");
  assert.match(top, /class="agent-menu ctx-menu"/, "浮板没有复用 Agent 选人那套底（--tg-float / shadow-3）");
  // 顺序：2026-10-28 定的是「环在左、载荷栏开关在右」—— 那是**栏还在**时的锚点关系。
  // 2026-10-29 右栏搬进上下文工作区后那枚开关没了，锚点换成**工作区两段开关**：
  // 它排在环左边（用户原话「就位于那个绿色圆环左侧」），环仍然是簇里最靠里的那枚仪表。
  // 三样东西都是绝对定位的兄弟，换顺序**不会报错**，只会悄悄换到另一边 —— 所以钉住先后。
  assert.ok(
    top.indexOf('class="ws-switch"') < top.indexOf('id="ctx-ring"'),
    "工作区开关该在环的左边（用户 2026-10-29 定）",
  );
  // 右栏的开关必须真的没了：留着它点了没反应（栏不存在），比没有更糟。
  assert.ok(!top.includes('id="toggle-right"'), "顶栏还留着载荷栏那枚 [◨] 开关 —— 栏已经搬走了");

  // 判规则时要**先剥掉注释**：这次的教训就写在一条注释里（"从前这里有 .statusline.over"），
  // 不剥的话字面匹配会把注释当成规则 —— 守卫测试自己先误报，比漏报更浪费人。
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /\.ctx-ring-fill \{ stroke: var\(--tg-ok\)/, "环没用 --tg-ok");
  // 三套主题都得有绿色，否则换主题后环就没了颜色（CSS 变量缺失 ⇒ stroke 回 initial = 黑）
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  const okCount = (theme.match(/--tg-ok:/g) ?? []).length;
  assert.equal(okCount, 4, `--tg-ok 该在 :root + 三套预设里各一条，实际 ${okCount} 条`);

  // —— 2026-10-28 用户定：「已连接 v0.1.0｜默认助手｜idle 这个也塞那个环的面板里」——
  // 于是输入框上方那行小字整条没了：连接 / 助手 / 运行改住环的浮板。四处联动，少一环都不会报错、
  // 只会让某处"看着还在但永远不更新"：
  //   ① App 把状态喂给顶栏（不再喂 Composer）② TopBar 存它并渲染成三行
  //   ③ Composer 没有状态行了 ④ main.css 没有 .statusline 规则了
  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  assert.ok(!composer.includes("statusline"), "Composer 里还留着状态行（已经搬进环的浮板）");
  assert.ok(!composer.includes("setStatus"), "Composer 还暴露着 setStatus（状态归顶栏）");
  assert.match(app, /topbarRef\.value\?\.setStatus\(/, "App 没把连接/助手/相位喂给顶栏");
  assert.ok(!app.includes("composerRef.value?.setStatus"), "App 还在往 Composer 塞状态行");
  assert.match(top, /function setStatus\(s\)/, "TopBar 没有 setStatus");
  assert.match(top, /const statusRows = computed/, "浮板里没有连接/助手/运行那三行");
  assert.match(top, /ctx-rows-status/, "浮板里那三行没接样式");
  assert.ok(!/\.statusline/.test(css), "main.css 里还留着 .statusline 规则（那一行已经删了）");
});

// 页签条也是"两处各写一遍"的那种东西（设置主栏五区 + Agent 编辑器的二级页签）。
// 底片滑动的算法（量 offsetLeft/offsetWidth）抄错一个像素看不出来，抄出两份就会一边顺一边顿，
// 而且 window resize / 界面缩放滑杆之后两边行为会分叉。⇒ 共用一个 `TabBar`。
test("页签条：设置主栏与 Agent 编辑器用的是同一个组件", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "TabBar.vue"), "utf8");
  assert.match(settings, /<TabBar v-model="tab" :tabs="SETTINGS_TABS" \/>/, "设置主栏没用 TabBar");
  assert.match(editor, /<TabBar v-model="tab" :tabs="tabs" \/>/, "Agent 编辑器没用 TabBar");
  // 手写一套的话会有这些痕迹：nav 里的 tab-thumb、以及自己去量 offsetLeft。
  for (const [name, src] of [["SettingsModal.vue", settings], ["AgentEditor.vue", editor]]) {
    assert.ok(!src.includes("tab-thumb"), `${name} 自己画了一枚 tab-thumb —— 该交给 TabBar`);
    assert.ok(!src.includes("offsetWidth"), `${name} 自己在量按钮宽度 —— 该交给 TabBar`);
  }
  assert.match(bar, /window\.addEventListener\("resize", moveThumb\)/, "TabBar 没跟窗口缩放（字大小变了底片会错位）");
});

// 用户 2026-10-28：「在具体某个 Agent 的设置界面里，我们最好也分 TAB，不然的话，全挤在一起很丑。」
// 分页签这件事**两个地方都要**：TabBar 在，且每个能力各占一页（加能力自动多一页，
// 别写成硬编码的两页 —— 硬编码的那种，加第三个能力时那个能力的开关会悄悄挤在最后一页里）。
test("Agent 编辑器分页签：基本 / 系统提示词 / 每个能力各一页", () => {
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  assert.match(editor, /\{ id: "base", label: "基本" \}/, "没有「基本」那一页");
  assert.match(editor, /\.\.\.ABILITY_IDS\.map\(\(id\) => \(\{ id, label: ABILITY_LABELS\[id\] \?\? id \}\)\)/,
    "能力那几页该由 ABILITY_IDS 生成（加能力自动多一页）");
  // 三个分区都在（v-show 切换：翻页不丢已填的东西）
  assert.match(editor, /v-show="tab === 'base'"/, "基本那一页没有分区");
  assert.match(editor, /v-show="tab === 'prompt'"/, "系统提示词那一页没有分区");
  assert.match(editor, /v-for="id in ABILITY_IDS" v-show="tab === id"/, "能力那几页没有分区");
  // 保存行在页签**外面**：哪一页都能存（藏在某一页里的那种，翻到别的页就找不到保存了）
  const saveAt = editor.indexOf(">保存<");
  const lastSectionEnd = editor.lastIndexOf("</section>");
  assert.ok(saveAt > lastSectionEnd, "保存行该在页签分区外面（否则只在某一页能看见）");
});

// 能力的**中文名**同样是"两处各写一遍"：前端 `ABILITY_LABELS`（页签名）与后端
// `abilities.definitions` 的 `Label`。叫法不一致不会报错，只会让人以为是两个东西。
test("能力中文名：前端 ABILITY_LABELS 与后端 definitions 的 Label 一致", () => {
  const go = readFileSync(join(REPO, "core", "internal", "abilities", "abilities.go"), "utf8");
  const block = go.match(/var definitions = \[\]Definition\{([\s\S]*?)\n\}/);
  assert.ok(block, "找不到 definitions 表");
  const pairs = [...block[1].matchAll(/\{ID: (\w+), Label: "([^"]+)"/g)];
  const goLabels = Object.fromEntries(pairs.map((m) => [m[1].toLowerCase(), m[2]]));
  assert.deepEqual(ABILITY_LABELS, goLabels, "前端能力名与后端 Label 对不上");
});

// —— 顶栏的模型选择浮板（2026-10-28 用户定：「可否把模型的那个选择器也改成这种自定义的面板，
// 按 Provider 分类的（接近 openwebui 风格的）面板？」）——
// 从前是个静默 `<select>`。换成自己的面板之后有两块要能验：
//   ① 纯逻辑（搜什么、怎么分组、当前那条显示什么）—— 直接测函数
//   ② 接线（按钮 → 面板 → emit → App 收）+ **旧的 `<select>` 真的删干净了**（CSS 留着就是死码）
const MODEL_SAMPLE = [
  {
    provider: "openrouter",
    items: [
      { value: modelKey("openrouter", "openai/gpt-4o"), label: "GPT-4o", provider_name: "OpenRouter", upstream_id: "openai/gpt-4o" },
      { value: modelKey("openrouter", "anthropic/claude-3.7"), label: "Claude 3.7", provider_name: "OpenRouter", upstream_id: "anthropic/claude-3.7" },
    ],
  },
  {
    provider: "本地",
    items: [
      { value: modelKey("本地", "qwen2.5:14b"), label: "Qwen 2.5 14B", provider_name: "本地 ollama", upstream_id: "qwen2.5:14b" },
    ],
  },
];

test("模型面板：按渠道分组、搜索命中显示名 / 渠道名 / 上游 id", () => {
  assert.equal(filterModelGroups(MODEL_SAMPLE, "").length, 2, "不搜的时候该有两个渠道组");
  // 组头用的是 provider_name（渠道的显示名），不是冷冰冰的 id
  assert.equal(filterModelGroups(MODEL_SAMPLE, "")[1].providerName, "本地 ollama");

  // 按显示名搜（不分大小写）
  const gpt = filterModelGroups(MODEL_SAMPLE, "gpt-4o");
  assert.equal(gpt.length, 1);
  assert.deepEqual(gpt[0].items.map((i) => i.label), ["GPT-4o"]);

  // 按上游 id 搜 —— 显示名里没有 "anthropic"，这条只有上游 id 能命中
  assert.equal(filterModelGroups(MODEL_SAMPLE, "anthropic")[0].items[0].label, "Claude 3.7");

  // 按渠道显示名搜 ⇒ 整组都在（搜"本地 ollama"该把它的模型都捞出来）
  const local = filterModelGroups(MODEL_SAMPLE, "本地 ollama");
  assert.equal(local.length, 1);
  assert.equal(local[0].items.length, 1);

  // 搜不到：空数组（界面上是"没有匹配的模型"），**不是**保留一堆空组头
  assert.deepEqual(filterModelGroups(MODEL_SAMPLE, "没有这个模型"), []);
  // 分完就空的组要丢掉 —— 留着空组头会让人以为"搜到了但没显示"
  assert.ok(filterModelGroups(MODEL_SAMPLE, "gpt").every((g) => g.items.length > 0), "不该留下空组");
});

test("模型面板：扁平序号连续（上下键要能一条条走完可见列表）", () => {
  // 顺序 = 分组顺序 + 组内顺序；序号 0..n-1 连续无洞
  const all = filterModelGroups(MODEL_SAMPLE, "");
  const flats = all.flatMap((g) => g.items.map((i) => i.flat));
  assert.deepEqual(flats, [0, 1, 2]);

  // 过滤之后仍然从 0 起、连续（否则上下键会跳空）
  const one = filterModelGroups(MODEL_SAMPLE, "gpt");
  assert.deepEqual(one.flatMap((g) => g.items.map((i) => i.flat)), [0]);
  // 搜索改变的是"序号是什么"，不是"序号从几开始"
  assert.deepEqual(filterModelGroups(MODEL_SAMPLE, "qwen").flatMap((g) => g.items.map((i) => i.flat)), [0]);
});

test("模型面板：当前模型不在列表里 ⇒ 单独一组排最前（别让它看着像没选）", () => {
  const orphan = { value: modelKey("删掉的渠道", "m1"), label: "删掉的渠道/m1（不在列表）" };
  const groups = filterModelGroups(MODEL_SAMPLE, "", orphan);
  assert.equal(groups.length, 3);
  assert.equal(groups[0].provider, ORPHAN_MODEL_GROUP);
  assert.equal(groups[0].providerName, "当前（不在列表）");
  assert.equal(groups[0].items[0].flat, 0, "孤儿项该拿到第一个序号");
  // 搜别的词时它可以让位（不是"永远钉在最上面"）
  assert.ok(!filterModelGroups(MODEL_SAMPLE, "gpt", orphan).some((g) => g.provider === ORPHAN_MODEL_GROUP));
});

test("模型面板：按钮上那行字（列表里有 → 显示名；没有 → 不在列表；列表还没到 → provider/model）", () => {
  const key = modelKey("openrouter", "openai/gpt-4o");
  assert.equal(curModelLabelOf(MODEL_SAMPLE, key), "GPT-4o");
  assert.equal(
    curModelLabelOf(MODEL_SAMPLE, modelKey("x", "y"), { value: modelKey("x", "y"), label: "x/y（不在列表）" }),
    "x/y（不在列表）",
  );
  // 列表还没到（异步）也不能空白：空白看着像"没选模型"
  assert.equal(curModelLabelOf([], modelKey("openrouter", "openai/gpt-4o")), "openrouter/openai/gpt-4o");
  assert.equal(curModelLabelOf([], ""), "选择模型");
});

test("模型面板：按钮 / 浮板 / emit / App 监听四处接着，且不再有原生 <select>", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");

  assert.match(top, /id="model-btn"/, "顶栏里没有那枚模型按钮");
  assert.match(top, /class="agent-menu model-menu"/, "浮板没有复用 Agent 选人那套底（--tg-float / shadow-3）");
  assert.match(top, /function toggleModel\(/, "TopBar 没有 toggleModel");
  assert.match(top, /emit\("select-model", value\)/, "面板选完没 emit select-model");
  assert.match(app, /@select-model=/, "App 没有监听 select-model（点了不会换模型）");
  // 三个浮板互斥：开模型面板要把 Agent 与上下文那两块关掉（否则叠在一起）
  assert.match(top, /agentOpen\.value = false;[\s\S]{0,80}ctxOpen\.value = false;/, "打开模型面板时没有关掉另外两块浮板");

  // 旧的静默下拉必须彻底删掉：留着 CSS 就是死码（chrome 里也不该再有这个 id）
  assert.ok(!top.includes('id="session-model"'), "TopBar 里还留着 #session-model");
  assert.ok(!top.includes("flat-select"), "TopBar 里还留着 .flat-select");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.ok(!/\.flat-select/.test(css), "main.css 里还留着 .flat-select 规则（下拉已经删了）");
  assert.match(css, /\.model-menu \{/, "main.css 里没有模型浮板的样式");
  // 搜索框与列表滚动分开：搜索框得留在上面不跟着滚
  assert.match(css, /\.model-list \{ overflow-y: auto/, "列表没有自己的滚动区（搜索框会跟着滚走）");
});

// —— 渠道编辑器里的 vendor / protocol 用中文名（2026-10-28 用户：「vendor 和 protocol……
//    应该用中文化的名字吧」）——
// 这里钉的是"名字只有一个来源"：协议的中文名与"这家能给哪些协议"全在后端
// （`providers.protocolNames` / `ProtocolOptionsFor`），界面只显示。
// 从前前端自己写死了一张 PROTOCOLS 表 —— 它既没有中文名，又把 `Validate` 一律拒绝的两个
// 占位协议（anthropic-messages / gemini-generate-content）摆进下拉：递给人一个按下去必炸的选项。
test("渠道编辑器：协议下拉来自后端，界面不自己列协议表", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  // 判"界面里有没有这个协议"之前要**先剥注释**：注释里提一句"这些都是占位、别列出来"是对的，
  // 留在注释里提醒后人恰恰是好事 —— 只有出现在**选项里**才是错。（行内 // 不整体剥：URL 里也有 //。）
  const code = editor
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/^\s*\/\/.*$/gm, "");

  // ① 不再有前端自己的协议清单
  assert.ok(!/const PROTOCOLS\s*=/.test(code), "编辑器里还留着一张自己的协议表（该从后端的 protocol_options 来）");
  // ② 占位协议绝不该在界面里出现（后端 Validate 一律拒绝它们）
  for (const dead of ["anthropic-messages", "gemini-generate-content"]) {
    assert.ok(!code.includes(dead), `编辑器里出现了后端一律拒绝的占位协议 ${dead}`);
  }
  // ③ 选项确实来自后端给的那一份
  assert.match(code, /curPreset\.value\?\.protocol_options/, "协议选项没接后端的 protocol_options");
  assert.match(code, /v-for="option in protocolChoices"/, "下拉没渲染 protocolChoices");
  // ④ 选了之后要有一句人话（下拉一关就只剩 id，用户没法判断区别）
  assert.match(code, /\{\{ protocolHint \}\}/, "选中的协议没有一句人话");
  // ⑤ vendor 与 protocol 的显示都带中文名（id 附后，不藏起来）
  assert.match(code, /\{\{ p\.name \}\}（\{\{ p\.vendor \}\}）/, "vendor 没显示中文名");
  assert.match(code, /\{\{ option\.label \}\}（\{\{ option\.protocol \}\}）/, "协议没显示中文名");
});

test("渠道列表：打中文名，内部 id 挂在 title 上", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /p\.vendor_name/, "列表没显示 vendor 的中文名");
  assert.match(settings, /p\.protocol_name/, "列表没显示 protocol 的中文名");
  // id 不能丢：手改 providers.json 的人得能对上号
  assert.match(settings, /:title="p\.vendor"/, "vendor 的内部 id 没留在 title 上");
  assert.match(settings, /:title="p\.protocol"/, "protocol 的内部 id 没留在 title 上");
});

// 新建渠道时"换 vendor 要不要跟着换 id"（2026-10-28）。
// 这段判错不会报错 —— 只会**静默地把人填好的名字换掉**，所以单独测。
test("新建渠道：换 vendor 跟着换 id，但手填过的 id 不许动", () => {
  // 一路点下来（id 还是自动填的）⇒ 跟着换
  assert.equal(providerIdForNextVendor("openai", "openai", "deepseek"), "deepseek");
  // 空着 ⇒ 也当自动值（新建第一屏）
  assert.equal(providerIdForNextVendor("", "", "deepseek"), "deepseek");
  // 手填过 ⇒ 一个字符都不碰（这是"别抢方向盘"那条）
  assert.equal(providerIdForNextVendor("我的渠道", "openai", "deepseek"), "我的渠道");
  assert.equal(providerIdForNextVendor("我的渠道", "", "deepseek"), "我的渠道");
  // 手填过之后又手动清空 ⇒ 回到"自动"那一侧
  assert.equal(providerIdForNextVendor("", "openai", "deepseek"), "deepseek");
  // 首尾空白不算内容（不然"填了个空格"就永远不再自动了）
  assert.equal(providerIdForNextVendor("  ", "", "dummy"), "dummy");
  // 没 vendor（理论上的空选）⇒ 清掉自动值，别留一个假 id
  assert.equal(providerIdForNextVendor("openai", "openai", ""), "");
});

// 新建渠道的三处流程改进（2026-10-28）：vendor 分组、先问 vendor 再问 id、端点给"真会打到哪儿"。
test("渠道编辑器：vendor 分组 / 先 vendor 后 id / 端点给出实际地址", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  const code = editor.replace(/<!--[\s\S]*?-->/g, "");

  // ① vendor 按后端给的 primary 分组，而不是平铺 11 个
  assert.match(code, /primaryPresets/, "vendor 没按 primary 分组");
  assert.match(code, /<optgroup label="常见">/, "没有「常见」那一组");
  // ② 顺序：vendor 在 id 前面（id 是改 vendor 时会跟着变的格，先填等于白填）
  const vendorAt = code.indexOf("走哪家（vendor）");
  const idAt = code.indexOf("<span>渠道 id</span>");
  assert.ok(vendorAt > 0 && idAt > 0, "找不到 vendor / id 那两格");
  assert.ok(vendorAt < idAt, "该先问 vendor、再问 id（id 会跟着 vendor 变）");
  // ③ 端点那一格要说出"真会打到哪儿"（留空 = 用预设，光靠 placeholder 说不清）
  assert.match(code, /effectiveBase/, "端点没有算出实际地址");
  assert.match(code, /这一发会打到/, "端点没把那句人话显示出来");
});

// 模型筛选器：多条规则用半角逗号分隔、命中任意一条就导入（2026-10-28 用户定：
// 「deepseek,mimo 会吧 deepseek-v4-flash 和 mimo-v2.6-flash 都允许，因为它们分别满足了一个规则」）。
//
// 这条只钉**界面把话说清楚了**：真逻辑在后端 registry.SplitModelFilter / FilterModels
// （那边有成组的单测）。但界面不说清"逗号"与"任意一条"，用户填了 `deepseek,mimo` 会以为
// 那是**一个**子串 ⇒ 一个模型都筛不出来，而界面还显示"已筛选" —— 静默的错。
test("渠道编辑器：模型筛选器说清逗号与「任意一条」", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  // **HTML 注释与 JS 注释都要剥**：设计意图写在注释里是对的（"命中任意一条就算中"），
  // 但这条测试问的是**界面上**有没有那么说。不剥 JS 注释的话，注释一句话就能让它假通过。
  const code = editor
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/^\s*\/\/.*$/gm, "");

  // 措辞不能再是"含此子串"（单条）—— 那正是要改掉的旧口径
  assert.ok(!/只导入 id 含此子串的模型/.test(code), "筛选器的标签还是单条子串口径");
  // 必须提到：半角逗号分隔 + 任意一条 + 一个含逗号的例子
  assert.match(code, /半角逗号/, "没说是半角逗号分隔");
  assert.match(code, /命中任意一条/, "没说清是 OR（命中任意一条）");
  assert.match(code, /deepseek,mimo/, "没给出那个具体的例子");
});


// 设置界面清理（2026-10-28 用户：「有没有无用选项？ 比如说 那个后端地址？」）。
//
// 「连接」页那一整页被删掉了：单容器部署里浏览器**永远同源**（vite 把 /api 代理给后端）、
// 地址由启动自检自动探出来（`apiBaseCandidates()`），连不上时门板会自己弹出来让人填。
// 更要紧的是它那个「保存」按钮绑的 `saveNet` 在重构时丢了 —— 点下去直接抛错。
// 这条钉的是"它不再回来"：留着只会让人以为"我得自己配点什么"。
test("设置：没有「连接」页，也没有绑不存在的 saveNet", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.ok(!settings.includes('id="panel-net"'), "「连接」页又回来了 —— 单容器部署里它没有用处");
  assert.ok(!/\btab === "net"/.test(settings), "还有指向 net 那一页的分区");
  // 绑一个不存在的函数 = 点下去必抛错（浏览器里是 ReferenceError，控制台才看得见）
  assert.ok(!/saveNet\b/.test(settings), "还在绑 saveNet（那个函数已经不存在了）");
  assert.ok(!/直接改地址|API 地址/.test(settings), "设置里又开始让人手填后端地址了");
});

// 页签条与 SETTINGS_TABS 是同一份：删一页要两边一起删（只删 panel、留着 tab，
// 点进去就是一块空白面板 —— 那种"看起来坏了"最费人）。
test("设置页签：id 与 panel 一一对应", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const panels = [...settings.matchAll(/v-show="tab === '([a-z]+)'"/g)].map((m) => m[1]);
  assert.deepEqual([...panels].sort(), ["agent", "chat", "prompt", "provider", "session"],
    `面板与页签对不上了：${panels.join(", ")}`);
  assert.deepEqual(SETTINGS_TABS.map((t) => t.id), ["chat", "session", "agent", "provider", "prompt"],
    "SETTINGS_TABS 的顺序变了");
});

// 「新会话默认值」那两格从前是**手打 id 的文本框**，说明文自己都写着
// 「填错不会报错，只会在发送时回落成『未配置模型』」—— 把校验推给用户。
// 改成下拉之后，能选出来的都是真存在的；但当前值即使不在列表里也必须保住（见下一条）。
test("设置：新会话默认值的渠道/模型是下拉，不是手打 id", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /providerOpts/, "渠道那格没走 providerChoices");
  assert.match(settings, /modelOpts/, "模型那格没走 modelChoicesFor");
  assert.match(settings, /@change="pickDefaultProvider\(\$event\.target\.value\)"/,
    "换渠道时不重算模型 —— 会留下「渠道 A + 只存在于 B 的模型」这种配不上的对");
  // 旧的"手填、填错不报错"那套说法不该再出现在界面上
  assert.ok(!/渠道与模型是手填的 id/.test(settings), "hint 还写着「渠道与模型是手填的 id」");
});

// 当前值不在列表里时**不许静默丢掉**：那可能是一个被删掉的渠道、一份手改过的
// providers.json，或 dummy 这种永远发现不到模型的渠道（它不联网）。
// 悄悄丢掉 = 一打开设置再一保存，人就发现自己的默认值被换成了别的。
test("设置默认值：当前值不在列表里也要留住，并说明白", () => {
  const opts = providerChoices([{ id: "a", vendor_name: "甲" }, { id: "b" }], "已删的渠道");
  const tail = opts[opts.length - 1];
  assert.equal(tail.value, "已删的渠道", "当前值被丢掉了");
  assert.ok(tail.missing, "没标出来它不在列表里");
  assert.equal(providerChoices([{ id: "a" }], "a").length, 1, "在列表里就不该重复补一条");

  // 模型那格的值是**裸 upstream_id**（不是 provider/model）：config.json 里存的就是裸 id，
  // 两处用两把尺会静默错位。
  const groups = [{ provider: "a", items: [{ upstream_id: "m1", label: "M1" }] }];
  assert.deepEqual(modelChoicesFor(groups, "a").map((c) => c.value), ["m1"]);
  assert.equal(modelChoicesFor(groups, "别的渠道").length, 0, "别的渠道的模型不该出现");
  const withCur = modelChoicesFor(groups, "a", "m-old");
  assert.equal(withCur[withCur.length - 1].value, "m-old");
  assert.ok(withCur[withCur.length - 1].missing);
});

// 上下文工作区（2026-10-29 用户定）：「每个对话分为俩部分：对话界面 和 上下文工作区 界面」，
// 开关「就位于那个绿色圆环左侧」。
//
// 这条钉三件事，每一件错了都不报错、只是"看起来怪"或"找不回来"：
//   ① 开关在**环左边**（顺序反了就成了"环跟着开关走"，位置全变）；
//   ② 切换**只换内容**、不动顶栏（顶栏带着开关，切掉就再也点不回来 —— 死锁）；
//   ③ 状态**不写 localStorage**（存下来下次打开直接落在空白工作区，人会以为消息没了）。
test("上下文工作区：开关在绿环左边，只切内容不切顶栏", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8").replace(/<!--[\s\S]*?-->/g, "");

  // ① 顺序：ws-switch 出现在 ctx-ring 之前，且都在 .header-right 里
  const switchAt = top.indexOf('class="ws-switch"');
  const ringAt = top.indexOf('id="ctx-ring"');
  const rightAt = top.indexOf('class="header-right"');
  assert.ok(switchAt > 0 && ringAt > 0 && rightAt > 0, "找不到 ws-switch / ctx-ring / header-right");
  assert.ok(rightAt < switchAt, "两段开关该在 .header-right 里（与环同一簇）");
  assert.ok(switchAt < ringAt, "开关该在绿环**左边**（用户原话），现在顺序反了");

  // ② 顶栏不在切换范围内：只有消息列 / 输入行 / 工作区三处带 v-show
  assert.match(app, /<main id="messages-vue" v-show="workspace === 'chat'"/, "消息列没跟着工作区切");
  assert.match(app, /<Composer\s+v-show="workspace === 'chat'"/, "输入行没跟着工作区切");
  // 标签现在是多行的（它接了 ref / 三个事件）⇒ 用 [\s\S] 跨行，别指望它一行写完。
  assert.match(app, /<ContextWorkspace\s+v-show="workspace === 'context'"/, "工作区没挂上");
  // 顶栏那行不许出现 workspace 的 v-show/v-if（切掉它 = 再也切不回来）
  const topbarTag = app.match(/<TopBar[\s\S]*?\/>/)?.[0] ?? "";
  assert.ok(!/v-show=|v-if=/.test(topbarTag), "顶栏被绑上了显示条件 —— 切到上下文就再也切不回来");

  // ③ 不落盘
  assert.ok(!/mc_workspace/.test(app), "工作区状态进了 localStorage —— 下次打开会落在空白工作区");
});

// 2026-10-29 第二轮（用户：「要不我们把右侧栏那个上下文给挪过来？」）：
// 右栏那三条 tab（载荷 / 状态 / 压缩）整块搬进上下文工作区，**栏本身删掉**。
//
// 这条钉的是"搬干净了"：搬一半最糟 —— 面板在新地方、栏的开关还在顶栏，点了没反应。
test("上下文工作区：右栏三 tab 搬进来了，栏本身删干净", () => {
  const ws = readFileSync(join(SRC, "components", "ContextWorkspace.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");

  // ① 面板挂在工作区里（不是各画一份 —— 两份必然在某天分叉）
  assert.match(ws, /import PayloadBar from "\.\/PayloadBar\.vue"/, "工作区没复用那块面板");
  assert.match(ws, /<PayloadBar/, "工作区里没渲染面板");
  // 四个 setter 一路透到面板身上（App 只认工作区这个 ref）
  for (const fn of ["setWire", "setTables", "setTree", "setCompactWire"]) {
    assert.match(ws, new RegExp(`${fn}:`), `工作区没转发 ${fn}（App 塞进来的数据到不了面板）`);
  }
  // 三件事照旧往上报（压缩树"看着才拉全量"靠它）
  assert.match(ws, /emit\('tab', t\)/, "tab 没往上报 —— 压缩树不会拉全量");
  assert.match(ws, /emit\('compact'/, "压缩没往上报");
  assert.match(ws, /emit\('compact-payload'/, "载荷预演没往上报");

  // ② 三条 tab 一个不少
  for (const t of ["payload", "state", "tree"]) {
    assert.match(bar, new RegExp(`selectTab\\('${t}'\\)`), `面板里少了 ${t} 那一页`);
  }

  // ③ 栏真的没了：它那些"栏才有的东西"一处都不许剩。
  //
  // ⚠ **判之前必须剥注释**：注释里写一句"`#scrim-right` 已经不存在了"是对的 ——
  // 那句正是在提醒后人别把它加回来。不剥注释，一句正确的注释就会让守卫假失败（踩过一次）。
  const strip = (text) => text
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/^\s*\/\/.*$/gm, "");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  for (const [name, raw] of [["PayloadBar.vue", bar], ["App.vue", app], ["TopBar.vue", top], ["main.css", css]]) {
    const text = strip(raw);
    assert.ok(!text.includes('id="payload-bar"') && !/^#payload-bar\b/m.test(text),
      `${name} 里还留着 #payload-bar —— 栏已经搬走，留着就是一条点不动的空壳`);
    assert.ok(!text.includes("scrim-right"), `${name} 里还留着右栏遮罩`);
    assert.ok(!text.includes("mc_bar_right_hidden"), `${name} 里还留着右栏的收起偏好`);
  }
  assert.ok(!top.includes("toggle-right"), "顶栏还有载荷栏开关");
  assert.ok(!/function toggleRight/.test(app), "App 里还留着 toggleRight");

  // ④ **删掉的声明，别名再被引用** —— 这一档 SSR 冒烟盖不到（错在 onMounted 里），
  // 却正是 2026-10-29 白屏那次：删了 `mqlRight` 的声明，漏了 onMounted/onUnmounted 里两处引用。
  // 构建不会报（JS 里未声明标识符是运行时的错），字面搜索也搜不到（我要搜的正是"它还在"）。
  // 只能显式列一份"这次删掉的名字"，逐个查引用。
  // ⚠ 下次删声明时把名字加进来 —— 清单漏了就等于没查。
  for (const gone of ["mqlRight", "narrowRight", "rightHidden", "toggleRight", "barSide", "scrimSide"]) {
    assert.ok(!new RegExp(`\\b${gone}\\b`).test(strip(app)),
      `App.vue 里还在引用 ${gone} —— 它已经删了，浏览器会 ReferenceError（整页白）`);
  }
  assert.ok(!top.includes("toggle-right"), "顶栏还有载荷栏开关");
  assert.ok(!/function toggleRight/.test(app), "App 里还留着 toggleRight");
  assert.ok(!/rightHidden/.test(app), "App 里还留着 rightHidden");
});

// 草稿态（还没建会话）时那三条 tab 全是空的 —— 要说一句，别让人以为面板坏了。
test("上下文工作区：草稿态要说明为什么是空的", () => {
  const ws = readFileSync(join(SRC, "components", "ContextWorkspace.vue"), "utf8");
  assert.match(ws, /hasSession/, "没区分草稿态");
  assert.match(ws, /草稿/, "草稿态没有一句人话");
});

// 切到上下文工作区时输入行是 display:none ⇒ getBoundingClientRect().height 量出来是 0。
// 那不是"输入行 0 高"，是"这一下没在量"：照写会让 `--composer-h` 掉到 0，
// 切回聊天时消息列底部留白先塌一帧再补回来（眼睛看得见，代码看不出来）。
test("工作区：量不到输入行高度时不写 --composer-h", () => {
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const fn = app.match(/function liftOverlays\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(fn, "找不到 liftOverlays");
  assert.match(fn, /if \(h <= 0\) return;/, "量到 0 时没提前返回 —— 隐藏态会把 --composer-h 写成 0");
  // 守卫必须在**真正的写入**之前（别用 indexOf("--composer-h")：注释里先提到它，
  // 那样写出来的断言会在守卫挪到写入之后时依然通过 —— 断言自己把自己骗了）。
  const guardAt = fn.indexOf("if (h <= 0) return;");
  const writeAt = fn.indexOf('setProperty("--composer-h"');
  assert.ok(writeAt > 0, "找不到写 --composer-h 的那句");
  assert.ok(guardAt > 0 && guardAt < writeAt, "守卫该在写 --composer-h 之前");
});

// 面板是 v-show 常驻（tab 状态要留着），所以"看着压缩 tab"这件事 App 只在**切 tab 那一下**知道。
// 于是从聊天切回工作区时树不会自己重拉 —— 而它可能刚被 setTree(null) 清过（换会话）。
// 这条钉的是那两个补喊 refreshTree 的地方（少一处就是"树一直空白"或"看着上一条会话的账"）。
test("上下文工作区：切进去 / 换会话时补拉压缩树", () => {
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const fn = app.match(/function selectWorkspace\(next\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(fn, "找不到 selectWorkspace");
  assert.match(fn, /workspace\.value === "context"\) refreshTree\(\)/,
    "切进工作区时没补拉压缩树 —— 树被 setTree(null) 清过就一直空着");
  // 换会话那条：setTree(null) 之后要紧跟一次 refreshTree。
  // ⚠ **别用 indexOf 找那句**：`refreshTree` 自己的 catch 里也有一模一样的
  // 「setTree(null)」（那一处是正确的：拉失败就清空），先命中的是它，断言就永远看着错的地方。
  // 锚点换成上方那句独有的 `summaryMsgs.clear();`。
  const anchor = app.indexOf("summaryMsgs.clear();");
  assert.ok(anchor > 0, "找不到换会话那段（summaryMsgs.clear）");
  const at = app.indexOf("payloadbarRef.value?.setTree(null);", anchor);
  assert.ok(at > 0, "换会话那段里没清树");
  const after = app.slice(at, at + 400);
  assert.match(after, /if \(workspace\.value === "context"\) refreshTree\(\)/,
    "换会话时没补拉压缩树 —— 会一直显示上一条会话的账");
});

// 上下文工作区：**平铺，不是面板**（2026-10-29 用户定：「不需要面板…我们完全可以平铺，
// 就像聊天界面的模型输出一样，不需要气泡框起来」）。
//
// 参照物是聊天里的 `.msg.assistant` —— 它本来就**没有底**（只有 `.msg.user` 才是气泡）。
// 所以工作区里也不该出现：卡片底、圆角、给每块内容套的边框。
// 这些都不会报错，只会"悄悄长回去"（下次谁想给某块加个 hover/分组，最顺手的就是加个 border +
// background + border-radius —— 三件套一上就又变成面板了）。
test("上下文工作区：平铺，不许再长出卡片和气泡", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const m = css.match(new RegExp(`(^|\\n)${sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`));
    return m ? m[2] : "";
  };

  // ① 外壳：没有底色、没有圆角、没有内边距
  const panel = rule(".ws-panel");
  assert.ok(panel, "找不到 .ws-panel 规则");
  assert.match(panel, /background:\s*none/, "工作区外壳又有底色了 —— 那又变成一张卡片");
  assert.match(panel, /border-radius:\s*0/, "工作区外壳又有圆角了");
  assert.match(panel, /padding:\s*0/, "工作区外壳又有内边距了（那是卡片的特征）");
  // 与聊天保持一致：宽度对齐阅读列、正文色
  assert.match(panel, /max-width:\s*var\(--read-col/, "没和聊天共用阅读列宽度");
  assert.match(panel, /color:\s*var\(--tg-text\)/, "正文色该是 --tg-text（不是提示色）");

  // ② 载荷的每一块：不许有边框/底色（从前是"小卡片"，用户点名不要）
  const pv = rule(".pv-msg");
  assert.ok(pv, "找不到 .pv-msg 规则");
  assert.match(pv, /border:\s*none/, ".pv-msg 又带边框了 —— 那是给每块内容套的框");
  assert.match(pv, /background:\s*none/, ".pv-msg 又有底色了（那是气泡）");
  // ⚠ 别写 `!/border-radius:\s*(?!0)/` 这种负向前瞻：`\s*` 会**回溯**到匹配空串，
  // 于是"下一个字符是空格"也让断言成立 —— 圆角明明非 0 却抓不到（真实的坑，写过一次）。
  // 正面断言"它必须是 0"更干脆，也更难写错。
  assert.match(pv, /border-radius:\s*0\b/, ".pv-msg 又有圆角了 —— 那是给每块内容套的框");
  // 块间只用一条细线分开（与状态表同一个手法）
  assert.match(pv, /border-bottom:\s*1px solid var\(--line\)/, "块之间该用一条细线分隔");

  // ③ role 标签：纯文字，不是彩色药丸
  const role = rule(".pv-role");
  assert.match(role, /border:\s*none/, "role 又变成带框的药丸了");
  assert.match(role, /background:\s*none|background:\s*transparent|border-radius:\s*0/, "role 又有底色了");

  // ④ 元信息 chip：裸文字（靠 ::before 加分隔点）
  const chip = rule(".pv-chip");
  assert.match(chip, /border:\s*none/, "元信息又变成带框的小标签了");
  assert.match(css, /\.pv-chip \+ \.pv-chip::before/, "元信息之间没有分隔点（裸文字会连成一片）");
});

// 「聊天 / 上下文」那颗开关的大小（2026-10-29 用户：「把那个切换按钮弄大一点」）。
//
// 它长在**悬浮顶栏**里 —— 顶栏是 absolute 层，消息列的顶部留白（`.messages-inner` 的
// padding-top）是唯一给它让位的东西。所以"放大"这件事有个硬上限：
// **总高不能超过那截留白**，否则第一条消息会被压在开关底下（不报错，只是看不见）。
// 这条按实际声明**算一遍**，两头都钉：够大（别悄悄改小）、又没顶到天花板。
test("工作区开关：够大，但没顶穿消息列的顶部留白", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const m = css.match(new RegExp(`(^|\\n)${sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`));
    return m ? m[2] : "";
  };
  // ⚠ 别一律要求 `em` 单位：`line-height: 1.3` 是**无单位倍数**（相对自身 font-size），
  // 而 `font-size` / `padding` 在这儿都写成 em。按名字分两种取法，取不到就 NaN（下面会报）。
  const em = (text, name) => {
    const m = text.match(new RegExp(`${name}:\\s*([\\d.]+)em`));
    return m ? Number(m[1]) : NaN;
  };
  const bare = (text, name) => {
    const m = text.match(new RegExp(`${name}:\\s*([\\d.]+)\\s*(?:;|$)`));
    return m ? Number(m[1]) : NaN;
  };

  const tab = rule(".ws-tab");
  const box = rule(".ws-switch");
  assert.ok(tab && box, "找不到 .ws-tab / .ws-switch");

  const font = em(tab, "font-size");
  const line = bare(tab, "line-height");                // 无单位倍数
  const padY = em(tab, "padding");                      // 竖排 padding（简写取第一个）
  const padBox = em(box, "padding");
  for (const [n, v] of [["font-size", font], ["line-height", line], ["padding(.ws-tab 竖)", padY], ["padding(.ws-switch)", padBox]]) {
    assert.ok(Number.isFinite(v), `.ws-tab / .ws-switch 的 ${n} 不是 em 数字，算不出高度`);
  }
  // em 是**相对自身 font-size** 的：padding 的 em 要先乘回 font，再落到父级那一把尺上。
  const height = font * line + 2 * padY * font + 2 * padBox;

  // ① 够大：至少与旁边那枚环（1.8em）同量级 —— 用户要的是"说得出口的控件"，不是一行小字。
  //    从前的 0.78em/0.16em 0.55em 只有 ≈1.35em，正是被嫌弃的那个尺寸。
  assert.ok(height >= 1.8, `开关太小了（算出来 ${height.toFixed(2)}em，至少 1.8em）`);

  // ② 没顶穿：消息列顶部留白是它唯一让位的东西。
  const inner = rule(".messages-inner");
  const reserve = em(inner, "padding");
  assert.ok(Number.isFinite(reserve), "读不出 .messages-inner 的顶部留白");
  assert.ok(height < reserve - 0.5,
    `开关 ${height.toFixed(2)}em 快顶到消息列顶部留白 ${reserve}em 了 —— 再大就会压住第一条消息`);
});

// 载荷面板的「折叠全部」（2026-10-29 用户定：「我希望有个按钮可以一键折叠全部（但没有一键展开的）」）。
//
// 那个不对称是**刻意的**，所以两头都得钉：
//   ① 折叠全部必须在（别被谁顺手删了/当成"和展开对称"给成对改了）；
//   ② **不许**长出"展开全部" —— 展开那一下会把整发请求（常含几十 KB 的 system prompt）
//      一次铺满面板，而折叠只是"收拾"。收起来永远安全，铺开需要人逐条确认。
test("载荷：有一键折叠全部，且没有一键展开", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const script = bar.slice(0, bar.indexOf("</script>")).replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  // ⚠ tpl 要**先剥 HTML 注释**：我要断言的正是"不许出现展开全部"，而注释里写一句
  // "没有反向的展开全部"是对的（那正是在提醒后人别加）—— 不剥注释，正确的注释会让守卫假失败。
  const tpl = bar.slice(bar.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");

  // ① 有按钮，且它落在载荷那一页的工具栏里
  assert.match(tpl, /class="pv-collapse-all"/, "载荷页没有「折叠全部」按钮");
  const toolbar = tpl.slice(tpl.indexOf('class="pv-toolbar"'), tpl.indexOf('id="payload-viz"'));
  assert.ok(toolbar.includes("pv-collapse-all"), "「折叠全部」没放在载荷页的工具栏里");
  assert.match(tpl, /@click="collapseAll"/, "按钮没接 collapseAll");

  // ② 折叠的实现：**每一条都显式记成 false**。
  //    `blockOpen` 的默认值取决于块长短（长块默认折叠），所以"不管当前状态一律收起"必须写显式的 false，
  //    不能靠"清空 map 让它回默认"—— 那样短块会自己弹开。
  const fn = script.match(/function collapseAll\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(fn, "找不到 collapseAll");
  assert.match(fn, /set\(i, false\)|set\([^,]+,\s*false\)/, "折叠时没把每条显式设成 false —— 短块会自己弹开");

  // ③ **没有展开全部**（用户明确不要）。这条防的是"顺手补个对称按钮"。
  assert.ok(!/展开全部|expandAll|expand-all/i.test(tpl), "多出了「展开全部」—— 用户明确不要（折叠是收拾，展开是倾泻）");
  assert.ok(!/function expandAll/.test(script), "脚本里冒出了 expandAll");

  // ④ 全收起来时禁用（别给一个点了没反应的样子）
  assert.match(tpl, /:disabled="allCollapsed"/, "全折叠后按钮没禁用（会变成点不动的死按钮）");

  // ⑤ 平铺样式：这个按钮也不许长成"盒子"
  const rule = css.match(/(?:^|\n)\.pv-collapse-all\s*\{([^}]*)\}/)?.[1] ?? "";
  assert.ok(rule, "找不到 .pv-collapse-all 样式");
  assert.match(rule, /background:\s*none/, "折叠按钮有底色了（这三页是平铺，不放盒子）");
  assert.match(rule, /border:\s*none/, "折叠按钮有边框了");
  assert.match(css, /\.pv-collapse-all:disabled/, "没给禁用态样式 —— 禁用后看起来还能点");
});

// 2026-10-29 顺手修的：压缩页那个「收起」× 从前是 `@click="compactWire = null"`，
// 而 `compactWire` 是 **computed（只读）** ⇒ 赋值只打一条警告、什么都不发生。
// 这类"死按钮"不报错、不误路由，就是点下去没反应 —— 而且**构建和契约测试都看不见**。
test("压缩载荷：「收起」按钮真的能收（不是给 computed 赋值）", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  // 那个只读赋值必须真的没了
  assert.ok(!/compactWire\s*=\s*null/.test(bar),
    "还在给只读的 compactWire 赋值 —— 按钮点下去毫无反应");
  const tpl = bar.slice(bar.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");
  assert.match(tpl, /@click="closeCompactPayload"/, "「收起」没接到一个真能改状态的函数");
  const script = bar.slice(0, bar.indexOf("</script>")).replace(/^\s*\/\/.*$/gm, "");
  const fn = script.match(/function closeCompactPayload\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(fn, "找不到 closeCompactPayload");
  // 改的必须是**来源**（compactInfo），不是那个 computed
  assert.match(fn, /compactInfo\.value\s*=/, "closeCompactPayload 没清掉真正的来源 compactInfo");
});
