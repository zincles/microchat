// microchat web UI 层 —— 纯渲染 + DOM 构造。顶层不碰 document，测试可直引纯函数。
export function formatThinkLabel(reasoningMs) {
  if (reasoningMs == null) return "思考过程";
  return `思考过程（${(reasoningMs / 1000).toFixed(1)}s）`;
}

export function isSummaryItem(item) {
  return item?.type === "summary";
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

export function renderOutgoingItem(doc, item) {
  const div = doc.createElement("div");
  div.className = "out" + (isSummaryItem(item) ? " summary" : "");
  const head = doc.createElement("span");
  head.className = "out-type";
  head.textContent =
    item.type === "summary"
      ? `summary ${item.from_idx ?? "?"}–${item.to_idx ?? "?"}（${item.blocks ?? "?"}块）`
      : item.type === "system"
        ? "system idx=0"
        : `message idx=${item.idx ?? "?"}`;
  div.appendChild(head);
  const body = doc.createElement("span");
  body.className = "out-text";
  body.textContent = ` ${String(item.content ?? "").slice(0, 80)}`;
  div.appendChild(body);
  return div;
}
