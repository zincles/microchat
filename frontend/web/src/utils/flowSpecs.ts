// Flow 节点的**结构权威**：类型、端口、默认 data、显示元信息。
//
// 以前这些事实散在 FlowNode.vue / FlowEditor.vue / flow.ts / flowtest.go 四份；
// 加一个节点要改多处，漏一处就静默失效。这里先把前端这一侧的"一个节点长什么样"
// 收成一张表；运行时执行仍由 flowtest.go 的 runner 负责。
import type { FlowNode } from "../api/types.ts";
import type { FlowDataType } from "./flow.ts";

export const TRIM_MODES = [
  { value: "tail", title: "保留最近 N 条" },
  { value: "head", title: "保留最早 N 条" },
  { value: "drop-head", title: "去掉开头 N 条" },
  { value: "drop-tail", title: "去掉结尾 N 条" },
] as const;
export type TrimMode = (typeof TRIM_MODES)[number]["value"];
export const DEFAULT_TRIM_COUNT = 10;
export const TRIM_ROLES = ["system", "user", "assistant"] as const;

export type FlowNodeKind =
  | "input" | "text" | "history" | "trim" | "merge"
  | "llm" | "join" | "judge" | "output";

export interface FlowPortSpec {
  handle: string;
  type: FlowDataType;
  label: string;
  required?: boolean;
}

export interface FlowNodeSpec {
  type: FlowNodeKind;
  label: string;
  badge: string;
  hint: string;
  category: "source" | "transform" | "sink";
  /** 新增节点时写进 data 的默认值。 */
  defaults: () => Record<string, unknown>;
  /** 静态端口或按 node.data 计算的动态端口。 */
  inputs: (node: FlowNode) => FlowPortSpec[];
  outputs: (node: FlowNode) => FlowPortSpec[];
  /** 节点自己的配置问题；全局的输入/输出/边校验仍在 flowIssues。 */
  validate?: (node: FlowNode) => string[];
}

const dataOf = (node: Pick<FlowNode, "data"> | undefined): Record<string, unknown> => node?.data ?? {};

interface FlowPromptDraft {
  id?: unknown;
  kind?: unknown;
  role?: unknown;
  content?: unknown;
}

const promptsOf = (node: FlowNode): FlowPromptDraft[] => {
  const prompts = dataOf(node).prompts;
  return Array.isArray(prompts) ? prompts as FlowPromptDraft[] : [];
};
const strings = (value: unknown): string[] => Array.isArray(value) ? value.map(String) : [];

function answerTypeOf(node: Pick<FlowNode, "data"> | undefined): FlowDataType {
  const type = String(dataOf(node).answer_type ?? "string");
  return type === "bool" || type === "number" || type === "choice" ? type : "string";
}

function llmInputs(node: FlowNode): FlowPortSpec[] {
  const ports: FlowPortSpec[] = [{ handle: "enabled", type: "bool", label: "启用" }];
  const prompts = promptsOf(node);
  for (const prompt of prompts) {
    const id = String(prompt?.id ?? "").trim();
    if (!id) continue;
    ports.push(prompt?.kind === "history"
      ? { handle: `history-${id}`, type: "messages", label: "历史" }
      : { handle: `prompt-${id}`, type: "string", label: "Prompt" });
  }
  return ports;
}

function trimIssues(node: FlowNode): string[] {
  const data = dataOf(node);
  const issues: string[] = [];
  const mode = String(data.mode ?? "tail");
  if (!TRIM_MODES.some((item) => item.value === mode)) issues.push("裁剪节点有未知的裁剪方式");
  const count = Number(data.count ?? DEFAULT_TRIM_COUNT);
  if (!Number.isInteger(count) || count < 1) issues.push("裁剪条数必须是正整数");
  if (Array.isArray(data.roles)) {
    for (const role of data.roles) {
      if (!(TRIM_ROLES as readonly string[]).includes(String(role))) issues.push("裁剪节点有未知的角色");
    }
  }
  return issues;
}

