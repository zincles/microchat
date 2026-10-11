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

// missingSegments：引用了、但库里没有的段 id（界面标红用；正常路径由后端写入/启动校验挡掉）。
export function missingSegments(ids, segments) {
  const have = new Set((segments ?? []).map((s) => s.id));
  return (ids ?? []).filter((id) => !have.has(id));
}
