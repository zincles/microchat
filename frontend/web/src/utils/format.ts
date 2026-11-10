// 纯函数（无 DOM）：测试直引这里。DOM 构造已迁 MessageBubble.vue。
import type { ContextUsage, DeletionPlan, ModelListItem, ProviderView } from "../api/types.ts";

// —— 模型/Agent 的键与标签（唯一出口，别在组件里手拼） ——
// 模型键 `provider|||upstream_id`：下拉 <option> 的 value 用它（分隔符是 magic 串，解析只在这里）。
export function modelKey(provider?: string | null, model?: string | null): string { return `${provider}|||${model}`; }
export function parseModelKey(v?: string | null): { provider: string; model: string } | null {
  const i = (v ?? "").indexOf("|||");
  if (i < 0) return null;
  return { provider: v!.slice(0, i), model: v!.slice(i + 3) };
}

export interface ModelLike {
  display_name?: string | null;
  name?: string | null;
  upstream_id?: string;
}

// 显示名三级回退（与后端两列同义：用户覆盖 → 上游名 → 上游 id）。
export function modelLabel(m?: ModelLike | null): string { return m?.display_name || m?.name || m?.upstream_id || ""; }

// provider-native tool 的中文名：id 是契约，标签只给界面。未知 id 原样显示（不要悄悄吞掉）。
export const NATIVE_TOOL_LABELS: Record<string, string> = {
  x_search: "X 搜索",
  web_search: "网页搜索",
  web_fetch: "网页抓取",
  code_execution: "代码执行",
};

export function nativeToolLabel(id?: string | null): string {
  const key = String(id ?? "").trim();
  return NATIVE_TOOL_LABELS[key] || key;
}

// nativeToolsLabel：输入栏上方那枚「工具：3」状态。
// 工具数来自模型规则派生的 native_tools；没有就是「工具：无」，不编 0。
export function nativeToolsLabel(ids?: string[] | null): string {
  const count = Array.isArray(ids) ? ids.length : 0;
  return count ? `工具：${count}` : "工具：无";
}

export function nativeToolsTitle(ids?: string[] | null): string {
  const list = Array.isArray(ids) ? ids.filter(Boolean) : [];
  if (!list.length) return "当前模型没有可用的内置工具（可在模型规则里追加）";
  return `内置工具：${list.map(nativeToolLabel).join("、")}`;
}

// toolRunHint：生成过程中“此刻在执行哪个服务端工具”的一句话。
// 后端只给上游原样的 kind/state；翻人话只在这里（与 nativeToolLabel 同一层）。
// xAI 的代码执行事件名是 code_interpreter（工具 id 是 code_execution），两种都认。
export type ToolRun = { kind: string; state: string } | null | undefined;
const TOOL_KIND_LABEL: Record<string, string> = {
  web_search: "网页搜索",
  web_fetch: "网页抓取",
  webfetch: "网页抓取",
  x_search: "X 搜索",
  code_interpreter: "代码执行",
  code_execution: "代码执行",
  file_search: "文件搜索",
  collections_search: "集合搜索",
  image_generation: "图像生成",
  mcp: "MCP 工具",
  shell: "命令行",
  tool_search: "工具搜索",
};
const TOOL_KIND_VERB: Record<string, string> = {
  web_search: "搜索网页",
  web_fetch: "抓取网页",
  webfetch: "抓取网页",
  x_search: "搜索 X",
  code_interpreter: "执行代码",
  code_execution: "执行代码",
  file_search: "搜索文件",
  collections_search: "搜索集合",
  image_generation: "生成图像",
  mcp: "调用 MCP",
  shell: "执行命令",
  tool_search: "搜索工具",
};
// sourceLabel：来源标题；xAI 的 web_search 标题常常只是 "1"（引用序号），
// 那种就退回域名，别在界面上显示一行没有信息量的数字。
export function sourceLabel(url: string, title?: string | null): string {
  const text = String(title ?? "").trim();
  if (text && !/^\d+$/.test(text)) return text;
  try {
    return new URL(url).hostname.replace(/^www\./, "") || url;
  } catch {
    return url;
  }
}

