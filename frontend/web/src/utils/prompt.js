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

// abilityPrompt：**能力**的提示词拼装（与后端 `abilities.AssemblePrompt` 同一规则 ——
// `title` 与 `compact` 都走那一个；2026-10-28 用户定「默认压缩载荷挑出来做成单独的内置 segment」）。
//
// 三条，和 agent 那条只差一个"默认模板也是段"：
//   1. base = 你填的 `prompt`（覆盖）；空 ⇒ 用**默认的那条内置模板段**（`templateSegmentIDs` 的第一条）。
//   2. 但**列表里已经挑了模板段中的任何一条** ⇒ 不再额外垫一份。这一条对候选同样成立：
//      压缩有通用版与 RP 版，挑 RP 版 ⇒ 生效模板**就是** RP 版，不夹通用版
//      （两份模板挂在同一条消息上会互相打架）。
//   3. 段按列表顺序追加在 base 后面，段间空行。
//
// 前端只拿它做**预览** —— 权威在后端（点「看载荷」看到的是真发的那一发）。
export function abilityPrompt(base, ids, segments, templateSegmentIDs) {
  const list = ids ?? [];
  const candidates = templateSegmentIDs ?? [];
  let head = String(base ?? "").trim();
  const explicit = candidates.some((id) => list.includes(id));
  if (!head && candidates.length && !explicit) {
    const fallback = String((segments ?? []).find((s) => s.id === candidates[0])?.text ?? "").trim();
    head = fallback;
  }
  return assemblePrompt(head, list, segments);
}

// missingSegments：引用了、但库里没有的段 id（界面标红用；正常路径由后端写入/启动校验挡掉）。
export function missingSegments(ids, segments) {
  const have = new Set((segments ?? []).map((s) => s.id));
  return (ids ?? []).filter((id) => !have.has(id));
}