function portsIssues(kind: string, values: unknown, fallback: string[]): string[] {
  const inputs = Array.isArray(values) ? values : fallback;
  const issues: string[] = [];
  const seen = new Set<string>();
  for (const input of inputs) {
    const id = String(input ?? "").trim();
    if (!id || seen.has(id)) issues.push(`${kind}有空的或重复的端口`);
    seen.add(id);
  }
  if (!inputs.length) issues.push(`${kind}至少需要一个端口`);
  return issues;
}

function llmIssues(node: FlowNode): string[] {
  const prompts = promptsOf(node);
  const issues: string[] = [];
  if (!prompts.length) issues.push("有 LLM 节点还没有 Prompt 条目");
  const seen = new Set<string>();
  for (const prompt of prompts) {
    const id = String(prompt?.id ?? "").trim();
    if (!id || seen.has(id)) issues.push("LLM 节点里有空 id 或重复的 Prompt/历史条目");
    seen.add(id);
    const kind = String(prompt?.kind ?? "prompt");
    if (kind !== "prompt" && kind !== "history") issues.push("LLM 条目类型只能是 prompt 或 history");
    if (kind !== "history" && !["system", "user", "assistant"].includes(String(prompt?.role ?? ""))) {
      issues.push("Prompt 条目的 role 只能是 system / user / assistant");
    }
  }
  return issues;
}

function judgeIssues(node: FlowNode): string[] {
  const data = dataOf(node);
  const issues: string[] = [];
  if (!String(data.provider ?? "").trim()) issues.push("JEV 判断节点还没选渠道");
  if (!String(data.model ?? "").trim()) issues.push("JEV 判断节点还没填模型");
  const answerType = String(data.answer_type ?? "string");
  if (!["string", "bool", "number", "choice"].includes(answerType)) issues.push("JEV 判断节点的答案类型不认识");
  const raw = data.questions;
  if (typeof raw !== "string" || !raw.trim()) {
    issues.push("JEV 判断节点还没有 questions");
  } else {
    try {
      const parsed = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) issues.push("JEV 判断节点的 questions 必须是 JSON object");
    } catch {
      issues.push("JEV 判断节点的 questions 不是合法 JSON");
    }
  }
  return issues;
}

const joinPorts = (node: FlowNode, key: "inputs" | "outputs", fallback: string[]): string[] => {
  const list = strings(dataOf(node)[key]);
  return list.length ? list : fallback;
};