export function toolRunHint(tool?: ToolRun): string {
  const kind = String(tool?.kind ?? "").trim();
  if (!kind) return "";
  const label = TOOL_KIND_LABEL[kind] || kind;
  const verb = TOOL_KIND_VERB[kind] || `调用 ${label}`;
  switch (String(tool?.state ?? "")) {
    case "searching":
    case "interpreting":
      return `正在${verb}…`;
    case "completed":
      return `${label}完成，正在整理结果…`;
    default:
      return `正在调用${label}…`;
  }
}

// providerDisplayName：渠道在界面上的显示名 —— **别名优先**，空则沿用旧回退（name → id）。
// 它只负责显示 / 搜索；会话、模型、路由、请求参数里永远还是 `id`。
export function providerDisplayName(p?: { alias?: string | null; name?: string | null; id?: string | null } | null): string {
  return String(p?.alias ?? "").trim() || String(p?.name ?? "").trim() || String(p?.id ?? "");
}

export interface ModelChoice {
  value: string;
  label: string;
  provider_name: string;
  // alias：渠道的显示别名（空 = 没有）；模型面板的组头优先显示它，但它**不是**键。
  alias?: string;
  upstream_id: string;
  brand?: string;
  icon?: string;
  flat?: number;
  [key: string]: unknown;
}

export interface ModelGroup {
  provider: string;
  providerName?: string;
  items: ModelChoice[];
  [key: string]: unknown;
}

// 下拉项（`/models` 的行 → {value,label}）。
// 2026-10-28：顶栏的模型选择器换成自定义面板之后，每项还带着 `provider_name` / `upstream_id` ——
// 面板要按渠道分组、搜索要能按渠道名与上游 id 命中（label 只是显示名，常常不含渠道信息）。
export function modelOption(m: ModelListItem): ModelChoice {
  return {
    value: modelKey(m.provider, m.upstream_id),
    label: modelLabel(m),
    provider_name: m.provider_name || m.provider,
    // 别名只是显示层；`value` 仍由 provider id + upstream_id 拼成。
    alias: String(m.alias ?? "").trim(),
    upstream_id: m.upstream_id,
    brand: m.brand,
    icon: m.icon,
  };
}
export function agentOption(a: { id: string; name?: string }): { id: string; label: string } {
  return { id: a.id, label: a.name || a.id };
}
// 谁在答（状态行与草稿共用；fallback 两处不同：会话里"未知模型"、草稿"未选模型"）。
export function whoText(provider?: string | null, model?: string | null, fallback = "未知模型"): string {
  return [provider, model].filter(Boolean).join("/") || fallback;
}
// 会话抬头：空标题按草稿/真会话两态回退（TopBar/SessionBar/设置列表/删除确认共用）。
export function sessionTitle(title?: string | null, isDraft = false): string {
  return (title ?? "").trim() || (isDraft ? "新对话" : "新会话");
}
export function formatThinkLabel(reasoningMs?: number | null): string {
  if (reasoningMs == null) return "思考过程";
  return `思考过程（${(reasoningMs / 1000).toFixed(1)}s）`;
}

// phaseText：把后端的相位词翻成人话（面板与悬停共用）。
export function phaseText(phase?: string | null): string {
  const labels: Record<string, string> = { idle: "空闲", pending: "等待响应", streaming: "生成中", error: "出错" };
  return labels[phase ?? ""] ?? "空闲";
}

// formatDuration：耗时说人话（面板与悬停共用）。毫秒只在不到一秒时出现。
export function formatDuration(ms?: number | null): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
  if (ms < 1000) return `${Math.max(0.1, Math.round(ms / 100) / 10)} 秒`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} 秒`;
  const m = Math.floor(ms / 60000);
  const s = Math.round((ms % 60000) / 1000);
  return s ? `${m} 分 ${s} 秒` : `${m} 分钟`;
}

// 状态行：当前助手 + 这一轮在干什么/多久。
// ⚠ 上下文占用**不在这一行**（2026-10-28 用户定）：它挪到右上角那枚小环（TopBar 的 `#ctx-ring`），
// 点开才看明细。所以这里不接 ctx —— 同一个数字住两处，只会让两处都可能过期。
export function formatStatusLine({ phase, elapsedMs, agent }: { phase?: string | null; elapsedMs?: number | null; agent?: string | null }): string {
  // 2026-10-14：中间那格从"渠道/模型"换成"当前助手" —— 模型住顶栏左上（别两处重复），
  // 助手住这儿（顶栏已无 Agent 下拉）。
  const t = elapsedMs != null ? ` ${formatDuration(elapsedMs)}` : "";
  return `${agent ?? "?"}｜${phaseText(phase)}${t}`.trim();
}

