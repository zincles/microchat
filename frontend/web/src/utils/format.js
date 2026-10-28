// 纯函数（无 DOM）：测试直引这里。DOM 构造已迁 MessageBubble.vue。

// —— 模型/Agent 的键与标签（唯一出口，别在组件里手拼） ——
// 模型键 `provider|||upstream_id`：下拉 <option> 的 value 用它（分隔符是 magic 串，解析只在这里）。
export function modelKey(provider, model) { return `${provider}|||${model}`; }
export function parseModelKey(v) {
  const i = (v ?? "").indexOf("|||");
  if (i < 0) return null;
  return { provider: v.slice(0, i), model: v.slice(i + 3) };
}
// 显示名三级回退（与后端两列同义：用户覆盖 → 上游名 → 上游 id）。
export function modelLabel(m) { return m?.display_name || m?.name || m?.upstream_id || ""; }
// 下拉项（`/models` 的行 → {value,label}）。
// 2026-10-28：顶栏的模型选择器换成自定义面板之后，每项还带着 `provider_name` / `upstream_id` ——
// 面板要按渠道分组、搜索要能按渠道名与上游 id 命中（label 只是显示名，常常不含渠道信息）。
export function modelOption(m) {
  return {
    value: modelKey(m.provider, m.upstream_id),
    label: modelLabel(m),
    provider_name: m.provider_name || m.provider,
    upstream_id: m.upstream_id,
  };
}
export function agentOption(a) { return { id: a.id, label: a.name || a.id }; }
// 谁在答（状态行与草稿共用；fallback 两处不同：会话里"未知模型"、草稿"未选模型"）。
export function whoText(provider, model, fallback = "未知模型") {
  return [provider, model].filter(Boolean).join("/") || fallback;
}
// taskLineText：输入框上方那行小字的文案（后台任务面板 `/tasks` → 一行）。
// 只数**还在跑**的（finished_at 有值的不算）；0 个 ⇒ 空串（调用方据此藏行）。
// 形状：`⟳ 后台任务 2：turn · compact`（kind 去重，最多列 3 种）。
export function taskLineText(board) {
  const n = board?.running ?? 0;
  if (!n) return "";
  const kinds = [...new Set((board?.tasks ?? [])
    .filter((t) => !t.finished_at && t.kind)
    .map((t) => t.kind))].slice(0, 3);
  return `⟳ 后台任务 ${n}${kinds.length ? `：${kinds.join(" · ")}` : ""}`;
}

// 会话抬头：空标题按草稿/真会话两态回退（TopBar/SessionBar/设置列表/删除确认共用）。
export function sessionTitle(title, isDraft = false) {
  return (title ?? "").trim() || (isDraft ? "新对话" : "新会话");
}

export function formatThinkLabel(reasoningMs) {
  if (reasoningMs == null) return "思考过程";
  return `思考过程（${(reasoningMs / 1000).toFixed(1)}s）`;
}

// 状态行：当前助手 + phase/耗时。
// ⚠ 上下文占用**不在这一行**（2026-10-28 用户定）：它挪到右上角那枚小环（TopBar 的 `#ctx-ring`），
// 点开才看明细。所以这里不接 ctx —— 同一个数字住两处，只会让两处都可能过期。
export function formatStatusLine({ phase, elapsedMs, agent }) {
  // 2026-10-14：中间那格从"渠道/模型"换成"当前助手" —— 模型住顶栏左上（别两处重复），
  // 助手住这儿（顶栏已无 Agent 下拉）。
  return `${agent ?? "?"}｜${phase ?? "idle"}${elapsedMs != null ? ` ${elapsedMs}ms` : ""}`.trim();
}

// contextRatio：已用 / 输入预算，夹到 0..1（分母缺失或为 0 ⇒ 0）。
// 分母是**输入预算**不是模型上下文（见 contextTitle）—— 这就是"填了 1 000 000 却显示 995904"的来由。
export function contextRatio(ctx) {
  const used = Number(ctx?.used_tokens ?? 0);
  const budget = Number(ctx?.budget_tokens ?? 0);
  if (!(budget > 0) || !Number.isFinite(used)) return 0;
  return Math.max(0, Math.min(1, used / budget));
}

// contextPercentText：给人看的占比。
// 一上来就是 344/1.04M 这种小占比 ⇒ 整数会显示成 0%（看起来像"坏了"），所以 10% 以下带一位小数、
// 0.1% 以下单说。**向下取整**（与 formatTokens 同一个理由：别把用量说大）。
export function contextPercentText(ctx) {
  const used = Number(ctx?.used_tokens ?? 0);
  if (!(used > 0)) return "0%";
  const r = contextRatio(ctx) * 100;
  if (r < 0.1) return "<0.1%";
  if (r < 10) return `${Math.floor(r * 10) / 10}%`;
  return `${Math.floor(r)}%`;
}

// CONTEXT_MIN_ARC：环上那道弧的**最小可见百分比**。
// 344/1.04M ≈ 0.03% ⇒ 四舍五入成 0，环整个消失 —— 那看起来像"坏了"，而不像"几乎没占"。
// 所以只要用了（used > 0）就至少画出这么长一道弧；**文字**照实报（contextPercentText），
// 弧只回答"有没有占用、大概多少"这个粗问题。
export const CONTEXT_MIN_ARC = 2;

