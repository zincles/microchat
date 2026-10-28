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
  deletionPlanSummary,
  formatRoutesOutcome,
  ABILITY_TEMPLATE_SEGMENT,
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

// 能力里的提示词段（2026-10-28）：`.vue` 里的接线**只能读源码验** —— 少接一环不会报错，
// 只会让"我加了段、它没生效"变成一场排查（后端那份由 `compact_segments_test.go` 用真载荷钉住）。
test("能力里的提示词段：编辑器读回 / 写回 / 挑 / 排序 / 删 五处都接着", () => {
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  assert.match(editor, /segments: \[\.\.\.\(one\.segments \?\? \[\]\)\]/, "fillRows 没把能力的段读回来");
  assert.match(editor, /segments: \[\.\.\.\(r\.segments \?\? \[\]\)\]/, "toPatch 没把段发出去");
  assert.match(editor, /function addAbilitySeg/, "没有「从段库挑一个加进来」");
  assert.match(editor, /function moveAbilitySeg/, "没有上下移（顺序就是语义）");
  assert.match(editor, /function dropAbilitySeg/, "没有移除");
  // 预览必须是"这一发真会发出去的提示词"（模板 + 段），不是只把段拼起来
  assert.match(editor, /abilityPrompt\(r\.prompt, r\.segments/, "预览没走能力的完整拼装");
  assert.match(editor, /ABILITY_TEMPLATE_SEGMENT\[id\]/, "预览不知道这能力的默认模板住在哪条内置段");
});

// 压缩的默认模板 2026-10-28 搬进段库（用户：「把默认压缩载荷挑出来，制作成单独的内置 segment」）。
// 前端要 preview 它，就得知道那条内置段的 id —— 而**真正的模板仍由后端拼**。
// 这个 id 在两处各写一遍（Go 常量 / JS 常量），写错**不会报错**，只会让预览少一段。
test("压缩默认模板的段 id：前端常量与后端 const 逐字相同", () => {
  const go = readFileSync(join(REPO, "core", "internal", "config", "config.go"), "utf8");
  const found = go.match(/CompactTemplateSegmentID\s*=\s*"([^"]+)"/);
  assert.ok(found, "core/internal/config/config.go 里找不到 CompactTemplateSegmentID");
  assert.equal(ABILITY_TEMPLATE_SEGMENT.compact, found[1], "前后端的压缩模板段 id 对不上");
  // 正文也得真在段库里（内置段清单里那一行）
  assert.match(go, /\{ID: CompactTemplateSegmentID, File: "segments\/compact-prompt\.md"\}/,
    "内置段清单里没有压缩模板那一行（正文住 config/segments/compact-prompt.md）");
});

test("能力提示词拼装：覆盖打头 / 空则垫内置模板段 / 挑了它就不重复", () => {
  const segs = [
    { id: "压缩提示词", text: "【模板】照我说的压。", builtin: true },
    { id: "风格", text: "【风格】用第二人称。" },
  ];
  // 什么都没填 ⇒ 就是内置模板段正文
  assert.equal(abilityPrompt("", [], segs, "压缩提示词"), "【模板】照我说的压。");
  // 加了段 ⇒ 模板在前
  assert.equal(abilityPrompt("", ["风格"], segs, "压缩提示词"), "【模板】照我说的压。\n\n【风格】用第二人称。");
  // 把模板段自己挑进来 ⇒ 按列表位置出现，**不重复**
  assert.equal(abilityPrompt("", ["风格", "压缩提示词"], segs, "压缩提示词"), "【风格】用第二人称。\n\n【模板】照我说的压。");
  // 填了覆盖 ⇒ 覆盖打头，不再垫默认
  assert.equal(abilityPrompt("【我的】短一点。", ["风格"], segs, "压缩提示词"), "【我的】短一点。\n\n【风格】用第二人称。");
  // 没有默认模板段的能力（title / judge）⇒ 行为就是普通的 base + 段
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
  // 顺序：环在**左**、载荷栏开关在**右**（2026-10-28 用户定）。开关是"控制栏的按钮"= 这一簇的锚，
  // 位置要稳；环是随状态变的仪表，挂它左边。两个都是绝对定位的兄弟，换顺序**不会报错**，
  // 只会悄悄换到另一边 —— 所以钉住先后。
  assert.ok(
    top.indexOf('id="ctx-ring"') < top.indexOf('id="toggle-right"'),
    "环该在载荷栏开关的左边（用户 2026-10-28 定）",
  );

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