// contextRatio：已用 / 输入预算，夹到 0..1（分母缺失或为 0 ⇒ 0）。
// 分母是**输入预算**不是模型上下文（见 contextTitle）—— 这就是"填了 1 000 000 却显示 995904"的来由。
export function contextRatio(ctx?: ContextUsage | null): number {
  const used = Number(ctx?.used_tokens ?? 0);
  const budget = Number(ctx?.budget_tokens ?? 0);
  if (!(budget > 0) || !Number.isFinite(used)) return 0;
  return Math.max(0, Math.min(1, used / budget));
}

// contextPercentText：给人看的占比。
// 一上来就是 344/1.04M 这种小占比 ⇒ 整数会显示成 0%（看起来像"坏了"），所以 10% 以下带一位小数、
// 0.1% 以下单说。**向下取整**（与 formatTokens 同一个理由：别把用量说大）。
export function contextPercentText(ctx?: ContextUsage | null): string {
  const used = Number(ctx?.used_tokens ?? 0);
  if (!(used > 0)) return "0%";
  const r = contextRatio(ctx) * 100;
  if (r < 0.1) return "不到 0.1%";
  if (r < 10) return `${Math.floor(r * 10) / 10}%`;
  return `${Math.floor(r)}%`;
}

// CONTEXT_MIN_ARC：环上那道弧的**最小可见百分比**。
// 344/1.04M ≈ 0.03% ⇒ 四舍五入成 0，环整个消失 —— 那看起来像"坏了"，而不像"几乎没占"。
// 所以只要用了（used > 0）就至少画出这么长一道弧；**文字**照实报（contextPercentText），
// 弧只回答"有没有占用、大概多少"这个粗问题。
export const CONTEXT_MIN_ARC = 2;

// contextArc：环该画多少（0..100 的百分比，直接喂 SVG 的 stroke-dasharray）。
export function contextArc(ctx?: ContextUsage | null): number {
  const used = Number(ctx?.used_tokens ?? 0);
  if (!(used > 0)) return 0;
  return Math.max(CONTEXT_MIN_ARC, Math.min(100, Math.round(contextRatio(ctx) * 100)));
}

// formatTokens：上下文数字的人话格式（2026-10-17 用户点名）：9857 → 9.8K、995904 → 995K、
// 1048576 → 1.04M。**一律向下取整**（不四舍五入）：这两个数里有一个是"我用了多少"，
// 向上取整会把用量说大；而且 995904 报成"996K"会让人以为分母真是 996K ✗。
export function formatTokens(n?: number | null): string {
  if (typeof n !== "number" || !Number.isFinite(n)) return "?";
  const sign = n < 0 ? "-" : "";
  const abs = Math.abs(n);
  const floored = (v: number, digits: number): number => { const f = 10 ** digits; return Math.floor(v * f + 1e-9) / f; };
  const trim = (s: string): string => (s.includes(".") ? s.replace(/\.?0+$/, "") : s);
  if (abs < 1000) return sign + String(abs);
  if (abs < 10000) return sign + trim(floored(abs / 1000, 1).toFixed(1)) + "K"; // 9.8K
  if (abs < 1000000) return sign + String(floored(abs / 1000, 0)) + "K";       // 995K
  return sign + trim(floored(abs / 1000000, 2).toFixed(2)) + "M";              // 1.04M
}