// contextArc：环该画多少（0..100 的百分比，直接喂 SVG 的 stroke-dasharray）。
export function contextArc(ctx) {
  const used = Number(ctx?.used_tokens ?? 0);
  if (!(used > 0)) return 0;
  return Math.max(CONTEXT_MIN_ARC, Math.min(100, Math.round(contextRatio(ctx) * 100)));
}

// formatTokens：上下文数字的人话格式（2026-10-17 用户点名）：9857 → 9.8K、995904 → 995K、
// 1048576 → 1.04M。**一律向下取整**（不四舍五入）：这两个数里有一个是"我用了多少"，
// 向上取整会把用量说大；而且 995904 报成"996K"会让人以为分母真是 996K ✗。
export function formatTokens(n) {
  if (n == null || !Number.isFinite(n)) return "?";
  const sign = n < 0 ? "-" : "";
  const abs = Math.abs(n);
  const floored = (v, digits) => { const f = 10 ** digits; return Math.floor(v * f + 1e-9) / f; };
  const trim = (s) => (s.includes(".") ? s.replace(/\.?0+$/, "") : s);
  if (abs < 1000) return sign + String(abs);
  if (abs < 10000) return sign + trim(floored(abs / 1000, 1).toFixed(1)) + "K"; // 9.8K
  if (abs < 1000000) return sign + String(floored(abs / 1000, 0)) + "K";       // 995K
  return sign + trim(floored(abs / 1000000, 2).toFixed(2)) + "M";              // 1.04M
}

// contextTitle：上下文环的悬停解释（桌面才有 hover；移动端靠环点开的浮板，两处都写同一句话）。
// 分母是**输入预算**不是模型上下文 —— 这是"填了 1 000 000 却显示 995904"的来由。
export function contextTitle(ctx) {
  if (!ctx) return "上下文占用（还没有会话）";
  const num = (v) => Number(v ?? 0).toLocaleString("en-US");
  return `输入预算 ${num(ctx.budget_tokens)} = 模型上下文 ${num(ctx.ctx_len)} − 输出预留 ${num(ctx.max_output)}`;
}

// formatCost：上游报的"这一发花了多少美元"（2026-10-20，OpenRouter 的 usage.cost）。
//
// 三档：< $0.01 给 4 位小数（便宜模型的单发就是 0.0004 这种量级，两位小数会全变成 $0.00）、
// 其余给 2 位；**恰好 0 ⇒ $0**（免费模型是真的 0，不是"没数据"）。
// `null`/`undefined` ⇒ 空串 —— 直连渠道（DeepSeek / OpenCode GO）不报 cost，
// 那时**什么都不显示**，绝不显示成 $0.00（那是"免费"的意思，会说谎）。
export function formatCost(usd) {
  if (usd == null || !Number.isFinite(usd)) return "";
  if (usd === 0) return "$0";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}

// usageFoot：气泡下面那行小字（token 用量 + 成本）。
//
// 口径（AGENTS.md「用量两处别混」）：单位是 token，`reasoning_tokens` 是
// `completion_tokens` 的**子集**（不是另加）；未命中 = prompt − cached（推导，不另存）。
// 空（没用量）⇒ 空串，调用方据此不渲染这一行。
export function usageFoot(usage) {
  if (!usage || usage.prompt_tokens == null) return "";
  const prompt = formatTokens(usage.prompt_tokens);
  const completion = formatTokens(usage.completion_tokens);
  const parts = [`${prompt}→${completion}`];
  if (usage.cached_tokens > 0) parts.push(`缓存命中 ${formatTokens(usage.cached_tokens)}`);
  if (usage.reasoning_tokens > 0) parts.push(`思考 ${formatTokens(usage.reasoning_tokens)}`);
  const cost = formatCost(usage.cost_usd);
  if (cost) parts.push(cost);
  return parts.join(" · ");
}

// usageFootTitle：那行小字的悬停解释（说清哪个数是子集、cost 是谁报的）。
export function usageFootTitle(usage) {
  if (!usage || usage.prompt_tokens == null) return "";
  const lines = [
    `输入 ${usage.prompt_tokens} tok（其中缓存命中 ${usage.cached_tokens ?? 0}）`,
    `输出 ${usage.completion_tokens ?? 0} tok（其中思考 ${usage.reasoning_tokens ?? 0} —— 是输出的子集，不另加）`,
  ];
  if (usage.cost_usd != null) lines.push(`成本 ${formatCost(usage.cost_usd)}：上游报的实扣金额`);
  return lines.join("\n");
}

// contextOverBudget：超预算 ⇒ 环变红（从前是状态行那行小字变红，信号不能丢）。
export function contextOverBudget(ctx) {
  return !!ctx?.over_budget;
}

