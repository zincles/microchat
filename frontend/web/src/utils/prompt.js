// 系统提示词拼装（与后端 `state.AssembleSystemPrompt` **同一规则**，2026-10-11 用户定）：
// base + 追加段，逐段拼接、段间空行（`\n\n`）；空 base / 空段 / 缺段都跳过。
// 前端只拿它做**预览** —— 权威在后端（`GET /sessions/{id}/prompt` 回拼装结果）。
export function assemblePrompt(base, ids, segments) {
  const parts = [];
  const trimmed = String(base ?? "").trim();
  if (trimmed) parts.push(trimmed);
  for (const id of ids ?? []) {
    const seg = (segments ?? []).find((s) => s.id === id);
    const text = String(seg?.text ?? "").trim();
    if (text) parts.push(text);
  }
  return parts.join("\n\n");
}

// abilityPrompt：**能力**的提示词拼装（与后端 `compact.compactTemplate` 同一规则，
// 2026-10-28 用户定「默认压缩载荷挑出来做成单独的内置 segment」）。
//
// 三条，和 agent 那条只差一个"默认模板也是段"：
//   1. base = 你填的 `prompt`（覆盖）；空 ⇒ 用那条**内置模板段**（`templateSegmentId`）。
//   2. 但**列表里已经挑了它** ⇒ 不再额外垫一份（否则同一段说明文出现两次）。
//   3. 段按列表顺序追加在 base 后面，段间空行。
//
// 前端只拿它做**预览** —— 权威在后端（点「看载荷」看到的是真发的那一发）。
export function abilityPrompt(base, ids, segments, templateSegmentId) {
  const list = ids ?? [];
  let head = String(base ?? "").trim();
  const explicit = templateSegmentId ? list.includes(templateSegmentId) : false;
  if (!head && templateSegmentId && !explicit) {
    head = String((segments ?? []).find((s) => s.id === templateSegmentId)?.text ?? "").trim();
  }
  return assemblePrompt(head, list, segments);
}

// missingSegments：引用了、但库里没有的段 id（界面标红用；正常路径由后端写入/启动校验挡掉）。
export function missingSegments(ids, segments) {
  const have = new Set((segments ?? []).map((s) => s.id));
  return (ids ?? []).filter((id) => !have.has(id));
}