// contextTitle：上下文环的悬停解释（桌面才有 hover；移动端靠环点开的浮板，两处都写同一句话）。
// 分母是**输入预算**不是模型上下文 —— 这是"填了 1 000 000 却显示 995904"的来由。
export function contextTitle(ctx?: ContextUsage | null): string {
  if (!ctx) return "上下文占用（还没有会话）";
  const num = (v?: number | null): string => Number(v ?? 0).toLocaleString("en-US");
  return `输入预算 ${num(ctx.budget_tokens)} tokens = 上下文上限 ${num(ctx.ctx_len)} − 输出预留 ${num(ctx.max_output)}`;
}

// formatCost：上游报的"这一发花了多少美元"（2026-10-20，OpenRouter 的 usage.cost）。
//
// 三档：< $0.01 给 4 位小数（便宜模型的单发就是 0.0004 这种量级，两位小数会全变成 $0.00）、
// 其余给 2 位；**恰好 0 ⇒ $0**（免费模型是真的 0，不是"没数据"）。
// `null`/`undefined` ⇒ 空串 —— 直连渠道（DeepSeek / OpenCode GO）不报 cost，
// 那时**什么都不显示**，绝不显示成 $0.00（那是"免费"的意思，会说谎）。
export function formatCost(usd?: number | null): string {
  if (typeof usd !== "number" || !Number.isFinite(usd)) return "";
  if (usd === 0) return "$0";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}

export interface UsageLike {
  prompt_tokens?: number | null;
  completion_tokens?: number | null;
  cached_tokens?: number | null;
  reasoning_tokens?: number | null;
  cost_usd?: number | null;
  [key: string]: unknown;
}

export interface FootMeta {
  idx?: number | string | null;
  reasoningMs?: number | null;
  durationMs?: number | null;
}

// identityLabel：气泡外那行身份（用户消息 hover 才出现）：`你 #3`。
// 没有编号（乐观上屏 / 还没落库）⇒ 只留名字，绝不显示成 `你 #undefined`。
export function identityLabel(who?: string | null, idx?: number | string | null): string {
  const w = String(who ?? "").trim();
  const n = idx === null || idx === undefined ? "" : String(idx).trim();
  if (!n) return w;
  return w ? `${w} #${n}` : `#${n}`;
}

// assistantIdentity：助手消息顶部身份。`kind` 是实际用过工具（tool_run）还是普通对话。
// 不用「智能体」：Agent 是人格/能力的词，不是消息类型；`助手-工具` 说的就是这条消息做了什么。
export function assistantIdentity(
  who?: string | null,
  kind?: string | null,
  idx?: number | string | null,
): string {
  const w = String(who ?? "").trim();
  const n = idx === null || idx === undefined ? "" : String(idx).trim();
  let label = w;
  if (w === "助手") label = kind === "tool_run" ? "助手-工具" : "助手-对话";
  if (!n) return label;
  return label ? `${label}#${n}` : `#${n}`;
}

// depthText：ST 的 injection_depth 人话解释。只对 absolute 有效；
// 0 = 最后一条之后，1 = 最后一条之前，2 = 倒数第二条之前。
export function depthText(depth?: number | null): string {
  const n = Number(depth);
  if (!Number.isFinite(n) || n < 0) return "";
  if (n === 0) return "最后一条之后";
  if (n === 1) return "最后一条之前";
  return `倒数第${n}条之前`;
}

// formatLatency：首字延迟（后端 `reasoning_ms`：受理 → 第一段正文），秒、两位小数。
// 没这一格（没存思考的渠道 / 还没有正文 / 生成中）⇒ 空串 ⇒ 脚注省掉这段。
export function formatLatency(ms?: number | null): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
  return `${(ms / 1000).toFixed(1)} s`;
}

// formatSpeed：输出速度（tps）＝ completion_tokens ÷ 整发墙钟时间。
//
// 不再按 reasoning_ms 拆分子分母：上游的 reasoning_tokens 有时**大于** completion_tokens
// （实测 deepseek 4.1：completion 12 / reasoning 66），拆出来的生成窗只剩 13ms、token 为负，
// 结果要么不显示、要么几百 tps 的假数。直接用总输出 / 总耗时更诚实，也和用户举例一致。
export function formatSpeed(usage?: UsageLike | null, _reasoningMs?: number | null, durationMs?: number | null): string {
  const completion = Number(usage?.completion_tokens ?? NaN);
  const duration = Number(durationMs ?? NaN);
  if (!Number.isFinite(completion) || completion <= 0) return "";
  if (!Number.isFinite(duration) || duration <= 0) return "";
  const tps = completion / (duration / 1000);
  return `${Number(tps.toFixed(1))} tps`;
}