const specs: Record<FlowNodeKind, FlowNodeSpec> = {
  input: {
    type: "input", label: "输入", badge: "IN", hint: "裸字符串：本轮用户输入",
    category: "source", defaults: () => ({}),
    inputs: () => [], outputs: () => [{ handle: "out", type: "string", label: "输出", required: true }],
  },
  text: {
    type: "text", label: "纯文本", badge: "TXT", hint: "无输入：输出这里写死的常量文本",
    category: "source", defaults: () => ({ text: "" }),
    inputs: () => [], outputs: () => [{ handle: "out", type: "string", label: "输出", required: true }],
  },
  history: {
    type: "history", label: "历史数据", badge: "H", hint: "messages[]：当前会话历史",
    category: "source", defaults: () => ({}),
    inputs: () => [], outputs: () => [{ handle: "out", type: "messages", label: "messages", required: true }],
  },
  trim: {
    type: "trim", label: "裁剪 messages", badge: "TR", hint: "messages[] → messages[]：按条数裁剪",
    category: "transform", defaults: () => ({ mode: "tail", count: 10 }),
    validate: trimIssues,
    inputs: () => [{ handle: "in", type: "messages", label: "messages", required: true }],
    outputs: () => [{ handle: "out", type: "messages", label: "messages", required: true }],
  },
  merge: {
    type: "merge", label: "拼接 messages", badge: "MG", hint: "多路 messages[] 按端口顺序拼接",
    category: "transform", defaults: () => ({ inputs: ["in-1", "in-2"] }),
    validate: (node) => portsIssues("拼接 messages 节点", dataOf(node).inputs, ["in-1", "in-2"]),
    inputs: (node) => joinPorts(node, "inputs", ["in-1", "in-2"]).map((handle) => ({ handle, type: "messages", label: handle })),
    outputs: () => [{ handle: "out", type: "messages", label: "messages", required: true }],
  },
  llm: {
    type: "llm", label: "调用 LLM", badge: "AI", hint: "手动组装 messages",
    category: "transform",
    defaults: () => ({ prompts: [{ id: `p-${Date.now().toString(36)}`, role: "system", content: "" }] }),
    validate: llmIssues,
    inputs: llmInputs,
    outputs: () => [
      { handle: "reasoning", type: "string", label: "思考" },
      { handle: "text", type: "string", label: "正文" },
    ],
  },
  join: {
    type: "join", label: "拼接和复制", badge: "J", hint: "多路拼接；只接一路时直接复制",
    category: "transform",
    defaults: () => ({ inputs: ["in-1", "in-2"], outputs: ["out-1"], separator: "\n\n---\n\n" }),
    validate: (node) => [
      ...portsIssues("拼接节点", dataOf(node).inputs, ["in-1", "in-2"]),
      ...portsIssues("拼接节点输出", dataOf(node).outputs, ["out-1"]),
    ],
    inputs: (node) => joinPorts(node, "inputs", ["in-1", "in-2"]).map((handle) => ({ handle, type: "string", label: handle })),
    outputs: (node) => joinPorts(node, "outputs", ["out-1"]).map((handle) => ({ handle, type: "string", label: handle })),
  },
  judge: {
    type: "judge", label: "JEV 判断", badge: "JEV", hint: "把场景/状态交给 JEV，输出概率答案 JSON",
    category: "transform",
    defaults: () => ({
      provider: "", model: "", answer_type: "string", answer_key: "", threshold: 0.5,
      questions: JSON.stringify({ involves: "本轮用户行为是否直接涉及这个角色？" }, null, 2),
    }),
    validate: judgeIssues,
    inputs: () => [{ handle: "state", type: "string", label: "场景/状态", required: true }],
    outputs: (node) => [{ handle: "out", type: answerTypeOf(node), label: "答案", required: true }],
  },
  output: {
    type: "output", label: "输出", badge: "OUT", hint: "最终结果写入聊天；用户消息端口可改写入库正文",
    category: "sink", defaults: () => ({}),
    inputs: () => [
      { handle: "text", type: "string", label: "正文" },
      { handle: "reasoning", type: "string", label: "思考" },
      { handle: "user", type: "string", label: "用户消息" },
    ],
    outputs: () => [],
  },
};

export const FLOW_NODE_SPECS = specs;

export function flowNodeSpec(type: string | undefined): FlowNodeSpec | undefined {
  return specs[type as FlowNodeKind];
}

export function nodeDefaults(type: string): Record<string, unknown> {
  return flowNodeSpec(type)?.defaults() ?? {};
}

export function nodeLabelFor(nodeOrType: FlowNode | string | undefined): string {
  const type = typeof nodeOrType === "string" ? nodeOrType : nodeOrType?.type;
  return flowNodeSpec(type)?.label ?? String(type ?? "");
}

export function nodeBadgeFor(nodeOrType: FlowNode | string | undefined): string {
  const type = typeof nodeOrType === "string" ? nodeOrType : nodeOrType?.type;
  return flowNodeSpec(type)?.badge ?? "·";
}

export function nodeHintFor(nodeOrType: FlowNode | string | undefined): string {
  const type = typeof nodeOrType === "string" ? nodeOrType : nodeOrType?.type;
  return flowNodeSpec(type)?.hint ?? "";
}

export function flowInputPorts(node: FlowNode): FlowPortSpec[] {
  return flowNodeSpec(node.type)?.inputs(node) ?? [];
}

export function flowOutputPorts(node: FlowNode): FlowPortSpec[] {
  return flowNodeSpec(node.type)?.outputs(node) ?? [];
}

export function judgeAnswerType(node: Pick<FlowNode, "data"> | undefined): FlowDataType {
  return answerTypeOf(node);
}
