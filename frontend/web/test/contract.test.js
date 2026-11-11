import { strict as assert } from "node:assert";
import { test } from "node:test";
import { createApi, mergeStreamSlice, DEFAULT_API_URL, normalizeApiBase, probeHealth } from "../src/api/client.ts";
import { parseWireMessages, previewBlock } from "../src/utils/payload.ts";
import { assemblePrompt, abilityPrompt, missingSegments } from "../src/utils/prompt.ts";
import { renderMarkdown } from "../src/utils/markdown.ts";
import { buildCartridge, cartridgeAgent } from "../src/utils/cartridge.ts";
import { emptyFlowGraph, flowDebugSteps, flowIssues, flowSourceType, flowTargetType } from "../src/utils/flow.ts";
import { parseCommand, filterCommands, COMMAND_META } from "../src/utils/commands.ts";
import { uuidv7 } from "../src/utils/ids.ts";
import { groupSessions, SESSION_GROUP_LABELS } from "../src/utils/sessionGroups.ts";
import { buildCompactTree, treeTotals } from "../src/utils/tree.ts";
import { buildChatRows, rerollTargetLive, bubbleText, editRerollTarget, latestPromptFor, seededMissingRuns, toolSources } from "../src/utils/chatrows.ts";
import { resetChrome, stepChrome } from "../src/utils/chrome.ts";
import { typewriterDuration, revealCount, TYPEWRITER_DEFAULT_MS, TYPEWRITER_MIN_MS, TYPEWRITER_MAX_MS } from "../src/utils/typewriter.ts";
import { computeViewportFit } from "../src/utils/viewport.ts";
import { classifyAxis, pickDrag, dragProgress, resolveRelease } from "../src/utils/swipe.ts";
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
  identityLabel,
  assistantIdentity,
  formatLatency,
  formatSpeed,
  nativeToolLabel,
  nativeToolsLabel,
  nativeToolsTitle,
  toolRunHint,
  sourceLabel,
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
  modelKey,
  parseModelKey,
  modelLabel,
  modelOption,
  providerDisplayName,
  whoText,
  sessionTitle,
} from "../src/utils/format.ts";
import { busySessionIds } from "../src/utils/tasks.ts";
import { rippleSpec, RIPPLE_SELECTOR } from "../src/utils/ripple.ts";
import { rememberWindow, recallWindow, forgetWindow, WINDOW_CACHE_MAX, WINDOW_CACHE_ROWS_MAX } from "../src/utils/windowcache.ts";
import { MODEL_ICON_OPTIONS } from "../src/utils/modelIcons.ts";
import { sendMode, wantsSend, themePref, normalizeTheme, apiBase, defaultApiBase, apiBaseCandidates, THEME_IDS, DEFAULT_THEME, DEFAULT_SEND, normalizeTextScale, textScale, applyTextScale, saveTextScale, flushPrefs, normalizeFontFamily, fontBody, fontMono, applyFonts, saveFontBody, saveFontMono, DEFAULT_FONT_BODY, DEFAULT_FONT_MONO, TEXT_SCALE_MIN, TEXT_SCALE_MAX, DEFAULT_TEXT_SCALE } from "../src/utils/prefs.ts";

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