// usageFoot：气泡底部只留用量/延迟/速度（身份已挪到消息顶部）。
//
// 口径（AGENTS.md「用量两处别混」）：单位是 token，`reasoning_tokens` 是
// `completion_tokens` 的**子集**（不是另加）；未命中 = prompt − cached（推导，不另存）。
// 各段都是**有才写**：没用量省用量、没时长省延迟/速度 —— 空 ⇒ 整个脚注不渲染。
// 缓存命中留在悬停说明里（脚注要短）。
export function usageFoot(usage?: UsageLike | null, meta?: FootMeta | null): string {
  const parts: string[] = [];
  if (usage && usage.prompt_tokens != null) {
    const completion = `输出：${formatTokens(usage.completion_tokens)}`;
    const reasoning = Number(usage.reasoning_tokens ?? 0);
    const withThink = reasoning > 0 ? `${completion}（思考 ${formatTokens(reasoning)}）` : completion;
    const cached = Number(usage.cached_tokens ?? 0);
    const withCache = cached > 0 ? `（已缓存：${formatTokens(cached)}）` : "";
    parts.push(`输入：${formatTokens(usage.prompt_tokens)}${withCache}， ${withThink}`);
  }
  const cost = usage ? formatCost(usage.cost_usd) : "";
  if (cost) parts.push(`成本 ${cost}`);
  const latency = formatLatency(meta?.reasoningMs);
  if (latency) parts.push(`首字延迟： ${latency}`);
  const speed = formatSpeed(usage, meta?.reasoningMs, meta?.durationMs);
  if (speed) parts.push(`速度： ${speed}`);
  return parts.length ? `${parts.join("， ")}。` : "";
}

// usageFootTitle：那行小字的悬停解释（说清哪个数是子集、cost/延迟/速度怎么来的）。
export function usageFootTitle(usage?: UsageLike | null, meta?: FootMeta | null): string {
  const lines: string[] = [];
  if (usage && usage.prompt_tokens != null) {
    lines.push(
      `输入 ${usage.prompt_tokens} tok（其中缓存命中 ${usage.cached_tokens ?? 0}）`,
      `输出 ${usage.completion_tokens ?? 0} tok（其中思考 ${usage.reasoning_tokens ?? 0} —— 是输出的子集，不另加）`,
    );
    if (usage.cost_usd != null) lines.push(`成本 ${formatCost(usage.cost_usd)}：上游报的实扣金额`);
  }
  const latency = formatLatency(meta?.reasoningMs);
  if (latency) lines.push(`首字延迟 ${latency}：受理 → 第一段正文`);
  const speed = formatSpeed(usage, meta?.reasoningMs, meta?.durationMs);
  if (speed) lines.push(`速度 ${speed}：输出 token ÷ 整发耗时`);
  return lines.join("\n");
}

// contextOverBudget：超预算 ⇒ 环变红（从前是状态行那行小字变红，信号不能丢）。
export function contextOverBudget(ctx?: ContextUsage | null): boolean {
  return !!ctx?.over_budget;
}

// 新建渠道时"换 vendor 要不要跟着换 id"（2026-10-28）。
//
// 判据只有一条：**id 还是自动填的那个**才动它。用户手打过 `我的渠道`，再改 vendor 就把它冲掉
// 等于抢方向盘；而新建时一路点下来（id 还没落地）就该省掉那一格。
// 纯函数是为了能测：这段逻辑出错不会报错，只会静默地把人填好的名字换掉。
export function providerIdForNextVendor(currentId?: string | null, autoId?: string | null, vendor?: string | null): string {
  const current = String(currentId ?? "").trim();
  const auto = String(autoId ?? "");
  // 手填过（当前值既非空、又不是上次自动填的那个）⇒ 一个字符都不碰。
  if (current && current !== auto) return current;
  return vendor ? String(vendor) : "";
}

