// Agent 卡带（2026-10-11 用户定）：**只导出 agent 自己** —— 用到的提示词段在导出时
// **拼平**（base + 逐段拼接，规则与后端 `state.AssembleSystemPrompt` 一致，见 utils/prompt.js）
// 写进 `system_prompt`，**不留任何引用** ⇒ 免除依赖、方便再分发（导入后是一段大提示词，
// 要不要拆段由人后续处理）。id 不带（导入方现铸）；渠道是软引用，也不带。
import { assemblePrompt } from "./prompt.js";

export const CARTRIDGE_VERSION = 1;

// buildCartridge：agent（`GET /agents` 的形状）+ 段库 ⇒ 卡带对象。
export function buildCartridge(agent, segments) {
  return {
    version: CARTRIDGE_VERSION,
    agent: {
      name: agent.name ?? "",
      system_prompt: assemblePrompt(agent.system_prompt, agent.segments, segments),
      first_message: agent.first_message ?? "",
      abilities: agent.abilities ?? {},
      prepend_state: !!agent.prepend_state,
      display_mode: agent.display_mode ?? "",
    },
  };
}

// cartridgeAgent：从卡带文件里取 agent 对象（形状不对 ⇒ 抛说得清的错）。
export function cartridgeAgent(doc) {
  const agent = doc?.agent;
  if (!agent || typeof agent !== "object") throw new Error("这不是 agent 卡带（缺 agent 对象）");
  if (!String(agent.name ?? "").trim()) throw new Error("卡带里没有名字（agent.name）");
  return {
    name: String(agent.name).trim(),
    system_prompt: String(agent.system_prompt ?? ""),
    first_message: String(agent.first_message ?? ""),
    abilities: agent.abilities ?? {},
    prepend_state: !!agent.prepend_state,
    display_mode: agent.display_mode === "roleplay" ? "roleplay" : undefined,
  };
}
