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
export function modelOption(m) { return { value: modelKey(m.provider, m.upstream_id), label: modelLabel(m) }; }
export function agentOption(a) { return { id: a.id, label: a.name || a.id }; }
// 谁在答（状态行与草稿共用；fallback 两处不同：会话里"未知模型"、草稿"未选模型"）。
export function whoText(provider, model, fallback = "未知模型") {
  return [provider, model].filter(Boolean).join("/") || fallback;
}
// 会话抬头：空标题按草稿/真会话两态回退（TopBar/SessionBar/设置列表/删除确认共用）。
export function sessionTitle(title, isDraft = false) {
  return (title ?? "").trim() || (isDraft ? "新对话" : "新会话");
}

export function formatThinkLabel(reasoningMs) {
  if (reasoningMs == null) return "思考过程";
  return `思考过程（${(reasoningMs / 1000).toFixed(1)}s）`;
}

// 状态行：占用 used/budget（over_budget 标红由 CSS 类承担）+ provider/model + phase/耗时。
export function formatStatusLine({ ctx, phase, elapsedMs, provider, model }) {
  const used = ctx?.used_tokens ?? "?";
  const budget = ctx?.budget_tokens ?? "?";
  const over = ctx?.over_budget ? " 超预算" : "";
  const who = whoText(provider, model);
  return `上下文 ${used}/${budget}${over}｜${who}｜${phase ?? "idle"}${elapsedMs != null ? ` ${elapsedMs}ms` : ""}`.trim();
}

export function statusOverBudget(ctx) {
  return !!ctx?.over_budget;
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
export const SETTINGS_TABS = [
  { id: "chat", label: "聊天" },
  { id: "ui", label: "界面" },
  { id: "net", label: "连接" },
  { id: "session", label: "会话" },
  { id: "agent", label: "Agent" },
  { id: "provider", label: "Provider" },
];

// formatRoutesOutcome：刷路由回形 {provider, models, error?} ⇒ 缓存行文案。
export function formatRoutesOutcome(o) {
  if (!o || typeof o !== "object") return "未刷新";
  if (o.error) return `${o.provider ?? "?"}：刷新失败（${o.error}）`;
  return `${o.provider ?? "?"}：${o.models ?? 0} 个模型`;
}

// 能力 id（三件，写死 —— 与后端 abilities 枚举同口径，未知的后端 400）。
export const ABILITY_IDS = ["title", "compact", "judge"];

// buildAbilitiesPatch：Agent 右详情 → PATCH 整段 abilities（给了就整段替换；nil=不动）。
// 每能力：checkbox 开关 + provider/model/prompt 三覆盖格（空串=不覆盖）。
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
    out[id] = one;
  }
  return out;
}