// ---- 顶栏的模型选择浮板（2026-10-28）----
// 为什么这些纯函数住这儿：面板要"按渠道分组 + 搜索"，而搜什么、怎么分组、当前选的是哪条
// 都能脱离 DOM 说清楚 —— 住组件里就只能靠正则去钉源码（钉不出"搜到了什么"）。
export const ORPHAN_MODEL_GROUP = "\u0000orphan";

// 一条模型匹配不匹配查询词：显示名 / 渠道名 / 上游 id 三者任一含它即算。
// 上游 id 必须参与 —— 显示名常常不含渠道信息（"GPT-4o" 是哪家的？），而人恰恰按它找。
export function modelMatches(m: ModelChoice, query?: string | null): boolean {
  const q = String(query ?? "").trim().toLowerCase();
  if (!q) return true;
  return [m.label, m.alias, m.provider_name, m.upstream_id].filter(Boolean).join(" ").toLowerCase().includes(q);
}

// 过滤 + 分组 + 扁平序号：面板的上下键要能一条条走完整个可见列表，
// 所以每项带 `flat`（在本列表里的下标；分组顺序 = 传入顺序，组内 = 原顺序）。
// 分完是空的组直接丢（搜索之后留一堆空组头只会让人以为没搜到）。
// `orphan` = 当前值不在列表里那一条（渠道删了 / 模型下架）：它排最前 —— 那是"我现在在哪"。
export function filterModelGroups(groups?: ModelGroup[] | null, query?: string | null, orphan: ModelChoice | null = null): ModelGroup[] {
  const out: ModelGroup[] = [];
  let flat = 0;
  if (orphan && modelMatches(orphan, query)) {
    out.push({ provider: ORPHAN_MODEL_GROUP, providerName: "当前（不在列表）", items: [{ ...orphan, flat: flat++ }] });
  }
  for (const g of groups ?? []) {
    const items = (g.items ?? []).filter((m) => modelMatches(m, query)).map((m) => ({ ...m, flat: flat++ }));
    if (!items.length) continue;
    out.push({ provider: g.provider, providerName: items[0].alias || items[0].provider_name || g.provider, items });
  }
  return out;
}

// 顶栏按钮上那行字：列表里找到就用显示名；找不到用"不在列表"那条；再找不到（列表还没到）
// 就把 key 拆开拼个 provider/model —— 总比空白强（空白看着像"没选模型"）。
export function curModelLabelOf(groups?: ModelGroup[] | null, cur?: string | null, orphan: ModelChoice | null = null): string {
  for (const g of groups ?? []) {
    const hit = (g.items ?? []).find((i) => i.value === cur);
    if (hit) return hit.label;
  }
  if (!(groups ?? []).length) return cur ? "加载模型…" : "选择模型";
  if (orphan) return orphan.label;
  const parsed = cur ? parseModelKey(cur) : null;
  return parsed ? `${parsed.provider}/${parsed.model}` : "选择模型";
}

export function groupModelsByProvider(models?: ModelListItem[] | null): Array<{ provider: string; items: ModelListItem[] }> {
  const groups = new Map<string, ModelListItem[]>();
  for (const m of models ?? []) {
    const k = m.provider ?? "unknown";
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k)!.push(m);
  }
  return [...groups.entries()].map(([provider, items]) => ({ provider, items }));
}

export interface Choice {
  value: string;
  label: string;
  missing?: boolean;
}

// providerChoices / modelChoicesFor：设置里**「新会话默认值」那两格**的下拉选项。
//
// 从前那两格是**手打 id 的文本框**，说明文自己都写着「填错不会报错，只会在发送时回落成
// 『未配置模型』」—— 那等于把校验推给用户。改成下拉之后，能选出来的都是真存在的东西。
//
// 但**当前值必须始终在里面**：它可能是一个已经不存在的渠道（手改过 providers.json / 删过渠道），
// 也可能是一条还没发现模型的渠道（`dummy` 就是这种 —— 它不联网，永远不会有一行模型）。
// 不在列表里就补一条并说清"不在列表里"：悄悄丢掉它 = 一保存就把人配好的默认值换成别的。
export function providerChoices(providers?: ProviderView[] | null, cur = ""): Choice[] {
  const items: Choice[] = (providers ?? []).map((p) => {
    // 默认值下拉要能区分同服务商的多条渠道（OpenRouter 可以挂两条不同端点），
    // 所以「服务商中文名 + 渠道标识」都留着；dummy 是内置示例，不摆内部 id。
    // 别名优先（它本来就是给人看的那一格），空则完全沿用原来的回退。
    const alias = String(p.alias ?? "").trim();
    const label = alias || (p.id === "dummy"
      ? "本地示例（不联网）"
      : (p.name || (p.vendor_name ? `${p.vendor_name}（${p.id}）` : p.id)));
    return { value: p.id, label };
  });
  if (cur && !items.some((i) => i.value === cur)) {
    items.push({ value: cur, label: `${cur}（不在渠道列表里）`, missing: true });
  }
  return items;
}

