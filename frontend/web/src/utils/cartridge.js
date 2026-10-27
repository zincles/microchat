// Agent 卡带（2026-10-11 用户定）：**agent 自己 + 它引用到的用户段**（id + 原文）——
// 结构照旧按 id 引用（**不拼平**）：段随卡带走 ⇒ 仍是零依赖，导进去还能继续组合、编辑。
//
// - 内置段**不带**（每台装完都有；且内置 id 保留、也造不出来）——agent 里对内置的引用照旧有效。
// - 导入的合并规则：系统里已有同 id 的段（用户段或内置）⇒ **丢弃卡带那份**（用本地的）；
//   没有 ⇒ 建它，并打上「导入」来源标记（`imported: true`）。
// - v1 老卡带（agent 的提示词已拼平、无 `segments` 数组）照样能导：字段都可缺省。
export const CARTRIDGE_VERSION = 2;

// buildCartridge：agent（`GET /agents` 的形状）+ 段库 ⇒ 卡带对象。
export function buildCartridge(agent, segments) {
  const refs = (agent.segments ?? []).map((x) => String(x));
  const bundled = [];
  for (const id of refs) {
    const seg = (segments ?? []).find((s) => s.id === id);
    if (seg && !seg.builtin) bundled.push({ id: seg.id, text: seg.text }); // 内置不带
  }
  return {
    version: CARTRIDGE_VERSION,
    agent: {
      name: agent.name ?? "",
      system_prompt: agent.system_prompt ?? "", // base 原文（不拼平）
      segments: refs, // 引用照旧
      first_message: agent.first_message ?? "",
      abilities: agent.abilities ?? {},
      prepend_state: !!agent.prepend_state,
      // ⚠ 没有 `display_mode`（2026-10-27 删）：显示只有一种形态，卡带里不必再记。
      // 老卡带里带着它也不报错 —— 读进来时没人看，下次导出自然就没了。
    },
    segments: bundled,
  };
}

// cartridgeAgent：从卡带文件里取 {agent, segments}（形状不对 ⇒ 抛说得清的错）。
export function cartridgeAgent(doc) {
  const agent = doc?.agent;
  if (!agent || typeof agent !== "object") throw new Error("这不是 agent 卡带（缺 agent 对象）");
  if (!String(agent.name ?? "").trim()) throw new Error("卡带里没有名字（agent.name）");
  const segments = [];
  for (const s of doc?.segments ?? []) {
    const id = String(s?.id ?? "").trim();
    if (!id) throw new Error("卡带里有空 id 的段");
    segments.push({ id, text: String(s?.text ?? "") });
  }
  return {
    agent: {
      name: String(agent.name).trim(),
      system_prompt: String(agent.system_prompt ?? ""),
      segments: (agent.segments ?? []).map((x) => String(x)),
      first_message: String(agent.first_message ?? ""),
      abilities: agent.abilities ?? {},
      prepend_state: !!agent.prepend_state,
    },
    segments,
  };
}
