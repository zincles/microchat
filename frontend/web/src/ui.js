// microchat web UI 层 —— 纯渲染 + DOM 构造。顶层不碰 document，测试可直引纯函数。
export function formatThinkLabel(reasoningMs) {
  if (reasoningMs == null) return "思考过程";
  return `思考过程（${(reasoningMs / 1000).toFixed(1)}s）`;
}

// 状态行：占用 used/budget（over_budget 标红由 CSS 类承担）+ provider/model + phase/耗时。
export function formatStatusLine({ ctx, phase, elapsedMs, provider, model }) {
  const used = ctx?.used_tokens ?? "?";
  const budget = ctx?.budget_tokens ?? "?";
  const over = ctx?.over_budget ? " 超预算" : "";
  const who = [provider, model].filter(Boolean).join("/") || "未知模型";
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

// SETTINGS_TABS：modal 四区（服务端/客户端/Agent/Provider），顺序固定。
export const SETTINGS_TABS = ["server", "client", "agent", "provider"];

// switchSettingsTab：tab 切换纯函数 —— 目标合法才切，否则留当前。
export function switchSettingsTab(current, target) {
  return SETTINGS_TABS.includes(target) ? target : current;
}

// snapshotSettings：开 modal 时 snapshot（深拷贝）；关时丢弃 ⇒ 不保存不写。
export function snapshotSettings(state) {
  return JSON.parse(JSON.stringify(state ?? null));
}

// settingsDirty：snapshot 与现表单比对 —— 全等才算干净（只 PUT 脏区用）。
export function settingsDirty(snap, cur) {
  return JSON.stringify(snap ?? null) !== JSON.stringify(cur ?? null);
}

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

// ---- DOM 构造（需传 doc，方便测试注入；浏览器传 document）----

export function renderMessage(doc, { who, role, content, reasoning, reasoningMs }) {
  const div = doc.createElement("div");
  div.className = `msg ${role === "user" ? "user" : "assistant"}`;
  const head = doc.createElement("div");
  head.className = "who";
  head.textContent = who;
  div.appendChild(head);
  if (reasoning) {
    const det = doc.createElement("details");
    det.className = "think";
    const sum = doc.createElement("summary");
    sum.textContent = formatThinkLabel(reasoningMs);
    det.appendChild(sum);
    const pre = doc.createElement("div");
    pre.className = "think-body";
    pre.textContent = reasoning;
    det.appendChild(pre);
    div.appendChild(det);
  }
  const body = doc.createElement("div");
  body.className = "text";
  body.textContent = content ?? "";
  div.appendChild(body);
  return div;
}