// ⚠ 值是 **upstream_id**（裸模型 id），不是 `modelKey(...)` 拼的 `provider/model`：
// `config.json` 的 `defaults.model` 存的就是裸 id（后端 `Defaults.Model = "dummy"`）。
// 两处用同一把尺，才不会在"渠道换了但模型没换"的时候静默错位。
export function modelChoicesFor(
  groups: Array<{ provider: string; items: Array<{ upstream_id: string; label: string }> }> | null | undefined,
  provider: string,
  cur = "",
): Choice[] {
  const items: Choice[] = [];
  for (const g of groups ?? []) {
    if (g.provider !== provider) continue;
    for (const item of g.items ?? []) items.push({ value: item.upstream_id, label: item.label });
  }
  if (cur && !items.some((i) => i.value === cur)) {
    items.push({
      value: cur,
      label: cur === "dummy" ? "本地示例（不在模型列表里）" : `${cur}（不在列表里）`,
      missing: true,
    });
  }
  return items;
}

export function deletionPlanSummary(plan?: DeletionPlan | null): string {
  const nm = plan?.deleted_message_ids?.length ?? 0;
  const ns = plan?.deleted_summary_ids?.length ?? 0;
  const nu = plan?.unlinked_message_ids?.length ?? 0;
  return `将删除 ${nm} 条消息、${ns} 条摘要，${nu} 条消息解链`;
}

// ---- 设置 modal 纯逻辑（无 DOM，测试直引） ----

export interface SettingsTab {
  id: string;
  label: string;
}

// SETTINGS_TABS：设置视图四区（顺序固定）—— **id 与中文名同一份**（按钮标签、panel id 都从它来）。
// 2026-11-02 用户「设置面板塞了太多东西」：**会话 / 渠道** 不再住设置 tab，
// 改由左栏底部的「会话 / 渠道」两个独立入口打开（同一个视图，`:only` 只显示那一页）。
// 「外观」区 2026-11-01 搬回设置（用户定：「把外观设置做到设置里」）——
// 它原本住**用户视图**（`UserView.vue`），而那个视图随账号功能一起删了：
// 身份/用户管理跟着账号没了，个人偏好（主题/缩放/背景图/发送键/预演/命令菜单）
// 整块搬进这一区，一个字都没改口径。
// 「连接」区 2026-10-28 **删掉**：那一页只有「后端地址 + Token」两格，而单容器部署里浏览器
// 永远同源（vite 把 /api 代理给后端），地址是启动自检自动探出来的；连不上时门板会自己弹出来
// 让人填 —— 正常部署里这格一辈子用不到。更糟的是它的「保存」按钮绑的 `saveNet` 在重构时丢了，
// 点下去直接抛错。留着只会让人以为"我得自己配点什么"。
// 「提示词」= 可复用提示词段（system prompt segment）的库；「智能体」里逐段引用（2026-10-11 用户定）。
export const SETTINGS_TABS: SettingsTab[] = [
  { id: "chat", label: "聊天" },
  { id: "agent", label: "智能体" },
  { id: "prompt", label: "提示词" },
  { id: "appearance", label: "外观" },
  { id: "tavern", label: "酒馆" },
];