test("流式合并：同一个游标重复读不重复追加", () => {
  const same = mergeStreamSlice({ text: "你好", reasoning: "想了" }, { from: 2, thinkFrom: 2 },
    { text: "好", thinking: "了", next: 2, think_next: 2 });
  assert.deepEqual(same, { text: "你好", reasoning: "想了", cur: { from: 2, thinkFrom: 2 }, reset: false });
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
  assert.equal(contextPercentText({ used_tokens: 344, budget_tokens: 1044480 }), "不到 0.1%");
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

test("usageFoot：底部只留用量/延迟/速度；没用量整行为空", () => {
  const foot = usageFoot({ prompt_tokens: 6585, completion_tokens: 945, cached_tokens: 0, reasoning_tokens: 333, cost_usd: 0.0012 });
  assert.ok(foot.includes("输入：6.5K， 输出：945（思考 333）")); // 向下取整：6585 ⇒ 6.5K
  assert.ok(foot.includes("成本 $0.0012"));
  assert.ok(!foot.includes("缓存命中") && !foot.includes("已缓存")); // 0 命中不啰嗦
  assert.ok(!foot.includes("首字延迟"));          // 没有时长 ⇒ 不编
  assert.ok(!foot.includes("速度"));
  assert.equal(usageFoot(null), "");              // 生成中 / 库外那条
  assert.equal(usageFoot({}), "");                // 上游没报
  assert.equal(usageFootTitle(null), "");

  // 缓存命中只进悬停说明（脚注要短），同一份用量两边不重复罗列。
  const withCache = usageFoot({ prompt_tokens: 100000, completion_tokens: 50, cached_tokens: 96000 });
  assert.ok(withCache.includes("输入：100K（已缓存：96K）"));
  assert.ok(!withCache.includes("$"));            // 没 cost ⇒ 没有价格那一格
  assert.ok(usageFootTitle({ prompt_tokens: 100000, completion_tokens: 50, cached_tokens: 96000 }).includes("缓存命中 96000"));
});

test("身份脚注：IDX/用量/首字延迟/速度齐全；缺哪格就省哪段", () => {
  // reasoning_ms = 受理 → 第一段正文（首字延迟）；duration_ms = 整发的墙钟时间。
  const full = usageFoot(
    { prompt_tokens: 1126, completion_tokens: 1229, reasoning_tokens: 900 },
    { idx: 4, reasoningMs: 1234, durationMs: 10000 },
  );
  assert.match(full, /^输入：1\.1K， 输出：1\.2K（思考 900）/);
  assert.ok(full.includes("首字延迟： 1.2 s"));
  // 生成窗口 = 10 − 1.234 = 8.766 s；生成 token = 1229 − 900 = 329 ⇒ 37.5 tps（≥10 收成整数 = 37）。
  assert.ok(full.includes("速度： 122.9 tps"));
  assert.ok(full.endsWith("。"));

  // 没有 reasoning_ms：整段算速度（100 token / 2 s = 50 tps），也没有首字延迟这一段。
  const noThink = usageFoot({ prompt_tokens: 10, completion_tokens: 100 }, { idx: 7, durationMs: 2000 });
  assert.ok(noThink.startsWith("输入：10， "));
  assert.ok(!noThink.includes("首字延迟"));
  assert.ok(noThink.includes("速度： 50 tps"));

  // 只有编号（生成中 / 库外那条）：脚注就只是编号，绝不编出 0 token。
  assert.equal(usageFoot(null, { idx: 3 }), ""); // 身份在顶部；底部没有用量就不出现
  assert.equal(usageFoot(null, { idx: null }), "");

  assert.equal(identityLabel("你", 3), "你 #3");
  assert.equal(identityLabel("你", null), "你");       // 乐观句还没编号
  assert.equal(identityLabel("", 3), "#3");
  assert.equal(identityLabel("你", undefined), "你");
  assert.equal(assistantIdentity("助手", "chat", 2), "助手-对话#2");
  assert.equal(assistantIdentity("助手", "tool_run", 3), "助手-工具#3");
  assert.equal(assistantIdentity("主持人", "tool_run", 4), "主持人#4");
  assert.equal(assistantIdentity("助手", "chat", null), "助手-对话");

  assert.equal(formatLatency(1234), "1.2 s");
  assert.equal(formatLatency(null), "");
  assert.equal(formatSpeed({ completion_tokens: 50 }, null, 1000), "50 tps");
  assert.equal(formatSpeed({ completion_tokens: 50 }, null, 0), "");   // 生成窗为 0 ⇒ 不算
  assert.equal(formatSpeed({ completion_tokens: 0 }, null, 1000), "");
  assert.equal(formatSpeed({ completion_tokens: 50, reasoning_tokens: 50 }, 900, 1000), "50 tps"); // 总输出 / 总耗时，不拆思考窗
  assert.equal(formatSpeed({ completion_tokens: 12, reasoning_tokens: 66 }, 2574, 2587), "4.6 tps"); // 上游 reasoning_tokens > completion 也不出负数
});

test("usageFoot 的悬停说明：点明思考是输出的子集、成本/延迟/速度的来源", () => {
  const title = usageFootTitle(
    { prompt_tokens: 100, completion_tokens: 40, cached_tokens: 80, reasoning_tokens: 30, cost_usd: 0.0004 },
    { idx: 2, reasoningMs: 800, durationMs: 4000 },
  );
  assert.ok(title.includes("输入 100 tok（其中缓存命中 80）"));
  assert.ok(title.includes("子集"));              // 别让人把 reasoning 再加一遍
  assert.ok(title.includes("$0.0004"));
  assert.ok(title.includes("上游报的"));
  assert.ok(title.includes("受理 → 第一段正文"));
  assert.ok(title.includes("输出 token ÷ 整发耗时"));

});

test("消息头：assistant 身份在正文顶部；user 身份在气泡外且 hover 才显形", () => {
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(bubble, /v-if="role !== 'user' && assistantIdent" class="msg-head"/, "assistant 身份不在顶部");
  assert.match(bubble, /assistantIdentity\(props\.who, props\.kind, props\.idx\)/, "顶部身份没接 kind");
  assert.ok(!/msg-idx/.test(bubble), "气泡里还留着 #编号");
  assert.ok(!/<span class="who">/.test(bubble), "气泡里还渲染 who");
  assert.match(css, /\.msg \.msg-head/, "CSS 没有消息头规则");
  assert.ok(!/\.msg \.who\b/.test(css), "CSS 还留着 who 规则");
  assert.ok(!/\.msg \.msg-idx\b/.test(css), "CSS 还留着气泡内编号规则");

  // assistant：脚注一行，含身份/用量/延迟/速度的拼装出口。
  assert.match(bubble, /v-if="role !== 'user' && foot" class="msg-foot"/, "assistant 脚注不该回到用户气泡里");
  assert.match(bubble, /usageFoot\(footUsage\.value, footTiming\.value\)/, "脚注没带上时长");
  assert.match(bubble, /durationMs: props\.durationMs/, "脚注没接 durationMs");
  assert.match(bubble, /props\.streaming \? null : props\.usage/, "生成中没把上一版的用量摘掉");
  assert.match(app, /durationMs: m\.duration_ms \?\? null/, "toRow 没带 duration_ms");
  assert.match(app, /:duration-ms="rowDuration\(row\.msg\)"/, "App 没把 duration_ms 喂给气泡");

  // user：身份在 `.msg` 的**外面**（`</div>` 之后），且 CSS 默认透明、hover 才 1。
  const identAt = bubble.indexOf('class="msg-ident"');
  const msgCloseAt = bubble.indexOf("</div>", bubble.indexOf('class="text"'));
  assert.ok(identAt > msgCloseAt, "用户身份还留在气泡里面");
  assert.match(css, /\.msg-ident \{[^}]*opacity:\s*0/, "用户身份默认没藏起来");
  assert.match(css, /\.msg-wrap\.user:hover \.msg-ident[^{]*\{[^}]*opacity:\s*1/, "用户身份 hover 不显形");
});

test("状态行：助手 + phase/耗时；上下文占用不在这行（2026-10-28 挪去右上角的环）", () => {
  const line = formatStatusLine({ phase: "streaming", elapsedMs: 300, agent: "默认助手" });
  assert.ok(line.includes("默认助手"));
  assert.ok(line.includes("生成中"));
  assert.ok(line.includes("0.3 秒"));
  assert.ok(!line.includes("streaming") && !line.includes("300ms"), "状态行还留着机器词");
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

test("Mermaid：围栏输出延迟渲染占位；关掉 mermaid/代码块能退化", () => {
  const html = renderMarkdown("```mermaid\ngraph TD\nA-->B\n```");
  assert.match(html, /class="mermaid-figure"/, "Mermaid 围栏没有输出图占位");
  assert.ok(html.includes("graph TD"), "Mermaid 源码没保留（流式/无 JS 时要能读）");
  assert.ok(!html.includes("language-mermaid"), "Mermaid 还被当普通代码块");
  assert.ok(!html.includes("\u0000MERMAID"), "占位符漏进 HTML");

  const noMermaid = renderMarkdown("```mermaid\ngraph TD\nA-->B\n```", { typeOn: (id) => id !== "mermaid" });
  assert.match(noMermaid, /<pre><code class="language-mermaid">/, "关掉 Mermaid 应退化成普通代码块");
  assert.ok(noMermaid.includes("graph TD"));

  const noCode = renderMarkdown("```mermaid\ngraph TD\nA-->B\n```", { typeOn: (id) => id !== "code_block" });
  assert.ok(noCode.includes("```mermaid"), "关掉代码块应把围栏原样画出来");

  const withMath = renderMarkdown("```mermaid\ngraph TD\nA[$x$]-->B\n```\n\n$$y^2$$");
  assert.ok(withMath.includes("A[$x$]"), "Mermaid 源码里的 $ 不该被 KaTeX 抽走");
  assert.match(withMath, /katex/, "围栏外的公式仍要渲染");
});

test("Mermaid：动态 import、strict、主题重绘与气泡接线", () => {
  const renderer = readFileSync(join(SRC, "utils", "mermaid.ts"), "utf8");
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const vite = readFileSync(join(SRC, "..", "vite.config.js"), "utf8");
  assert.match(renderer, /import\("mermaid"\)/, "Mermaid 没走动态 import（会压进首屏包）");
  assert.match(renderer, /securityLevel:\s*"strict"/, "Mermaid 没开 strict");
  assert.match(renderer, /htmlLabels:\s*false/, "Mermaid 没关 HTML labels");
  assert.match(renderer, /mermaid\.render/, "没有调用 mermaid.render");
  assert.match(bubble, /renderMermaidIn/, "气泡没接 Mermaid 渲染");
  assert.match(bubble, /mc-theme-changed/, "主题切了没重绘 Mermaid");
  assert.match(vite, /globIgnores/, "Mermaid 可选 chunk 没从 PWA 预缓存排除");
  assert.match(vite, /runtimeCaching/, "排除的 Mermaid chunk 没有运行时缓存");
});

// 2026-11-03 用户报的红色块：模型在正文里写「用 `$$` 包裹」—— 反引号里的 $$ 不是公式开头。
// 旧实现先抓 $$ 再做 Markdown，导致后续所有 $$ 配对错位，最后一大段正文被当成一条畸形公式，
// KaTeX 用 .katex-error 把整段涂红。代码必须先保护，再抽数学定界符。
test("Markdown：行内代码/代码块里的 $$ 不参与块级公式配对", () => {
  const src = "公式块均使用 `$$` 包裹：\n\n---\n\n### 三、使用要点\n\n$$x^2$$\n\n正文 $a+b$。";
  const html = renderMarkdown(src);
  assert.ok(!html.includes("katex-error"), "行内代码里的 $$ 又把公式配对带歪了（整段会变红）");
  assert.match(html, /<code>\$\$<\/code>/, "行内代码没有原样保留");
  assert.match(html, /katex/, "真正的公式没有渲染");

  const fenced = renderMarkdown("```\n$$\n```\n\n$$y^2$$");
  assert.ok(!fenced.includes("katex-error"), "代码块里的 $$ 影响了公式");
  assert.match(fenced, /<code>\$\$/, "代码块内容丢了");

  // 中文标点紧贴闭合 $：必须在这里收口，不能一路吃到下一句。
  const cjk = renderMarkdown("内层函数满足 $u(x) \\to 0$，则公式中的 $x$ 可替换；又如 $A - B$），只有当 $A$ 与 $B$ 不抵消。");
  assert.ok(!cjk.includes("katex-error"), "中文标点后的 $ 仍被吞进公式");
  assert.ok(cjk.includes("内层函数满足"), "正文被误当成公式吞掉");
  assert.ok(cjk.includes("只有当"), "后半句被误当成公式吞掉");
});

// DeepSeek 一类模型写的是 LaTeX display 定界符 \[...\]，不是 $$。
// 最后那个 \boxed{...} 是 KaTeX 原生命令（boxpad/fbox），不是 CSS 卡片。
test("Markdown：\\[...\\] 也是块级公式，\\boxed 走 KaTeX 原生边框", () => {
  const html = renderMarkdown("\\[\\boxed{x^2}\\]");
  assert.match(html, /katex-display/, "\\[...\\] 没被识别成 display math");
  assert.match(html, /boxpad|fbox/, "\\boxed 没被 KaTeX 渲染成框");
  assert.ok(!html.includes("\\["), "字面 \\[ 还露在输出里");
});

test("Markdown 渲染：HTML 一律转义、数学走 KaTeX 占位", () => {
  const h = renderMarkdown("<script>alert(1)</script>");
  assert.ok(!h.includes("<script>"));
  assert.ok(h.includes("&lt;script&gt;"));
  assert.ok(renderMarkdown("$x^2$").includes("katex"));
});

test("载荷分块：默认全折叠；预览单行截断（换行压空格）", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  assert.match(bar, /return openMap\.value\.has\(i\) \? openMap\.value\.get\(i\)!? : false/,
    "载荷正文分块默认没有全折叠");
  assert.match(bar, /return cpOpenMap\.value\.has\(i\) \? cpOpenMap\.value\.get\(i\)!? : false/,
    "压缩载荷分块默认没有全折叠");
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

// 2026-11-11 用户定：智能体列表改为 名称 / 标识 / 类型 / 描述 / 是否默认。
test("智能体列表：名称|标识|类型|描述|是否默认", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  assert.match(settings, /<thead><tr><th>名称<\/th><th>标识<\/th><th>类型<\/th><th>描述<\/th><th>是否默认<\/th><\/tr><\/thead>/, "表头不是五列目标顺序");
  assert.match(settings, /function agentType\(/, "没有类型派生");
  assert.match(settings, /a\.description \|\| "—"/, "描述列没渲染（空值该显示 —）");
  assert.match(settings, /class="agent-desc"/, "描述列没有省略/悬停样式钩子");
  assert.match(editor, /const description = ref\(/, "编辑器没有 description 状态");
  assert.match(editor, /description: description\.value\.trim\(\)/, "保存没有写 description");
});

// 2026-11-11：新建智能体时选传统 / FlowAgent；FlowAgent 的流程页是第一版节点编辑器。
test("FlowAgent：端口有类型，且每个端点只许一条线", () => {
  const input = { id: "in", type: "input", position: { x: 0, y: 0 }, data: {} };
  const history = { id: "h", type: "history", position: { x: 0, y: 0 }, data: {} };
  const llm = { id: "l", type: "llm", position: { x: 0, y: 0 }, data: { prompts: [{ id: "p1", role: "system", content: "x" }] } };
  const join = { id: "j", type: "join", position: { x: 0, y: 0 }, data: { inputs: ["in-1", "in-2"] } };
  assert.equal(flowSourceType(input, "out"), "string");
  assert.equal(flowSourceType(history, "out"), "messages");
  assert.equal(flowSourceType(llm, "reasoning"), "string");
  assert.equal(flowSourceType(llm, "text"), "string");
  assert.equal(flowTargetType(llm, "history"), "messages");
  assert.equal(flowTargetType(llm, "prompt-p1"), "string");
  assert.equal(flowTargetType(join, "in-1"), "string");
  const graph = {
    version: 1,
    nodes: [input, history, llm, join, { id: "out", type: "output", position: { x: 0, y: 0 }, data: {} }],
    edges: [
      { id: "e1", source: "in", target: "l", targetHandle: "prompt-p1" },
      { id: "e2", source: "h", target: "l", targetHandle: "history" },
      { id: "e3", source: "l", sourceHandle: "text", target: "j", targetHandle: "in-1" },
      { id: "e4", source: "l", sourceHandle: "reasoning", target: "j", targetHandle: "in-2" },
      { id: "e5", source: "j", target: "out" },
    ],
  };
  assert.deepEqual(flowIssues(graph), []);
  graph.edges.push({ id: "e6", source: "in", target: "j", targetHandle: "in-2" });
  assert.ok(flowIssues(graph).some((x) => x.includes("一个出口只能接一条线")));
  assert.ok(flowIssues(graph).some((x) => x.includes("一个入口只能接一条线")));
});

test("FlowAgent：新建菜单两种类型，流程编辑器有输入/LLM/拼接和复制节点", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  const flow = readFileSync(join(SRC, "components", "FlowEditor.vue"), "utf8");
  const flowNode = readFileSync(join(SRC, "components", "FlowNode.vue"), "utf8");
  assert.match(settings, /startNewAgent\('prompt'\)/, "新建菜单没有传统智能体");
  assert.match(settings, /startNewAgent\('flow'\)/, "新建菜单没有 FlowAgent");
  assert.match(settings, /<FlowAgent|FlowAgent/, "菜单没有 FlowAgent 文案");
  assert.match(editor, /agentKind === 'flow'/, "AgentEditor 没按类型切换页签");
  assert.match(flow, /<VueFlow/, "没有 Vue Flow 画布");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  assert.match(css, /\.flow-canvas \.vue-flow__node \{[^}]*background:\s*transparent !important/, "Vue Flow 外层白底没剥掉（输入/输出节点会套白框）");
  assert.match(css, /\.flow-canvas \.vue-flow__controls-button \{[^}]*background:\s*var\(--md-surface-container-high\)/, "画布控制键没跟 MD3 主题");
  assert.match(flow, /class="flow-fullscreen-btn"/, "流程编辑器没有网页全屏按钮");
  assert.match(flow, /class="flow-debug"/, "流程编辑器没有调试面板");
  assert.match(flow, /flowDebugSteps/, "调试面板没有结构预演数据");
  assert.match(flow, /发送测试/, "流程编辑器没有测试发送按钮");
  assert.match(flow, /api\.testFlow/, "测试发送没接后端");
  assert.match(flowNode, /class="flow-node-test"/, "节点悬停没有测试 I/O 浮层");
  assert.match(flowNode, /class="flow-node-delete/, "节点没有删除按钮");
  assert.match(flowNode, /id="reasoning"/, "LLM 节点没有思考出口");
  assert.match(flowNode, /id="history"/, "LLM 节点没有 history 输入");
  assert.match(flowNode, /拼接和复制/, "拼接节点没有改名为拼接和复制");
  assert.match(flowNode, /class="flow-join-inputs"/, "拼接和复制节点没有多输入端口");
  assert.match(flowNode, /addJoinInput/, "拼接节点不能增加输入端口");
  assert.match(flowNode, /class="flow-history-input"/, "history 输入没有独立标签/端口");
  assert.match(flowNode, /id="text"/, "LLM 节点没有正文出口");
  assert.match(flowNode, /每行自己的左侧输入孔|prompt-\$\{prompt\.id\}/, "Prompt 条目没有独立输入孔");
  assert.ok(flowDebugSteps(emptyFlowGraph()).length >= 2, "空图没有调试步骤");
  assert.match(css, /\.flow-editor\.expanded \{[^}]*position:\s*fixed/, "网页全屏没有铺满 #app");
  assert.match(css, /\.flow-editor\.expanded \.flow-main \{[^}]*flex:\s*1 1 auto/, "全屏时 flow-main 没有吃掉剩余高度");
  assert.match(css, /\.flow-debug \{[^}]*position:\s*absolute/, "调试栏仍占画布宽度");
  assert.match(css, /\.flow-panel-test \{[^}]*display:\s*flex/, "测试面板没有独立内容区");
  assert.match(flow, /flow-node-row/, "节点面板没有节点列表");
  assert.match(flow, /flow-panel-tabs/, "面板没有 测试/调试/节点 tabs");
  for (const type of ["input", "history", "llm", "join", "output"]) {
    assert.ok(flow.includes(`addNode('${type}')`) || flow.includes(`addNode("${type}")`), `流程编辑器缺 ${type} 节点`);
  }
  const empty = emptyFlowGraph();
  assert.ok(flowIssues(empty).some((x) => x.includes("输出节点还没有输入")), "空图没有输出连线警告");
  const badPrompt = {
    version: 1,
    nodes: [
      { id: "in", type: "input", position: { x: 0, y: 0 }, data: {} },
      { id: "a", type: "llm", position: { x: 1, y: 1 }, data: { prompts: [{ id: "p1", role: "tool", content: "x" }] } },
      { id: "out", type: "output", position: { x: 2, y: 2 }, data: {} },
    ],
    edges: [
      { id: "e1", source: "in", target: "a", targetHandle: "prompt-p1" },
      { id: "e2", source: "a", target: "out" },
    ],
  };
  assert.ok(flowIssues(badPrompt).some((x) => x.includes("role 只能是")), "没有拦住非法的 Prompt role");
});

test("agent 卡带 v2：带引用段（内置不带）、结构不拼平；v1 老卡带照收；导入侧校验", () => {
  const lib = [
    { id: "katex", text: "数学用 $…$ 写。" },
    { id: "mcp", text: "需要联网时调用搜索。" },
    { id: "合理的RolePlay", text: "内置的", builtin: true },
  ];
  const agent = {
    id: "rp-agent-simple", name: "RP Agent", description: "负责主持与旁白", system_prompt: "你是主持人。",
    segments: ["katex", "合理的RolePlay", "mcp"], first_message: "开场",
    // 能力也引用段（2026-10-28）：`mcp` 与 agent 自己那份**重叠** ⇒ 卡带里只出现一次
    abilities: { title: { enabled: false }, compact: { segments: ["mcp", "katex"] } }, prepend_state: true,
  };
  const card = buildCartridge(agent, lib);
  assert.equal(card.version, 2);
  assert.equal(card.agent.description, "负责主持与旁白");
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
  assert.equal(back.agent.description, "负责主持与旁白");
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
  // annotations：与 messages 同下标；只标记 prompt / injection，不猜历史。
  const annotated = parseWireMessages({
    body: { messages: [{ role: "user", content: "历史" }, { role: "system", content: "注入" }] },
    annotations: [{ index: 1, type: "injection", prompt_id: "style", depth: 2, order: 100 }],
  });
  assert.equal(annotated.messages[0].annotation, null);
  assert.deepEqual(annotated.messages[1].annotation, {
    index: 1, type: "injection", promptId: "style", depth: 2, order: 100,
  });
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
  // 标题模型来源（2026-11-04）：三档只有 title 写，空/缺省不落盘（老配置继续按 provider/model 兼容）。
  assert.deepEqual(
    buildAbilitiesPatch({ title: { enabled: true, model_source: "session" } }),
    { title: { enabled: true, model_source: "session" } },
  );
  // 能力的追加段（2026-10-28）：空清单**不发**（省略 = 没有段），顺序照抄（顺序就是语义）
  assert.deepEqual(
    buildAbilitiesPatch({ compact: { enabled: true, segments: ["b", "", "a", "b"] } }),
    { compact: { enabled: true, segments: ["b", "a", "b"] } },
  );
});

test("标题模型三档：Agent 里可切全局 / 当前 / Agent，全局默认当前模型", () => {
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const config = readFileSync(join(SRC, "..", "..", "..", "core", "internal", "config", "config.go"), "utf8");
  for (const label of ["跟随全局设置", "使用当前模型", "跟随 Agent 设置"]) {
    assert.ok(editor.includes(label), `Agent 编辑器缺 ${label}`);
  }
  assert.match(editor, /rows\[id\]\.modelSource === 'agent'/, "Agent 模型下拉没跟着来源出现");
  assert.match(settings, /titleSelectOptions = computed\(\(\) => \[\{ label: "使用当前模型", value: "" \}/, "全局标题模型缺「使用当前模型」");
  assert.match(settings, /title_provider: titleModel\?\.provider/, "全局标题模型没拆成 provider/model 落盘");
  assert.match(config, /TitleProvider\s+string `json:"title_provider,omitempty"`/);
  assert.match(config, /ModelSource\s+string `json:"model_source,omitempty"`/);
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
  assert.match(list, /引用了不存在的片段/, "缺段的汇总警告没了");
  assert.match(list, /拼好的提示词预览（\{\{ assembled\.length \}\} 字）/, "拼装预览没了");
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


// —— 背景图（utils/background.ts）：2026-11-01 起**住服务器**（不再是 localStorage 的一枚 data URL）。
import { describeBackground, backgroundCssUrl } from "../src/utils/background.ts";

function stubStorage() {
  const m = new Map();
  globalThis.localStorage = {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
}

// 输入栏形态（2026-10-20 加、2026-11-04 用户要求**整体删除**）：
// 不再有「浮动 / 贴底」二选一，也不再往根节点挂 `data-composer`。
test("输入栏只有一种布局：composer_style / data-composer 已删", () => {
  stubStorage();
  const prefs = readFileSync(join(SRC, "utils", "prefs.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const pane = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");
  const backend = readFileSync(join(SRC, "..", "..", "..", "core", "internal", "config", "prefs.go"), "utf8");
  for (const [name, text] of [["prefs.js", prefs], ["main.css", css], ["App.vue", app], ["AppearancePane", pane], ["prefs.go", backend]]) {
    assert.ok(!/composer_style|composerStyle|data-composer|DEFAULT_COMPOSER/.test(text), `${name} 还留着输入栏形态`);
  }
  assert.ok(!pane.includes("输入框样式"), "设置里还有「输入框样式」");
});

// 字体（2026-11-04）：标准字体 / 等宽字体都存服务器；空 = 内置字族。
test("字体：标准 / 等宽分开存，留空回落系统，坏声明清空", () => {
  stubStorage();
  const style = new Map();
  const root = {
    style: {
      setProperty: (k, v) => style.set(k, v),
      removeProperty: (k) => style.delete(k),
    },
  };
  const originalDoc = globalThis.document;
  globalThis.document = { documentElement: root };
  try {
    assert.equal(normalizeFontFamily('  "Noto Sans SC", sans-serif '), '"Noto Sans SC", sans-serif');
    assert.equal(normalizeFontFamily("A\nB"), "A B");
    assert.equal(normalizeFontFamily("A; background: red"), "");
    assert.equal(saveFontBody("Fira Sans"), "Fira Sans");
    assert.equal(saveFontMono("JetBrains Mono"), "JetBrains Mono");
    assert.equal(fontBody(), "Fira Sans");
    assert.equal(fontMono(), "JetBrains Mono");
    assert.equal(style.get("--app-font-body"), `Fira Sans, ${DEFAULT_FONT_BODY}`);
    assert.equal(style.get("--app-font-mono"), `JetBrains Mono, ${DEFAULT_FONT_MONO}`);
    saveFontBody("");
    saveFontMono("");
    assert.equal(style.has("--app-font-body"), false);
    assert.equal(style.has("--app-font-mono"), false);
    applyFonts();
    assert.equal(style.has("--app-font-body"), false);
  } finally {
    globalThis.document = originalDoc;
  }
});

// 背景图的地址怎么拼（2026-11-01 改口径后**唯一**值得钉的地方）：
//   - `url` 型 = 用户填的外部地址，原样用；
//   - `file` 型 = 服务器上那张，拼到我们自己的端点，并带 `v=` 破缓存
//     —— 图换了但路径不变，只有版本号能让浏览器重取（后端 `getBackground` 的 ETag 配套）。
// 从前这里测的是"localStorage 里的坏 JSON 当没设"，那条存储层没了，测试跟着换。
test("背景图地址：url 原样、file 拼端点且带版本号破缓存", () => {
  assert.equal(backgroundCssUrl(null), "");
  assert.equal(backgroundCssUrl({ kind: "url", value: "https://example.com/a.png" }),
    "https://example.com/a.png");
  const fileUrl = backgroundCssUrl({ kind: "file", version: "abc123" });
  assert.ok(fileUrl.endsWith("/config/prefs/background?v=abc123"), fileUrl);
  // 坏 kind（既不 url 也不 file）当没设 —— 别拼出一个指向不知道哪儿的地址。
  assert.equal(backgroundCssUrl({ kind: "nope", value: "x" }), "");
});

test("背景图标注：未设 / URL 截断 / 上传带尺寸", () => {
  assert.equal(describeBackground(null), "未设置（纯色背景）");
  const long = "https://example.com/" + "x".repeat(80);
  assert.ok(describeBackground({ kind: "url", value: long }).includes("…"));
  const txt = describeBackground({ kind: "file", width: 1920, height: 1080 });
  assert.ok(txt.includes("1920×1080"), txt);
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

test("模型选项带 brand/icon：选择器能画家族图标，不把 family 当渠道", () => {
  const choice = modelOption({
    provider: "xai",
    provider_name: "xAI",
    alias: "xAI 主渠道",
    upstream_id: "grok-4.7",
    name: "Grok 4.7",
    display_name: null,
    thinking_levels: ["low", "high"],
    brand: "xAI",
    icon: "xai",
    matched_rules: ["xai"],
  });
  // value 仍是 provider id + upstream_id —— 别名绝不参与键。
  assert.equal(choice.value, modelKey("xai", "grok-4.7"));
  assert.equal(choice.brand, "xAI");
  assert.equal(choice.icon, "xai");
  assert.equal(choice.provider_name, "xAI");
  assert.equal(choice.alias, "xAI 主渠道");
  // 组头优先别名，搜索也能按别名命中。
  const groups = filterModelGroups([{ provider: "xai", items: [choice] }], "");
  assert.equal(groups[0].providerName, "xAI 主渠道");
  assert.equal(filterModelGroups([{ provider: "xai", items: [choice] }], "主渠道").length, 1);
});

test("providerDisplayName：别名优先，空则沿用 name → id", () => {
  assert.equal(providerDisplayName({ id: "opencode-go-2", name: null, alias: "OpenCode GO" }), "OpenCode GO");
  assert.equal(providerDisplayName({ id: "opencode-go-2", name: null, alias: "  " }), "opencode-go-2");
  assert.equal(providerDisplayName({ id: "opencode-go-2", name: "旧显示名", alias: "" }), "旧显示名");
});

test("渠道分隔：管理列表行间与模型选择器组头之间都有发丝线", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  // 管理列表：关掉基类的下边线，只留行与行之间的上边线（避免双线）。
  assert.match(css, /#provider-list \.plist tbody td\s*\{[^}]*border-bottom:\s*none/);
  assert.match(css, /#provider-list \.plist tbody tr \+ tr td\s*\{[^}]*border-top/);
  // 模型选择器：非首组的组头画线。
  assert.match(css, /\.model-group-head:not\(:first-child\)\s*\{[^}]*border-top/);
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
} from "../src/utils/chatrows.ts";

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


// 上下文环合并后台任务（2026-11-02 用户定：「以上下文指示器的绿色圈圈为主体。中央原本空着的
// 区域，放入代表活跃后台任务数量的数字。在后台任务运行时，绿色圈圈会转动」）。
//
// 原来那枚独立的任务徽标（`#task-badge`）删掉；数字进环心，点环打开的面板里同时有上下文与任务明细。
test("上下文环：圆心是后台任务数，有任务时转动，面板里能取消", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  const fmt = readFileSync(join(SRC, "utils", "format.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const tpl = top.slice(top.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");

  // ① 独立徽标没了；数字在环心，环本身还是上下文占比。
  assert.ok(!/id="task-badge"/.test(tpl), "还有独立的后台任务徽标 —— 该并进上下文环");
  assert.ok(!/toggleTasks|taskOpen/.test(top), "还留着独立任务浮板的开关/状态");
  assert.match(tpl, /id="ctx-ring"/, "上下文环没了");
  assert.match(tpl, /v-if="activeTaskCount > 0"/, "环心数字没有按任务数显示/隐藏");
  assert.match(tpl, /\{\{ taskCountText \}\}/, "环心没有后台任务数字");
  assert.match(tpl, /class="ctx-ring-n"/, "环心数字没有独立样式层（em 叠乘会缩水）");
  assert.match(tpl, /:class="\{ over: ctxOver, idle: !ctx, busy: taskBusy \}"/, "环没有 busy 态");
  assert.match(tpl, /aria-label="上下文占用与后台任务"/, "环的无障碍名没包含后台任务");
  assert.match(tpl, /:title="ringTitle"/, "环的 title 没合并任务信息");

  // ①b 面板分两个 tab（2026-11-02 用户定）：上下文状态 / 运行任务，不再上下叠。
  // 面板文案要严谨、专业（2026-11-02 用户定）：用标准口径，不写口语化说明。
  assert.match(top, /\["剩余可用", .*tokens/, "上下文面板没给剩余可用额度");
  assert.match(top, /\["自动压缩阈值", c\.trigger_tokens/, "上下文面板没给自动压缩阈值");
  assert.match(top, /\["上次实际用量"/, "上次实际用量没有按 token 报");
  assert.match(top, /phaseText\(s\.phase\)/, "相位还在面板里印 idle/streaming");
  assert.match(top, /refresh_models: "刷新模型"/, "任务种类没翻成中文");
  assert.ok(!/t\.kind \|\| "\?"/.test(tpl), "任务行还在直接印原始 kind");
  assert.match(top, /\["连接状态", s\.conn/, "状态行没有连接状态");
  assert.match(top, /\["运行状态", phase/, "状态行没有运行状态");
  assert.match(top, /输入预算 .* tokens = 上下文上限 .* − 输出预留/, "预算口径没有写清楚");
  assert.match(css, /\.ctx-key \{[^}]*width:\s*6\.8em/, "标签列太窄（「自动压缩阈值」「上次实际用量」会换行）");
  assert.ok(!/还能装|给回复留的地方|没有在跑的任务|上次用了/.test(top), "面板里还留着口语化文案");
  assert.match(top, /const ctxTab = ref(?:<[^>]+>)?\("context"\)/, "面板没有 tab 状态");
  assert.match(tpl, /<VTabs[\s\S]*?v-model="ctxTab"/, "面板不是 Vuetify VTabs");
  assert.match(tpl, /<VTab value="context">上下文<\/VTab>/, "没有上下文 tab");
  assert.match(tpl, /<VTab value="tasks">任务/, "没有任务 tab");
  assert.match(tpl, /<VWindow v-model="ctxTab"/, "面板内容没有用 Vuetify VWindow 切换");
  assert.match(tpl, /<VWindowItem value="context">/, "上下文内容没有挂在 context 窗口");
  assert.match(tpl, /<VWindowItem value="tasks">/, "任务内容没有挂在 tasks 窗口");
  assert.match(top, /filter\(\(t\) => !t\.finished_at\)/, "任务 tab 没滤掉已结束的任务（它叫「正在运行」）");
  assert.match(top, /const runningQueues = computed\(\(\) => queues\.value\.filter\(\(q\) => !q\.finished_at\)\)/,
    "任务 tab 的队列没滤掉已结束的");
  assert.match(tpl, /<VWindowItem value="tasks">[\s\S]*?class="[^"]*ctx-tasks"/,
    "任务内容没有挂在任务 tab 下");

  // ② 数字 = **在跑**的条数（finished_at 有值的不算）；这个口径只有一处。
  assert.match(top, /const taskRunning = computed\(\(\) => tasks\.value\?\.running \?\? 0\)/,
    "环心数字没取 board.running（自己去数 tasks 就会与后端不一致）");
  assert.match(top, /Math\.max\(taskRunning\.value, queueRunning\.value\)/,
    "环心没把队列算进去（两步之间会显示 0，但队列显然还在跑）");

  // ③ 没有后台任务时**不显示 0**（2026-11-02 用户定）；有任务时整个环转（数字本身不动）。
  assert.ok(!/v-if="taskRunning"/.test(tpl), "别用 taskRunning 藏数字 —— 队列也要算");
  assert.match(css, /#ctx-ring\.busy svg[^{]*\{[^}]*animation:\s*ctx-ring-spin/, "有任务时环不转");
  assert.match(css, /@keyframes ctx-ring-spin/, "没有环转动的 keyframes");
  assert.match(css, /prefers-reduced-motion[\s\S]{0,120}ctx-ring\.busy svg/,
    "减少动效时没兜底（环不该转，但数字还在）");
  assert.match(css, /\.ctx-ring-n \{[^}]*font-size:\s*0\.68em/, "环心数字没有字号（会与 width 叠乘）");

  // ④ 喂数只有一处；输入框上方那行小字连尸首都不许留。
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /topbarRef\.value\?\.setTasks\(board\)/, "App 没把任务账本喂给顶栏");
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /topbarRef\.value\?\.setTasks\(null\)/, "取不到任务时该喂 null（环心归 0）");
  assert.ok(!/setTaskLine/.test(composer), "Composer 还留着 setTaskLine");
  assert.ok(!/taskline/.test(composer), "Composer 还留着 .taskline 那行");
  assert.ok(!/taskLineText/.test(fmt), "format.js 还留着 taskLineText（没有调用方了）");
  assert.ok(!/taskLineText/.test(app), "App 还在引用 taskLineText");

  // ⑤ 点环打开的面板里同时有任务明细与取消。
  assert.match(tpl, /v-for="g in taskGroups"/, "环的面板没有按大类分组列任务");
  assert.match(tpl, /v-for="t in g\.items"/, "环的面板没有逐条列出任务");
  assert.match(tpl, /taskElapsed\(t\)/, "面板没显示任务跑了多久");
  assert.match(tpl, /@click\.stop="emit\('cancel-task', t\.id\)"/, "面板不能取消任务");
  assert.match(tpl, /@click\.stop="emit\('cancel-queue', q\.id\)"/, "面板不能取消队列");
  assert.match(css, /\.task-row\.failed\b/, "失败的任务行没有样式");
});

// 队列任务（2026-11-01 用户定）：「队列」「一般任务」「LLM 生成任务」三个词要在面板上分得开。
//
// 队列 = 一串串行的原子任务（有进度、可取消）；原子任务按大类分组。
// **两层，不是一个列表** —— 队列跑的时候下面同时会有"当前这一步"的原子任务，那不是重复。
test("后台任务面板：队列单独一栏（带进度与取消），原子任务按大类分组", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const tpl = top.slice(top.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");

  // ① 队列那一栏：只遍历**没结束**的 `runningQueues`，带 `index/total` 进度
  assert.match(tpl, /v-for="q in runningQueues"/, "浮板里没有正在跑的队列那一栏");
  assert.match(tpl, /第 \{\{ q\.index \}\} \/ \{\{ q\.total \}\} 步/, "队列行没有「第几/共几步」进度");

  // ② 取消按钮：列表本来就只含在跑的，点了把 id 报上去（App 去发请求）
  assert.match(tpl, /class="task-cancel"/, "队列行没有取消按钮");
  assert.match(tpl, /@click\.stop="emit\('cancel-queue', q\.id\)"/, "取消按钮没把队列 id 报上去");
  assert.match(top, /defineEmits(?:\(\[[^\]]*"cancel-queue"|<\{[^}]*"cancel-queue")/, "TopBar 没声明 cancel-queue");
  assert.match(app, /@cancel-queue="cancelQueue"/, "App 没接 cancel-queue");
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /function cancelQueue\(id[^)]*\)/, "App 没有 cancelQueue");
  // 取消完要**立刻**刷一次面板（别等那 2 秒的节拍）
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /api\.cancelQueue\(id\)[\s\S]{0,80}refreshTasks\(\)/, "取消之后没补刷面板");
  assert.match(api, /cancelQueue:\s*\(queueId[^)]*\)\s*=>\s*call<[^>]+>\("POST", `\/tasks\/queues\/\$\{enc\(queueId\)\}\/cancel`\)/,
    "API 客户端的 cancelQueue 路径不对");

  // ③ 大类：按**后端给的 `group`** 分，前端不按 kind 猜
  assert.match(top, /const key = t\.group \|\| "plain"/, "分组没读后端给的 group（按 kind 猜会分岔）");
  assert.match(top, /GROUP_LABELS(?:\s*:\s*Record<[^>]+>)?\s*=\s*\{ llm: "模型调用", plain: "其他任务" \}/,
    "两个大类的名字不规范（模型调用 / 其他任务）");
  assert.match(tpl, /class="task-group-head"/, "分组没有小标题");

  // ④ 队列归哪一类由后端给（压缩 = llm），前端别自己判
  assert.ok(!/kind === "compact"/.test(top), "前端在按 kind 判大类 —— 该读 group");

  // ⑤ 样式：队列行 / 进度 / 取消按钮都得有
  for (const cls of [".task-queue", ".task-progress", ".task-cancel"]) {
    assert.ok(css.includes(cls), `缺少 ${cls} 的样式`);
  }
});

test("任务面板：分段压缩显示一个 Job 和它下面的子 Task，不是平铺一个任务", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const tpl = top.slice(top.indexOf("<template>"));
  assert.match(top, /compact_job: "分段压缩"/, "任务种类表没有 compact_job");
  assert.match(top, /const taskRows = computed/, "没有按 parent_id 组任务行");
  assert.match(top, /t\.parent_id/, "任务面板没看父 Job id");
  assert.match(top, /t\.depth > 0/, "子 Task 没有缩进层级");
  assert.match(top, /taskRoleText\(t\)/, "没有 Job / 任务角色牌");
  assert.match(tpl, /class="task-role"/, "任务行没有角色牌");
  assert.match(css, /\.task-row\.child\s*\{[^}]*padding-left/, "子 Task 没有缩进样式");
});

test("顶栏是不透明的（消息滚到栏底下要被挡住）", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = css.match(/#chat-col > header \{[^}]*\}/g)?.find((r) => /position:\s*absolute/.test(r));
  assert.ok(rule, "找不到 #chat-col > header 的定位规则");
  assert.ok(!/background:\s*none/.test(rule), "顶栏还是透明的（background: none）");
  assert.match(rule, /background:\s*var\(--md-surface-container-low\)/, "顶栏底色该用 MD3 surface 容器铺满");
  // 「不透明」要的是挡住，不是把栏画成一个盒子 —— 别顺手加边框/分隔线。
  assert.ok(!/border-bottom/.test(rule), "顶栏加了分隔线（用户只说不透明）");
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

// 启动自检（2026-11-01 换口）：`/auth/status` 随鉴权一起删了 ⇒ 探 `/health`。
// 两道闸照旧 —— 地址先规范化（裸地址会被 fetch 当相对路径）、回体必须是 microchat 的 JSON。
test("probeHealth：先规范化再探 /health；非 microchat 的 200 拒绝", async () => {
  const ok = await probeHealth("x:8787", { fetchFn: async (url) => {
    assert.equal(url, "http://x:8787/api/v1/health"); // 裸地址 ⇒ 补协议、补 /api/v1
    return { ok: true, status: 200, json: async () => ({ status: "ok", version: "1" }) };
  } });
  assert.equal(ok.version, "1");
  await assert.rejects(
    () => probeHealth("http://x:8787", { fetchFn: async () => ({ ok: true, status: 200, json: async () => ({ hello: "world" }) }) }),
    (e) => /不是 microchat/.test(e.message),
  );
});

// 鉴权与账号**已经砍掉**了（2026-11-01 用户定：只允许来自 127.0.0.1 的访问）。
// 这条守着"别把 Bearer / 门板 / 用户管理悄悄加回来" —— 前后端一起删过一轮，加回来就是倒退。
test("鉴权与账号已经砍掉：没有 token、没有门板、没有用户管理", () => {
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  const prefs = readFileSync(join(SRC, "utils", "prefs.ts"), "utf8");
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const srcDir = join(SRC, "..", "src");

  // ① API 客户端：没有登录/登出/用户管理那几个口
  for (const gone of ["authLogin", "authSetup", "probeAuthStatus", "authPost"]) {
    assert.ok(!api.includes(gone), `api/client.ts 还留着 ${gone}`);
  }
  for (const gone of ["me: () =>", "logout: () =>", "createUser", "deleteUser", "setUserPassword"]) {
    assert.ok(!api.includes(gone), `api/client.ts 还留着 ${gone}`);
  }

  // ② 没有凭据存槽
  assert.ok(!prefs.includes("apiToken"), "prefs.js 还留着 apiToken");
  assert.ok(!/localStorage\.setItem\("mc_token"/.test(prefs), "prefs.js 还在写 mc_token");

  // ③ 没有门板 / 用户视图（文件都删了）
  for (const gone of ["ConnectGate.vue", "UserView.vue"]) {
    assert.ok(!readFileSync(join(srcDir, "App.vue"), "utf8").includes(gone), `App.vue 还在 import ${gone}`);
    assert.throws(() => readFileSync(join(SRC, "components", gone)), `${gone} 该已经被删掉`);
  }
  for (const gone of ["showGate", "gateError", "currentUser", "isAdmin", "userOpen"]) {
    assert.ok(!app.includes(gone), `App.vue 还留着 ${gone}`);
  }

  // ④ 启动自检走 /health
  assert.match(app, /probeHealth\(cand\)/, "启动自检没走 probeHealth");
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

// 流式打字机（2026-11-03 用户定）：后端一块一块到，前端把这一块摊在几帧里露出。
test("打字机：按前后两包的到达间隔决定这一拍的速度；线性露出", () => {
  assert.equal(typewriterDuration(0), TYPEWRITER_DEFAULT_MS); // 第一包没有上一包
  assert.equal(typewriterDuration(20), TYPEWRITER_MIN_MS);
  assert.equal(typewriterDuration(100), 100);
  assert.equal(typewriterDuration(500), 500); // 0s→0.5s 这一拍摊 0.5s
  assert.equal(typewriterDuration(700), 700); // 0.5s→1.2s 这一拍摊 0.7s
  assert.equal(typewriterDuration(5000), TYPEWRITER_MAX_MS);
  assert.equal(revealCount(0, 100, 0, 300), 0);
  assert.equal(revealCount(0, 100, 150, 300), 50);
  assert.equal(revealCount(0, 100, 300, 300), 100);
  assert.equal(revealCount(40, 100, 999, 300), 100);

  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const tw = readFileSync(join(SRC, "composables", "useTypewriter.ts"), "utf8");
  assert.match(tw, /const streamDisplay = ref\(/, "没有打字机显示状态");
  assert.match(app, /setStreamTarget\(merged\.text, merged\.reasoning\)/, "轮询到字后没进打字机");
  assert.match(app, /streamText: streamDisplay\.value\.text/, "气泡还在直接吞整块");
  assert.match(tw, /const gap = twLastAt \? start - twLastAt : 0/, "没有按上一包到这一包的真实间隔计时");
  assert.match(tw, /duration: typewriterDuration\(gap\)/, "打字时长不是按包间隔算的");
  assert.match(app, /await waitTypewriter\(\)/, "收尾没等打字机放完（会跳一下）");
  assert.ok((app.match(/setStreamTarget\(/g) ?? []).length >= 2, "重摇候选流没接打字机");
  assert.match(app, /previewText: streamDisplay\.value\.text/, "重摇气泡没读打字机前缀");
  assert.match(app, /await waitTypewriter\(\);[\s\S]{0,100}await switchReroll/, "重摇切版前没等最后一块放完");
  assert.match(tw, /requestAnimationFrame|requestFrame/, "没有逐帧推进");
  assert.ok(!/streamDisplay\.value\.reasoning \|\| m\.reasoning/.test(app),
    "思考还回落到完整 target —— 会整块瞬间冒出");
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  assert.match(bubble, /(className = "stream-cursor"|class="stream-cursor")/, "流式正文没有终端光标");
  assert.match(bubble, /createTreeWalker\(textEl\.value, NodeFilter\.SHOW_TEXT/, "光标没贴在最后一个文字节点后");
  assert.match(bubble, /正在向后端发送请求/, "没有发送中状态");
  assert.match(bubble, /等待 Provider 响应/, "没有等待 Provider 状态");
  assert.match(app, /startSendingBubble/, "没有先画发送中助手占位");
  assert.match(app, /phase = "waiting"/, "202 后没切到等待 Provider");
  assert.match(css, /\.stream-cursor \{[^}]*position:\s*absolute[^}]*animation:\s*stream-cursor-blink/, "终端光标不是绝对定位的稳定覆盖层/不闪");
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)[\s\S]{0,160}\.stream-cursor/, "减少动效时没兜底");
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

// 最新 AI 回复底部的「编辑最新 Prompt」入口：A-B-C-D-E-F ⇒ 编辑 E，不用翻回去点 E。
test("最新 Prompt：A-B-C-D-E-F 指向 E；只在尾条 AI 上给按钮", () => {
  const list = [
    { id: "a", role: "user" }, { id: "b", role: "assistant" },
    { id: "c", role: "user" }, { id: "d", role: "assistant" },
    { id: "e", role: "user" }, { id: "f", role: "assistant" },
  ];
  assert.equal(latestPromptFor(list), "e");
  assert.equal(latestPromptFor([{ id: "u", role: "user" }]), null);
  assert.equal(latestPromptFor([{ id: "a", role: "assistant" }]), null);
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  assert.match(app, /const latestPromptId = computed\(\(\) => latestPromptFor\(vueMessages\.value\)\)/,
    "App 没算最新 Prompt");
  assert.match(app, /:prompt-id="row\.msg\.messageId === lastMsgId \? latestPromptId : null"/,
    "没把 Prompt id 交给尾条 AI");
  assert.match(app, /@edit-prompt="onBubbleEditPrompt"/, "没接「编辑最新 Prompt」");
  assert.match(bubble, /v-if="role !== 'user' && isLast && promptId"/, "按钮不该出现在非尾条/用户消息上");
  assert.match(bubble, /\$?emit\('edit-prompt', promptId\)/, "按钮没把 Prompt id 报上去");
  assert.match(bubble, /v-html="ICONS\.notebookPen"/, "「编辑最新 Prompt」还是纯文字按钮，没换成记事本+笔图标");
  assert.ok(!/>编辑最新 Prompt</.test(bubble), "按钮里还留着纯文字");
});

// 编辑后「保存并重摇」的合法位置（2026-11-02 用户定）：
// 只有尾条 assistant、或紧挨着它的前一条 user（例如 user2 后面只剩 AI2）可以；
// 编辑 user1 不行 —— 它后面还跟着多条消息，重摇尾条 AI2 跟 user1 没关系。
test("编辑后能否重摇：尾条 assistant 或它前面的那条 user", () => {
  const list = [
    { id: "u1", role: "user" },
    { id: "a1", role: "assistant" },
    { id: "u2", role: "user" },
    { id: "a2", role: "assistant" },
  ];
  assert.equal(editRerollTarget(list, "a2"), "a2");
  assert.equal(editRerollTarget(list, "u2"), "a2"); // 用户2 保存后重摇 AI2
  assert.equal(editRerollTarget(list, "u1"), null);
  assert.equal(editRerollTarget(list, "a1"), null);
  assert.equal(editRerollTarget(list.slice(0, 3), "u2"), null, "后面没有 assistant 时不该给重摇");
  assert.equal(editRerollTarget(list, "missing"), null);
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
// 主题 id 在**三处**各写一遍：`src/utils/prefs.ts` 的 `THEME_IDS`、`src/assets/theme.css` 的
// `html[data-theme="…"]` 规则、`UserView.vue` 那个下拉的 option。改了一处忘了另一处**不会报错**
// —— 页面只是安静地退回默认配色（或者让人选到一个没有规则的 id）。
// 所以把三张表钉死：THEME_IDS 是唯一权威，另外两处必须**正好**是它（顺序也一样）。
// 读文件比对，不引 CSS/Vue 解析器。
import { existsSync, readFileSync } from "node:fs";
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
  // 2026-11-01：这块从删掉的 UserView 搬到了 AppearancePane（设置里的「外观」区）。
  const vue = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");
  // VSelect：选项在 themeOptions 数组里，顺序照 THEME_IDS。
  assert.match(vue, /<VSelect[^>]*:model-value="theme"[^>]*:items="themeOptions"/, "没找到主题 VSelect");
  const opts = vue.match(/const themeOptions = \[([\s\S]*?)\];/);
  assert.ok(opts, "没找到 themeOptions");
  const chosen = [...opts[1].matchAll(/value: "([^"]*)"/g)].map((m) => m[1]);
  assert.deepEqual(chosen, THEME_IDS, "主题下拉的选项与 THEME_IDS 对不上");
});

test("外观缩放：拖动只改草稿，点保存缩放才应用", () => {
  const pane = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");
  assert.match(pane, /scaleDirty/, "没有未保存状态");
  assert.match(pane, /function onScaleInput[\s\S]{0,220}scaleDirty\.value = true/, "拖动没有只改草稿");
  assert.match(pane, /function saveScale[\s\S]{0,220}saveTextScale\(textScale\.value\)/, "没有保存缩放动作");
  assert.match(pane, /:disabled="!scaleDirty"[\s\S]*保存缩放/, "保存缩放按钮没接未保存状态");
  assert.match(pane, /@click="saveScale"/, "保存缩放按钮没动作");
  assert.ok(!/function onScaleInput[\s\S]{0,220}saveTextScale/.test(pane), "拖动仍然立即保存/应用");
  assert.ok(!/flushPrefs/.test(pane), "缩放还保留松手立即落盘");
});

test("设置-聊天：默认值 → 上下文 → 压缩 → 生成，顺序固定", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const chatAt = settings.indexOf('<section id="panel-chat"');
  const sessionAt = settings.indexOf('<section id="panel-session"');
  assert.ok(chatAt > 0 && sessionAt > chatAt, "找不到聊天/会话面板");
  const heads = ["新会话默认", "上下文与加载", "自动压缩", "生成与标题"].map((h) => settings.indexOf(`<h3>${h}</h3>`, chatAt));
  assert.ok(heads.every((at) => at > chatAt && at < sessionAt), "聊天设置有分区标题缺失");
  assert.ok(heads.every((at, i) => i === 0 || at > heads[i - 1]), "聊天设置分区顺序不对");
  assert.ok(!settings.includes("<h3>回复与自动压缩</h3>"), "旧的“回复与自动压缩”还留着");
  const sessionPart = settings.slice(sessionAt, settings.indexOf("</VTabsWindowItem>", sessionAt));
  assert.ok(!sessionPart.includes("新会话默认"), "会话管理里还留着新会话默认设置");
});

test("外观页：发送键 / 预演 / 命令菜单三格都在（搬用户视图时丢过）", () => {
  const vue = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");
  // 发送键的三档必须与 `prefs.js` 的 `wantsSend` 完全一致（多一档 = 选了不生效）。
  assert.match(vue, /<VSelect[^>]*:model-value="send"[^>]*:items="sendOptions"/, "没找到发送方式 VSelect —— 入口又丢了");
  const opts = vue.match(/const sendOptions = \[([\s\S]*?)\];/);
  assert.ok(opts, "没找到 sendOptions");
  const options = [...opts[1].matchAll(/value: "([^"]*)"/g)].map((m) => m[1]);
  assert.deepEqual(options, ["enter", "shift-enter", "button"], "发送键三档与 wantsSend 对不上");

  assert.match(vue, /:model-value="preview"/, "「输入时预演载荷」开关没了");
  assert.match(vue, /:model-value="cmdMenu"/, "「命令菜单」开关没了");

  // 开关必须"先赋值、再存"：`v-model + @update:model-value="persist"` 两个监听同挂 update 时，
  // 显式那个可能先跑 ⇒ 存的是旧值（实测：界面 on、服务端 off）。
  for (const fn of ["setPreview", "setCmdMenu", "setChromeHide", "setMdEnabled"]) {
    assert.match(vue, new RegExp(`function ${fn}\\(v[^)]*\\)`), `${fn} 不存在（又会存成旧值）`);
  }
  assert.ok(!/v-model="(preview|cmdMenu|chromeHide|mdEnabled)"[^>]*@update:model-value="persist/.test(vue),
    "开关又用回了 v-model + @update:model-value=\"persist\"（会存旧值）");

  // 三格都必须落盘（`persist` 是唯一出口）。
  for (const fn of ["saveSendMode", "savePreview", "saveCommandMenu"]) {
    assert.match(vue, new RegExp(`${fn}\\(`), `${fn} 没有接到设置项`);
  }
});

test("主题：删掉的老 id 都能搬到现存的一档（不许让人主题凭空变样）", () => {
  // 这些 id 曾经写进过别人的 localStorage。删主题时如果只删 CSS 规则，他们匹配不到任何规则
  // ⇒ 页面退回默认配色，用户看到的是"我的主题自己变了"。
  const LEGACY = ["graphite", "deepseek", "midnight", "wine", "forest", "paper", "breeze", "breeze-dark"];
  for (const id of LEGACY) {
    const moved = normalizeTheme(id);
    assert.ok(THEME_IDS.includes(moved), `老主题「${id}」被搬到了「${moved}」，那不是一个现存的主题`);
    assert.notEqual(moved, undefined);
  }
  // 深夜蓝/酒红/森绿都是深色 ⇒ 归暗色；日间白 ⇒ 亮色；Breeze Dark ⇒ Breeze。钉住这几条映射。
  assert.equal(normalizeTheme("midnight"), "dark");
  assert.equal(normalizeTheme("paper"), "light");
  assert.equal(normalizeTheme("breeze"), "light");
  assert.equal(normalizeTheme("breeze-dark"), "dark");
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

// 界面缩放（2026-11-01 起**存服务器**，不再写 localStorage）：
// 这里钉三件事 —— 缺省 100%、存完**当场**生效（不是等刷新）、读的时候也要夹。
// 从前那条还断言 `localStorage.mc_text_scale` 往返；现在那一格不再是存储（只是老数据的
// 读入兜底），所以改成断言"写的是服务器那份缓存"，而不是"写进了 localStorage"。
test("界面缩放：存到服务器那份缓存、读回、挂到根节点的 --app-text-scale", () => {
  stubStorage();
  const vars = {};
  const originalDoc = globalThis.document;
  globalThis.document = { documentElement: { style: { setProperty: (k, v) => { vars[k] = v; } } } };
  try {
    // ① 老数据的读入兜底：localStorage 里还留着旧值时读得出来（迁移前的那一帧靠它）。
    //    ⚠ 顺序有讲究：**先**测兜底，**再**测存盘 —— 一旦 `saveTextScale` 写过缓存，
    //    缓存就是权威了，再改旧键不会影响读取（那正是"服务器权威"该有的样子）。
    localStorage.setItem("mc_text_scale", "1.05");
    assert.equal(textScale(), 1.05);
    localStorage.setItem("mc_text_scale", "3");   // 旧键被手改成越界值
    assert.equal(textScale(), TEXT_SCALE_MAX);    // 读的时候也要夹

    // ② 存盘：进缓存、当场挂到根节点（不等刷新），读回来还是它。
    saveTextScale(1.15);
    assert.equal(vars["--app-text-scale"], "1.15");
    assert.equal(textScale(), 1.15);

    applyTextScale(0.9);
    assert.equal(vars["--app-text-scale"], "0.9");
  } finally {
    globalThis.document = originalDoc;
  }
});

test("界面缩放：松手 / 页面隐藏会立刻 flush，不把最后一格留给 250ms 防抖", async () => {
  stubStorage();
  const vars = {};
  const originalDoc = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { documentElement: { style: { setProperty: (k, v) => { vars[k] = v; } } } };
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return { ok: true, status: 200, json: async () => ({ prefs: { text_scale: "1.3" }, defaults: {}, markdown_types: [] }) };
  };
  try {
    // 让 sharedApi 换一个 base => 重建客户端，确保它拿的是下面这枚 stub fetch
    // （前面测试留下的防抖定时器可能已经用真 fetch 建过客户端）。
    localStorage.setItem("mc_api", "http://test.invalid/api/v1");
    saveTextScale(1.3);
    assert.equal(calls.length, 0, "防抖期不该已经发请求");
    flushPrefs();
    await new Promise((r) => setTimeout(r, 0));
    assert.equal(calls.length, 1, "flush 没有立刻发 PUT");
    assert.equal(calls[0].init.keepalive, true, "刷新前那一发必须是 keepalive");
    assert.equal(vars["--app-text-scale"], "1.3");
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.document = originalDoc;
  }
});

test("界面缩放：开机先用镜像铺第一帧（服务器答话前不闪回 100%）", () => {
  const prefs = readFileSync(join(SRC, "utils", "prefs.ts"), "utf8");
  assert.match(prefs, /const mirrored = readMirror\(\);/);
  assert.match(prefs, /if \(mirrored\) cache = merge\(FALLBACK_PREFS, mirrored\);/);
  assert.match(prefs, /addEventListener\("pagehide", flushOnHide\)/);
  assert.match(prefs, /visibilitychange/);
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

  const client = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  assert.match(client, /compactPayload:\s*\(id[^)]*\)/, "api 里没有 compactPayload");
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
  const tpl = top.slice(top.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(top, /id="ctx-ring"/, "顶栏里没有那枚环");
  assert.match(top, /pathLength="100"/, "弧长没走 pathLength 归一化（dasharray 会被当成绝对长度）");
  assert.match(top, /setContext/, "TopBar 没有暴露 setContext");
  assert.match(top, /class="agent-menu ctx-menu"/, "浮板没有复用 Agent 选人那套底（--tg-float / shadow-3）");
  // 2026-11-09 用户定：环稍微加粗；切 tab 时浮板尺寸不能跳；外围留 padding；
  // 2026-11-10：四 tab 要全部可见（放宽 + 取消 Vuetify 的 90px 最小宽）。
  assert.match(tpl, /<VCard class="agent-menu ctx-menu"/, "浮板不是 Vuetify VCard");
  assert.match(tpl, /<VTabs[\s\S]*?v-model="ctxTab"/, "浮板 tab 不是 Vuetify VTabs");
  assert.match(tpl, /<VWindow v-model="ctxTab"/, "浮板内容没有用 VWindow");
  assert.match(tpl, /<VProgressLinear/, "上下文占用条不是 Vuetify VProgressLinear");
  assert.match(tpl, /<VList[\s\S]*?class="ctx-list"/, "上下文明细不是 Vuetify VList");
  assert.match(css, /#ctx-ring circle \{[^}]*stroke-width:\s*4/, "环没有加粗");
  assert.match(css, /\.ctx-menu \{[^}]*width:\s*min\(30em/, "浮板宽度没钉死（切 tab 会变宽窄）");
  assert.match(css, /\.ctx-tabs \.v-tab \{[^}]*min-width:\s*0/, "四个 tab 会被 Vuetify 默认 90px 最小宽挤掉");
  assert.match(css, /\.ctx-menu \{[^}]*height:\s*min\(23\.5em/, "浮板高度没钉死（切 tab 会跳高矮）");
  assert.match(css, /\.agent-menu\.ctx-menu \{[^}]*padding:\s*1em/, "浮板外围没有 padding（被 .agent-menu 的 0.25em 覆盖了）");
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
  assert.match(css, /\.ctx-ring-fill \{ stroke: var\(--tg-ok\)/, "环没用 --tg-ok");
  // 三套主题都得有绿色，否则换主题后环就没了颜色（CSS 变量缺失 ⇒ stroke 回 initial = 黑）
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  const okCount = (theme.match(/--tg-ok:/g) ?? []).length;
  assert.equal(okCount, 5, `--tg-ok 该在 :root + light/dark 各一条 + auto 两条，实际 ${okCount} 条`);

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
  assert.match(top, /function setStatus\(s[^)]*\)/, "TopBar 没有 setStatus");
  assert.match(top, /const statusRows = computed/, "浮板里没有连接/助手/运行那三行");
  assert.match(top, /ctx-list-status/, "浮板里那三行没接样式");
  assert.ok(!/\.statusline/.test(css), "main.css 里还留着 .statusline 规则（那一行已经删了）");
});

// 页签条：设置主栏与 Agent 编辑器改用 Vuetify 的 VTabs + VTabsWindowItem。
test("页签条：设置与 Agent 都用 Vuetify VTabs/VTabsWindow", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  assert.match(settings, /<VTabs[\s\S]*v-model="tab"[\s\S]*<VTab v-for="t in SETTINGS_TABS"[\s\S]*@focus="tab = t\.id"/, "设置主栏没用 VTabs/VTab（或缺自动激活）");
  assert.match(editor, /<VTabs[\s\S]*v-model="tab"[\s\S]*<VTab v-for="t in tabs"[\s\S]*@focus="tab = t\.id"/, "Agent 编辑器没用 VTabs/VTab（或缺自动激活）");
  assert.match(settings, /<VTabsWindow v-model="tab"[\s\S]*<VTabsWindowItem value="chat"/, "设置没有真正的 tabpanel");
  assert.match(editor, /<VTabsWindow v-model="tab"[\s\S]*<VTabsWindowItem value="base"/, "Agent 编辑器没有真正的 tabpanel");
  for (const [name, src] of [["SettingsModal.vue", settings], ["AgentEditor.vue", editor]]) {
    assert.ok(!src.includes("TabBar"), `${name} 还引用旧 TabBar`);
    assert.ok(!src.includes("tab-thumb"), `${name} 还自己画滑动底片`);
    assert.ok(!src.includes("offsetWidth"), `${name} 还自己在量按钮宽度`);
  }
});

// 用户 2026-10-28：「在具体某个 Agent 的设置界面里，我们最好也分 TAB，不然的话，全挤在一起很丑。」
// 分页签这件事**两个地方都要**：VTabs 在，且每个能力各占一页（加能力自动多一页，
// 别写成硬编码的两页 —— 硬编码的那种，加第三个能力时那个能力的开关会悄悄挤在最后一页里）。
test("Agent 编辑器分页签：传统 Agent 是基本/系统提示词，FlowAgent 是基本/流程", () => {
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  assert.match(editor, /\{ id: "base", label: "基本" \}/, "没有「基本」那一页");
  assert.match(editor, /\.\.\.ABILITY_IDS\.map\(\(id\) => \(\{ id, label: ABILITY_LABELS\[id\] \?\? id \}\)\)/,
    "能力那几页该由 ABILITY_IDS 生成（加能力自动多一页）");
  // 三个分区都在真正的 VTabsWindowItem 里（翻页不丢已填的东西）
  assert.match(editor, /<VTabsWindowItem value="base">/, "基本那一页没有 tabpanel");
  assert.match(editor, /<VTabsWindowItem v-if="agentKind !== 'flow'" value="prompt">/, "传统 Agent 的系统提示词页没有 tabpanel");
  assert.match(editor, /<VTabsWindowItem v-if="agentKind === 'flow'" value="flow">/, "FlowAgent 的流程页没有 tabpanel");
  assert.match(editor, /<FlowEditor v-model:graph="flowGraph"/, "流程页没有接 Vue Flow 编辑器");
  assert.match(editor, /<VTabsWindowItem v-for="id in ABILITY_IDS" :key="id" :value="id">/, "能力那几页没有 tabpanel");
  // 保存行在页签**外面**：哪一页都能存（藏在某一页里的那种，翻到别的页就找不到保存了）
  const saveAt = editor.indexOf(">保存<");
  const windowEnd = editor.indexOf("</VTabsWindow>");
  assert.ok(saveAt > windowEnd, "保存行该在页签分区外面（否则只在某一页能看见）");
});

// 2026-11-11 用户：酒馆预设会接管整套提示词，必须把“忽略了什么”写在选项旁边。
test("Agent 酒馆预设：选中时警告说明会接管哪些提示词", () => {
  const editor = readFileSync(join(SRC, "components", "AgentEditor.vue"), "utf8");
  assert.match(editor,  /提示词方案（酒馆预设）/, "预设字段没有提醒会接管");
  assert.match(editor, /class="field-warning"/, "没有显眼的警告块");
  for (const text of ["系统提示词", "追加片段", "手工提示词方案", "会话自己的「自定义提示词」", "选「不使用」"]) {
    assert.ok(editor.includes(text), `警告缺了：${text}`);
  }
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

test("模型图标：前端选项与后端封闭集合一致", () => {
  const go = readFileSync(join(REPO, "core", "internal", "config", "model_rules.go"), "utf8");
  const match = go.match(/ModelIconIDs = \[\]string\{([^}]*)\}/);
  assert.ok(match, "找不到后端 ModelIconIDs");
  const ids = [...match[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual(MODEL_ICON_OPTIONS.map((option) => option.value), ids,
    "前端图标选项与后端允许列表不一致");
  // 图标必须本地托管（models.dev 下载到 public/），不能运行时打外链。
  const iconComponent = readFileSync(join(SRC, "components", "ModelIcon.vue"), "utf8");
  assert.ok(!/https?:\/\/models\.dev/.test(iconComponent), "模型图标不该走外链");
  for (const option of MODEL_ICON_OPTIONS) {
    assert.ok(existsSync(join(REPO, "frontend", "web", "public", "model-icons", `${option.value}.svg`)),
      `缺少本地图标 ${option.value}.svg`);
    assert.match(iconComponent, new RegExp(`\\.mi-${option.value}\\b`),
      `ModelIcon.vue 缺少 .mi-${option.value} 的遮罩样式（图标会静默不显示）`);
  }
});

// 模型规则：结构化编辑器（不是让用户裸改 JSON）。规则有顺序，必须能移动。
test("模型规则编辑器：结构化控件 + 顺序调整，不是 JSON textarea", () => {
  const editor = readFileSync(join(SRC, "components", "ModelRulesEditor.vue"), "utf8");
  assert.ok(!/<textarea/i.test(editor), "模型规则编辑器不该是裸 JSON textarea");
  assert.match(editor, /新增规则/, "缺少新增规则入口");
  assert.match(editor, /moveRule/, "缺少规则顺序调整");
  assert.match(editor, /VSwitch/, "缺少 is_final / thinking 开关");
  assert.match(editor, /HelpTip/, "属性旁缺少问号注释");
  assert.match(editor, /MODEL_ICON_OPTIONS/, "图标该是固定选项，不是自由输入");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /<VTab value="global">全局模型规则<\/VTab>/, "全局规则该和渠道列表同级");
  const provider = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  assert.match(provider, /<ModelRulesEditor scope="provider"/, "渠道详情该有自己的覆盖规则编辑器");
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

test("模型面板：按钮上那行字（列表里有 → 显示名；没有 → 不在列表；列表还没到 → 加载中）", () => {
  const key = modelKey("openrouter", "openai/gpt-4o");
  assert.equal(curModelLabelOf(MODEL_SAMPLE, key), "GPT-4o");
  assert.equal(
    curModelLabelOf(MODEL_SAMPLE, modelKey("x", "y"), { value: modelKey("x", "y"), label: "x/y（不在列表）" }),
    "x/y（不在列表）",
  );
  // 列表还没到（异步）时不要再闪 provider/upstream_id 裸串；用中性的加载占位。
  assert.equal(curModelLabelOf([], modelKey("openrouter", "openai/gpt-4o")), "加载模型…");
  assert.equal(curModelLabelOf([], ""), "选择模型");
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(top, /const props = (?:withDefaults\()?defineProps/, "props 没接住 ⇒ computed 里 props 是未定义、会静默空白");
  assert.match(top, /if \(props\.loading\) return ""/, "会话加载时没有压住模型名");
  assert.match(top, /v-if="loading" class="model-loading"/, "会话加载时没有骨架占位");
  assert.match(app, /:loading="loadingSession"/, "App 没把会话加载态传给顶栏");
});

test("模型面板：按钮 / 浮板 / emit / App 监听四处接着，且不再有原生 <select>", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");

  assert.match(top, /id="model-btn"/, "顶栏里没有那枚模型按钮");
  assert.match(top, /class="agent-menu model-menu"/, "浮板没有复用 Agent 选人那套底（--tg-float / shadow-3）");
  assert.match(top, /function toggleModel\(/, "TopBar 没有 toggleModel");
  assert.match(top, /emit\("select-model", value\)/, "面板选完没 emit select-model");
  assert.match(app, /@select-model=/, "App 没有监听 select-model（点了不会换模型）");
  // 顶栏剩下模型/上下文两块浮板；Agent 已搬到输入栏上方，由 AgentMenu 自己管。
  assert.match(top, /if \(!modelOpen\.value\) return;[\s\S]{0,60}ctxOpen\.value = false;/, "打开模型面板时没有关掉上下文浮板");
  const agentMenu = readFileSync(join(SRC, "components", "AgentMenu.vue"), "utf8");
  assert.match(agentMenu, /class="agent-btn"/, "AgentMenu 没有头像按钮");
  assert.match(agentMenu, /class="agent-menu agent-picker"/, "AgentMenu 没有选人浮板");
  assert.match(app, /<template #toolbar>[\s\S]{0,300}<AgentMenu/, "Composer 的 toolbar slot 没有放 AgentMenu");
  assert.match(app, /:model-value="sessionInfo\.agent_id \|\| ''"/, "AgentMenu 没有接当前 agent");

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
  assert.match(code, /protocolChoices\.value\.map\(\(option\) => \(\{ label: option\.label, value: option\.protocol \}\)\)/, "下拉没渲染 protocolChoices");
  // ④ 选了之后要有一句人话（下拉一关就只剩 id，用户没法判断区别）
  assert.match(code, /\{\{ protocolHint \}\}/, "选中的协议没有一句人话");
  // ⑤ 显示只用中文名；内部 id 附在显示名里（VSelect 没有 option 的 title）
  assert.match(code, /label: p\.name \+ \(p\.vendor \?/, "服务商没显示中文名 / 内部 id");
  assert.match(code, /label: option\.label, value: option\.protocol/, "接口类型没显示中文名");
});

test("渠道列表：服务商打中文名，内部 id 挂在 title 上", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  assert.match(settings, /p\.vendor_name/, "列表没显示 vendor 的中文名");
  // 协议列已从列表移除（用户说"说什么话"看不懂）；它只在编辑器里，用中文名 + id 附后。
  assert.ok(!/p\.protocol_name/.test(settings), "列表又把 protocol 那列加回来了");
  assert.match(editor, /label: option\.label, value: option\.protocol/, "编辑器里的接口类型没显示中文名");
  // id 不能丢：手改 providers.json 的人得能对上号
  assert.match(editor, /label: p\.name \+ \(p\.vendor \?/, "vendor 的内部 id 没跟着显示名");
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
  assert.match(code, /vendorOptions = computed\(\(\) => \[\.\.\.primaryPresets\.value, \.\.\.otherPresets\.value\]/, "vendor 没按 primary/other 合并成 VSelect 选项");
  // ② 顺序：服务商在 id 前面（id 是改服务商时会跟着变的格，先填等于白填）
  const vendorAt = code.indexOf("<span>服务商</span>");
  const idAt = code.indexOf("<span>渠道标识</span>");
  assert.ok(vendorAt > 0 && idAt > 0, "找不到服务商 / id 那两格");
  assert.ok(vendorAt < idAt, "该先问服务商、再问 id（id 会跟着服务商变）");
  // ③ 端点那一格要说出"真会打到哪儿"（留空 = 用预设，光靠 placeholder 说不清）
  assert.match(code, /effectiveBase/, "端点没有算出实际地址");
  assert.match(code, /实际请求地址/, "端点没把那句人话显示出来");
  // ④ 按钮：从「保存 / 刷新模型 / 刷路由」收成「保存并刷新 / 测试连通性」。
  assert.match(code, />保存并刷新</, "没有「保存并刷新」");
  assert.match(code, />测试连通性</, "没有「测试连通性」");
  assert.ok(!/>刷新模型</.test(code) && !/>刷路由</.test(code), "旧的刷新/刷路由按钮又回来了");
});

test("渠道列表：列名说人话，筛选器并进模型数", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  for (const label of ["渠道", "服务商", "模型", "密钥"]) {
    assert.match(settings, new RegExp(`<th>${label}</th>`), `渠道列表缺「${label}」列`);
  }
  assert.ok(!/走哪家|说什么话|筛选器<\/th>/.test(settings), "渠道列表又出现了走哪家/说什么话/筛选器这种黑话");
  assert.match(settings, /p\.model_filter[^>]*title=/, "筛选器没有并进模型那一格（title 提示）");
});
// 模型筛选器：多条规则用半角逗号分隔、命中任意一条就导入（2026-10-28 用户定：
// 「deepseek,mimo 会吧 deepseek-v4-flash 和 mimo-v2.6-flash 都允许，因为它们分别满足了一个规则」）。
//
// 这条只钉**界面把话说清楚了**：真逻辑在后端 registry.SplitModelFilter / FilterModels
// （那边有成组的单测）。但界面不说清"逗号"与"任意一条"，用户填了 `deepseek,mimo` 会以为
// 那是**一个**子串 ⇒ 一个模型都筛不出来，而界面还显示"已筛选" —— 静默的错。
// 渠道详情里的模型清单（2026-11-02 用户定）：能看见这个渠道导入了什么，但**不能编辑**。
test("渠道详情：只读列出这个渠道导入的模型", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  assert.match(editor, /const models = ref(?:<[^>]+>)?\(\[\]\)/, "编辑器没有模型列表状态");
  assert.match(editor, /models\.value = p\.models \?\? \[\]/, "打开详情时没带出已有模型");
  assert.match(editor, /models\.value = view\?\.models \?\? \[\]/, "保存并刷新后没更新模型列表");
  for (const label of ["模型", "上下文"]) {
    assert.match(editor, new RegExp(`<th>${label}</th>`), `模型清单缺「${label}」列`);
  }
  assert.match(editor, /class="model-upstream"[^>]*>\{\{ m\.upstream_id \}\}/, "模型清单没显示上游 ID");
  assert.match(editor, /v-for="m in models"/, "模型清单没有遍历 models");
  assert.ok(/<h3>模型（\{\{ modelCount \}\}）<\/h3>/.test(editor), "详情里没有模型数量标题");
  assert.ok(!/v-for="m in models"[\s\S]{0,400}@click/.test(editor), "模型清单不该能点开编辑");
  assert.ok(!/v-for="m in models"[\s\S]{0,400}<button/.test(editor), "模型清单不该有操作按钮");
});

test("渠道编辑器：模型筛选说清逗号与「包含任意一个」", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  // **HTML 注释与 JS 注释都要剥**：设计意图写在注释里是对的（"命中任意一条就算中"），
  // 但这条测试问的是**界面上**有没有那么说。不剥 JS 注释的话，注释一句话就能让它假通过。
  const code = editor
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/^\s*\/\/.*$/gm, "");

  // 措辞不能再是"含此子串"（单条）—— 那正是要改掉的旧口径
  assert.ok(!/只导入 id 含此子串的模型/.test(code), "筛选器的标签还是单条子串口径");
  // 必须提到：半角逗号分隔 + 任意一个 + 一个含逗号的例子
  assert.match(code, /半角逗号/, "没说是半角逗号分隔");
  assert.match(code, /任意一个/, "没说清是“包含任意一个就导入”");
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

// 页签条与 SETTINGS_TABS 是同一份；会话/渠道的 panel 还在，但**不再住设置 tab** ——
// 它们由左栏底部的独立入口用 `:only` 打开。这条同时钉住两头，防止"只删 tab、
// panel 变成永远进不去的死页"或"又偷偷塞回设置里"。
test("设置页签：六区 + 会话/渠道/智能体/更新独立入口", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  // 渠道页内部还有「渠道列表 / 全局模型规则」两个二级页签（list/global），
  // 它们不在设置主栏里；这里只取真正的设置 panel。
  const panels = [...settings.matchAll(/<VTabsWindowItem value="([a-z]+)"/g)]
    .map((m) => m[1]).filter((id) => id !== "list" && id !== "global");
  // 七个 panel 都在真正的 tabpanel 里（session/provider/update 由独立入口打开）。
  assert.deepEqual([...panels].sort(), ["agent", "appearance", "chat", "prompt", "provider", "session", "tavern", "update"],
    `panel 不齐：${panels.join(", ")}`);
  // 键盘焦点即激活（VSlideGroup 的 ArrowLeft/Right 只搬焦点，@focus 补上 WAI-ARIA 自动激活）
  assert.match(settings, /@focus="tab = t\.id"/, "设置 VTabs 没在焦点时激活");
  // 设置主栏六个 tab：酒馆负责 ST 导入/转换，更新在最后。
  assert.deepEqual(SETTINGS_TABS.map((t) => t.id), ["chat", "agent", "prompt", "appearance", "tavern", "update"],
    "SETTINGS_TABS 少/多了区，或顺序变了");
  // 独立入口：左栏底会话 / 渠道 / 智能体 + App 用 :only 打开同一个视图；更新走品牌头。
  assert.match(bar, /@click="\$emit\('open-sessions'\)"/, "左栏没有「会话」入口");
  assert.match(bar, /@click="\$emit\('open-providers'\)"/, "左栏没有「渠道」入口");
  assert.match(bar, /@click="\$emit\('open-agent'\)"/, "左栏没有「助手」入口");
  assert.match(bar, /\bid="agents-manage"[\s\S]*?\n\s*助手\n/, "底部标签该是「助手」");
  assert.ok(!bar.includes('id="update-open"'), "左栏底部还在画「更新」入口（它已挪到品牌头与设置 tab）");
  assert.match(settings, /only\??:\s*string/, "SettingsModal 没有独立入口的 only 参数");
  assert.match(settings, /<h2 v-else class="settings-title">/, "独立入口没有页头标题");
  assert.match(settings, /<VTabs[\s\S]*v-if="!only"/, "独立入口仍渲染设置 VTabs");
});

// 「新会话默认值」那两格从前是**手打 id 的文本框**，说明文自己都写着
// 「填错不会报错，只会在发送时回落成『未配置模型』」—— 把校验推给用户。
// 改成下拉之后，能选出来的都是真存在的；但当前值即使不在列表里也必须保住（见下一条）。
test("设置：新会话默认值的渠道/模型是下拉，不是手打 id", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /providerOpts/, "渠道那格没走 providerChoices");
  assert.match(settings, /modelOpts/, "模型那格没走 modelChoicesFor");
  assert.match(settings, /@update:model-value="onDefaultProviderChange"/,
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

  // 别名优先显示，但选项值仍是 id（默认值保存 / 发消息都用它）。
  const aliased = providerChoices([{ id: "opencode-go-2", alias: "OpenCode GO", vendor_name: "OpenCode Go" }]);
  assert.equal(aliased[0].label, "OpenCode GO");
  assert.equal(aliased[0].value, "opencode-go-2");
  // 空别名完全回落到旧行为（vendor_name（id））。
  assert.equal(providerChoices([{ id: "b", alias: "", vendor_name: "乙" }])[0].label, "乙（b）");

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
  const switchAt = top.indexOf('class="ws-switch workspace-tabs"');
  const ringAt = top.indexOf('id="ctx-ring"');
  const rightAt = top.indexOf('class="header-right"');
  assert.ok(switchAt > 0 && ringAt > 0 && rightAt > 0, "找不到 ws-switch / ctx-ring / header-right");
  assert.ok(rightAt < switchAt, "两段开关该在 .header-right 里（与环同一簇）");
  assert.ok(switchAt < ringAt, "开关该在绿环**左边**（用户原话），现在顺序反了");

  // ② 顶栏不在切换范围内：聊天 / 上下文住在 VTabsWindow 里；顶栏不许有 v-show/v-if
  assert.match(app, /<VTabsWindow v-model="workspace" class="workspace-window"/, "工作区没有被 VTabsWindow 接住");
  assert.match(app, /<VTabsWindowItem value="chat">[\s\S]*?<main id="messages-vue">/, "聊天页不在 window item 里");
  assert.match(app, /<VTabsWindowItem value="context">[\s\S]*?<ContextWorkspace/, "上下文页不在 window item 里");
  assert.ok(!/<main id="messages-vue" v-show/.test(app), "消息列还在用 v-show");
  assert.ok(!/<Composer\s+v-show/.test(app), "输入行还在用 v-show");
  assert.ok(!/<ContextWorkspace\s+v-show/.test(app), "上下文还在用 v-show");
  // 顶栏那行不许出现 workspace 的 v-show/v-if（切掉它 = 再也切不回来）
  const topbarTag = app.match(/<TopBar[\s\S]*?\/>/)?.[0] ?? "";
  assert.ok(!/v-show=|v-if=/.test(topbarTag), "顶栏被绑上了显示条件 —— 切到上下文就再也切不回来");

  // ③ 不落盘
  assert.ok(!/mc_workspace/.test(app), "工作区状态进了 localStorage —— 下次打开会落在空白工作区");
});

// 2026-10-29 第二轮（用户：「要不我们把右侧栏那个上下文给挪过来？」）：
test("渠道最大并发：编辑器可设，0 = 不限，高级压缩会与它取 min", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  assert.match(editor, /maxConcurrency/, "渠道编辑器没有最大并发状态");
  assert.match(editor, /max_concurrency/, "保存体里没有 max_concurrency");
  assert.match(editor, /最大并发（0 = 不限）/, "没有最大并发输入项");
  assert.match(api, /providerPresets/, "API 客户端丢了预设入口");
});

test("分段压缩 / 补齐空档：第四、第五 tab，预览 → 并发运行 → 整体状态", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  const ws = readFileSync(join(SRC, "components", "ContextWorkspace.vue"), "utf8");
  const adv = readFileSync(join(SRC, "components", "AdvancedCompact.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  // ① 三个子 tab 住在「压缩」这一页里（2026-11-09 合并），不是顶层 tab。
  assert.match(bar, /class="compact-subtabs"/, "压缩页没有子 tab");
  for (const [label, id] of [["预览", "preview"], ["分段压缩", "advanced"], ["补齐空档", "repair"]]) {
    assert.ok(bar.includes(`value="${id}">${label}</VTab>`), `缺少「${label}」子 tab`);
    assert.match(bar, new RegExp(`<VTabsWindowItem value="${id}">`), `「${label}」没有自己的 tabpanel`);
  }
  assert.ok(bar.indexOf('class="compact-subtabs"') < bar.indexOf('VTabsWindowItem value="advanced"'), "子 tab 不在压缩页里");
  assert.match(bar, /<AdvancedCompact :session-id="sessionId"/, "分段压缩没拿到会话 id");
  assert.match(bar, /<AdvancedCompact :session-id="sessionId" repair/, "补齐空档没打开 repair");

  // ①b 界面说人话：不要把 chunk/block/空洞/作业/idx 这些内部词摆给用户。
  const advTpl = adv.slice(adv.indexOf("<template>"));
  for (const word of ["空洞", "作业", "begin_idx", "end_idx"]) {
    assert.ok(!advTpl.includes(word), `分段压缩界面还露着内部词：${word}`);
  }
  assert.ok(bar.includes(">分段压缩</VTab>") && bar.includes(">补齐空档</VTab>"), "两个 tab 没改成说人话的名字");

  // ② 参数 / 预览 / 运行 / 取消。
  for (const field of ["begin_idx", "end_idx", "step_blocks", "concurrency", "max_attempts"]) {
    assert.ok(adv.includes(field), `分段压缩缺参数 ${field}`);
  }
  assert.match(adv, /advancedCompactPreview/, "没有预览请求");
  assert.match(adv, /advancedCompact\(/, "没有运行请求");
  assert.match(adv, /advancedCompactStatus/, "没有作业状态轮询");
  assert.match(adv, /advancedCompactCancel/, "没有取消");
  assert.match(adv, /preview\.holes/, "预览没显示未覆盖区间");
  assert.match(adv, /emit\("done"\)/, "完成后没通知外层刷新");

  // ③ API 与 App 接线。
  assert.match(api, /advancedCompactPreview:/, "API 客户端缺 advancedCompactPreview");
  assert.match(api, /advancedCompact:/, "API 客户端缺 advancedCompact");
  assert.match(api, /advancedCompactStatus:/, "API 客户端缺 advancedCompactStatus");
  assert.match(api, /advancedCompactCancel:/, "API 客户端缺 advancedCompactCancel");
  assert.match(app, /function onAdvancedDone\(\)/, "App 没有分段压缩完成后的刷新");
  assert.match(app, /@advanced-done="onAdvancedDone"/, "App 没接 advanced-done");
  assert.match(ws, /@advanced-done=/, "工作区没透传 advanced-done");

  // ④ 并发数 / 单块尝试是参数，不是写死；tab 可横向滚，别把 5 个挤断。
  assert.match(css, /\.context-tabs \{/, "5 个 tab 没有 Vuetify 容器");
  assert.match(adv, /concurrency/, "没有并发参数");
  assert.match(adv, /max_attempts/, "没有单块尝试参数");
});

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

  // ② 三条 tab 一个不少（真 tabpanel，不是 v-show）
  assert.match(bar, /<VTabsWindow v-model="tab" class="context-window"/, "上下文没有 VTabsWindow");
  for (const t of ["payload", "state", "tree"]) {
    assert.match(bar, new RegExp(`<VTabsWindowItem value="${t}">`), `面板里少了 ${t} 那一页`);
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
  const app = readFileSync(join(SRC, "composables", "useOverlayLayout.ts"), "utf8");
  const fn = app.match(/function liftOverlays\(\)[\s\S]*?\n  \}/)?.[0] ?? "";
  assert.ok(fn, "找不到 liftOverlays");
  assert.match(fn, /if \(h <= 0\) return;/, "量到 0 时没提前返回 —— 隐藏态会把 --composer-h 写成 0");
  // 守卫必须在**真正的写入**之前（别用 indexOf("--composer-h")：注释里先提到它，
  // 那样写出来的断言会在守卫挪到写入之后时依然通过 —— 断言自己把自己骗了）。
  const guardAt = fn.indexOf("if (h <= 0) return;");
  const writeAt = fn.indexOf('setProperty("--composer-h"');
  assert.ok(writeAt > 0, "找不到写 --composer-h 的那句");
  assert.ok(guardAt > 0 && guardAt < writeAt, "守卫该在写 --composer-h 之前");
});

// 面板是 v-show 常驻（tab 状态要留着），所以"看着哪一页"这件事 App 只在**切 tab 那一下**知道。
// 2026-11-02 起不再是"一律补拉树"：改由 `refreshContextLazy()` 按当前 tab 只拉那一页
//（载荷 / 状态 / 压缩树）—— 换会话时无条件的 outgoing + state 是远端部署的主要延迟来源。
test("上下文工作区：切进去 / 换会话时按当前 tab 补拉", () => {
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const side = readFileSync(join(SRC, "composables", "useSidePanels.ts"), "utf8");
  const fn = side.match(/function selectWorkspace\(next[^)]*\)[\s\S]*?\n  \}/)?.[0] ?? "";
  assert.ok(fn, "找不到 selectWorkspace");
  assert.match(fn, /workspace\.value === "context"\) onWorkspaceContext\(\)/,
    "切进工作区时没补拉当前 tab");
  assert.match(app, /onWorkspaceContext:\s*refreshContextLazy/, "App 没把 refreshContextLazy 接给工作区");
  // 懒加载的分派只此一处：三种 tab 各管各的。
  const lazy = app.match(/function refreshContextLazy\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(lazy, "找不到 refreshContextLazy");
  assert.match(lazy, /payloadTab === "tree"\) refreshTree\(\)/, "压缩 tab 没拉树");
  assert.match(lazy, /payloadTab === "state"\) refreshStateView\(sessionID\)/, "状态 tab 没拉状态");
  assert.match(lazy, /else if \(payloadTab === "payload"\) refreshHistoryPreview\(\)/, "载荷 tab 没拉历史预演");
  const hp = app.match(/async function refreshHistoryPreview\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(hp, "找不到 refreshHistoryPreview");
  assert.ok(!/previewOn\(\)/.test(hp), "载荷 tab 仍受输入预览开关影响（打开载荷就该拉）");

  // 换会话那条：enterSession 落地之后也要按当前 tab 补拉。
  // ⚠ 从前这里用 `summaryMsgs.clear()` 当锚点 —— 它先命中的是 `startDraft`，等于一直在测草稿，
  //    enterSession 根本没被钉住（2026-11-02 才发现）。直接取函数体。
  const enter = app.match(/async function enterSession\(id, preloaded = null\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(enter, "找不到 enterSession");
  assert.match(enter, /refreshContextLazy\(\)/, "换会话时没按当前 tab 补拉 —— 会一直显示上一条会话的账");
  const draft = app.match(/async function startDraft\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(draft, "找不到 startDraft");
  assert.match(draft, /refreshContextLazy\(\)/, "回草稿时没按当前 tab 补拉");
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
test("工作区开关：VTabs，尺寸仍在顶栏内", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(top, /<VTabs[\s\S]*?class="ws-switch workspace-tabs"/, "工作区开关不是 VTabs");
  assert.match(top, /<VTab value="chat"[^>]*>聊天<\/VTab>/, "缺「聊天」");
  assert.match(top, /<VTab value="context"[^>]*>上下文<\/VTab>/, "缺「上下文」");
  assert.match(css, /#chat-col > header \.workspace-tabs \.v-tab \{[^}]*height:\s*2\.1em/, "工作区 tab 没有高度");
  assert.match(css, /#chat-col > header \.workspace-tabs \.v-tab \{[^}]*font-size:\s*0\.875em/, "工作区 tab 字号丢了");
  assert.match(css, /#chat-col > header \.workspace-tabs \.v-tab\.v-tab--selected/, "工作区选中 tab 没有 primary 色");
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
  assert.match(tpl, /class="collapse-all"/, "没有「折叠全部」按钮");
  const toolbar = tpl.slice(tpl.indexOf('class="pv-toolbar"'), tpl.indexOf('id="payload-viz"'));
  assert.ok(toolbar.includes("collapse-all"), "「折叠全部」没放在载荷页的工具栏里");
  assert.match(tpl, /@click="collapseAll"/, "按钮没接 collapseAll");

  // ② 折叠的实现：**每一条都显式记成 false**（旧默认是按块长，短块会弹开；现在默认本就全收）。
  const fn = script.match(/function collapseAll\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(fn, "找不到 collapseAll");
  assert.match(fn, /set\(i, false\)|set\([^,]+,\s*false\)/, "折叠时没把每条显式设成 false —— 短块会自己弹开");

  // ③ **没有展开全部**（用户明确不要）。这条防的是"顺手补个对称按钮"。
  assert.ok(!/展开全部|expandAll|expand-all/i.test(tpl), "多出了「展开全部」—— 用户明确不要（折叠是收拾，展开是倾泻）");
  assert.ok(!/function expandAll/.test(script), "脚本里冒出了 expandAll");

  // ④ 全收起来时禁用（别给一个点了没反应的样子）
  assert.match(tpl, /:disabled="allCollapsed"/, "全折叠后按钮没禁用（会变成点不动的死按钮）");

  // ⑤ 平铺样式：这个按钮也不许长成"盒子"。
  //    2026-11-02 起底色/边框收进共享的「幽灵按钮底座」（.icon-btn/.edit-btn…），
  //    所以这里同时钉两件事：底座确实去底去边框；`.collapse-all` 自己**没有**再上一套。
  const rule = css.match(/(?:^|\n)\.collapse-all\s*\{([^}]*)\}/)?.[1] ?? "";
  const base = css.match(/\.icon-btn,\s*\.link-btn,\s*\.cp-close,[\s\S]*?\{([^}]*)\}/)?.[1] ?? "";
  assert.ok(rule, "找不到 .collapse-all 样式");
  assert.ok(base, "找不到幽灵按钮底座（background:none/border:none 应该只有一处）");
  assert.match(base, /background:\s*none/, "幽灵按钮底座没有去底色");
  assert.match(base, /border:\s*none/, "幽灵按钮底座没有去边框");
  assert.ok(!/background:/.test(rule), "折叠按钮自己又上了底色（应复用底座）");
  assert.ok(!/border:/.test(rule), "折叠按钮自己又描了边（应复用底座）");
  assert.match(css, /\.collapse-all:disabled/, "没给禁用态样式 —— 禁用后看起来还能点");
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

// 压缩树每行要带一段原文预览（2026-10-29 用户定：「条目不是简单的 #3 你，而是
// #3 你 XXXXX…（XXXXX 为你原始输入文本的前 n 个字符）」，为了能一眼定位；摘要块同理）。
test("压缩树：每行带原文预览（剥状态块 / 压平空白 / 截断加省略号）", () => {
  const msgs = [
    // ① 开头就是 <state> 块 —— 不剥的话每行预览都是 "state …"，等于定位不到（RP 消息很常见）
    { id: "m1", idx: 1, role: "user", summary_id: "s1",
      content: "<state 玩家状态>\nHP = 10\n</state>\n你推开门，门轴发出一声长响。" },
    // ② 只有状态块、没有正文 ⇒ 预览为空（不能退化成 "state HP = 10"）
    { id: "m2", idx: 2, role: "assistant", summary_id: "s1",
      content: "<state>\nHP = 10\n</state>" },
    // ③ 换行/连续空格要压平（单行预览），首尾空白要 trim
    { id: "m3", idx: 3, role: "user", content: "  \n\n  我靠近   那盏灯。  \n" },
    // ④ 超长要截断并加 `…`
    { id: "m4", idx: 4, role: "assistant", content: "灯".repeat(100) },
  ];
  const s1 = { id: "s1", parent_summary_id: null, type: "message", begin_message_id: "m1",
    end_message_id: "m2", blocks: 1, tokens: 40, dirty: false,
    text: "玩家推开门进入门厅，走廊尽头有一盏亮着的灯。" };
  const tree = buildCompactTree(msgs, [s1]);
  const [summary, leaf3, leaf4] = tree;

  // 摘要块：预览取**摘要自己**的正文（那一坨被压成的东西），不是原文
  assert.equal(summary.kind, "summary");
  assert.equal(summary.preview, "玩家推开门进入门厅，走廊尽头有一盏亮着的灯。");
  const kids = summary.children;
  assert.equal(kids[0].preview, "你推开门，门轴发出一声长响。", "状态块没剥掉 —— 预览会变成 'state …'");
  assert.equal(kids[1].preview, "", "只有状态块的消息，预览该是空（别把 state 内容当正文）");

  // 空白压平
  assert.equal(leaf3.preview, "我靠近 那盏灯。", "空白没压平（换行/连续空格该并成一个）");

  // 截断：40 字 + `…`
  assert.equal([...leaf4.preview].length, 41, "超长预览该是 40 字 + 省略号");
  assert.ok(leaf4.preview.endsWith("…"), "截断没加省略号");
});

// 预览的**唯一权威**：剥状态块必须复用 markdown.js 的 splitState，
// 不许在 tree.js 里另写一条正则 —— 两份实现一分岔，预览和气泡就对不上，而且没人知道该信谁。
test("压缩树：预览剥状态块复用 splitState（不另写正则）", () => {
  const tree = readFileSync(join(SRC, "utils", "tree.ts"), "utf8");
  assert.match(tree, /import\s*\{[^}]*splitState[^}]*\}\s*from\s*"\.\/markdown\.ts"/,
    "没复用 splitState —— 预览和气泡会各剥各的");
  // 不许出现第二条 state 正则（`state` 出现在正则里就说明自己另写了一套）
  const code = tree.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  assert.ok(!/<\/state|state\|current_state/.test(code), "tree.ts 里另写了一条状态块正则 —— 该用 splitState");
});

// 压缩页也要「折叠全部」（2026-10-29 用户：「在"压缩"里，也请准备一键折叠的按钮吧」）。
//
// 这一页有两堆能折的东西：**压缩树**（目录节点）与「看载荷」拉回来的**分块列表**。
// 一个写着"全部"的按钮只该有一个含义 ⇒ 两堆一起收，禁用条件也两堆一起算。
test("压缩：有一键折叠全部，树与分块一起收，且没有一键展开", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  const tree = readFileSync(join(SRC, "components", "CompactTree.vue"), "utf8");
  const tpl = bar.slice(bar.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");
  const script = bar.slice(0, bar.indexOf("</script>")).replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  // ① 压缩页有这个按钮，且按钮在 tree tab 里
  const treePane = tpl.slice(tpl.indexOf('id="compact-tree"'));
  assert.ok(treePane.includes('class="collapse-all"'), "压缩页没有「折叠全部」按钮");
  assert.match(treePane, /@click="collapseCompact"/, "按钮没接 collapseCompact");

  // ② 折叠状态提到了父组件：树变成**受控**的
  //    留在子组件里的话，父组件要么摸不进去、要么得靠 defineExpose 约定 —— 而且"全收了吗"
  //    这个判断会变成跨组件读取，禁用条件就没法算。
  assert.match(tree, /collapsed\??:\s*(?:Set<string>|\{[^}]*type:\s*Object)/, "CompactTree 的折叠状态没提成 prop");
  assert.match(tree, /emit\("update:collapsed"/, "CompactTree 没发 update:collapsed（父组件改不动它）");
  assert.match(tpl, /v-model:collapsed="treeCollapsed"/, "树没接上 v-model:collapsed");
  // ⚠ 发新 Set、不改传进来的那份（就地改 prop 等于绕过 update 约定）
  assert.match(tree, /new Set\(props\.collapsed\)/, "toggle 没基于 props 生成新 Set");

  // ③ 两堆一起收：树节点 + 压缩载荷分块
  const fn = script.match(/function collapseCompact\(\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(fn, "找不到 collapseCompact");
  assert.match(fn, /collapsibleSet\(tree\.value\)/, "没把有孩子的树节点加进折叠集合");
  assert.match(fn, /cpNext\.set\(i, false\)|set\(i,\s*false\)/,
    "没把压缩载荷的分块显式设成 false —— 短块会自己弹开（载荷页踩过）");
  // ④ 禁用条件两堆一起算
  const all = script.match(/const compactAllCollapsed = computed\(\(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? "";
  assert.ok(all, "找不到 compactAllCollapsed");
  assert.match(all, /collapsibleIds/, "禁用条件没算树");
  assert.match(all, /compactMessages/, "禁用条件没算压缩载荷那几块");

  // ⑤ 换树要**默认全部收起**（2026-11-03 用户定：压缩后的文本默认折叠，点开才看）
  const setTree = script.match(/function setTree\([\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(setTree, "找不到 setTree");
  assert.match(script, /function collapsibleSet\(nodes[^)]*\)/, "没有收集可折叠节点 id 的纯函数");
  assert.match(setTree, /treeCollapsed\.value = collapsibleSet\(tree\.value\)/, "换树没默认全收起");
  const treeTpl = tree.slice(tree.indexOf("<template>"));
  assert.match(treeTpl, /r\.n\.kind !== 'summary' \|\| !collapsed\.has\(r\.n\.id\)/,
    "折叠的摘要还显示压缩后的预览文本");

  // ⑥ 仍然**不许**有展开全部
  assert.ok(!/展开全部|expandAll/.test(tpl), "压缩页也冒出了「展开全部」—— 用户只说要折叠");

  // ⑦ 两处折叠按钮共用同一个类（各写一套必分岔）
  assert.equal((tpl.match(/class="collapse-all"/g) ?? []).length, 2, "两个折叠按钮没用同一个类");
  assert.ok(!/pv-collapse-all|ctree-collapse-all/.test(bar + css), "还有按钮在用自己的旧类名 —— 样式会分岔");
});

// 三个 tab 的字号（2026-10-29 用户：「载荷/状态/压缩 三个字至少要大一点吧」）。
//
// ⚠ 这里有个**叠乘陷阱**：`.ws-panel` 自己 `font-size: 0.92em`，tab 的 em 是**相对它**的，
// 所以 "tab 是 0.95em" 落到根上只有 0.87em —— 比正文还小。光看声明数字（0.95 接近 1）
// 完全看不出问题，必须乘一遍。所以这条按实际声明**算有效字号**，判据也很朴素：
// **tab 不该比它标注的内容更小**（≥ 面板正文的 1em）。
test("上下文面板：三个 tab 不小于正文", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => css.match(new RegExp(`(?:^|\\n)${sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
  const em = (text, name) => {
    const m = text.match(new RegExp(`${name}:\\s*([\\d.]+)em`));
    return m ? Number(m[1]) : NaN;
  };

  const panel = em(rule(".ws-panel"), "font-size");
  const tab = em(rule(".context-tabs .v-tab"), "font-size");
  assert.ok(Number.isFinite(panel) && Number.isFinite(tab), "读不出 .ws-panel / .context-tabs .v-tab 的 font-size");

  const effective = panel * tab; // 落到根上的有效字号
  assert.ok(effective >= 0.98,
    `tab 有效字号只有 ${effective.toFixed(2)}em（${tab}em × 面板 ${panel}em）—— 比正文还小，看着像附注`);
  // 也别大得离谱（把 tab 做得比正文大两档就是另一种失衡）
  assert.ok(effective <= 1.35, `tab 有效字号 ${effective.toFixed(2)}em 太大了 —— 这排字是导航，不该压过内容`);
  // 点击区域也要够（纵向 padding 撑高度，别只靠字号）
  const pad = em(rule(".context-tabs .v-tab"), "padding");
  assert.ok(pad >= 0.3, `tab 纵向 padding 只有 ${pad}em —— 触摸目标太小`);
});

// 撤销压缩（2026-11-01 用户定：「增加一个'删除'图标的按钮，悬停会显示为'撤销'」）。
//
// 合法性只有一份（后端 `compact.PlanUndo`：顶层都可撤；更早的会级联同层更晚的），
// 界面**只读不算** —— 这里钉的是"数据搬对了、两处都接上了、不能撤时不是死按钮"。
test("撤销压缩：树与摘要卡两处按钮，只按后端给的 undoable 亮", () => {
  const tree = readFileSync(join(SRC, "components", "CompactTree.vue"), "utf8");
  const card = readFileSync(join(SRC, "components", "SummaryCard.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  const ws = readFileSync(join(SRC, "components", "ContextWorkspace.vue"), "utf8");
  const app = readFileSync(join(SRC, "..", "src", "App.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const treeTpl = tree.slice(tree.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");
  const cardTpl = card.slice(card.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");

  // ① 接口：DELETE 到 summaries/{id}
  assert.match(api, /undoSummary:\s*\(sid[^)]*\)\s*=>\s*call<[^>]+>\("DELETE", `\/sessions\/\$\{enc\(sid\)\}\/summaries\/\$\{enc\(sumid\)\}`/,
    "API 客户端没有 undoSummary（或方法/路径不对）");

  // ② 树行：垃圾桶图标 + 悬停"撤销" + 禁用系在 undoable 上
  assert.match(treeTpl, /class="ctree-undo"/, "压缩树行没有撤销按钮");
  assert.match(treeTpl, /:disabled="!r\.n\.undoable"/, "树上的撤销按钮没有系在 undoable 上（会变成能点但失败）");
  assert.match(treeTpl, /撤销这次压缩/, "树上的撤销按钮悬停没写「撤销」");
  assert.ok(treeTpl.includes("ICONS.trash"), "树上的撤销按钮没用垃圾桶图标");
  // 只有摘要行才有 —— 消息行（叶子）不可撤，别长出一颗灰按钮
  assert.match(treeTpl, /v-if="r\.n\.kind === 'summary'"/, "树上的撤销按钮没有限定在摘要行");

  // ③ 摘要卡：与刷新/编辑并排
  assert.match(cardTpl, /aria-label="撤销这次压缩"/, "摘要卡没有撤销按钮");
  assert.match(cardTpl, /:disabled="!summary\.undoable/, "摘要卡的撤销按钮没系在 summary.undoable 上");
  assert.match(cardTpl, /撤销这次压缩/, "摘要卡的撤销按钮悬停没写「撤销」");
  assert.ok(cardTpl.includes("ICONS.trash"), "摘要卡的撤销按钮没用垃圾桶图标");

  // ④ 一路往上报：树 → 面板 → 工作区 → App（少一环就是点了没反应）
  assert.match(tree, /defineEmits(?:\(\[[^\]]*"undo"|<\{[^}]*undo:)/, "CompactTree 没声明 undo 事件");
  assert.match(bar, /@undo="\(id\) => emit\('undo', id\)"/, "面板没把树的 undo 往上报");
  assert.match(bar, /defineEmits(?:\(\[[^\]]*"undo"|<\{[^}]*undo:)/, "面板没声明 undo");
  assert.match(ws, /@undo="\(id\) => emit\('undo', id\)"/, "工作区没把 undo 往上报");
  assert.match(ws, /defineEmits(?:\(\[[^\]]*"undo"|<\{[^}]*undo:)/, "工作区没声明 undo");
  // App：两处都接上（摘要卡给自己那条 id；工作区把 id 透传给同一个处理函数）
  assert.match(app, /@undo="undoSummary\(row\.summary\.id\)"/, "App 没接摘要卡的 undo");
  assert.match(app, /@undo="undoSummary"/, "App 没接工作区的 undo");
  assert.match(app, /async function undoSummary\(summaryID\)/, "App 没有 undoSummary");
  // 撤销会让装配**变长**（那段回到原文）⇒ 三个视图都要重算，少一个就是数字不更新
  const fn = app.match(/async function undoSummary\(summaryID\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(fn, /reloadWindow\(\)/, "撤销后没重拉消息列（装配变长了）");
  assert.match(fn, /refreshStateView\(/, "撤销后没刷新状态视图");
  assert.match(fn, /refreshStatusline\(/, "撤销后没刷新上下文环");
  // 失败要把后端那句话原样给人看
  assert.match(fn, /err\.message/, "撤销失败时没显示出后端给的说明");
  // 更早的压缩要先预览、带级联警告，再删
  assert.match(fn, /api\.undoPreview\(/, "撤销前没有拿级联预览");
  assert.match(fn, /confirmAsk\(/, "级联撤销没弹警告");
  assert.match(fn, /preview\.count > 1/, "没有按级联数量决定是否警告");
  assert.match(fn, /同时撤销同层更晚/, "警告没解释会连带撤销什么");

  // ⑤ 禁用态必须显式压暗（否则是个"看起来能点"的死按钮）
  assert.match(css, /\.ctree-undo:disabled/, "树上撤销按钮没给禁用样式");
  assert.match(css, /\.ctree-undo:disabled\s*\{[^}]*opacity/, "禁用态没压暗");
});

// 数据层：`buildCompactTree` 要把后端的 undoable/level 搬到树上（界面不自己判）。
test("撤销压缩：树节点原样搬后端的 undoable / level", () => {
  const msgs = [
    { id: "m1", idx: 1, role: "user", content: "a".repeat(10), summary_id: "s1" },
    { id: "m2", idx: 2, role: "assistant", content: "b".repeat(10), summary_id: "s1" },
    { id: "m3", idx: 3, role: "user", content: "c" },
  ];
  // 后端算了：s1 在第 2 层、可撤。
  const s1 = { id: "s1", parent_summary_id: null, type: "summary", begin_message_id: "m1",
    end_message_id: "m2", blocks: 1, tokens: 9, dirty: false, text: "梗概", level: 2, undoable: true };
  const tree = buildCompactTree(msgs, [s1]);
  const top = tree[0];
  assert.equal(top.kind, "summary");
  assert.equal(top.undoable, true, "undoable 没搬到树上");
  assert.equal(top.level, 2, "level 没搬到树上");

  // 后端没给（缺席）⇒ 不能撤。这是唯一口径：`!!s.undoable`。
  s1.undoable = undefined;
  const tree2 = buildCompactTree(msgs, [s1]);
  assert.equal(tree2[0].undoable, false, "undoable 缺席时该按不可撤处理");

  // 消息节点没有撤销这回事。
  const leaf = tree2.find((n) => n.kind === "message");
  assert.ok(leaf, "该有一条消息叶子");
  assert.ok(!leaf.undoable, "消息节点不该带 undoable");
});

// 创建副本 / 导出 JSON 2026-11-04 从会话设置面板挪到左栏「选项」浮板。
// 会话设置面板只留设置本身；浮板承担四条动作：设置 / 副本 / 导出 / 删除。
test("左栏浮板：创建副本与导出 JSON 都在这里，会话设置面板不再有它们", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const panel = readFileSync(join(SRC, "components", "SessionSettings.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  const tpl = panel.slice(panel.indexOf("<template>")).replace(/<!--[\s\S]*?-->/g, "");

  // ① 浮板四项，顺序就是用户说的顺序。
  const items = ["会话设置", "创建副本", "导出 JSON", "删除会话"];
  let last = -1;
  for (const label of items) {
    const at = bar.indexOf(`>${label}<`);
    assert.ok(at > last, `浮板缺「${label}」或顺序不对`);
    last = at;
  }
  assert.match(bar, /@click="pickCopy"/, "浮板没有复制动作");
  assert.match(bar, /@click="pickExport"/, "浮板没有导出动作");

  // ② 接口与 App 接线。
  assert.match(api, /copySession:\s*\(id[^)]*\)\s*=>\s*call<[^>]+>\("POST", `\/sessions\/\$\{enc\(id\)\}\/copy`\)/,
    "API 客户端没有 copySession（或路径不对）");
  assert.match(app, /@copy="copySessionFromBar"/, "App 没接左栏 copy");
  assert.match(app, /@export="exportSessionFromBar"/, "App 没接左栏 export");
  assert.match(app, /function copySessionFromBar\(id\)/, "App 没有 copySessionFromBar");
  assert.match(app, /function exportSessionFromBar\(id\)/, "App 没有 exportSessionFromBar");
  assert.match(app, /downloadJSON\(doc,/, "导出没有下载 JSON");
  assert.match(app, /function afterCopy\(fresh\)/, "App 没有 afterCopy");
  assert.match(app, /enterSession\(fresh\.id\)/, "复制完之后没进副本");
  assert.match(app, /refreshSessionsBar\(\)/, "复制完之后没刷左栏（副本不出现）");

  // ③ 面板里移除：创建副本那一节、导出按钮、copy 事件都不许留。
  assert.ok(!tpl.includes("<h3>创建副本</h3>"), "会话设置面板里还有创建副本");
  assert.ok(!tpl.includes("exportJSON"), "会话设置面板里还有导出按钮");
  assert.ok(!tpl.includes("导出对话"), "会话设置面板里还有导出按钮");
  assert.ok(!/emit\("copy"/.test(panel), "SessionSettings 还留着 copy 事件");
  assert.ok(!/defineEmits\(\[[^\]]*"copy"/.test(panel), "SessionSettings 还声明 copy 事件");
});

// 2026-11-01 的两个删除：死接口与死字段不许长回来。
test("过时的东西不回来：outgoing 墓碑、debug/last-payload、config.version、telegram", () => {
  const server = readFileSync(join(SRC, "..", "..", "..", "core", "internal", "server", "server.go"), "utf8");
  const cfg = readFileSync(join(SRC, "..", "..", "..", "core", "internal", "config", "config.go"), "utf8");

  // ① `GET /sessions/{id}/outgoing` 是 410 墓碑（真请求问 POST 那条），删了
  assert.ok(!server.includes('GET /api/v1/sessions/{session_id}/outgoing'), "outgoing 的 410 墓碑接口还在");
  // ② `GET /debug/last-payload` 前端与 TUI 都没有入口，删了
  assert.ok(!server.includes("/debug/last-payload"), "debug/last-payload 还在（没有调用方）");
  // ③ `telegram` 那一段（TG bot 随 TUI 一起砍）
  assert.ok(!server.includes("/config/telegram"), "TG 配置接口还在");
  assert.ok(!cfg.includes("TelegramConfig"), "config.go 里还有 TelegramConfig");
  // ④ `version` 从没人读过
  assert.ok(!/\bVersion\s+int\s+`json:"version"`/.test(cfg), "config.go 里还有那个没人读的 version 字段");
});

// 设置 → 外观改完，聊天壳上的活开关要立刻跟上。
//
// 2026-11-01：外观区从用户视图搬进设置时，`onPrefsSaved` 搬过来了、模板上的
// `@prefs-saved` 却漏了 —— 函数定义了没人调，命令菜单那颗 ☰ 要刷新页面才变。
// 这类"定义了但没人用"的错 SSR 冒烟抓不到（它不报错），只能正面钉住。
test("设置 → 外观改完，聊天壳上的活开关跟手（@prefs-saved 接上）", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const modal = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const pane = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");

  assert.match(pane, /emit\("prefs-saved"\)/, "AppearancePane 改完没发 prefs-saved");
  assert.match(modal, /<AppearancePane[^>]*@prefs-saved=/, "SettingsModal 没把 AppearancePane 的 prefs-saved 转发出来");
  assert.match(app, /<SettingsModal[\s\S]*?@prefs-saved="onPrefsSaved"/,
    "App 没接 SettingsModal 的 prefs-saved（改外观后聊天壳不跟手）");
  assert.match(app, /function onPrefsSaved\(\)/, "App 没有 onPrefsSaved");
  assert.match(app, /cmdMenu\.value = commandMenuOn\(\)/, "onPrefsSaved 没把命令菜单那颗 ☰ 换成新偏好");
});

// 生成轮询不许每拍刷全量（2026-11-01）。
//
// 从前 300ms 的循环每拍两次额外请求：GET /sessions（全量列表）+ GET /context
// （O(n) 全量装配：列消息 + 现演状态 + 拼出站）。一条 30 秒的回复 ≈ 200 个请求。
// 现在：相位每拍照推（纯本地），列表与占用等翻 idle 再刷。
test("生成轮询不刷全量：withContext:false + 列表只在收尾刷", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /function refreshStatusline\(phase, elapsedMs = null, \{ withContext = true \} = \{\}\)/,
    "refreshStatusline 没有 withContext 开关");

  const start = app.indexOf("function applyTurnStream");
  const end = app.indexOf('let connText = "未连接"', start);
  const loop = app.slice(start, end === -1 ? start + 4000 : end);
  assert.match(loop, /refreshStatusline\(st\.phase, st\.elapsed_ms, \{ withContext: false \}\)/,
    "轮询里还在按默认问 /context（每拍一次全量装配）");
  const calls = (loop.match(/refreshSessionsBar\(\)/g) || []).length;
  assert.ok(calls <= 1, `轮询里 refreshSessionsBar() 还在每拍刷（找到 ${calls} 处；只该在收尾那一次）`);
});

// ── markdown 逐类型开关（2026-11-01）────────────────────────────────
//
// 用户口径：「增加 markdown 渲染开关 …… 所有的会被 Markdown 解析的非 <div> HTML 块，
// 我们为每个类型都加个开关。不然瞎解析不好。」
//
// 这里测的是**行为**（渲染出来的 HTML），不是"界面上有那个开关" ——
// 后者只能说明画了个框，说明不了关掉之后到底还解不解析。
// allOff：所有类型都关（总开关仍开）—— "关掉每一类"的极端情形。
const allOff = { typeOn: () => false };

test("markdown 逐类型关掉时：原样显示（不是渲染成别的、也不是吃掉标记）", () => {
  const cases = [
    ["粗体", "**粗**", "**粗**"],
    ["斜体", "*斜*", "*斜*"],
    ["删除线", "~~删~~", "~~删~~"],
    ["标题", "# 标题", "# 标题"],
    ["引用", "> 引文", "> 引文"],
    ["分隔线", "---", "---"],
    ["行内代码", "用 `code` 吧", "用 `code` 吧"],
    ["链接", "[名](https://a.b)", "[名](https://a.b)"],
    ["图片", "![说明](https://a.b/x.png)", "![说明](https://a.b/x.png)"],
    ["自动链接", "看 https://a.b/c", "看 https://a.b/c"],
    ["下划线", "__划__", "__划__"],
  ];
  for (const [label, src, want] of cases) {
    const html = renderMarkdown(src, allOff);
    assert.ok(html.includes(want), `${label}：关掉后该原样显示 ${want}，得到 ${html}`);
  }
});

test("markdown 逐类型关掉时：块级语法不再变成元素、但换行留着", () => {
  const list = renderMarkdown("- a\n- b", allOff);
  assert.ok(!list.includes("<ul>") && !list.includes("<li>"), `列表还在解析：${list}`);
  assert.ok(list.includes("- a") && list.includes("- b"), list);
  assert.ok(list.includes("<br>"), "块级原文必须把换行留住（否则一坨挤成一行）");

  const table = renderMarkdown("| a | b |\n| - | - |\n| 1 | 2 |", allOff);
  assert.ok(!table.includes("<table>"), `表格还在解析：${table}`);
  assert.ok(table.includes("| a | b |"), table);

  const code = renderMarkdown("```js\nlet x = 1;\n```", allOff);
  assert.ok(!code.includes("<code") && !code.includes("<pre>"), `代码块还在解析：${code}`);
  assert.ok(code.includes("```js"), code);
});

test("markdown 逐类型关掉时：只关那一类，别的照常渲染", () => {
  const noBold = { typeOn: (id) => id !== "bold" };
  const html = renderMarkdown("**粗** 和 *斜*", noBold);
  assert.ok(html.includes("**粗**"), `粗体关了该原样：${html}`);
  assert.ok(html.includes("<em>斜</em>"), `斜体没关，该照常渲染：${html}`);

  const noTable = { typeOn: (id) => id !== "table" };
  const mixed = renderMarkdown("| a |\n| - |\n| 1 |\n\n**粗**", noTable);
  assert.ok(!mixed.includes("<table>"), `表格关着还在解析：${mixed}`);
  assert.ok(mixed.includes("<strong>粗</strong>"), `表格之外的粗体该照常：${mixed}`);
});

test("markdown 总开关关掉：整段原文 + 换行保留", () => {
  const html = renderMarkdown("**粗**\n# 标题", { enabled: false });
  assert.ok(!html.includes("<strong>"), html);
  assert.ok(!html.includes("<h1>"), html);
  assert.ok(html.includes("**粗**") && html.includes("# 标题"), html);
  assert.ok(html.includes("<br>"), "总开关关掉也要留住换行");
});

test("markdown 数学：关掉时**保留原始定界符**（$a$ 不许被改成 \\(a\\)）", () => {
  const noMath = { typeOn: (id) => id !== "katex" };
  // ⚠ 这条是踩出来的：`String.replace` 的替换串里 `$$` 是转义序列，
  // 最早那版把 `$$x^2$$` 悄悄吃成了 `$x^2$` —— 不报错，只是内容少了一半。
  assert.ok(renderMarkdown("$$x^2$$", noMath).includes("$$x^2$$"), "display 数学该原样");
  assert.ok(renderMarkdown("式中 $a+b$ 这样", noMath).includes("$a+b$"), "单 $ 该原样（别换成 \\(…\\)）");
  assert.ok(renderMarkdown(String.raw`式中 \(a+b\) 这样`, noMath).includes(String.raw`\(a+b\)`), "行内 \\(…\\) 该原样");
  // 开着的数学仍是 KaTeX（不是"关掉才不渲染"的反面漏了）。
  assert.ok(renderMarkdown("$$x^2$$").includes("katex"), "数学开着的该渲染成 KaTeX");
});

test("markdown 转义：关掉任何类型都不影响 XSS 那道门", () => {
  // 裸 HTML 从来就不解析：我们**先行转义** `&` 与 `<`。
  const html = renderMarkdown("<script>alert(1)</script>", allOff);
  assert.ok(!html.includes("<script>"), `转义被绕过了：${html}`);
  assert.ok(html.includes("&lt;script&gt;"), html);
  const html2 = renderMarkdown("<img src=x onerror=alert(1)>");
  assert.ok(!html2.includes("<img src=x"), `转义被绕过了：${html2}`);
});

// 前端渲染用的类型清单必须与后端**逐个对上**（多一个不触发、少一个点了没反应）。
test("工具状态：无/1/2/3，标题列出工具", () => {
  assert.equal(nativeToolsLabel([]), "工具：无");
  assert.equal(nativeToolsLabel(undefined), "工具：无");
  assert.equal(nativeToolsLabel(["x_search"]), "工具：1");
  assert.equal(nativeToolsLabel(["x_search", "web_search", "code_execution"]), "工具：3");
  assert.equal(nativeToolLabel("x_search"), "X 搜索");
  assert.equal(nativeToolLabel("unknown_tool"), "unknown_tool");
  assert.ok(nativeToolsTitle(["x_search", "web_search"]).includes("X 搜索"));
  assert.ok(nativeToolsTitle([]).includes("没有可用"));

  const tools = readFileSync(join(SRC, "components", "ToolsMenu.vue"), "utf8");
  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(tools, /tool-check/, "工具菜单没有勾选框");
  assert.match(tools, /emit\("toggle"/, "工具菜单没有切换事件");
  assert.match(composer, /nativeToolsDisabled/, "Composer 没接已关闭工具");
  assert.match(app, /toggleNativeTool/, "App 没有保存工具选择");
  assert.match(app, /native_tools_disabled/, "App 没读会话的工具开关");
  assert.match(app, /patchSession\(sessionID, \{ native_tools_disabled/, "工具选择没有 PATCH 到会话");
});

test("markdown 类型清单：前端的 FALLBACK 与后端 MarkdownTypes 对得上", () => {
  const go = readFileSync(join(SRC, "..", "..", "..", "core", "internal", "config", "prefs.go"), "utf8");
  const registry = go.slice(go.indexOf("var MarkdownTypes = []MarkdownType{")
    , go.indexOf("// markdownTypeIDs"));
  const ids = [...registry.matchAll(/\{ID:\s*"([\w]+)"/g)].map((m) => m[1]);
  assert.ok(ids.length >= 10, `后端类型清单看起来没解析出来：${ids}`);

  // `renderMarkdown` 认识的每一类（BLOCK_TYPES / INLINE_TYPES 的键 + 这几个特例）
  const js = readFileSync(join(SRC, "utils", "markdown.ts"), "utf8");
  const blockKeys = [...js.slice(js.indexOf("const BLOCK_TYPES"), js.indexOf("// 行内语法"))
    .matchAll(/^\s{2}(\w+):\s*"/gm)].map((m) => m[1]);
  const inlineKeys = [...js.slice(js.indexOf("const INLINE_TYPES"), js.indexOf("// buildMarked"))
    .matchAll(/^\s{2}(\w+):\s*"/gm)].map((m) => m[1]);
  const handled = new Set([...blockKeys, ...inlineKeys,
    "breaks", "katex", "underline", "link", "autolink", "mermaid"]);

  for (const id of ids) {
    assert.ok(handled.has(id), `后端有类型 ${id}，但 markdown.js 不认识它（那个开关点了没反应）`);
  }
});

// 顶栏阴影（2026-11-01 用户定：「顶部栏有阴影」）。
//
// 这条不是"好看一点"：顶栏是**不透明**的悬浮层，压在滚过去的消息上 ——
// 没有阴影时那条边是齐刷刷一刀切（像正文被裁掉了），有阴影才读成"栏在内容**上面**"。
// 也就是说，阴影在这儿替**边框**干活。所以同时钉住两件事：
//   ① 必须有阴影（而且要**向下**的 —— 顶栏的阴影往下落，不是往两边）；
//   ② 仍**不许描线**（2026-11-01 的另一条口径：「不透明要的是挡住，不是把栏画成一个盒子」）。
test("顶栏有向下阴影（替边框干活），且仍不描线", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, ""); // 判"有没有声明"要先剥注释

  // 只认**悬浮层那条**主规则（`position: absolute`）。不能按"第一个 #chat-col > header {"
  // 取 —— 2026-11-02 窄屏媒体查询里另有一条调 gap/padding 的同名规则会排在它前面。
  const headerRule = strip(css).match(/#chat-col > header \{(?=[^}]*position:\s*absolute)[^}]*\}/);
  assert.ok(headerRule, "找不到 #chat-col > header 那条规则");

  // ① 有阴影，且用的是 token（组件不许写死色值 —— theme.css 顶上那条规矩）。
  assert.match(headerRule[0], /box-shadow:\s*var\(--shadow-top\)/,
    "顶栏没有 box-shadow: var(--shadow-top)");

  // ② 真**向下**：offset-y 为正（负的就是往上，落在栏自己背后，看不见）。
  const token = strip(theme).match(/--shadow-top:\s*([^;]+);/);
  assert.ok(token, "theme.css 里没有 --shadow-top");
  const offsets = [...token[1].matchAll(/(-?[\d.]+)px\s+(-?[\d.]+)px/g)].map((m) => Number(m[2]));
  assert.ok(offsets.length >= 2, `--shadow-top 应该是两层（接触影 + 离开感），得到 ${offsets}`);
  for (const y of offsets) {
    assert.ok(y > 0, `--shadow-top 的第 ${y}px 不是向下 —— 顶栏的阴影要落在栏下面`);
  }

  // ③ 仍不描线。
  assert.ok(!/border(-bottom|-block-end)?:\s*(?!none)/.test(headerRule[0].replace(/border-radius[^;]*;/g, "")),
    "顶栏加回了边框 —— 那条口径是用阴影、不用线");
});

// ── 按停任务（2026-11-01 用户定）────────────────────────────────────
//
// 用户口径：「任务菜单里，我希望有办法取消任务。而且要考虑原子性。如果任务被取消了，
// 那么就相当于没做过。」
//
// 后端那半（原子性）由 `core/internal/server/task_cancel_test.go` 钉着 —— 它会真的
// 掐断一发上游、然后断言库里一条摘要都没多、重提交压的还是同一段。
// 这里只钉**前端这条路接上了没有**：按钮、事件、接口、以及"按不动的按钮不许画"。
test("任务面板：能停的任务有取消按钮，按不动的不画", () => {
  const bar = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");

  // ① 接口：POST /tasks/{id}/cancel
  assert.match(api, /cancelTask:\s*\(id[^)]*\)\s*=>\s*call<[^>]+>\("POST", `\/tasks\/\$\{enc\(id\)\}\/cancel`\)/,
    "API 客户端没有 cancelTask（或路径不对）");

  // ② 按钮：列表只含正在跑的任务，所以只看 `cancelable`。
  //    ⚠ 不能停的任务（refresh_models）按了没反应 —— 不给它画按钮。
  assert.match(bar, /v-if="t\.cancelable"[\s\S]{0,400}@click\.stop="emit\('cancel-task', t\.id\)"/,
    "任务行没有那颗取消按钮（或条件写漏了）");

  // ③ 事件一路接到 App（emit → 模板 → 处理函数 → 接口）。
  assert.match(bar, /defineEmits(?:\(\[[^\]]*"cancel-task"|<\{[^}]*"cancel-task")/, "TopBar 没声明 cancel-task 事件");
  assert.match(app, /@cancel-task="cancelTask"/, "App 没接 TopBar 的 cancel-task");
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /function cancelTask\(id[^)]*\)/, "App 没有 cancelTask");
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /api\.cancelTask\(id\)/, "cancelTask 没调接口");

  // ④ 被取消的压缩**不是失败也不是完成** —— 界面上得说清"一条都没写，重提交还是压同一段"。
  //    不加这一支，`canceled` 会掉进"压缩完成"那条分支（因为它非 running 非 error），
  //    于是按停之后弹一句"压缩完成" —— 那是假的。
  const poll = app.slice(app.indexOf("async function pollCompact"), app.indexOf("// —— 顶栏 ——"));
  assert.match(poll, /c\.state === "canceled"/, "压缩轮询没认 canceled 状态");
  assert.ok(poll.indexOf('"canceled"') < poll.indexOf("压缩完成"),
    "canceled 那一支必须排在压缩完成前面（否则被当成功）");
});

// ── 按停重摇之后，界面必须"退出来"（2026-11-01 用户报的 bug）──────────
//
// 用户原话：「当我取消重摇后（从小面板里），对话界面还是老样子，在等待。」
//
// 根因：两条轮询循环都只在**活着**的时候同步状态 ——
//
//     if (st.active && st.target_kind === "message") rerollState.value = st;
//
// 状态变成"没了"（取消 / 失败 / 只剩原文一版）时**一个字都不写**，`rerollState` 就停在
// 旧的 `running:true` 上 ⇒ `rerollTargetLive` 一直为真 ⇒ 气泡一直是空白 +"正在输出"，
// 直到下一次进会话才恢复。这正是"老样子，在等待"。
//
// 这类 bug 不报错、不误路由、构建也过 —— 只能按"状态是不是**无条件**写回"来钉。
test("重摇轮询：状态没了也要写回（否则取消后界面一直等）", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");

  for (const [fn, ref, kind] of [
    ["pollReroll", "rerollState", "message"],
    ["pollRerollSummary", "summaryReroll", "summary"],
  ]) {
    const start = app.indexOf(`async function ${fn}(sid)`);
    assert.ok(start > 0, `找不到 ${fn}`);
    const body = app.slice(start, app.indexOf("\n}", start));
    // 必须有"不活跃 ⇒ 显式写回 inactive"这条（三元那半边），而不是只有"活跃才写"。
    assert.match(body, new RegExp(`${ref}\\.value = st\\.active && st\\.target_kind === "${kind}" \\? st : \\{ active: false \\}`),
      `${fn} 在状态变成"没了"时没有写回 inactive —— 取消之后界面会一直等`);
    // 反面：**不许**再出现"活跃才写"那种写法（它正是这个 bug 的形状）。
    assert.ok(!new RegExp(`if \\(st\\.active && st\\.target_kind === "${kind}"\\) ${ref}\\.value = st;`).test(body),
      `${fn} 里还留着"活跃才写"的老写法`);
  }

  // 面板按停之后要顺手对齐两族状态（不一定有轮询在跑）。
  const tasksSrc = readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8");
  assert.match(tasksSrc, /onCanceled\?\.\(\)/, "cancelTask 没调用 onCanceled 回调");
  assert.match(app, /onCanceled:\s*\(\)\s*=>\s*\{[\s\S]*?refreshReroll\(\)/, "App 没在 onCanceled 里对齐消息族重摇状态");
  assert.match(app, /onCanceled:\s*\(\)\s*=>\s*\{[\s\S]*?refreshRerollSummary\(\)/, "App 没在 onCanceled 里对齐摘要族重摇状态");
});

// 按停**不许偷偷换版**：摇完只有"真的多出一版"才切过去。
//
// 若直接 `switchReroll(st.count)`：按停时候选列表还在（之前摇过几版就还在），`count` 没变，
// 消息编辑走独立模态（2026-11-02 用户定）：不把旧气泡就地换成 textarea；/edit 也不再弹原生 prompt。
test("消息编辑：应用内模态，不就地改气泡", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const modal = readFileSync(join(SRC, "components", "MessageEditor.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  assert.ok(!/inline-edit|inline-input/.test(bubble), "气泡里还留着就地编辑的 textarea");
  assert.ok(!/window\.prompt/.test(app), "/edit 还在用原生 prompt");
  assert.match(bubble, /\$?emit\('edit', messageId, 'content'\)/, "改正文按钮没打开编辑器");
  assert.match(bubble, /\$?emit\('edit', messageId, 'reasoning'\)/, "改思考按钮没打开编辑器");
  assert.match(app, /<MessageEditor/, "App 没挂消息编辑器");
  assert.match(app, /@edit="onBubbleEdit"/, "气泡的 edit 事件没接上");
  assert.match(app, /function onBubbleEdit\(mid, kind\)/, "没有 onBubbleEdit");
  assert.match(modal, /api\.editMessage\(props\.sessionId, props\.message\.id, body\)/, "模态保存没走消息编辑接口");
  assert.match(modal, /props\.kind === "reasoning" \? \{ reasoning: text\.value \} : \{ content: text\.value \}/,
    "模态没按正文/思考分别提交");
  assert.match(modal, /<VDialog/, "消息编辑没有用 Vuetify VDialog（会被左侧栏压住）");
  assert.match(modal, /attach="#app"/, "VDialog 没挂到 #app（全屏/移动端包含块会错）");
  assert.match(modal, /<VCard/, "消息编辑没有用 Vuetify 卡片壳");
  assert.match(modal, /<VTextarea/, "正文/思考输入没有用 Vuetify VTextarea");
  assert.match(modal, /class="message-edit-foot"/, "保存/退出没有独立底栏");
  assert.match(modal, /保存并重摇/, "没有「保存并重摇」");
  assert.match(modal, /const expanded = ref\(false\)/, "没有网页全屏状态");
  assert.match(modal, /function toggleExpanded\(\)/, "没有网页全屏切换");
  assert.match(modal, /ICONS\.maximize/, "全屏按钮没有图标");
  assert.ok(!/requestFullscreen|document\.fullscreenElement|fullscreenchange/.test(modal),
    "还在用浏览器 Fullscreen API（会占整块屏幕）");
  assert.match(modal, /@click="save\(true\)"/, "「保存并重摇」没走带 reroll 的保存");
  assert.match(css, /\.modal-actions \{[^}]*margin-left:\s*auto[^}]*justify-content:\s*flex-end/, "保存/退出没有靠右");
  assert.match(app, /editRerollTarget\(vueMessages\.value, mid\)/, "没有判断这条编辑后能不能重摇");
  assert.match(app, /rerollable: !!rerollTargetId/, "没有把可重摇状态带给编辑器");
  assert.match(app, /:rerollable="messageEditTarget\.rerollable"/, "没把 rerollable 传给编辑器");
  assert.match(app, /if \(result\.reroll && target\?\.rerollTargetId\)[\s\S]{0,200}rerollNow\(target\.rerollTargetId\)/, "保存并重摇没接到重摇目标");
  assert.match(css, /\.message-edit-dialog \.v-overlay__content \{[^}]*width:\s*96vw[^}]*height:\s*92vh/s,
    "消息编辑窗口没有接近全屏（96vw × 92vh）");
  assert.match(css, /\.message-edit-dialog\.expanded \.v-overlay__content \{[^}]*width:\s*100%[^}]*height:\s*100%/s,
    "网页全屏没有铺满 #app（不是浏览器全屏）");
  assert.match(css, /\.message-edit-field \{[^}]*flex:\s*1 1 auto[^}]*min-height:\s*0/s,
    "文本框没有撑满剩余高度");
});

// 那句会把正文切到**已经存在**的最后一版上 —— 用户按的是"取消"，界面却换了一版内容。
// 那就是取消干了实事，正是要避免的。
test("重摇：没多出版就不切版本（取消 ≠ 换一版）", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");

  assert.match(app, /async function rerollNow\(targetId = null\)/, "rerollNow 没接收被点气泡的 id");
  assert.match(app, /const target = targetId \|\| lastMsgId\.value/, "rerollNow 没优先用被点气泡的 id");
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  assert.match(bubble, /\$?emit\('reroll', messageId\)/, "重摇按钮没把目标 id 报上去");

  for (const [enter, switchCall, refresh] of [
    ["async function rerollNow(targetId = null)", "await switchReroll(sid, st.count)", "refreshReroll()"],
    ["async function summaryRefresh(", "await summarySwitch(st.count)", "refreshRerollSummary()"],
  ]) {
    const start = app.indexOf(enter);
    assert.ok(start > 0, `找不到 ${enter}`);
    const body = app.slice(start, app.indexOf("\n}\n", start));
    assert.match(body, /const versionsBefore = /, `${enter} 没记"摇之前有几版"`);
    assert.match(body, /st\.count <= versionsBefore/, `${enter} 没判"到底多没多出一版"`);
    // 切版本那一句必须排在"没多出"那道闸**之后**。
    assert.ok(body.indexOf("st.count <= versionsBefore") < body.indexOf(switchCall),
      `${enter} 里先切版本后判"多没多出" —— 顺序反了等于没判`);
    assert.match(body, new RegExp(`${refresh.replace(/[()]/g, "\\$&")}`), `${enter} 被按停时没对齐状态`);
  }
});

// ── 回到前台要自己对齐（2026-11-01 用户定）──────────────────────────
//
// 用户原话：「假设，我发送了消息，llm要一分钟才能返回结果。此时我切换到其他网页。然后2min后
// 回来。此时，我必须打开别的对话再回到当前会话才能刷新结果。这尤其常见于安卓浏览器里。」
//
// 为什么非做不可：**后台标签页的计时器会被节流甚至冻住**（安卓内存紧时直接丢页面）——
// 本地那个轮询循环要么卡在很久以前那一拍、要么已经因为一次网络错退出。
// 唯一可信的是后端，所以回来就问一句"现在什么相位"。
test("回到前台会跟后端对齐（三条事件都接）", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");

  // ① 三条事件都要接，而且都要卸（少了 pageshow 就漏掉安卓从 bfcache 恢复那条路）。
  for (const ev of ['"visibilitychange"', '"focus"', '"pageshow"']) {
    assert.match(app, new RegExp(`addEventListener\\(${ev}, resyncOnForeground\\)`), `没接 ${ev}`);
    assert.match(app, new RegExp(`removeEventListener\\(${ev}, resyncOnForeground\\)`), `没卸 ${ev}`);
  }

  const start = app.indexOf("async function resyncOnForeground()");
  assert.ok(start > 0, "找不到 resyncOnForeground");
  const body = app.slice(start, app.indexOf("\n// resumeReroll", start));

  // ② **还在跑就只接轮询、不许重拉消息列** —— 重拉会把正在流的那个气泡换成库里的，
  //    等于把活人从屏幕上抹掉。
  assert.match(body, /st\.phase === "pending" \|\| st\.phase === "streaming"/,
    "resync 没区分'还在跑'");
  const running = body.slice(body.indexOf('st.phase === "pending"'), body.indexOf('// ② 已经落地'));
  assert.ok(!/reloadWindow/.test(running), "还在跑的时候重拉了消息列 —— 会把正在流的回复抹掉");

  // ③ 已经落地 ⇒ 对齐（消息列 + 状态 + 占用 + 重摇）。
  assert.match(body, /await reloadWindowKeepingScroll\(\)/, "落地之后没重拉消息列（或没保留滚动位置）");
  assert.match(body, /refreshContextLazy\(\)/, "落地之后没按当前工作区懒刷上下文");
  assert.ok(!/outgoingPreview\(sessionID/.test(body), "聊天界面的前台对齐还在无条件拉 /outgoing");
  assert.match(body, /refreshStatusline\(/, "落地之后没刷状态行（上下文占用就在那儿）");

  // ④ 判"要不要接轮询"靠**循环在不在**（liveTurnPoll），不能靠"有没有 stream 气泡" ——
  //    气泡是循环死掉之后留下的假象，拿它当判据就永远不接。
  assert.match(app, /let liveTurnPoll = 0;/, "没有 liveTurnPoll 计数");
  assert.match(app, /liveTurnPoll\+\+;/, "轮询开始没计数");
  assert.match(app, /liveTurnPoll--;/, "轮询结束没减计数");
  assert.match(body, /liveTurnPoll === 0/, "resync 没按'循环在不在'决定接不接");

  // ⑤ 连发事件要合并（切回来时三条常常一串来）。
  assert.match(body, /resyncing \|\| now - lastResyncAt < 300/, "连发的三条事件没有合并");
});

// 轮询循环碰到偶发网络错**不许直接死掉**。
//
// 后台切回来那几拍请求本来就会失败；从前一抛出去循环就没了，而合成气泡还留在列表里
// ⇒ 界面一直"在等待"。现在单拍失败继续下一拍，连着十拍不行才认栽。
test("一轮的轮询：单拍失败继续，不判死刑", () => {
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  assert.match(api, /turnPoll:\s*\(id[^)]*\)/, "API 客户端没有 turnPoll");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const start = app.indexOf("async function pollTurnHTTP(sid, turnID, my, row)");
  assert.ok(start > 0);
  const body = app.slice(start, app.indexOf("async function pollTurn(sid, turnID)", start));
  assert.match(body, /let misses = 0;/, "没有连续失败计数");
  assert.match(body, /if \(\+\+misses > 10\) throw err;/, "失败没有上限（会死循环）或没有上限判断");
  assert.match(body, /misses = 0;/, "成功之后没把计数清零");
  const tryAt = body.indexOf("poll = await api.turnPoll(sid, cur.from, cur.thinkFrom);");
  assert.ok(tryAt > 0, "api.turnPoll 不在容错的那层 try 里");
  assert.ok(!/await api\.status\(sid\)/.test(body), "轮询还在额外发 status —— 两发串行又回来了");
  assert.match(body, /delay = Math\.max\(80, 300 - \(performance\.now\(\) - requestAt\)\)/,
    "没有按 RTT 自适应下一拍间隔");
  // 主路径是 SSE；SSE 建不起来才回退 HTTP。
  assert.match(app, /if \(activeTurnPolls\.has\(sid\)\) return;/, "没有单飞闸（两个循环会写乱同一个显示前缀）");
  assert.ok(/activeTurnPolls\.add\(sid\)/.test(app) && /activeTurnPolls\.delete\(sid\)/.test(app), "单飞闸没有登记/释放");
  assert.match(app, /new EventSource\(api\.turnEventsUrl\(sid, 0, 0\)\)/, "主路径没有 SSE");
  assert.match(app, /await pollTurnEvents\(sid, turnID, my, m\)/, "SSE 入口没接");
  assert.match(app, /await pollTurnHTTP\(sid, turnID, my, m\)/, "SSE 没有 HTTP 回退");
  assert.match(app, /st\.message_id !== turnID/, "迟到的旧连接没校验轮次 id");
});

// ── 域名部署：Host 闸门（2026-11-02）────────────────────────────
//
// 用户把服务放到 `tavern.zincles.cc` 后面（Cloudflare + 反代 + 密码），被 vite 直接挡了：
//
//   Blocked request. This host ("tavern.zincles.cc") is not allowed.
//
// vite 默认只放行 localhost 与各种 IP（防 DNS rebinding）。先前的修法是让域名走环境变量 ——
// 于是**每次换部署形态都要先撞一次 403、回来配一遍**。用户同日定案：「前端就不能不设域名限制吗」
// ⇒ **默认放行任何 Host**，想收窄才设名单。
//
// 三件事钉住：默认必须是放行的、名单仍然能收窄、两个 server 都要挂上。
test("vite：Host 默认不限制，设了名单才收窄", () => {
  const config = readFileSync(join(SRC, "..", "vite.config.js"), "utf8");
  const body = config.replace(/\/\/[^\n]*/g, ""); // 先剥注释 —— 注释里全是这些词

  // ① 默认**放行**：名单为空时必须落到 `true`。
  //    这条正是用户要的：不设任何东西，域名部署就能用。
  assert.match(body, /allowedHosts:\s*allowedHosts\.length\s*\?\s*allowedHosts\s*:\s*true/,
    "默认不是'不限制任何 Host' —— 没设名单时域名部署还会 403");

  // ② 名单来自环境变量（不是写死域名，也不是塞进 config.json —— 那个文件后端会整体重写，
  //    塞进去的自定义键会在下一次改设置时悄悄消失）。
  assert.match(body, /process\.env\.MICROCHAT_ALLOWED_HOSTS/, "没读 MICROCHAT_ALLOWED_HOSTS");
  // podman-compose 未设置 `${VAR:-}` 时会把字面量塞进容器 —— 必须当空，否则 Host 闸门收窄成
  // 一个不存在的域名，域名部署全 403（2026-11-08 重建容器时实踩）。
  assert.match(body, /isEnvPlaceholder/, "没识别 podman-compose 留下的 ${...} 占位符");
  assert.ok(!/config\.json[\s\S]{0,200}allowed_hosts/.test(body),
    "把允许的 Host 塞进了 config.json —— 后端整体重写会把它抹掉");
  assert.ok(!/allowedHosts:\s*\[\s*"/.test(body), "值写死成了字面量数组");

  // ③ 两个 server 都要挂上（preview 是部署口，dev 也常常要透过反代访问）。
  for (const key of ["server:", "preview:"]) {
    const rule = body.slice(body.indexOf(key), body.indexOf("}", body.indexOf(key)));
    assert.match(rule, /\.\.\.hostGate/, `${key} 没挂上 Host 闸门`);
  }

  // ④ 生效值要打在日志里 —— 部署时"到底生效没有"不该靠猜（`podman logs` 能看见）。
  assert.match(body, /console\.log\(\s*`\[vite\] allowedHosts/, "没有把生效值打进日志");
});

// ── 容器里的后端必须绑 0.0.0.0（2026-11-02 实测修的）──────────────
//
// 后端默认绑 `127.0.0.1`（宿主机上那是唯一闸门）。但在**容器**里绑回环 ⇒ 宿主的端口发布
// 转发到容器 eth0，而那里没人听 ⇒ `127.0.0.1:8787:8787` 能连上却**永远没有应答**。
// 表现是"连得上、没响应"，比连不上更难查。
test("容器 entrypoint：后端绑 0.0.0.0，且端口在启动前就解析好", () => {
  const script = readFileSync(
    join(SRC, "..", "..", "..", "docker", "entrypoint.sh"), "utf8");
  const body = script.replace(/^\s*#.*$/gm, ""); // 先剥注释

  // ① 必须是 `-addr "0.0.0.0:$PORT"` —— 绑回环的话发布出来也是死的。
  assert.match(body, /microchat-server\s+-addr\s+"0\.0\.0\.0:\$PORT"/,
    "后端没有以 -addr 0.0.0.0:$PORT 启动（容器里绑回环 ⇒ 宿主发布没应答）");

  // ② `$PORT` 必须在**起后端之前**算出来 —— 顺序反了就是 `-addr "0.0.0.0:"`（退化成默认地址）。
  const portAt = body.indexOf("PORT=${PORT:-8787}");
  const startAt = body.indexOf("-addr");
  assert.ok(portAt > 0, "没有 `PORT=${PORT:-8787}` 这个兜底");
  assert.ok(portAt < startAt, "端口在起后端之后才解析 —— 那时 $PORT 还是空的");
  // 依赖卷跨容器持久：只判断 vite 在不在会漏掉新插件（VPS 实踩 vite-plugin-vuetify）。
  // 旧镜像兼容：Dockerfile 已装 git，但早于这次改动的镜像不会因 restart 重建。
  assert.match(body, /apk add --no-cache git/, "没有旧镜像缺 git 的兜底安装");
  assert.match(body, /go build -buildvcs=false/, "go build 没关 VCS stamping（镜像缺 git 时会 128 起不来）");
  // 源码未变时跳过 Go / Vite 构建（2026-11-09）：两份哈希分开，只改一侧只重建一侧。
  assert.match(body, /GO_HASH_FILE|go_source_hash/, "没有后端源码哈希缓存");
  assert.match(body, /WEB_HASH_FILE|web_source_hash/, "没有前端源码哈希缓存");
  assert.match(body, /复用已有二进制/, "后端没有跳过构建的分支");
  assert.match(body, /复用已有 dist/, "前端没有跳过构建的分支");
  assert.match(body, /npm ci/, "entrypoint 没有安装前端依赖");
  assert.match(body, /\.microchat-lock/, "没有用 lock 哈希标记 installed 状态");
  assert.ok(!/\[ -d node_modules\/vite \] \|\| npm ci/.test(body),
    "还在用「vite 在就算装好了」的旧判据");
});

// ── 前端可用性收尾（2026-11-02：实看 390px 截图后补的）──────────────
//
// 三条都是"不报错、不误路由、构建也过"的那类：空草稿是一页纯黑、模型名被挤成一个字母、
// 会话操作键小到手指点不准。钉住它们，免得下次改布局又悄悄退回去。
test("草稿：defaults 不覆盖已选模型（draftPrefs 必须保持同一对象引用）", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /Object\.assign\(draftPrefs,/, "startDraft 没有就地重置 draftPrefs");
  assert.ok(!/^\s*draftPrefs\s*=\s*\{/m.test(app),
    "startDraft 又给 draftPrefs 重新赋值了 → composable 改旧对象、sendFromDraft 读新对象");
  assert.match(app, /if \(!draftPrefs\.provider && !draftPrefs\.model\)/,
    "defaults 回来时没有保护用户已经选过的模型");
});

test("空草稿首屏：有欢迎语与当前助手/模型，不是一整页纯黑", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(app, /v-if="draft && !chatRows\.length" class="draft-welcome"/,
    "草稿空态没有欢迎块");
  assert.match(app, /draftAgentName/, "欢迎块没给当前助手");
  assert.match(app, /draftModelName/, "欢迎块没给当前模型");
  assert.match(app, /直接在下面输入第一条消息开始/, "没说清下一步该做什么");
  assert.match(css, /\.draft-welcome\s*\{/, "欢迎块没有样式（会与消息列挤在一起）");
});

test("输入框：Vuetify MD3 输入栏 + 发送键", () => {
  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(composer, /import \{ VBtn, VTextarea \} from "vuetify\/components"/, "输入栏没用 Vuetify 组件");
  assert.match(composer, /<VTextarea[\s\S]*variant="solo-filled"/, "输入框不是 Vuetify MD3 filled 形态");
  assert.match(composer, /bg-color="surface-container"/, "输入框没用 MD3 容器色");
  assert.match(css, /#composer \{[^}]*width:\s*min\(calc\(100% - 1\.2em\), var\(--read-col, 840px\)\)/, "输入栏没有与正文同宽");
  assert.match(css, /#composer \{[^}]*left:\s*50%/, "输入栏没有居中");
  assert.match(css, /#composer \{[^}]*transform:\s*translateX\(-50%\)/, "输入栏居中位移没接上");
  assert.match(composer, /class="composer-actions"/, "按钮列没有容器");
  assert.match(composer, /id="send-btn"/, "没有发送键");
  assert.ok(!composer.includes('id="expand-btn"'), "全屏键还留着");
  assert.ok(!composer.includes("composer-fs"), "全屏输入层还留着");
  assert.ok(!composer.includes("Teleport"), "全屏 Teleport 还留着");
  assert.ok(!/\.composer-box/.test(css), "旧手写输入面板样式还留着");
  assert.ok(!/composer-fs|expand-btn/.test(css), "样式里还留着全屏输入");
  const pane = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");
  assert.match(pane, /<span>标准字体<\/span>/, "外观设置没有标准字体");
  assert.match(pane, /<span>等宽字体（代码块）<\/span>/, "外观设置没有等宽字体");
  assert.match(pane, /@change="saveFonts"/, "字体输入没接保存");
  assert.match(pane, /fontBody as fontBodyPref/, "字体值没从服务器偏好读回");
});

test("左栏顶部：Brand Title 在顶，新对话在中间，对话/生图/库存 在下方（Gemini 式）", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(bar, /class="brand-name">MicroChat</, "左栏顶部缺 Brand Title");
  assert.match(bar, /class="brand-mark"/, "品牌标题没有标");
  const newAt = bar.indexOf('id="session-new"');
  const tabsAt = bar.indexOf('class="session-mode-tabs"');
  assert.ok(newAt > 0 && tabsAt > 0, "找不到新对话 / tabs");
  assert.ok(newAt < tabsAt, "对话/生图/库存 应该在新对话下方");
  assert.match(bar, /<VBtnToggle[\s\S]*?v-model="mode"/, "左栏模式不是 VBtnToggle");
  assert.match(bar, /mandatory/, "模式切换不是 mandatory（能取消到无选中）");
  assert.match(bar, /:aria-pressed="mode === 'chat'"/, "对话按钮没有 pressed 状态");
  assert.match(bar, />对话</, "缺「对话」按钮");
  assert.match(bar, />生图</, "缺「生图」按钮");
  assert.match(bar, /生图（暂未接入）/, "生图按钮没标暂未接入");
  assert.match(bar, />库存</, "缺「库存」按钮");
  assert.match(bar, /库存（暂未接入）/, "库存按钮没标暂未接入");
  assert.match(bar, /:aria-pressed="mode === 'inventory'"/, "库存按钮没有 pressed 状态");
  assert.match(bar, /const mode = ref(?:<[^>]+>)?\("chat"\)/, "默认模式不是对话");
  assert.match(css, /#sessions-bar \.session-mode-tabs \{[^}]*width:\s*calc\(100% - 1\.1em\)/, "模式切换没有占满栏宽");
  assert.match(css, /#sessions-bar \.session-mode-tabs \.v-btn \{[^}]*flex:\s*1 1 0/, "三个按钮没有均分栏宽");
  assert.match(css, /#sessions-bar \.brand-name \{[^}]*font-size:\s*1\.05em/, "品牌标题没有独立字号");
});
test("移动端顶栏：模型名吃剩余空间，不再被挤成一个字母", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#chat-col > header \.model-anchor\s*\{[^}]*flex:\s*1 1 auto/,
    "模型外壳不会伸缩 —— 窄屏只剩固定宽度");
  assert.match(css, /#chat-col > header \.model-btn\s*\{[^}]*flex:\s*1 1 auto/,
    "模型按钮自己不伸缩");
  assert.match(css, /#chat-col > header \.model-caret\s*\{[^}]*display:\s*none/,
    "窄屏还留着 8px 的下拉箭头，白占模型名的位置");
  // 2026-11-02：空间够就完整显示；390 这种真放不下时允许换行，不再一律截成省略号。
  assert.match(css, /\.model-anchor\s*\{[^}]*flex:\s*1 1 auto/, "宽屏也没把剩余空间给模型");
  assert.match(css, /\.model-btn-label\s*\{[^}]*flex:\s*0 1 auto[^}]*min-width:\s*0/, "模型名不能收缩");
  assert.match(css, /@media \(max-width: 1100px\)[\s\S]{0,900}#chat-col > header\s*\{[^}]*gap:\s*0\.35em/,
    "中等宽度没有收紧顶栏间距（还是把模型名挤掉）");
  assert.match(css, /@media \(max-width: 700px\)[\s\S]{0,700}\.model-btn-label\s*\{[^}]*white-space:\s*normal/,
    "极窄屏仍不换行（只能截断）");
  assert.match(css, /\.messages-inner \{[^}]*padding-top:\s*max\([^)]*--topbar-h,/s,
    "换行后的顶栏高度没有从消息列留白里让出来");
  const layout = readFileSync(join(SRC, "composables", "useOverlayLayout.ts"), "utf8");
  assert.match(layout, /topbarRO = new ResizeObserver\(sync\)/, "App 没有观察顶栏高度");
  assert.match(layout, /--topbar-h/, "App 没有把顶栏高度写进 --topbar-h");
});

test("会话列表：一行只有一枚选项键，设置/删除收进浮板，上下间距更紧", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(bar, /\{\{ s\.title \|\| "新会话" \}\}/, "会话条目没有标题");
  assert.ok(!/s\.messages|relativeTime|updated_at/.test(bar), "会话条目还在显示条数/日期元数据");
  assert.match(css, /#session-list \{[^}]*padding:\s*0\.35em 0\.55em 0\.8em/, "会话列表没有留边距");
  // 2026-11-04 用户定：不再有 ☰ 与 × 两枚键，只剩一个「选项」。
  assert.match(bar, /v-html="ICONS\.more"/, "会话选项键没有图标");
  assert.ok(!/v-html="ICONS\.x"/.test(bar), "行内还留着关闭 ×");
  assert.ok(!/class="close"/.test(bar), "行内还留着关闭键");
  assert.match(bar, /class="session-menu"/, "没有会话选项浮板");
  assert.match(bar, /@click="pickSettings"[^>]*>会话设置/, "浮板里没有会话设置");
  assert.match(bar, /@click="pickCopy"[^>]*>创建副本/, "浮板里没有创建副本");
  assert.match(bar, /@click="pickExport"[^>]*>导出 JSON/, "浮板里没有导出 JSON");
  assert.match(bar, /class="danger"[^>]*@click="pickDelete">删除会话|@click="pickDelete"[^>]*>删除会话/, "浮板里没有删除会话");
  assert.equal((bar.match(/class="item-btn"/g) || []).length, 1, "一行不该再有多枚操作键");
  // MD3 紧凑列表：2.4em 行、full 圆角、label-large 字号、2.4em 图标按钮。
  assert.match(css, /#sessions-bar \.session-item \{[^}]*min-height:\s*2\.4em/, "会话行不是紧凑密度（2.4em）");
  assert.match(css, /\.session-item \{[^}]*border-radius:\s*var\(--md-shape-full\)/, "会话行不是 full 圆角");
  assert.match(css, /\.session-item \{[^}]*font-size:\s*var\(--md-type-label-large\)/, "会话标题不是 MD3 label-large");
  assert.match(css, /\.session-item \.item-btn \{[^}]*width:\s*2\.4em/, "会话选项键不是紧凑尺寸");
});

test("会话行选项键：未选中隐藏；悬停/键盘焦点/当前会话才显示（触屏同规则）", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  // 收起必须是**全局**规则：不能在 hover 媒体里才算数 —— PWA/触屏对 hover 能力的报告不可靠。
  assert.match(css, /#sessions-bar \.session-item \.item-btn \{[^}]*opacity:\s*0[^}]*pointer-events:\s*none/,
    "未悬停/未选中的行没有收起选项键（应保留盒子，用 opacity + pointer-events）");
  assert.match(css, /#sessions-bar \.session-item\.sel \.item-btn,[\s\S]*?opacity:\s*1[^}]*pointer-events:\s*auto/,
    "当前会话那行没有常亮选项键");
  assert.match(css, /#sessions-bar \.session-item \.item-btn:focus-visible[\s\S]*?opacity:\s*1[^}]*pointer-events:\s*auto/,
    "键盘焦点没有点亮选项键");
  const hoverBlock = css.match(/@media \(hover: hover\) \{([\s\S]*?)\n\}/);
  assert.ok(hoverBlock, "没有保留鼠标悬停的 @media (hover: hover) 规则");
  assert.match(hoverBlock[1], /#sessions-bar \.session-item:hover \.item-btn,[\s\S]*?opacity:\s*1[^}]*pointer-events:\s*auto/,
    "鼠标悬停没有点亮选项键");
  assert.ok(!/@media \(hover: none\)[\s\S]*?\.item-btn/.test(css),
    "又给触屏开了「三个点常驻」的例外");
  assert.ok(!/#sessions-bar \.session-item[^{]*\.item-btn \{[^}]*display:\s*none/.test(css),
    "选项键不能用 display:none 收起（会连焦点和命中区一起丢掉）");
});

test("空输入时的发送键不是向下箭头（那个位置会被读成「滚到底」）", () => {
  const icons = readFileSync(join(SRC, "components", "icons.ts"), "utf8");
  const cont = icons.slice(icons.indexOf("cont:"), icons.indexOf("menu:"));
  assert.ok(!cont.includes("M12 5v14"), "续写图标又变回向下箭头");
  assert.match(cont, /M3 12a9 9/, "续写图标应是回转箭头");
});

test("公式比列宽时给提示（不能只是右缘一刀切）", () => {
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(bubble, /scrollWidth\s*>\s*el\.clientWidth/, "没有量公式是否真的溢出");
  assert.match(bubble, /classList\.toggle\("scroll-x"/, "没给溢出的公式挂提示类");
  assert.match(css, /\.katex-display\.scroll-x\s*\{[^}]*box-shadow/, "提示类没有视觉实现");
  assert.match(css, /\.katex-display\s*\{[^}]*margin:\s*0\.8em 0[^}]*padding-bottom:\s*0\.3em/,
    "KaTeX 块上下仍太挤（margin/padding 没放宽）");
});

// ── 生产交付（2026-11-02：远端部署"响应慢"的静态侧）────────────────────────
test("静态产物缓存：哈希 assets immutable，入口/SW 仍 no-cache", () => {
  const vite = readFileSync(join(SRC, "..", "vite.config.js"), "utf8");
  assert.match(vite, /assetCacheHeaders\(\)/, "vite 没挂缓存头插件");
  assert.match(vite, /max-age=31536000, immutable/, "哈希资源没有 immutable");
  assert.match(vite, /max-age=604800/, "字体没给一周缓存");
  assert.match(vite, /path\.startsWith\("\/fonts\/"\)/, "字体与哈希资源没分开");
});

test("Roboto Flex 是 MD3 默认字体，且是子集（别把全量塞回首屏）", () => {
  const font = readFileSync(join(SRC, "..", "public", "fonts", "RobotoFlex-Latin.woff2"));
  assert.ok(font.length < 150 * 1024, `RobotoFlex-Latin.woff2 是 ${font.length} 字节，子集应小于 150KB`);
  assert.equal(font.toString("ascii", 0, 4), "wOF2", "不是 WOFF2");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  assert.match(css, /font-family:\s*"Roboto Flex"/, "没有 @font-face Roboto Flex");
  assert.match(css, /font-family:\s*var\(--app-font-body,\s*"Roboto Flex"/, "body 没把 Roboto Flex 排第一");
  const prefs = readFileSync(join(SRC, "utils", "prefs.ts"), "utf8");
  assert.match(prefs, /DEFAULT_FONT_BODY = '"?Roboto Flex|DEFAULT_FONT_BODY = '"Roboto Flex/, "默认字体栈不是 Roboto Flex");
});

test("设置页：标题只有一套语言，设置与会话/渠道管理页同宽", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  // 所有 panel 的 h3 统一：正文色半粗 + 蓝色竖条；卡片不许再单独定义一份。
  assert.match(css, /\.settings-cols \.panel h3\s*\{[^}]*color:\s*var\(--md-on-surface\)/, "h3 不是 MD3 正文色");
  assert.match(css, /\.settings-cols \.panel h3::before\s*\{[^}]*background:\s*var\(--md-primary\)/, "h3 没有统一的 MD3 竖条");
  assert.ok(!/\.settings-card h3::before/.test(css), "卡片又给 h3 单独定义了一套竖条");
  // 六个 tab 的宽度必须一起写，切 tab 不能忽宽忽窄。
  const width = css.match(/#panel-appearance\s*,[^{]*#panel-provider\s*\{[^}]*max-width:\s*760px/);
  assert.ok(width, "六个 panel 没有统一 760 宽");
});

test("开机请求：会话/Agent 不重复，重活按需，加载有骨架", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const modelsSrc = readFileSync(join(SRC, "composables", "useModelsAgents.ts"), "utf8");
  // Agent 列表同一拍共享一发（顶栏 + 草稿首段/状态行都要它）。
  assert.match(modelsSrc, /function fetchAgentsOnce\(\)/, "没有 Agent 请求去重");
  for (const fn of ["refreshAgents", "fillAgentSelect"]) {
    const body = modelsSrc.match(new RegExp(`(?:async )?function ${fn}\\([^)]*\\)[\\s\\S]*?\\n  \\}`))?.[0] ?? "";
    assert.match(body, /fetchAgentsOnce\(\)/, `${fn} 没走去重后的 Agent 请求`);
  }
  // boot 已经拉过 /sessions ⇒ enterSession 直接吃现成的。
  assert.match(app, /enterSession\(last, sessions\)/, "开机恢复没把已拉到的会话列表传进去");
  // 2026-11-05：列表在屏幕上就直接用（高延迟下省一个完整 RTT），缓存没有才真去拉。
  assert.match(app, /let sessions = preloaded \?\? sessionsCache\.value/, "enterSession 仍无条件再拉一次 /sessions");
  // 进会话不再无条件 POST /outgoing / GET /state：只在工作区看得见时按 tab 拉。
  const enter = app.match(/async function enterSession\(id, preloaded = null\)[\s\S]*?\n\}/)?.[0] ?? "";
  assert.ok(enter, "找不到 enterSession");
  assert.ok(!/api\.outgoingPreview/.test(enter), "enterSession 又无条件预演历史载荷了");
  assert.match(enter, /refreshContextLazy\(\)/, "进会话没按工作区 tab 懒加载");
  // 加载空白用骨架撑住。
  assert.match(app, /const loadingSession = ref\(true\)/, "没有 loadingSession 状态");
  assert.match(app, /v-if="loadingSession" class="chat-skeleton"/, "消息列没有加载骨架");
});

// ── 左栏入口（2026-11-02 用户定）────────────────────────────────────────────
//
// 「安装说明」删掉；左栏底改成 会话 / 渠道 / 设置。会话与渠道走同一个设置视图的
// `:only` 模式，不再占设置 tab。
test("左栏：有会话/渠道/智能体/设置，没有安装说明", () => {
  const pwa = readFileSync(join(SRC, "utils", "pwa.ts"), "utf8");
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");

  assert.match(bar, /id="sessions-manage"/, "左栏没有会话入口");
  assert.match(bar, /id="providers-manage"/, "左栏没有渠道入口");
  assert.match(bar, /id="settings-open"/, "左栏没有设置入口");
  assert.match(bar, /id="agents-manage"/, "左栏没有智能体入口");
  assert.ok(!bar.includes('id="update-open"'), "左栏底部还有更新入口（已挪到品牌头）");
  // 四个入口并排 + Vuetify MD3：不再手写 .foot-btn 竖排
  assert.match(bar, /<VBtn[\s\S]*class="bar-action"[\s\S]*stacked/, "底部入口不是 Vuetify VBtn(stacked)");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#sessions-bar \.bar-foot \{[^}]*flex-direction: row/, "底部入口没有并排");
  assert.match(css, /#sessions-bar \.bar-foot \.bar-action \{[^}]*flex: 1 1 0/, "四个入口没等宽");
  assert.ok(!/foot-btn/.test(bar), "还留着旧 .foot-btn");
  for (const dead of ["install-app", "安装说明", "installable", "standalone", "download"]) {
    assert.ok(!bar.includes(dead), `左栏还留着安装入口的 ${dead}`);
  }
  assert.ok(!/beforeinstallprompt/.test(pwa), "PWA 安装事件还挂在模块里（入口已删）");
  assert.ok(!/installApp/.test(app), "App 还留着 installApp 死代码");
});

// 更新面板（2026-11-09）：显示当前提交；容器里一键 git pull --ff-only + 重启。
test("更新面板：显示提交 + 拉取并重启（由 entrypoint 接手）", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const pane = readFileSync(join(SRC, "components", "UpdatePane.vue"), "utf8");
  const upd = readFileSync(join(SRC, "utils", "update.ts"), "utf8");
  const view = readFileSync(join(SRC, "composables", "useSettingsView.ts"), "utf8");
  const client = readFileSync(join(SRC, "api", "client.ts"), "utf8");

  assert.match(bar, /class="brand"[\s\S]*?@click="\$emit\('open-update'\)"/, "品牌头没接更新入口");
  assert.match(app, /@open-update="\(\) => openManage\('update'\)"/, "App 没把更新入口接到设置视图");
  assert.match(view, /\["provider", "agent", "update"\]\.includes\(pane\)/, "openManage 不认识 update / agent");
  assert.match(settings, /<UpdatePane :ask="ask"/, "设置视图里没有更新面板");
  assert.match(settings, /props\.only === "update" \? "更新"/, "更新页没有独立页头");
  assert.match(settings, /if \(props\.only === "update"\) return;/, "更新页还在白拉设置页数据");
  assert.match(pane, /<VDivider class="update-divider"/, "本地/远端两块之间没有分隔符");
  assert.match(pane, /当前本地版本/, "没有本地版本区块");
  assert.match(pane, /GitHub 最新版本/, "没有 GitHub 最新版本区块");
  assert.match(pane, /status\.remote_short/, "更新面板没显示 GitHub 最新提交");
  assert.match(pane, /status\.remote_subject/, "更新面板没显示 GitHub 最新提交说明");
  assert.match(pane, /status\.remote_commit === status\.commit/, "没有把当前提交和远端最新匹配");
  assert.match(pane, /remote_relation === 'ahead'/, "没有区分本地领先远端");
  assert.match(pane, /remote_relation === 'behind'/, "没有区分本地落后远端");
  assert.match(pane, /pullUpdate/, "更新面板没有拉取动作");
  assert.match(upd, /started_at !== baseline/, "重启后没有等新进程（会被旧进程骗过去）");
  assert.match(pane, /saveUpdateAutoCheck/, "更新面板没有自动检查开关");
  assert.match(pane, /update_auto_check|updateAutoCheckOn/, "自动检查开关没接偏好");
  assert.match(pane, /<VSwitch/, "自动检查不是 Vuetify 开关");
  assert.ok(pane.indexOf('class="update-auto"') < pane.indexOf('class="update-version"'), "自动检查不在顶部两块之上");
  assert.ok(pane.indexOf('class="update-auto"') < pane.indexOf('class="update-divider"'), "自动检查与下面两块之间没有分隔符");
  const updateBar = readFileSync(join(SRC, "components", "UpdateBar.vue"), "utf8");
  assert.match(updateBar, /backend/, "顶部更新条不认识后端更新");
  assert.match(updateBar, /update-backend/, "顶部更新条没有更新前后端的动作");
  assert.match(app, /checkBackendUpdate/, "App 没有自动检查 GitHub");
  assert.match(app, /mc-update-auto-check/, "更新面板开关没通知 App");
  assert.match(app, /:backend="backendUpdate"/, "顶部更新条没接后端更新状态");
  assert.match(pane, /restart_supported/, "没区分容器部署与宿主二进制");
  assert.match(pane, /needsRebase/, "没识别分叉需要变基");
  assert.match(pane, /rebaseUpdate/, "没有变基动作");
  assert.match(pane, /pullAndRestart\(needsRebase\)/, "分叉时主按钮没切到变基");
  assert.match(pane, /变基并重启/, "界面没有「变基并重启」");
  assert.match(client, /getUpdateStatus: \(\) => call<UpdateStatus>\("GET", "\/update"\)/, "API 客户端没有 GET /update");
  assert.match(client, /pullUpdate: \(\) => call<UpdatePullResult>\("POST", "\/update\/pull"\)/, "API 客户端没有 POST /update/pull");
  assert.match(client, /rebaseUpdate: \(\) => call<UpdatePullResult>\("POST", "\/update\/rebase"\)/, "API 客户端没有 POST /update/rebase");
});

// 模型规则：grok-4 × response 默认追加 xAI native tools（2026-11-09 用户定）。
test("模型规则：支持 match_protocol 与追加 server_tools / local_tools", () => {
  const editor = readFileSync(join(SRC, "components", "ModelRulesEditor.vue"), "utf8");
  const types = readFileSync(join(SRC, "api", "types.ts"), "utf8");
  const go = readFileSync(join(SRC, "..", "..", "..", "core", "internal", "config", "model_rules.go"), "utf8");
  assert.match(editor, /match_protocol/, "规则编辑器没有协议条件");
  assert.match(editor, /openai-response/, "协议选项里没有 response");
  assert.match(editor, /server_tools/, "规则编辑器没有服务端工具");
  assert.match(editor, /local_tools/, "规则编辑器没有本地工具");
  assert.match(editor, /serverToolOptions/, "没有 native tool 选项");
  assert.match(editor, /x_search/, "native tool 选项里没有 x_search");
  assert.match(types, /match_protocol\?/, "API 类型没有 match_protocol");
  assert.match(types, /server_tools\?/, "API 类型没有 server_tools");
  assert.match(types, /local_tools\?/, "API 类型没有 local_tools");
  assert.match(go, /xai-grok-4-native/, "默认规则没有 grok-4 × response 条目");
  assert.match(go, /MatchProtocol:\s*"openai-response"/, "默认规则没限定 response");
  assert.match(go, /ServerTools:\s*stringSlicePtr\("x_search", "web_search", "code_execution"\)/,
    "默认规则没有追加 xAI 服务端 tools");
});

// PWA 小工具（2026-11-09 用户定）：更新面板里核对当前壳是否最新，并能直接切换。
test("更新面板：PWA 前端壳核对与切换", () => {
  const pane = readFileSync(join(SRC, "components", "UpdatePane.vue"), "utf8");
  const pwa = readFileSync(join(SRC, "utils", "pwa.ts"), "utf8");
  const vite = readFileSync(join(SRC, "..", "vite.config.js"), "utf8");

  assert.match(pane, /PWA \/ 已安装应用/, "更新面板里没有 PWA 区块");
  assert.match(pane, /__BUILD_ID__/, "没有读当前页面的构建身份");
  assert.match(pane, /fetchLatestBuild/, "没有读服务器上的 build.json");
  assert.match(pane, /checkPwaUpdate/, "没有手动检查 Service Worker 更新");
  assert.match(pane, /applyPwaUpdate/, "没有切换 PWA 的动作");
  assert.match(pane, /forceReloadPwa/, "没有强制重新加载的兜底");
  assert.match(pane, /v-if="pwaUpdateAvailable"/, "发现更新时没有「更新并刷新」入口");
  assert.match(pane, /Service Worker/, "没有显示 Service Worker 状态");
  assert.match(pwa, /getRegistration/, "没有读当前 Service Worker 注册");
  assert.match(pwa, /\.update\(\)/, "检查更新没有调用 registration.update()");
  assert.match(pwa, /waiting/, "没有识别等待切换的新版本");
  assert.match(pwa, /startUpdate/, "切换 PWA 没走既有的 skipWaiting 路径");
  assert.match(pwa, /caches\.delete/, "强制重新加载没有清 Cache Storage");
  assert.match(vite, /buildInfoPlugin/, "Vite 没生成前端构建身份");
  assert.match(vite, /fileName:\s*"build\.json"/, "没有写出 dist/build.json");
  assert.match(vite, /__BUILD_ID__/, "构建身份没注入到 JS");
  assert.ok(!/globPatterns:\s*\[[^\]]*json/.test(vite), "build.json 不该进 Service Worker 预缓存");
});

// 左栏三面板 + 主题循环键（2026-11-09 用户定）。
test("左栏：对话/生图/库存三面板；主题三态按钮在品牌与收起键之间", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  assert.match(bar, /id="theme-toggle"/, "左栏没有主题按钮");
  assert.match(bar, /THEME_IDS/, "主题按钮没读唯一权威的主题清单");
  assert.match(bar, /ICONS\.sun[\s\S]*ICONS\.moon[\s\S]*ICONS\.themeAuto/, "主题按钮缺三态图标");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#sessions-bar \.theme-toggle \{[^}]*background:\s*none/, "主题按钮不该有背景");
  const brandAt = bar.indexOf('class="brand"');
  const themeAt = bar.indexOf('id="theme-toggle"');
  const toggleAt = bar.indexOf('id="toggle-left"');
  assert.ok(brandAt < themeAt && themeAt < toggleAt, "主题按钮不在品牌右侧、收起键左侧");
  assert.match(bar, /v-if="mode === 'chat'"[\s\S]*id="session-list"/, "对话列表没有只在对话面板显示");
  assert.match(bar, /v-else-if="mode === 'image'"[\s\S]*生图/, "没有生图面板");
  assert.match(bar, /v-else[\s\S]*库存/, "没有库存面板");
  assert.match(bar, /mode\.value === "image" \? "新绘图任务"/, "生图模式的新建按钮文案没变");
  assert.match(bar, /mode\.value === "inventory" \? "暂定"/, "库存模式的新建按钮文案没变");
  assert.match(bar, /:disabled="mode === 'inventory'"/, "库存模式的暂定按钮还能点");
  assert.ok(bar.indexOf('id="session-new"') < bar.indexOf('class="session-mode-tabs"'), "新按钮不在分段切换上方");
});

test("PWA 图标：尺寸对、换掉了纸飞机", () => {
  const dir = join(SRC, "..", "public");
  const sizeOf = (name) => {
    const b = readFileSync(join(dir, name));
    assert.equal(b.toString("ascii", 1, 4), "PNG", `${name} 不是 PNG`);
    return { w: b.readUInt32BE(16), h: b.readUInt32BE(20) };
  };
  assert.deepEqual(sizeOf("pwa-192.png"), { w: 192, h: 192 });
  assert.deepEqual(sizeOf("pwa-512.png"), { w: 512, h: 512 });
  assert.deepEqual(sizeOf("pwa-maskable-512.png"), { w: 512, h: 512 });
  assert.deepEqual(sizeOf("apple-touch-icon.png"), { w: 180, h: 180 });

  const favicon = readFileSync(join(dir, "favicon.svg"), "utf8");
  assert.ok(!favicon.includes("M3.714 3.048"), "favicon 还是旧纸飞机");
  assert.match(favicon, /#60a5fa/, "favicon 没用新图标的主色");
  assert.match(favicon, /M256 172c6 40/, "favicon 里没有星芒");
});

// ── CSS 复用（2026-11-02）────────────────────────────────────────────────────
//
// 幽灵按钮的 `background/border/cursor` 与几种文字色蒙层以前到处抄；现在各只有一处。
// 这条防的是"下次加按钮时又顺手抄一遍"—— 抄了不会报错，只会让下一次调色改不动。
test("CSS 复用：幽灵按钮底座与蒙层 token 只有一处", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  // ① 底座只能有一份，且三件事齐全。
  const base = css.match(/\.icon-btn,\s*\.link-btn,\s*\.cp-close,[\s\S]*?\{([^}]*)\}/)?.[1] ?? "";
  assert.ok(base, "找不到幽灵按钮底座");
  assert.match(base, /background:\s*none/, "底座没有去底色");
  assert.match(base, /border:\s*none/, "底座没有去边框");
  assert.match(base, /cursor:\s*pointer/, "底座没有指针样式");

  // ② 具体按钮不许再把这三条抄回来（只查最典型的四个）。
  for (const sel of [".icon-btn", ".link-btn", ".msg-ops \\.v-btn"]) {
    const rule = css.match(new RegExp(`${sel}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
    assert.ok(rule, `找不到 ${sel} 的规则`);
    assert.ok(!/background:\s*none/.test(rule), `${sel} 又自己写了一遍 background:none（应复用底座）`);
    assert.ok(!/border:\s*none/.test(rule), `${sel} 又自己写了一遍 border:none（应复用底座）`);
    assert.ok(!/cursor:\s*pointer/.test(rule), `${sel} 又自己写了一遍 cursor:pointer（应复用底座）`);
  }

  // ③ 蒙层浓度 token 在 theme.css，main.css 只管引用。
  for (const id of ["--mix-6", "--mix-8", "--mix-10", "--mix-13", "--mix-18"]) {
    assert.match(theme, new RegExp(`${id}:\\s*color-mix`), `theme.css 缺少 ${id}`);
  }
  assert.ok(!/color-mix\(in srgb, var\(--tg-text\) 6%/.test(css), "main.css 又出现了写死的 6% 蒙层");
});

test("聊天布局：16px / 1.7 行高、840 阅读列、更松的消息间距", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /--read-col, 840px/, "阅读列还停在 920px（中文行太长）");
  assert.match(css, /\.msg \{[^}]*font-size:\s*calc\(16px \* var\(--app-text-scale/, "聊天正文没有 16px 字号");
  assert.match(css, /\.msg \{[^}]*line-height:\s*1\.7/, "聊天正文行高没放宽");
  assert.match(css, /#?messages-inner \{[^}]*gap:\s*0\.8em/, "消息之间还是挤在一起");
  assert.match(css, /\.msg-wrap\.assistant \.msg \{[^}]*padding:\s*0\.45em 0/, "assistant 上下仍太挤");
  assert.match(css, /\.msg \.text p \{[^}]*margin:\s*0 0 0\.7em/, "段落间距没放宽");
  assert.match(css, /\.msg \.msg-foot \{[^}]*font-size:\s*var\(--md-type-label-small\)/, "用量脚注没有收成 MD3 label-small");
  assert.match(css, /\.icon-btn, \.link-btn, button:has\(> svg\)[^{]*\{[^}]*align-items:\s*center/, "图标按钮没有统一 flex 居中");
  assert.match(css, /button > svg \{[^}]*display:\s*block/, "SVG 还按 inline 基线落（会偏下）");
});

test("回到前台重拉消息列时，用户没在底部就保留原来的滚动位置", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /async function reloadWindowKeepingScroll\(\{[^)]*\} = \{\}\)/, "没有保留滚动位置的重拉函数");
  assert.match(app, /const stick = nearBottom\(el\)/, "没判断重拉前是否贴底");
  assert.match(app, /if \(stick\) next\.scrollTop = next\.scrollHeight/, "贴底时没继续贴底");
  assert.match(app, /next\.scrollTop = Math\.min\(top,/, "不在底部时没恢复原位置");
  assert.match(app, /dropStreamBubbles\(\);\s*await reloadWindowKeepingScroll\(\)/, "前台对齐没走保留滚动");
  assert.match(app, /await waitTypewriter\(\);[\s\S]{0,160}await reloadWindowKeepingScroll\(\)/, "一轮落库后仍会把不贴底的人拽到底部");
});

test("消息操作行：编辑/思考/重摇/删除始终可见，不再等 hover", () => {
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.ok(!/hover-only/.test(bubble), "气泡里还留着 hover-only（那三个按钮又会被藏起来）");
  assert.ok(!/\.msg-ops \.hover-only/.test(css), "CSS 还在按 hover 藏操作按钮");
  assert.match(css, /\.msg-ops \{[^}]*opacity:\s*0\.72/, "操作行没有常显的底噪");
});

test("消息窗口：默认 5 段，设置里可改，首屏与加载更多共用", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(app, /const windowLimit = ref\(5\)/, "消息窗口默认不是 5");
  assert.match(app, /windowStart\(segments, limit\)/, "首屏没用可配置窗口");
  assert.match(app, /limit = windowLimit\.value/, "窗口取数没有默认取 history_window");
  assert.match(app, /prevWindowStart\(vueSegments\.value, windowFrom\.value, windowLimit\.value\)/, "加载更多没用可配置窗口");
  assert.match(app, /history_window/, "App 没从 chat 配置读 history_window");
  assert.match(settings, /v-model="chat\.history_window"/, "设置里没有首屏消息数输入");
  assert.match(settings, /首屏加载多少条消息/, "设置项没有说人话的标签");
});

test("设计系统：tab 统一下划线、设置平铺、圆角收口", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  // 设置 / Agent 用 Vuetify VTabs（MD3 primary tabs）；顶栏/上下文仍各用自带 indicator。
  assert.ok(!/\.settings-nav/.test(css), "旧 TabBar 的 .settings-nav 样式还留着");
  // 顶栏/上下文都换成了 Vuetify 组件，indicator 由 VBtnToggle / VTabs 自己画。
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  assert.match(top, /<VTabs[\s\S]*?class="ws-switch workspace-tabs"/, "顶栏工作区不是 VTabs");
  assert.match(bar, /<VTabs[\s\S]*?class="context-tabs"/, "上下文 tab 不是 VTabs");
  // 设置页：无卡片底、无连体组圆角
  assert.match(css, /\.settings-card \{[^}]*background:\s*none[^}]*padding:\s*0/, "设置分节还带卡片底");
  assert.match(css, /\.settings-cols \.panel \.field[^{]*\{[^}]*background:\s*none/, "设置行还带卡片底");
  assert.ok(!/settings-cols \.panel h3 \+ \.field[^}]*border-radius/.test(css), "设置字段又连成卡片组");
  // 圆角/输入框收口
  assert.match(theme, /--r-card:\s*12px/, "卡片圆角没有统一到 12");
  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  assert.match(composer, /rounded="xl"/, "输入框没走 Vuetify 的 MD3 shape");
  assert.ok(!/#input \{/.test(css), "旧 #input 样式还留着");
});

test("载荷预览：面板里可开关；关掉不请求，偏好与输入预演分开", () => {
  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const prefs = readFileSync(join(SRC, "utils", "prefs.ts"), "utf8");
  assert.match(bar, /const preview = ref\(payloadPreviewOn\(\)\)/, "载荷面板没有自己的预览开关");
  assert.match(bar, /<span class="lbl">预览载荷<\/span>/, "载荷开关没有可见标签");
  assert.match(bar, /emit\("preview"/, "载荷开关没把变化报给 App");
  assert.match(app, /payloadPreviewOn\(\)/, "载荷请求没检查载荷预览开关");
  assert.match(app, /function onPayloadPreview\(on\)/, "App 没接载荷预览开关");
  assert.match(prefs, /payload_preview: true/, "前端缺省没有 payload_preview（默认应开）");
});

test("消息窗口批量取：摘要切出的多个区间压成一发 POST", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const api = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  assert.match(api, /messageRanges:\s*\(id[^)]*\)\s*=>\s*call<[^>]+>\("POST", `\/sessions\/\$\{enc\(id\)\}\/messages\/batch`/,
    "API 客户端没有 messageRanges");
  assert.match(app, /function batchRanges\(runs\)/, "没有把 Infinity 尾段转成 to=0 的纯函数");
  assert.match(app, /b === Infinity \? 0 : b/, "批量区间没有处理尾部");
  assert.equal((app.match(/api\.messageRanges\(/g) ?? []).length, 2,
    "首屏与加载更多没有都走批量接口");
  assert.ok(!/for \(const \[a, b\] of runs\)[\s\S]{0,120}await api\.listMessages/.test(app),
    "还在按区间循环发请求（RTT 会乘以区间数）");
});

// 渠道标识可改（2026-11-05 用户定：「渠道标识为啥不能修改了？要修改啊！」）。
//
// 钉四件事，缺一件就"看着能改、其实改不动/改一半"：
// ① 编辑态那一格**不再 disabled**；② 保存时把新标识以 `new_id` 发出去；
// ③ 回执带上 id —— 父组件要拿它换对象（否则改名后再保存会打到已不存在的旧 id）；
// ④ 父组件真的会把 editProvider 换成新对象，并通知顶栏重拉模型分组。
test("渠道标识可改：编辑态输入框可用、保存发 new_id、成功后父组件换对象", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");

  const idField = editor.match(/<span>渠道标识<\/span><VTextField[^>]*>/);
  assert.ok(idField, "找不到渠道标识那一格");
  assert.ok(!/disabled/.test(idField[0]), "编辑态仍然禁用了渠道标识（用户明确要能改）");
  assert.match(editor, /body\.new_id = nextID/, "保存时没有把新标识发出去");
  assert.match(editor, /emit\("saved", savedID\)/, "保存回执没带 id");
  assert.match(editor, /改名会一起改掉已绑定这条渠道的会话/, "编辑态没有说清改名的后果");

  assert.match(settings, /@saved="onProviderSaved"/, "父组件没接带 id 的保存回执");
  assert.match(settings, /function onProviderSaved\(id[^)]*\)/, "没有 onProviderSaved");
  assert.match(settings, /editProvider\.value = providers\.value\.find\(/, "改名后没把编辑器指向新对象");
  assert.match(settings, /emit\("models-changed"\)/, "改名后没通知顶栏重拉模型分组");
});

// 渠道别名（2026-11-09）：显示别名与渠道标识是**两件事** ——
// 别名只走 alias 字段（空 = 清），id 只走 new_id；别让改别名把会话 / 模型引用一起搬走。
test("渠道别名：编辑框可填、保存发 alias，不碰 new_id", () => {
  const editor = readFileSync(join(SRC, "components", "ProviderEditor.vue"), "utf8");
  assert.match(editor, /<span>显示别名（可选）<\/span><VTextField[^>]*v-model="alias"/, "找不到别名那一格");
  assert.match(editor, /alias\.value = p\.alias \?\? ""/, "编辑态没带入已保存的别名");
  assert.match(editor, /body\.alias = alias\.value\.trim\(\)/, "保存时没有把别名发出去");
  assert.ok(!/body\.new_id = alias/.test(editor), "别名被当成渠道标识发了");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /providerDisplayName\(p\)/, "渠道列表没显示别名");
  assert.match(settings, /provider-id/, "渠道列表的别名旁没保留真实 id");
});

// 左栏行内环（2026-11-05 用户定）：一条会话有任务在跑，会话列表那一行就得有反应。
//
// 判定只在一处（`utils/tasks.ts`）：顶栏环心、任务面板、左栏行内环吃同一份 `/tasks` 快照。
// 这条钉两件最容易错的事：**已完成的不算**（面板还会把它列一阵子）、**队列踩着的会话也算**
// （两步之间服务端可能短暂地没有原子任务，只看 tasks 会让环一闪一闪）。
test("忙碌会话：只认还在跑的任务，队列当前那一步也算", () => {
  const board = {
    running: 3,
    tasks: [
      { id: "t1", session: "a", finished_at: null }, // 跑着 ⇒ a
      { id: "t2", session: "b", finished_at: 123 },  // 已收尾 ⇒ 不算
      { id: "t3", session: null, finished_at: null }, // refresh_models 不绑会话 ⇒ 不算
      { id: "t4", session: "a", finished_at: null }, // 同一条会话两个任务 ⇒ 去重
    ],
    queues: [
      { id: "q1", state: "进行中", current: { session_id: "c" } },
      { id: "q2", state: "成功", current: { session_id: "d" }, finished_at: 9 }, // 已结束 ⇒ 不算
      { id: "q3", state: "进行中" }, // 两步之间没有 current ⇒ 不硬加
    ],
  };
  assert.deepEqual(busySessionIds(board).sort(), ["a", "c"]);
  assert.deepEqual(busySessionIds(null), [], "读不到面板 ⇒ 空，不是崩溃");
  assert.deepEqual(busySessionIds({}), [], "空面板 ⇒ 空");
});

test("忙碌会话接到左栏：同一拍、读不到就清空、行上有环且会转", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");

  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /busyIds\.value = busySessionIds\(board\)/, "App 没把 /tasks 快照算成忙碌会话");
  assert.match(readFileSync(join(SRC, "composables", "useTasks.ts"), "utf8"), /busyIds\.value = \[\]/, "任务读不到时没清空（会留下永远转的假象）");
  assert.match(app, /:busy-ids="busyIds"/, "没把忙碌会话传给左栏");
  assert.match(bar, /v-if="busySet\.has\(s\.id\)"/, "行上没有按会话 id 判断");
  assert.match(bar, /class="session-busy"/, "没有行内环元素");
  assert.match(css, /@keyframes session-busy-spin/, "环没有转起来的动画");
  assert.match(css, /prefers-reduced-motion[\s\S]{0,300}session-busy[\s\S]{0,120}animation: none/, "reduced-motion 下没关动画");
});

// —— 高延迟（1s+ RTT）下的界面口径（2026-11-05）——
// 目标不是"让网络变快"，而是**别让等待凭空多出一个来回**，也别让旧数据在新语境里挂着。

test("窗口取数：尾部种子已覆盖时不再补发", () => {
  const seeded = [{ idx: 6 }, { idx: 7 }, { idx: 8 }];
  // 窗口正好是最后几条（最常见的形状）⇒ 第二拍是空的
  assert.deepEqual(seededMissingRuns([[6, Infinity]], seeded), []);
  assert.deepEqual(seededMissingRuns([[6, 8]], seeded), []);
  // 窗口比种子更老 ⇒ 只补老的一段，别把种子覆盖的部分再要一遍
  assert.deepEqual(seededMissingRuns([[1, Infinity]], seeded), [[1, 5]]);
  assert.deepEqual(seededMissingRuns([[1, 5], [6, 8]], seeded), [[1, 5]]);
  // 没有种子（加载更多那条路）⇒ 原样要
  assert.deepEqual(seededMissingRuns([[1, 3]], []), [[1, 3]]);
  assert.deepEqual(seededMissingRuns([[4, Infinity]], []), [[4, Infinity]]);
});

test("高延迟：发送先上屏、失败撤回；切会话不先拉列表；窗口并行带种子", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const modelsSrc = readFileSync(join(SRC, "composables", "useModelsAgents.ts"), "utf8");

  // ① 乐观发送：addMessage 必须在 await 之前；失败要把文字还回输入框
  const sendStart = app.indexOf("async function send(content)");
  const sendEnd = app.indexOf("async function resend()");
  const send = app.slice(sendStart, sendEnd);
  assert.ok(sendStart > 0 && sendEnd > sendStart, "找不到 send()");
  assert.ok(send.indexOf('addMessage("你", content, "user")') < send.indexOf("await api.sendMessage"), "发送不是先上屏再等 202");
  assert.match(send, /dropOptimistic\(optimistic\)/, "发送失败没撤掉乐观气泡");
  assert.match(send, /setText\(content\)/, "发送失败没把输入的文字还回去");
  assert.match(send, /settleOptimisticUser\(optimistic, accepted\?\.user\)/, "202 回来后没换成服务端那条（编号/编辑都要真 id）");

  // ② 草稿那条是两个 back-to-back 请求（建会话 → 受理），同样先上屏
  const draft = app.slice(app.indexOf("async function sendFromDraft"), app.indexOf("function stopCurrent"));
  assert.ok(draft.indexOf('addMessage("你", text, "user")') < draft.indexOf("await api.createSession"), "草稿首句不是先上屏再建会话");
  assert.match(draft, /setText\(text\)/, "草稿发送失败没把文字还回去");

  // ③ 切会话：列表在屏幕上就直接用，别先 GET /sessions 再画
  const enter = app.slice(app.indexOf("async function enterSession"), app.indexOf("async function boot"));
  assert.match(enter, /let sessions = preloaded \?\? sessionsCache\.value/, "切会话还是在无条件拉列表");
  assert.match(enter, /if \(!sessions\.some\(\(s\) => s\.id === id\)\)/, "缓存里没有时没有兜底去拉");
  assert.match(enter, /sessionsFetchedAt > SESSIONS_FRESH_MS/, "列表缓存没有后台保鲜（会一直用旧标题）");

  // ④ 窗口第一拍就带"最后 N 条"，摘要没横跨时第二拍为空
  const load = app.slice(app.indexOf("async function fetchWindowNow"), app.indexOf("async function loadWindow"));
  assert.match(load, /api\.listMessagesLast\(id, limit\)/, "第一拍没带尾部种子");
  assert.match(load, /seededMissingRuns\(runs, recent(?: as any)?\)/, "没按种子算缺口（会把已取到的再要一遍）");

  // ⑤ 模型清单缓存先画后对（切会话不再闪「加载模型…」）
  assert.match(modelsSrc, /if \(modelsCache\) paintModelOptions\(modelsCache\)/, "模型清单没有先画缓存");

  // ⑥ 编辑消息：PATCH 回来先就地换正文，再对账（别让旧字多挂一个 RTT）
  const editor = readFileSync(join(SRC, "components", "MessageEditor.vue"), "utf8");
  assert.match(editor, /emit\("saved", \{ reroll, message: updated \}\)/, "编辑器没把更新后的消息带回来");
  const edited = app.slice(app.indexOf("async function onMessageEdited"), app.indexOf("async function rerollNow"));
  assert.ok(edited.indexOf("row.content = updated.content") < edited.indexOf("reloadWindowKeepingScroll"), "编辑后不是先本地生效再对账");
});

// 回看缓存（2026-11-05 用户定：「切换不同对话时，最好也预留缓存吧」）。
//
// 存的是"上一屏"（消息行 + 段 + 窗口起点）；切回时 0 RTT 画出来，再后台对账。
// 三个必须成立的性质：命中不脏缓存（存/取都拷贝）、LRU 有上限、删会话能清掉。
test("回看缓存：拷贝进出、LRU 上限、可遗忘", () => {
  const cache = new Map();
  const rows = [{ id: "m1" }, { id: "m2" }];
  rememberWindow(cache, "a", { rows, segments: [{ kind: "msg", idx: 1 }], windowFrom: 1 });
  // 改动传进去的原数组不该改到缓存（remember 拷贝）
  rows.push({ id: "m3" });
  // recall 返回副本
  const got = recallWindow(cache, "a");
  assert.equal(got.rows.length, 2, "remember 没做拷贝");
  got.rows.push({ id: "x" });
  assert.equal(recallWindow(cache, "a").rows.length, 2, "recall 没做拷贝");
  assert.equal(recallWindow(cache, "nope"), null, "未命中的会话该回 null");

  // LRU：填满上限后最老的被挤掉；命中会触碰（重新排到最新）
  for (let i = 0; i < WINDOW_CACHE_MAX; i++) rememberWindow(cache, `k${i}`, { rows: [], segments: [], windowFrom: 1 });
  recallWindow(cache, "k0"); // 触碰 => k1 变成最老
  rememberWindow(cache, "brand-new", { rows: [], segments: [], windowFrom: 1 });
  assert.equal(recallWindow(cache, "k1"), null, "超上限没有淘汰最老的");
  assert.ok(recallWindow(cache, "k0"), "命中触碰之后仍被误淘汰");

  forgetWindow(cache, "k0");
  assert.equal(recallWindow(cache, "k0"), null, "forgetWindow 没删掉");

  // 翻过上百页的长会话不缓存（几 MB 正文挂内存里不值当，而且只存后半段会让窗口起点对不上）
  const big = new Map();
  rememberWindow(big, "big", {
    rows: Array.from({ length: WINDOW_CACHE_ROWS_MAX + 1 }, (_, i) => ({ id: String(i) })),
    segments: [], windowFrom: 1,
  });
  assert.equal(recallWindow(big, "big"), null, "超长窗口不该缓存");
});

test("回看缓存接到切会话：先画缓存再对账；对账期间本地动过就作废", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const enter = app.slice(app.indexOf("async function enterSession"), app.indexOf("async function boot"));
  const load = app.slice(app.indexOf("async function loadWindow"), app.indexOf("async function reloadWindow"));

  assert.match(enter, /const cached = recallWindow\(windowCache, id\)/, "切会话没查回看缓存");
  assert.ok(enter.indexOf("recallWindow(windowCache, id)") < enter.indexOf("await loadWindow"), "缓存命中时仍然先等网络");
  assert.match(enter, /loadingSession\.value = false; \/\/ 一屏已经在了/, "缓存命中还挂着骨架");
  assert.match(enter, /reloadWindowKeepingScroll\(\{ onlyIfUnchanged: true \}\)/, "缓存命中后没有后台对账（或没带防覆盖闸）");
  assert.match(load, /rememberWindow\(windowCache, sid/, "loadWindow 成功后没把这一屏记进缓存");
  assert.match(load, /onlyIfUnchanged && \(rev !== localRev \|\| pendingSends > 0\)/, "后台对账没有和本地动作/在飞发送对账");
  assert.match(app, /localRev\+\+/, "本地动作没有推进 localRev");
  assert.match(app, /forgetWindow\(windowCache, id\)/, "删会话没清它的缓存");
});

// 弱网兜底（2026-11-05）：GET 有超时、网络层失败重试一次；POST 不套这个上限
// （刷新模型/导入这类会打上游长活，套 20 秒会把正常的长请求剪断）。
test("弱网：GET 带超时且网络失败重试一次，POST 不套上限", async () => {
  const seen = [];
  let calls = 0;
  const fetchFn = async (url, opts) => {
    seen.push({ url, opts });
    calls++;
    if (calls === 1) throw new TypeError("fetch failed");
    return { status: 204, ok: true, json: async () => null };
  };
  const api = createApi({ base: "http://x/api/v1", fetchFn });
  await api.listSessions();
  assert.equal(seen.length, 2, "GET 网络失败没有重试一次");
  assert.ok(seen[0].opts.signal, "GET 没有超时信号");
  assert.ok(seen[1].opts.signal, "重试丢了超时信号");
  await api.sendMessage("s1", "hi");
  assert.equal(seen[seen.length - 1].opts.signal, undefined, "POST 不该套 GET 超时");
});

// 悬停/触摸预取 + 开屏并行（2026-11-05）：
//   · 桌面停 140ms 才算意图（扫过列表不乱发）；触屏/键盘立即；
//   · 同一屏单飞（悬停后马上点下去只发一遍）；并发最多 3；
//   · boot 的探活与首批 chat/sessions 并行，且 warm 用的是**第一个候选**（不是 apiBase() 的 8787 默认）。
test("悬停预取与开屏并行：意图触发、单飞、上限、warm 用首选候选", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const client = readFileSync(join(SRC, "api", "client.ts"), "utf8");

  assert.match(bar, /@pointerenter="prefetchHover\(s, \$event\)"/, "会话行没有悬停预取");
  assert.match(bar, /setTimeout\(\(\) => emit\("prefetch", s\.id\), 140\)/, "悬停没有 140ms 意图判定");
  assert.match(bar, /@touchstart\.passive="prefetchNow\(s\)"/, "触屏没有预取");
  assert.match(bar, /@focusin="prefetchNow\(s\)"/, "键盘 focus 没有预取");
  assert.match(app, /@prefetch="prefetchSession"/, "App 没接预取事件");
  assert.match(app, /function prefetchSession\(id\)/, "没有 prefetchSession");
  assert.match(app, /PREFETCH_MAX_ACTIVE/, "预取没有并发上限");
  assert.match(app, /const tailInflight = new Map\(\)/, "同一屏没有单飞（悬停后马上点会发两遍）");
  assert.match(app, /const warmApi = createApi\(\{ base: firstBase \}\)/, "boot 没有并行预热首批数据");
  assert.match(app, /if \(winner === firstBase\) \{\s*\[chatCfg, sessions\] = await warm;/, "warm 结果没被用上（或候选没对上就吃了错误基址的数据）");
  assert.match(client, /timeout = 8000/, "探活没有超时（黑洞地址会把启动按住）");
  assert.match(client, /GET_TIMEOUT_MS = 20000/, "GET 没有超时上限");
});

// MD3 改版（2026-11-05 用户定：「目标是完美的 MD3 风格 + 动画 + 加载效果」）。
// 先落左侧会话栏：token 层先立起来，后面的页面才有一致语言可复用。
test("MD3 token 层：颜色/形状/动效/字体角色都在，三套主题各有角色", () => {
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  for (const token of ["--md-primary", "--md-on-primary-container", "--md-secondary-container",
    "--md-surface-container-low", "--md-on-surface-variant", "--md-outline-variant", "--md-error"]) {
    assert.ok(theme.includes(`${token}:`), `缺 MD3 颜色角色 ${token}`);
  }
  for (const token of ["--md-shape-full", "--md-shape-lg", "--md-ease-emphasized", "--md-dur-300",
    "--md-state-hover", "--md-type-label-large", "--md-elev-2"]) {
    assert.ok(theme.includes(`${token}:`), `缺 MD3 工艺 token ${token}`);
  }
  // auto 的语义色跟随系统；MD3 角色本身由 Vuetify light/dark 两档提供。
  assert.match(theme, /html\[data-theme="auto"\]\s*\{[\s\S]*--tg-bg:/, "auto 没有语义色");
  assert.match(theme, /@media \(prefers-color-scheme: light\)[\s\S]*html\[data-theme="auto"\]/, "auto 不跟随系统亮色");
  // 字体角色必须是 em（跟随 --app-text-scale），不能写 px
  assert.ok(!/--md-type-[a-z-]+:\s*\d+px/.test(theme), "MD3 字体角色写成了 px（界面缩放会对它失效）");
});

test("左栏 MD3：分段按钮 / extended FAB / 48dp 行 / 状态层 / MD3 忙碌环", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  // 分段按钮（对话/生图）与 extended FAB
  assert.match(css, /\.session-mode-tabs \{[^}]*border:\s*1px solid var\(--md-outline\)/, "模式切换不是 MD3 分段按钮");
  assert.match(css, /\.session-mode-tabs \.v-btn\.v-btn--active \{[^}]*background:\s*var\(--md-secondary-container\)/, "分段按钮选中态不是 secondary-container");
  assert.match(css, /#session-new \{[^}]*background:\s*var\(--md-primary-container\)/, "新建不是 MD3 extended FAB");
  assert.match(css, /#session-new \{[^}]*box-shadow:\s*var\(--md-elev-1\)/, "FAB 没有 elevation 1");
  assert.match(css, /#session-new:hover \{[^}]*box-shadow:\s*var\(--md-elev-2\)/, "FAB 悬停没有升到 elevation 2");

  // 行：48dp、full 圆角、状态层、选中 tonal
  assert.match(css, /\.session-item \{[^}]*min-height:\s*3\.2em/, "会话行不是 48dp");
  assert.match(css, /\.session-item::after \{[^}]*opacity:\s*0/, "会话行没有状态层");
  assert.match(css, /\.session-item:hover::after \{[^}]*--md-state-hover/, "没有 MD3 hover 状态层");
  assert.match(css, /\.session-item\.sel \{[^}]*background:\s*var\(--md-secondary-container\)/, "选中行不是 secondary-container");

  // 忙碌环：SVG + 旋转 + dash（MD3 indeterminate），reduced-motion 停转
  assert.match(bar, /<svg[\s\S]{0,200}class="session-busy"/, "忙碌态不是 SVG 环");
  assert.match(bar, /session-busy-arc/, "忙碌环没有 dash 弧");
  assert.match(css, /@keyframes session-busy-dash/, "忙碌环没有 dash 动画");
  assert.match(css, /prefers-reduced-motion[\s\S]{0,400}session-busy[\s\S]{0,160}animation:\s*none/, "reduced-motion 下没关忙碌环");

  // 加载：骨架 + 顶部 linear progress，且 App 真的喂了状态
  assert.match(bar, /v-if="loading && !sessions\.length" class="session-skeleton"/, "首次加载没有骨架");
  assert.match(css, /@keyframes session-skeleton-shimmer/, "骨架没有 shimmer");
  assert.match(bar, /v-if="refreshing" class="md-linear-progress"/, "后台刷新没有 linear progress");
  assert.match(css, /@keyframes md-linear-progress/, "linear progress 没有动画");
  assert.match(css, /@keyframes session-item-in/, "列表项没有进场动画");
  assert.match(bar, /v-html="ICONS\.plus"/, "新建按钮没有用图标（还是文字加号）");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /const sessionsLoading = ref\(true\)/, "App 没有首次加载态");
  assert.match(app, /const sessionsRefreshing = ref\(false\)/, "App 没有后台刷新态");
  assert.match(app, /:loading="sessionsLoading"/, "没把加载态传给左栏");
  assert.match(app, /:refreshing="sessionsRefreshing"/, "没把刷新态传给左栏");

  // 浮板也是 MD3 menu
  assert.match(css, /\.session-menu \{[^}]*background:\s*var\(--md-surface-container\)/, "会话浮板不是 MD3 menu");
  assert.match(css, /\.session-menu \{[^}]*border-radius:\s*var\(--md-shape-xs\)/, "MD3 menu 圆角不是 4dp");
});

// MD3 第二刀（2026-11-05）：顶栏 + 输入区。
test("顶栏 MD3：surface 容器 / 40dp 文本按钮 / MD3 菜单 / 两段 tab indicator", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const header = css.match(/#chat-col > header \{(?=[^}]*position:\s*absolute)[^}]*\}/)?.[0] ?? "";
  assert.match(header, /background:\s*var\(--md-surface-container-low\)/, "顶栏不是 MD3 surface 容器");
  assert.match(css, /#chat-col > header \{ min-height:\s*2\.65em; height:\s*2\.65em; padding:\s*0\.15em 0\.6em;/, "顶栏没有再收窄到 2.65em");
  assert.match(header, /box-shadow:\s*var\(--shadow-top\)/, "顶栏丢了向下阴影（用户 2026-11-01 定）");
  assert.match(css, /\.model-btn \{[^}]*min-height:\s*2em/, "模型按钮没有收窄到 2em");
  assert.match(css, /\.model-btn \{[^}]*border-radius:\s*var\(--md-shape-full\)/, "模型按钮不是 full 圆角");
  assert.match(css, /\.agent-menu \{[^}]*background:\s*var\(--md-surface-container\)/, "菜单不是 MD3 surface-container");
  assert.match(css, /\.agent-menu \{[^}]*border-radius:\s*var\(--md-shape-xs\)/, "菜单不是 4dp 圆角");
  assert.match(css, /@keyframes md-menu-in/, "菜单没有进场动画");
  assert.match(css, /\.model-search \{[^}]*border-radius:\s*var\(--md-shape-full\)/, "模型搜索不是 MD3 搜索栏");
  // 全局 input 基线特异性 0,4 会压掉单类选择器 —— 必须有一条 `#chat-col > header` 作用域的覆盖。
  assert.match(css, /#chat-col > header \.model-search \{[^}]*border-radius:\s*var\(--md-shape-full\)/, "模型搜索没拿回 MD3 圆角（会被全局 input 基线压掉）");
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  assert.match(top, /@keydown\.esc="closeMenus"/, "顶栏浮板没有 Esc 统一收起");
  assert.match(top, /function closeMenus\(\)/, "没有 closeMenus");
  assert.match(css, /\.model-item \{[^}]*min-height:\s*3em/, "模型项不是 48dp");
  assert.match(top, /<VTabs[\s\S]*?class="ws-switch workspace-tabs"/, "顶栏工作区不是 VTabs");
  assert.match(css, /#ctx-ring \{[^}]*width:\s*2em/, "上下文环没有收窄到 2em");
  assert.match(css, /\.ctx-ring-track \{[^}]*var\(--md-outline-variant\)/, "环轨道不是 outline-variant");
  assert.match(css, /\.ctx-ring-fill \{[^}]*stroke:\s*var\(--tg-ok\)/, "环的正常色不该丢（绿=正常）");
});

test("输入区：Vuetify 4 MD3 接管 input/send/think", () => {
  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  const thinking = readFileSync(join(SRC, "components/ThinkingMenu.vue"), "utf8");
  const plugin = readFileSync(join(SRC, "plugins/vuetify.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  // 组件：Vuetify 自己的输入/按钮/菜单，不再手写 .composer-box / #send-btn 外形
  assert.match(composer, /<VTextarea[\s\S]*variant="solo-filled"/, "输入框不是 Vuetify MD3");
  assert.match(composer, /<VBtn[\s\S]*id="send-btn"/, "发送键不是 Vuetify VBtn");
  assert.match(composer, /:color="running \? 'error' : 'primary-container'"/, "发送键不是低亮 primary-container");
  assert.match(thinking, /<VMenu[\s\S]*location="top end"/, "思考菜单不是从输入栏向上弹");
  assert.match(thinking, /<VList/, "思考选项不是 Vuetify VList");
  assert.match(thinking, /<VBtn/, "思考入口不是 Vuetify VBtn");

  // 主题：MD3 blueprint + 三套应用色（不是 Vuetify 默认 Material 蓝）
  assert.match(plugin, /blueprint: md3/, "没启用 Vuetify MD3 blueprint");
  assert.match(plugin, /themes: \{ dark: darkTheme, light: lightTheme \}/, "Vuetify 主题没跟上 light/dark");
  assert.match(plugin, /installVuetifyThemeBridge/, "没把 data-theme 桥到 Vuetify");
  assert.match(plugin, /prefers-color-scheme: light/, "auto 没桥到系统亮色");

  // 旧未分层 CSS 不能改写 Vuetify 组件
  assert.match(css, /#app \.v-btn, \.v-overlay-container \.v-btn, #app \.v-field__input, #app \.v-tab \{ all: revert-layer; \}/, "Vuetify 组件还会被旧按钮/输入样式压住");
  assert.ok(!/#composer button \{/.test(css), "旧的 #composer button 样式还留着");
  assert.ok(!/#input \{/.test(css), "旧的 #input 样式还留着");
});

// MD3 第三刀（2026-11-05）：消息区（气泡 / 思考 / 摘要卡 / 操作行 / 代码块）。
test("消息区 MD3：中性灰用户气泡 / 32dp 操作 / 思考容器 / 卡片摘要 / 代码容器", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  assert.match(css, /\.msg\.user \{[^}]*background:\s*var\(--md-surface-container-high\)/, "用户气泡不是 Gemini 中性灰容器");
  assert.match(css, /\.msg\.user \{[^}]*color:\s*var\(--md-on-surface\)/, "用户气泡正文色不是 on-surface");
  assert.match(css, /\.msg\.user \{[^}]*border-radius:\s*var\(--md-shape-lg\) var\(--md-shape-lg\) var\(--md-shape-xs\)/, "用户气泡不是 16dp/4dp 尾巴圆角");
  assert.match(css, /\.msg-ops \.v-btn \{[^}]*width:\s*2em[^}]*height:\s*2em/, "操作键不是 32dp VBtn");
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  assert.match(bubble, /<VBtn[^>]*variant="text"[^>]*title="改正文"/, "操作键不是 VBtn");
  assert.match(css, /\.msg \.think \{[^}]*background:\s*var\(--md-surface-container-low\)/, "思考折叠不是 MD3 容器");
  assert.match(css, /\.sum-block \{[^}]*background:\s*var\(--md-surface-container-low\)/, "摘要卡不是 MD3 卡片");
  assert.match(css, /\.sum-block \{[^}]*box-shadow:\s*var\(--md-elev-1\)/, "摘要卡没有 elevation 1");
  assert.match(css, /\.msg \.text code \{[^}]*background:\s*var\(--md-surface-container-high\)/, "行内代码不是 MD3 容器色");
  assert.match(css, /\.msg \.text pre \{[^}]*border:\s*1px solid var\(--md-outline-variant\)/, "代码块没有 MD3 描边");
  assert.match(css, /\.msg \.typing span \{[^}]*background:\s*var\(--md-primary\)/, "等首字节的三点不是 primary");
  assert.match(css, /\.stream-cursor \{[^}]*background:\s*var\(--md-primary\)/, "流式光标不是 primary");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /<VBtn[\s\S]*?id="load-more"[\s\S]*?variant="outlined"/, "加载更多不是 VBtn outlined");
});

// MD3 第四刀（2026-11-05）：设置页 + 浮层 + 全局控件。
test("设置页 MD3：segmented tab / 平铺行 / 表格 / 字段标签", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.ok(!/\.settings-nav/.test(css), "旧 TabBar 的 .settings-nav 样式还留着");
  assert.match(css, /\.settings-cols \.panel h3::before \{[^}]*var\(--md-primary\)/, "h3 竖条不是 primary");
  assert.match(css, /\.settings-cols \.panel \.field, \.settings-cols \.panel \.row \{[^}]*var\(--md-outline-variant\)/, "设置行分隔线不是 outline-variant");
  assert.match(css, /#settings-view \.field > span \{[^}]*var\(--md-on-surface-variant\)/, "字段标签不是 on-surface-variant");
  assert.match(css, /#settings-view \.plist th[^{]*\{[^}]*var\(--md-on-surface-variant\)/, "表头不是 on-surface-variant");
  assert.match(css, /\.settings-cols \.panel table\.plist \{[^}]*var\(--md-surface-container-low\)/, "表格不是 MD3 容器");
});

test("浮层 MD3：dialog / menu / snackbar / banner，全局按钮与字段", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /button \{[^}]*background:\s*var\(--md-secondary-container\)/, "默认按钮不是 MD3 tonal");
  assert.match(css, /button \{[^}]*border-radius:\s*var\(--md-shape-full\)/, "按钮不是 full 圆角");
  assert.match(css, /button\.primary \{[^}]*background:\s*var\(--md-primary\)/, "主按钮不是 filled primary");
  assert.match(css, /button\.danger \{[^}]*color:\s*var\(--md-error\)/, "危险按钮不是 error 色");
  assert.match(css, /input:not\(\[type="checkbox"\]\)[^{]*\{[^}]*border:\s*1px solid var\(--md-outline\)/, "输入框不是 MD3 outlined 字段");
  assert.match(css, /input:not\(\[type="checkbox"\]\)[^{]*:focus[^{]*\{[^}]*var\(--md-primary\)/, "输入框 focus 不是 primary");
  const appearance = readFileSync(join(SRC, "components", "AppearancePane.vue"), "utf8");
  assert.match(appearance, /<VSwitch[\s\S]*?color="primary"/, "开关开态不是 primary");
  assert.match(css, /\.modal-box \{[^}]*background:\s*var\(--md-surface-container-high\)[^}]*border-radius:\s*var\(--md-shape-xl\)/, "对话框不是 MD3 dialog");
  assert.match(css, /\.modal-box \{[^}]*box-shadow:\s*var\(--md-elev-3\)/, "对话框没有 elevation 3");
  assert.match(css, /\.palette, \.picker \{[^}]*background:\s*var\(--md-surface-container\)/, "命令面板不是 MD3 菜单面");
  assert.match(css, /\.palette-item, \.picker-item \{[^}]*min-height:\s*3em/, "命令项不是 48dp");
  assert.match(css, /\.palette-item\.sel, \.picker-item\.sel \{[^}]*secondary-container/, "选中项不是 secondary-container");
  assert.match(css, /\.toast \{[^}]*background:\s*var\(--md-inverse-surface\)/, "toast 不是 MD3 snackbar");
  assert.match(css, /\.toast::before/, "toast 没有语义点");
  assert.match(css, /#update-bar \{[^}]*position:\s*relative/, "更新条不是顶部独立占位");
  assert.match(css, /#update-bar \{[^}]*width:\s*100%/, "更新条没有占满顶部左右宽度");
  assert.match(css, /#update-bar \{[^}]*background:\s*var\(--md-primary-container\)/, "更新条不够显眼（不是 primary-container）");
  assert.match(css, /#update-bar \{[^}]*margin:\s*0;/, "更新条没有贴边");
  assert.match(css, /@keyframes md-dialog-in/, "对话框没有进场动画");
});

// 2026-11-11 用户报：上下文工作区里载荷与状态两个 tab 不能上下滚，压缩可以。
// 根因是这两个 pane 只写了 overscroll-behavior、没写 overflow-y；压缩的滚动者是最内层
// `.compact-preview-pane`，所以只有它正常。
test("上下文工作区：载荷/状态面板自己滚，压缩交给最内层 pane", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#payload-pane,\s*#state-view\s*\{[^}]*overflow-y:\s*auto/, "载荷/状态没有自己的纵向滚动");
  assert.match(css, /#compact-tree\s*\{[^}]*overflow:\s*hidden/, "压缩面板不该自己滚（内层 pane 才是滚动者）");
});

// MD3 第五刀（2026-11-06）：上下文工作区（载荷 / 状态 / 压缩 / 分段 / 修洞）。
test("上下文工作区 MD3：primary tabs / 载荷行 / 状态 / 压缩树 / 高级压缩", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  const bar = readFileSync(join(SRC, "components", "PayloadBar.vue"), "utf8");
  assert.match(bar, /<VTabs[\s\S]*?class="context-tabs"/, "上下文 tab 不是 VTabs");
  assert.match(css, /\.context-tabs \.v-tab \{/, "找不到上下文 tab 规则");

  assert.match(css, /\.pv-head \{[^}]*min-height:\s*2\.5em/, "载荷行头不是 40dp");
  assert.match(css, /\.pv-head:hover \{[^}]*--md-on-surface[^}]*6%/, "载荷行头没有状态层");
  assert.match(css, /\.pv-role \{[^}]*font-size:\s*var\(--md-type-label-small\)/, "role 标签不是 label-small");
  assert.match(css, /\.pv-content \{[^}]*font-size:\s*var\(--md-type-body-medium\)/, "载荷正文不是 body-medium");

  assert.match(css, /#state-view h4 \{[^}]*color:\s*var\(--md-primary\)/, "状态表名不是 primary");
  assert.match(css, /#state-view \.state-row \{[^}]*var\(--md-outline-variant\)/, "状态行分隔线不是 outline-variant");

  assert.match(css, /\.ctree-row \{[^}]*border-radius:\s*var\(--md-shape-sm\)/, "压缩树行不是 MD3 圆角");
  assert.match(css, /\.ctree-row\.sum \.ctree-bar \{[^}]*var\(--md-primary\)/, "摘要容量条不是 primary 色调");
  assert.match(css, /\.ctree-undo \{[^}]*width:\s*2em[^}]*border-radius:\s*var\(--md-shape-full\)/, "撤销键不是 32dp 圆形");

  assert.match(css, /\.adv-progress \{[^}]*height:\s*4px[^}]*var\(--md-surface-container-highest\)/, "高级压缩进度条不是 MD3");
  assert.match(css, /\.adv-progress i \{[^}]*background:\s*var\(--md-primary\)/, "进度填充不是 primary");
  assert.match(css, /\.adv-holes \{[^}]*background:\s*var\(--md-error-container\)/, "空洞提示不是 error-container");
  assert.match(css, /\.adv-chunk \{[^}]*min-height:\s*2\.5em/, "chunk 行不是 40dp");
  assert.match(css, /\.adv-dot\.running \{[^}]*background:\s*var\(--md-primary\)/, "运行中点不是 primary");
  assert.match(css, /\.ws-draft-note \{[^}]*background:\s*var\(--md-surface-container-low\)/, "草稿提示不是 MD3 容器");
});

// MD3 快修（2026-11-06）：草稿欢迎页 / 空态 / 滚动条 / 展开淡入。
test("MD3 快修：草稿欢迎 / 空态 / 滚动条 / 展开动画", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /\.draft-welcome h2 \{[^}]*font-size:\s*var\(--md-type-title-large\)/, "草稿标题不是 MD3 title-large");
  assert.match(css, /\.dw-hint kbd \{[^}]*var\(--md-surface-container-high\)/, "kbd 不是 MD3 容器");
  assert.match(css, /\.empty \{[^}]*var\(--md-on-surface-variant\)/, "空态不是 on-surface-variant");
  assert.match(css, /\*::-webkit-scrollbar-thumb \{[^}]*var\(--md-outline-variant\)/, "滚动条不是 outline-variant");
  assert.match(css, /@keyframes md-fade-in/, "展开没有淡入动画");
  assert.match(css, /prefers-reduced-motion[\s\S]{0,200}md-fade-in|md-fade-in[\s\S]{0,400}prefers-reduced-motion/, "淡入没有 reduced-motion 兜底");
});

// MD3 波纹（2026-11-06 用户定）与密度（用户：「padding 太浪费」）。
test("波纹：触点 → 圆（纯函数），委托安装一次，reduce 下不做", () => {
  const spec = rippleSpec({ width: 100, height: 40, left: 0, top: 0 }, 10, 20);
  assert.equal(Math.round(spec.size), 220);  // 长边 100 * 2.2
  assert.equal(Math.round(spec.left), -100); // 10 - 110
  assert.equal(Math.round(spec.top), -90);   // 20 - 110
  assert.ok(RIPPLE_SELECTOR.includes(".session-item"), "会话行没接波纹");
  assert.ok(RIPPLE_SELECTOR.includes("button"), "按钮没接波纹");

  const main = readFileSync(join(SRC, "..", "src", "main.ts"), "utf8");
  const ripple = readFileSync(join(SRC, "utils", "ripple.ts"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  assert.match(main, /installRipple\(\)/, "main.js 没装波纹");
  assert.match(ripple, /pointerdown/, "没有指针按下入口");
  assert.match(ripple, /prefers-reduced-motion/, "reduce 下没禁用");
  assert.match(css, /@keyframes md-ripple/, "没有波纹动画");
  assert.match(css, /\.md-ripple-host \{[^}]*overflow:\s*hidden/, "波纹宿主没裁剪");
  assert.match(ripple, /md-ripple-layer/, "没有独立裁剪层");
  assert.match(css, /\.md-ripple-host > \.md-ripple-layer \{[^}]*border-radius:\s*inherit/, "裁剪层没继承宿主圆角");
  assert.match(css, /\.md-ripple-host/, "Vuetify 键没有波纹宿主");
  assert.match(css, /prefers-reduced-motion[\s\S]{0,160}\.md-ripple/, "reduce 下没兜底");
});

test("密度：行高/顶栏/FAB/输入/设置/消息间距都收了一档", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#sessions-bar \.session-item \{[^}]*min-height:\s*2\.4em/, "会话行没收到紧凑密度（2.4em）");
  assert.match(css, /#sessions-bar #session-new \{[^}]*min-height:\s*3em/, "FAB 没收到 48dp");
  assert.match(css, /#chat-col > header \{ min-height:\s*2\.65em/, "顶栏没有再收窄到 2.65em");
  assert.match(css, /\.messages-inner \{[^}]*gap:\s*0\.75em/, "消息间距没放大到 0.75em");
  assert.match(css, /\.messages-inner \{[^}]*padding-top:\s*3\.15em/, "消息列顶部留白没跟着顶栏收");
  assert.match(css, /\.settings-cols \.panel \.field, \.settings-cols \.panel \.row \{[^}]*padding:\s*0\.45em/, "设置行没收到 0.45em");
  assert.match(css, /\.agent-item, \.model-item \{[^}]*min-height:\s*2\.5em/, "菜单项没收到 40dp");
});

test("左栏底：会话/渠道/设置上方有一条 outline-variant 分隔线", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#sessions-bar \.bar-foot \{[^}]*border-top:\s*1px solid var\(--md-outline-variant\)/, "底部管理入口上方没有分隔线");
});

test("会话分组标题：更大间距 + 非首组淡分隔线", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  // 以文件末尾最后一次定义为准：同特异性下它就是真正生效的规则。
  const tail = css.slice(css.lastIndexOf("#sessions-bar .session-group-head {"));
  assert.match(tail, /^#sessions-bar \.session-group-head \{ padding: 1em 0\.75em 0\.3em/, "分组标题内边距不是最终生效值");
  assert.match(tail, /#sessions-bar \.session-group-head:first-child \{ padding-top: 0\.45em/, "首组顶部留白不是最终生效值");
  assert.match(tail, /#sessions-bar \.session-group-head:not\(:first-child\) \{[^}]*margin-top:\s*1em/, "组间外边距没有放大到 1em");
  assert.match(tail, /#sessions-bar \.session-group-head:not\(:first-child\) \{[^}]*border-top:\s*1px solid color-mix\(in srgb, var\(--md-outline-variant\) 55%/, "非首组没有淡分隔线");
});


// 思考档位（2026-11-08）：Session 字段 + 输入栏正上方按钮区的 Vuetify 菜单。
test("思考档位：Session 字段 + 输入栏上方 Vuetify 菜单", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(app, /:thinking="currentThinking"/, "Composer 没接思考档位");
  assert.match(app, /@select-thinking="setThinking"/, "Composer 没把档位变化接回来");
  assert.match(app, /thinking_level: draftPrefs\.thinking_level/, "草稿预演没带思考档位");
  assert.match(app, /if \(draftPrefs\.thinking_level\) body\.thinking_level = draftPrefs\.thinking_level/, "建会话没带思考档位");

  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  assert.ok(!/ThinkingMenu|thinking-options|select-thinking/.test(top), "顶栏里还留着思考菜单");

  const composer = readFileSync(join(SRC, "components", "Composer.vue"), "utf8");
  assert.match(composer, /class="composer-toolbar"/, "输入栏上方没有按钮区");
  assert.match(composer, /<ThinkingMenu/, "按钮区里没有思考档位");
  const toolbarAt = composer.indexOf('class="composer-toolbar"');
  const textareaAt = composer.indexOf("<VTextarea", toolbarAt);
  assert.ok(toolbarAt > 0 && textareaAt > toolbarAt, "按钮区不在输入框正上方");

  const thinking = readFileSync(join(SRC, "components/ThinkingMenu.vue"), "utf8");
  assert.match(thinking, /<VMenu[\s\S]*location="top end"[\s\S]*origin="bottom end"/, "菜单没从输入栏向上弹");
  assert.match(thinking, /<VListItem/, "菜单项不是 Vuetify 的 MD3 list item");
  assert.match(thinking, /推理等级：/, "按钮没有显示「推理等级：…」");
  assert.match(thinking, /thinkingLabel/, "按钮没用中文档位名");
  assert.match(thinking, /option\.label/, "菜单项没有中文名");
  assert.match(thinking, /thinking-raw/, "菜单项没有附上原始 ASCII 档位");

  const types = readFileSync(join(SRC, "api/types.ts"), "utf8");
  assert.match(types, /thinking_level: string/, "Session 类型没带 thinking_level");
  assert.match(types, /thinking_levels: string\[\]/, "模型清单没带思考能力");
});


// 设置界面控件（2026-11-06 用户定）：可见按钮/下拉/多行输入全部 Vuetify；
// 只允许保留隐藏的 file input（原生文件框的触发器是 VBtn）。
test("设置界面：可见控件全部 Vuetify，旧 ToggleSwitch/TabBar 已删", () => {
  const files = ["SettingsModal", "AgentEditor", "ProviderEditor", "SegmentEditor", "SegmentList", "AppearancePane", "SessionSettings"];
  for (const f of files) {
    const code = readFileSync(join(SRC, "components", `${f}.vue`), "utf8")
      .replace(/<!--[\s\S]*?-->/g, "")
      .replace(/<input\b[^>]*type="file"[^>]*>/g, "");
    assert.ok(!/<button\b/.test(code), `${f}.vue 还有原生 button`);
    assert.ok(!/<select\b/.test(code), `${f}.vue 还有原生 select`);
    assert.ok(!/<textarea\b/.test(code), `${f}.vue 还有原生 textarea`);
    assert.ok(!/<input\b/.test(code), `${f}.vue 还有可见的原生 input`);
  }
  assert.ok(!existsSync(join(SRC, "components", "ToggleSwitch.vue")), "旧 ToggleSwitch.vue 还在");
  assert.ok(!existsSync(join(SRC, "components", "TabBar.vue")), "旧 TabBar.vue 还在");
});

test("主题：Vuetify theme 是唯一色值源，theme.css 只做映射", () => {
  const plugin = readFileSync(join(SRC, "plugins", "vuetify.ts"), "utf8");
  const theme = readFileSync(join(SRC, "assets", "theme.css"), "utf8");
  assert.match(plugin, /const darkTheme = \{[\s\S]*?background: "#131314"/, "暗色底不是 Gemini #131314");
  assert.match(plugin, /const darkTheme = \{[\s\S]*?"surface-container": "#1e1f20"/, "暗色容器不是 Gemini #1e1f20");
  assert.match(plugin, /const darkTheme = \{[\s\S]*?primary: "#a8c7fa"/, "暗色强调色不是 Gemini #a8c7fa");
  assert.match(theme, /html\[data-theme="dark"\] \{[\s\S]*?--tg-bg: #131314[\s\S]*?--tg-link: #a8c7fa/, "theme.css 的暗色语义槽没跟上 Gemini");
  assert.match(theme, /--md-primary:\s*rgb\(var\(--v-theme-primary\)\)/, "theme.css 没把强调色映射到 Vuetify");
  assert.ok(!/--md-primary:\s*#d0bcff/.test(theme), "theme.css 还留着旧的紫色硬编码");
});

// 2026-11-06 用户报：左下角 会话→渠道→设置 切换时有 tab 滑入，且「渠道→设置」方向是反的。
// 根因是三个管理页共用了设置内部的 VTabsWindow，window item 顺序与底部入口顺序不一致。
test("会话/渠道管理页不跑设置 tab 滑入（页面切换不是 tab）", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /<VTabsWindow[^>]*:key="props\.only \|\| 'settings'"/, "管理页没有按 only 重挂载 window，tab 滑入还会反向");
});

// 设置页签的 window item 顺序必须与 SETTINGS_TABS 一致：Vuetify 的滑入方向按
// window item 注册顺序算，不是按 tab 标签顺序算（2026-11-06 用户报「行为不太对」）。
test("设置页签顺序：window item 与标签顺序一致（提示词在外观前）", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const items = [...settings.matchAll(/<VTabsWindowItem value="([^"]+)"/g)].map((m) => m[1]);
  const visible = items.filter((id) => ["chat", "agent", "prompt", "appearance", "tavern", "update"].includes(id));
  assert.deepEqual(visible, ["chat", "agent", "prompt", "appearance", "tavern", "update"], "设置 window item 顺序与标签顺序不一致，滑入方向会反");
});


// 酒馆（2026-11-06 用户定）：第五个设置 tab，专门放 ST 预设/角色卡/聊天记录的导入与转换。
test("酒馆：第五个设置 tab，ST 聊天记录导入搬到这里", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  assert.match(settings, /<VTabsWindowItem value="tavern">/, "没有酒馆 pane");
  const tavern = settings.slice(settings.indexOf('<VTabsWindowItem value="tavern">'), settings.indexOf('<VTabsWindowItem value="provider">'));
  assert.match(tavern, /SillyTavern 聊天记录/, "酒馆里没有 ST 聊天记录导入");
  assert.match(tavern, /@change="importSTFile"/, "酒馆没接 ST 导入");
  assert.match(tavern, /预设|角色卡/, "酒馆没说明预设/角色卡转换职责");
  const session = settings.slice(settings.indexOf('<VTabsWindowItem value="session">'), settings.indexOf('<VTabsWindowItem value="agent">'));
  assert.ok(!/SillyTavern/.test(session), "ST 导入还留在会话管理页");
});

// 酒馆原始存档（2026-11-06）：只存字节；前端上传/下载都不走 JSON 重写。
test("酒馆原始 JSON：上传走原始 body，导出走 Blob（不 JSON 重写）", () => {
  const client = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const dl = readFileSync(join(SRC, "utils", "download.ts"), "utf8");
  assert.match(client, /uploadTavernPreset:\s*\(file: File\)/, "没有原始上传方法");
  assert.match(client, /callBytes<TavernPreset>\("POST", `\/tavern\/presets\?name=/, "上传没走 raw body");
  const dlSrc = client.slice(client.indexOf("downloadTavernPreset:"));
  assert.match(dlSrc.slice(0, 600), /return res\.blob\(\)/, "下载没保持 Blob");
  assert.match(settings, /importTavernPresetFile/, "酒馆没有原始 JSON 导入");
  assert.match(settings, /downloadTavernPreset\(p\)/, "酒馆没有原始导出");
  assert.match(settings, /原始字节/, "界面没说明原样存储");
  assert.match(dl, /export function downloadBlob/, "没有 Blob 下载工具");
});

// 2026-11-11 用户定：ST 预设的「查看」与「提取」合并成一个弹窗两个 tab；
// 每个有效条目可导入为提示词片段，并记下来源预设。删预设时问是否一并删关联片段。
test("ST 预设内容：解析预览/有效内容/原始 JSON 合并成一个面板", () => {
  const settings = readFileSync(join(SRC, "components", "SettingsModal.vue"), "utf8");
  const inspector = readFileSync(join(SRC, "components", "TavernInspector.vue"), "utf8");
  const client = readFileSync(join(SRC, "api", "client.ts"), "utf8");
  assert.ok(!/\bviewingPreset\b|\beffectivePreset\b|extractTavernPreset|\bviewTavernPreset\b/.test(settings),
    "旧的查看/提取两个弹窗还留着");
  assert.match(settings, /<TavernInspector/, "没有接合并后的内容面板");
  assert.match(inspector, /role="tab"[\s\S]*有效内容[\s\S]*role="tab"[\s\S]*解析预览[\s\S]*role="tab"[\s\S]*原始 JSON/, "三个 tab 没接全");
  assert.ok(!/previewingPreset|previewTavernPreset/.test(settings), "旧的解析预览弹窗还留着");
  assert.match(inspector, /preset_id:\s*props\.preset\.id/, "导入片段没记来源预设");
  assert.match(inspector, /dynamicMacro/, "没有拦动态宏（静态段会留下不替换的占位符）");
  assert.match(inspector, /openEntries/, "有效条目没有折叠状态");
  assert.match(inspector, /v-if="entryOpen\(entry, index\)"/, "正文不是展开时才挂进 DOM");
  const entry = inspector.slice(inspector.indexOf('class="tavern-effective-entry"'), inspector.indexOf("</article>", inspector.indexOf('class="tavern-effective-entry"')));
  assert.ok(entry.indexOf("导入为片段") >= 0 && entry.indexOf("导入为片段") < entry.indexOf("tavern-effective-content"),
    "导入按钮还在正文下面，不在标题右侧");
  assert.match(inspector, /createSegment\(/, "没有导入为片段的动作");
  assert.match(client, /deleteTavernPreset:\s*\(id: string, segments\?: "delete" \| "keep"\)/, "删预设没有 segments 参数");
  assert.match(settings, /deleteTavernPreset\(p\.id, mode\)/, "界面没有把 keep/delete 传给后端");
  assert.match(settings, /一并删除它们吗/, "删预设没有提醒关联片段");
  assert.match(settings, /segmentSource/, "段列表没有显示导入来源");
});

// 2026-11-11 用户定：左上角品牌标志可点击，跳到更新面板；更新页底部介绍项目与作者。
test("品牌标志可点击跳更新；更新页底部有项目介绍与 GitHub 链接", () => {
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const pane = readFileSync(join(SRC, "components", "UpdatePane.vue"), "utf8");
  assert.match(bar, /<button[\s\S]*class="brand"[\s\S]*@click="\$emit\('open-update'\)"/, "品牌标志不是跳更新面板的按钮");
  assert.match(app, /@open-update="\(\) => openManage\('update'\)"/, "App 没接 open-update");
  assert.match(pane, /关于 MicroChat/, "更新页没有项目介绍");
  assert.match(pane, /Zincles/, "没有作者 Zincles");
  assert.match(pane, /DeepSeekV4\.1Flash/, "没有作者 DeepSeekV4.1Flash");
  assert.ok(!/DeepSeekV4\.1Flash（你）/.test(pane), "作者后面不该再带（你）");
  assert.match(pane, /https:\/\/github\.com\/zincles\/microchat/, "没有 GitHub 链接");
});

// 2026-11-07 用户报：列表项里的 `$$…$$` 块解析成 raw `$$`。
// 根因是"缩进 4 格先当代码块保护"把列表续行吞了；缩进代码交给 marked 自己判断。
test("Markdown：列表项里缩进的 $$ 块进入 KaTeX，而不是 <pre><code>", () => {
  const html = renderMarkdown("*   **情况 A**\n    若曲线 $L$ 为 $x=1$：\n    $$\n    \\int_L f(x,y) \\, ds = 1\n    $$\n    注：$a<b$");
  assert.match(html, /<ul>\s*<li>/, "列表项没解析");
  assert.ok(!html.includes("$$"), "raw $$ 还留在输出里");
  assert.ok(!/<pre><code>/.test(html), "列表续行被吞成代码块");
  assert.match(html, /katex-display/, "display 公式没有走 KaTeX");
  assert.match(html, /katex/, "行内公式没有走 KaTeX");
});

// 2026-11-07：中文里 `都**“引号内容”**` 不满足 CommonMark flanking，会露出 raw **。
test("Markdown：中文标点前的 ** 粗体也能解析", () => {
  const html = renderMarkdown('无论是哪一类，计算的核心思想都是**“化曲为直，转化为定积分”**。');
  assert.ok(!html.includes("**"), "还露着 raw **");
  assert.match(html, /<strong>“化曲为直，转化为定积分”<\/strong>/, "中文引号粗体没解析");
});

// 2026-11-07 用户定：左栏用 Vuetify 原生 VNavigationDrawer；VApp 只作 layout provider。
test("左栏：VApp + VNavigationDrawer（桌面是布局列，移动端是抽屉）", () => {
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  const bar = readFileSync(join(SRC, "components", "SessionBar.vue"), "utf8");
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(app, /<VApp>/, "App 根不是 VApp");
  assert.match(bar, /<VNavigationDrawer[^>]*id="sessions-bar"/, "左栏不是 VNavigationDrawer");
  assert.match(app, /<SessionBar[\s\S]*?:collapsed="leftHidden"/, "左栏 collapsed 没跟响应式 leftHidden 绑定");
  assert.match(bar, /:class="\{ collapsed: props\.collapsed \}"/, "VNavigationDrawer 自己没绑 collapsed（外部 classList 会被重渲染抹掉）");
  assert.match(bar, /:model-value="true"[^>]*permanent/, "左栏没有 permanent");
  assert.match(bar, /:scrim="false"/, "左栏用了 Vuetify 自己的遮罩（我们仍有 #scrim-left）");
  assert.match(css, /#sessions-bar\.v-navigation-drawer \{[^}]*position:\s*relative !important/, "桌面抽屉没变成布局列");
  assert.match(css, /#sessions-bar > \.v-navigation-drawer__content \{[^}]*display:\s*flex[^}]*flex-direction:\s*column/, "抽屉内容没接上旧 flex 布局");
  assert.match(css, /#sessions-bar\.collapsed \{[^}]*translateX\(-102%\) !important/, "移动端收起没有滑出（被 inline transform 压住）");
});

test("工具状态：上游 kind/state 翻成一句人话", () => {
  assert.equal(toolRunHint({ kind: "web_search", state: "searching" }), "正在搜索网页…");
  assert.equal(toolRunHint({ kind: "web_search", state: "completed" }), "网页搜索完成，正在整理结果…");
  assert.equal(toolRunHint({ kind: "x_search", state: "searching" }), "正在搜索 X…");
  // xAI 的代码执行事件名是 code_interpreter，工具 id 是 code_execution。
  assert.equal(toolRunHint({ kind: "code_interpreter", state: "interpreting" }), "正在执行代码…");
  assert.equal(toolRunHint({ kind: "code_execution", state: "in_progress" }), "正在调用代码执行…");
  assert.equal(toolRunHint(null), "");
});

test("来源标签：引用序号退回域名，正常标题照用", () => {
  assert.equal(sourceLabel("https://www.example.com/a", "1"), "example.com");
  assert.equal(sourceLabel("https://example.com/a", "Example News"), "Example News");
  assert.equal(sourceLabel("not-a-url"), "not-a-url");
});

test("工具来源：多次搜索合并时按 URL 去重", () => {
  const sources = toolSources([
    { sources: [{ url: "https://a.example/1", title: "A" }, { url: "https://b.example/2" }] },
    { sources: [{ url: "https://a.example/1", title: "A2" }, { url: "https://c.example/3" }] },
  ]);
  assert.deepEqual(sources.map((source) => source.url), [
    "https://a.example/1", "https://b.example/2", "https://c.example/3",
  ]);
  assert.equal(sources[0].title, "A");
  assert.deepEqual(toolSources(null), []);
});

test("工具卡和来源都在正文上方；每个工具卡自己折叠来源", () => {
  const bubble = readFileSync(join(SRC, "components", "MessageBubble.vue"), "utf8");
  const cardAt = bubble.indexOf('class="tool-cards"');
  const textAt = bubble.indexOf('ref="textEl" class="text"');
  assert.ok(cardAt > 0 && textAt > 0);
  assert.ok(cardAt < textAt, "工具卡必须先于正文");
  assert.ok(bubble.includes('<details v-for="(tool, i) in tools"'), "工具卡必须是 details");
  assert.ok(bubble.includes('v-else-if="sources.length"'), "live 来源兜底仍在");
});

test("输入栏的推理等级/工具按钮共用同一套尺寸与风格", () => {
  const css = readFileSync(join(SRC, "assets", "main.css"), "utf8");
  assert.match(css, /\.composer-toolbar \.thinking-btn,\s*\.composer-toolbar \.tools-btn\s*\{/);
  assert.match(css, /\.thinking-label,\s*\.tools-label\s*\{/);
});

test("上下文圆环的请求体覆盖面板：无外层花括号、验证/保存、App 落库", () => {
  const top = readFileSync(join(SRC, "components", "TopBar.vue"), "utf8");
  const app = readFileSync(join(SRC, "App.vue"), "utf8");
  assert.match(top, /value="overrides">请求体覆盖/, "圆环面板没有请求体覆盖 tab");
  assert.match(top, /class="ctx-overrides-input"/, "没有 JSON 输入框");
  assert.match(top, /省略外层花括号/, "没有说明不用花括号");
  assert.match(top, /function parseOverrides\(/, "没有本地解析");
  assert.match(top, /emit\("save-overrides", parsed\)/, "保存没有 emit");
  assert.match(app, /@save-overrides="saveRequestOverrides"/, "App 没有接保存事件");
  assert.match(app, /request_overrides: overrides/, "保存没有 PATCH request_overrides");
});