// 新建渠道时"换 vendor 要不要跟着换 id"（2026-10-28）。
//
// 判据只有一条：**id 还是自动填的那个**才动它。用户手打过 `我的渠道`，再改 vendor 就把它冲掉
// 等于抢方向盘；而新建时一路点下来（id 还没落地）就该省掉那一格。
// 纯函数是为了能测：这段逻辑出错不会报错，只会静默地把人填好的名字换掉。
export function providerIdForNextVendor(currentId, autoId, vendor) {
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
export function modelMatches(m, query) {
  const q = String(query ?? "").trim().toLowerCase();
  if (!q) return true;
  return [m?.label, m?.provider_name, m?.upstream_id].filter(Boolean).join(" ").toLowerCase().includes(q);
}

// 过滤 + 分组 + 扁平序号：面板的上下键要能一条条走完整个可见列表，
// 所以每项带 `flat`（在本列表里的下标；分组顺序 = 传入顺序，组内 = 原顺序）。
// 分完是空的组直接丢（搜索之后留一堆空组头只会让人以为没搜到）。
// `orphan` = 当前值不在列表里那一条（渠道删了 / 模型下架）：它排最前 —— 那是"我现在在哪"。
export function filterModelGroups(groups, query, orphan = null) {
  const out = [];
  let flat = 0;
  if (orphan && modelMatches(orphan, query)) {
    out.push({ provider: ORPHAN_MODEL_GROUP, providerName: "当前（不在列表）", items: [{ ...orphan, flat: flat++ }] });
  }
  for (const g of groups ?? []) {
    const items = (g.items ?? []).filter((m) => modelMatches(m, query)).map((m) => ({ ...m, flat: flat++ }));
    if (!items.length) continue;
    out.push({ provider: g.provider, providerName: items[0].provider_name || g.provider, items });
  }
  return out;
}

// 顶栏按钮上那行字：列表里找到就用显示名；找不到用"不在列表"那条；再找不到（列表还没到）
// 就把 key 拆开拼个 provider/model —— 总比空白强（空白看着像"没选模型"）。
export function curModelLabelOf(groups, cur, orphan = null) {
  for (const g of groups ?? []) {
    const hit = (g.items ?? []).find((i) => i.value === cur);
    if (hit) return hit.label;
  }
  if (orphan) return orphan.label;
  const parsed = cur ? parseModelKey(cur) : null;
  return parsed ? `${parsed.provider}/${parsed.model}` : "选择模型";
}

export function groupModelsByProvider(models) {
  const groups = new Map();
  for (const m of models ?? []) {
    const k = m.provider ?? "unknown";
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(m);
  }
  return [...groups.entries()].map(([provider, items]) => ({ provider, items }));
}

export function deletionPlanSummary(plan) {
  const nm = plan?.deleted_message_ids?.length ?? 0;
  const ns = plan?.deleted_summary_ids?.length ?? 0;
  const nu = plan?.unlinked_message_ids?.length ?? 0;
  return `将删除 ${nm} 条消息、${ns} 条摘要，${nu} 条消息解链`;
}

// ---- 设置 modal 纯逻辑（无 DOM，测试直引） ----

// SETTINGS_TABS：设置视图六区（顺序固定）—— **id 与中文名同一份**（按钮标签、panel id 都从它来）。
// （原来的「界面」区已搬到用户视图；用户视图与设置视图平级，不带 tab。）
// 「提示词」= 可复用提示词段（system prompt segment）的库；「智能体」里逐段引用（2026-10-11 用户定）。
export const SETTINGS_TABS = [
  { id: "chat", label: "聊天" },
  { id: "net", label: "连接" },
  { id: "session", label: "会话" },
  { id: "agent", label: "智能体" },
  { id: "provider", label: "渠道" },
  { id: "prompt", label: "提示词" },
];

// formatRoutesOutcome：刷路由回形 {provider, models, error?} ⇒ 缓存行文案。
export function formatRoutesOutcome(o) {
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
export const ABILITY_LABELS = { title: "起标题", compact: "压缩" };

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
export const ABILITY_TEMPLATE_SEGMENTS = {
  compact: ["压缩提示词", "RP压缩提示词"],
  title: ["起标题提示词"],
};

// buildAbilitiesPatch：Agent 右详情 → PATCH 整段 abilities（给了就整段替换；nil=不动）。
// 每能力：checkbox 开关 + provider/model/prompt 三覆盖格（空串=不覆盖）+ 追加段 id 列表
// （2026-10-28 加：`segments` 与 agent 的 `segments` 同一套 —— 顺序 = 拼接顺序）。
export function buildAbilitiesPatch(rows) {
  const out = {};
  for (const id of ABILITY_IDS) {
    const r = rows?.[id];
    if (!r) continue;
    const one = { enabled: !!r.enabled };
    for (const k of ["provider", "model", "prompt"]) {
      const v = (r[k] ?? "").trim();
      if (v) one[k] = v;
    }
    // 段：空清单**不发**（省略 = 没有段，与 `enabled` 之下的空串同理）；
    // 去掉空串并保持顺序 —— 顺序就是语义，别去重排序。
    const segments = (r.segments ?? []).map((x) => String(x ?? "").trim()).filter(Boolean);
    if (segments.length) one.segments = segments;
    out[id] = one;
  }
  return out;
}