// formatRoutesOutcome：刷路由回形 {provider, models, error?} ⇒ 缓存行文案。
export function formatRoutesOutcome(o?: { provider?: string; models?: number; error?: string } | null): string {
  if (!o || typeof o !== "object") return "未刷新";
  if (o.error) return `${o.provider ?? "?"}：刷新失败（${o.error}）`;
  return `${o.provider ?? "?"}：${o.models ?? 0} 个模型`;
}

// 能力 id（写死 —— 与后端 `abilities.definitions` 同口径，未知的后端 400）。
//
// `judge` 2026-10-28 **退役**（用户：「暂时砍掉 agent 的 judge 功能」）：它没有流程、
// 也没有模板，留在界面上只是一块点了没反应的开关。后端把它当"退役的 id"放行
// （老 `agents.json` 里写着它照常能读），但不再列进枚举 ⇒ 这里也不能再列。
// 契约测试拿这张表去对 `core/internal/abilities/abilities.go` 的 definitions。
export const ABILITY_IDS = ["title", "compact"];

// 能力的中文名（页签 / 标题用）。**与后端 `abilities.definitions` 的 `Label` 逐字相同** ——
// 它是给人看的那一层，两处叫法不同不会报错，只会让人疑惑"这是不是两个东西"。
// 契约测试从 `core/internal/abilities/abilities.go` 里读 `Label: "…"` 来对。
export const ABILITY_LABELS: Record<string, string> = { title: "起标题", compact: "压缩" };

// 能力的**模板住在哪几条内置段**（2026-10-28：模板都搬进了段库；压缩有两条 —— 通用版与 RP 版）。
//
// 数组的**第一条是默认**，后面是候选。挑中任何一条都算"我要这个模板"：后端
// `abilities.AssemblePrompt` 于是不再自动垫默认那份 ⇒ 挑 RP 版得到的就是 RP 版，
// 不夹通用版（两份模板叠在一起会互相打架：一份写"不要复述原文"，另一份写"细节不能丢"）。
//
// ⚠ 这些 id 与后端 `config.CompactTemplateSegmentID` / `config.CompactRPTemplateSegmentID` /
// `config.TitleTemplateSegmentID` 必须逐字相同 —— 写错不报错，只是预览里少一段
// （真正的模板仍由后端拼）。契约测试从 `core/internal/config/config.go` 里读常量与内置段清单来对。
// 每个在册能力都必须在这张表里（没有模板的能力，是因为它连流程都没有 —— 那就不该在册）。
export const ABILITY_TEMPLATE_SEGMENTS: Record<string, string[]> = {
  compact: ["压缩提示词", "RP压缩提示词"],
  title: ["起标题提示词"],
};

export interface AbilityRow {
  enabled?: boolean;
  provider?: string;
  model?: string;
  prompt?: string;
  model_source?: string;
  segments?: string[];
}

export interface AbilityPatch {
  enabled: boolean;
  provider?: string;
  model?: string;
  prompt?: string;
  model_source?: string;
  segments?: string[];
}

// buildAbilitiesPatch：Agent 右详情 → PATCH 整段 abilities（给了就整段替换；nil=不动）。
// 每能力：checkbox 开关 + provider/model/prompt 三覆盖格（空串=不覆盖）+ 追加段 id 列表
// （2026-10-28 加：`segments` 与 agent 的 `segments` 同一套 —— 顺序 = 拼接顺序）。
export function buildAbilitiesPatch(rows?: Record<string, AbilityRow> | null): Record<string, AbilityPatch> {
  const out: Record<string, AbilityPatch> = {};
  for (const id of ABILITY_IDS) {
    const r = rows?.[id];
    if (!r) continue;
    const one: AbilityPatch = { enabled: !!r.enabled };
    const keys = ["provider", "model", "prompt", "model_source"] as const;
    for (const k of keys) {
      const v = String(r[k] ?? "").trim();
      if (v) (one as unknown as Record<string, unknown>)[k] = v;
    }
    // 段：空清单**不发**（省略 = 没有段，与 `enabled` 之下的空串同理）；
    // 去掉空串并保持顺序 —— 顺序就是语义，别去重排序。
    const segments = (r.segments ?? []).map((x) => String(x ?? "").trim()).filter(Boolean);
    if (segments.length) one.segments = segments;
    out[id] = one;
  }
  return out;
}
