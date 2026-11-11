// FlowAgent 图的前端纯数据层：空图、归一化、保存前检查。
//
// 这一层不认识 Vue Flow，也不认识运行时；它只认 `FlowGraph` 的 JSON。
// 节点类型先保持最小集合：input（字符串）/ history（messages[]）/ trim（messages[]）/
// llm / join / output。以后加 template / condition 时，先在这里扩白名单，再改编辑器与运行时。
import type { FlowEdge, FlowGraph, FlowNode } from "../api/types.ts";

export const FLOW_VERSION = 1;
export const DEFAULT_JOIN_SEPARATOR = "\n\n---\n\n";
export const FLOW_NODE_TYPES = ["input", "history", "trim", "llm", "join", "output"] as const;
export type FlowNodeType = (typeof FLOW_NODE_TYPES)[number];
export type FlowDataType = "string" | "messages";

export const TRIM_MODES = [
  { value: "tail", title: "保留最近 N 条" },
  { value: "head", title: "保留最早 N 条" },
  { value: "drop-head", title: "去掉开头 N 条" },
  { value: "drop-tail", title: "去掉结尾 N 条" },
] as const;
export type TrimMode = (typeof TRIM_MODES)[number]["value"];
export const DEFAULT_TRIM_COUNT = 10;

const norm = (handle?: string, fallback = "out"): string => handle || fallback;

// 端口类型是连接校验的唯一依据：字符串只能接字符串，messages[] 只能接 messages 端口。
export function flowSourceType(node: FlowNode | undefined, handle?: string): FlowDataType | null {
  if (!node) return null;
  const h = norm(handle, "out");
  switch (node.type) {
    case "input":
    case "join":
      return h === "out" || /^out-\d+$/.test(h) ? "string" : null;
    case "history":
    case "trim":
      return h === "out" ? "messages" : null;
    case "llm":
      return h === "text" || h === "reasoning" ? "string" : null;
    default:
      return null;
  }
}

export function flowTargetType(node: FlowNode | undefined, handle?: string): FlowDataType | null {
  if (!node) return null;
  const h = norm(handle, "in");
  switch (node.type) {
    case "llm":
      if (h.startsWith("history-")) return "messages";
      return h.startsWith("prompt-") ? "string" : null;
    case "trim":
      return h === "in" ? "messages" : null;
    case "join":
      return h.startsWith("in-") ? "string" : null;
    case "output":
      return h === "in" ? "string" : null;
    default:
      return null;
  }
}

export function trimModeOf(data?: Record<string, any> | null): TrimMode {
  const value = String(data?.mode ?? "tail");
  return TRIM_MODES.some((mode) => mode.value === value) ? value as TrimMode : "tail";
}

export function trimCountOf(data?: Record<string, any> | null): number {
  const count = Number(data?.count ?? DEFAULT_TRIM_COUNT);
  return Number.isInteger(count) && count > 0 ? count : DEFAULT_TRIM_COUNT;
}

export function trimMessages<T>(messages: readonly T[], mode: TrimMode, count: number): T[] {
  const n = Number.isInteger(count) && count > 0 ? count : DEFAULT_TRIM_COUNT;
  switch (mode) {
    case "head": return messages.slice(0, n);
    case "drop-head": return messages.slice(n);
    case "drop-tail": return messages.slice(0, Math.max(0, messages.length - n));
    default: return messages.slice(Math.max(0, messages.length - n));
  }
}

export function emptyFlowGraph(): FlowGraph {
  return {
    version: FLOW_VERSION,
    nodes: [
      { id: "input-1", type: "input", position: { x: 40, y: 100 }, data: {} },
      { id: "output-1", type: "output", position: { x: 300, y: 100 }, data: {} },
    ],
    edges: [],
  };
}

export function normalizeFlowGraph(graph?: FlowGraph | null): FlowGraph {
  if (!graph || !Array.isArray(graph.nodes) || !Array.isArray(graph.edges)) return emptyFlowGraph();
  const nodes: FlowNode[] = graph.nodes
    .filter((n) => n && typeof n.id === "string" && n.id)
    .map((n) => ({
      id: n.id,
      type: String(n.type ?? "llm"),
      position: { x: Number(n.position?.x ?? 0), y: Number(n.position?.y ?? 0) },
      data: n.data && typeof n.data === "object" ? { ...n.data } : {},
    }));
  const ids = new Set(nodes.map((n) => n.id));
  const edges: FlowEdge[] = graph.edges
    .filter((e) => e && ids.has(e.source) && ids.has(e.target))
    .map((e) => ({
      id: String(e.id ?? `edge-${e.source}-${e.target}`),
      source: e.source,
      target: e.target,
      ...(e.sourceHandle ? { sourceHandle: e.sourceHandle } : {}),
      ...(e.targetHandle ? { targetHandle: e.targetHandle } : {}),
    }));
  return { version: FLOW_VERSION, nodes, edges };
}

// 保存前检查：不阻止保存草稿，但编辑器要把问题摆出来。
// 第一版只允许 LLM 节点手动组装 messages；不引用 Agent，所以不存在嵌套 FlowAgent。
export function flowIssues(graph: FlowGraph): string[] {
  const issues: string[] = [];
  const nodes = graph.nodes ?? [];
  const edges = graph.edges ?? [];
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const counts = (type: string): number => nodes.filter((n) => n.type === type).length;
  if (counts("input") !== 1) issues.push("需要恰好一个输入节点");
  if (counts("output") !== 1) issues.push("需要恰好一个输出节点");
  for (const node of nodes) {
    if (!FLOW_NODE_TYPES.includes(node.type as FlowNodeType)) issues.push(`不认识的节点类型：${node.type}`);
    if (node.type === "history" && !edges.some((e) => e.source === node.id)) {
      issues.push("历史数据节点还没有接出去");
    }
    if (node.type === "trim") {
      const mode = String(node.data?.mode ?? "tail");
      if (!TRIM_MODES.some((item) => item.value === mode)) issues.push("裁剪节点有未知的裁剪方式");
      const count = Number(node.data?.count ?? DEFAULT_TRIM_COUNT);
      if (!Number.isInteger(count) || count < 1) issues.push("裁剪条数必须是正整数");
    }
    if (node.type === "llm") {
      const prompts = Array.isArray(node.data?.prompts) ? node.data.prompts : [];
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
    }
  }
  for (const node of nodes) {
    if (node.type === "join") {
      const inputs = Array.isArray(node.data?.inputs) ? node.data.inputs : ["in-1", "in-2"];
      const seenInputs = new Set<string>();
      for (const input of inputs) {
        const id = String(input ?? "").trim();
        if (!id || seenInputs.has(id)) issues.push("拼接节点有空的或重复的输入端口");
        seenInputs.add(id);
      }
      if (!inputs.length) issues.push("拼接节点至少需要一个输入端口");
      const outputs = Array.isArray(node.data?.outputs) ? node.data.outputs : ["out-1"];
      const seenOutputs = new Set<string>();
      for (const output of outputs) {
        const id = String(output ?? "").trim();
        if (!id || seenOutputs.has(id)) issues.push("拼接节点有空的或重复的输出端口");
        seenOutputs.add(id);
      }
      if (!outputs.length) issues.push("拼接节点至少需要一个输出端口");
    }
  }
  const usedSources = new Set<string>();
  const usedTargets = new Set<string>();
  for (const edge of edges) {
    if (!byId.has(edge.source) || !byId.has(edge.target)) issues.push("有连线指向不存在的节点");
    if (edge.source === edge.target) issues.push("节点不能连到自己");
    const sourceHandle = edge.sourceHandle || "out";
    const targetHandle = edge.targetHandle || (byId.get(edge.target)?.type === "output" ? "in" : "in");
    const sourceKey = `${edge.source}\0${sourceHandle}`;
    const targetKey = `${edge.target}\0${targetHandle}`;
    if (usedSources.has(sourceKey)) issues.push("一个出口只能接一条线（要多路请用拼接节点）");
    if (usedTargets.has(targetKey)) issues.push("一个入口只能接一条线（要多路请用拼接节点）");
    usedSources.add(sourceKey);
    usedTargets.add(targetKey);
    const st = flowSourceType(byId.get(edge.source), sourceHandle);
    const tt = flowTargetType(byId.get(edge.target), targetHandle);
    if (!st || !tt) issues.push("连线指向了不存在的端口");
    else if (st !== tt) issues.push(`端口类型不匹配：${st} → ${tt}`);
  }
  const output = nodes.find((n) => n.type === "output");
  if (output && !edges.some((e) => e.target === output.id)) issues.push("输出节点还没有输入");
  // 拓扑排序查环：节点少，直接 Kahn。
  const indegree = new Map(nodes.map((n) => [n.id, 0]));
  for (const edge of edges) if (indegree.has(edge.target)) indegree.set(edge.target, (indegree.get(edge.target) ?? 0) + 1);
  const queue = [...indegree.entries()].filter(([, d]) => d === 0).map(([id]) => id);
  let visited = 0;
  while (queue.length) {
    const id = queue.shift()!;
    visited++;
    for (const edge of edges.filter((e) => e.source === id)) {
      const next = indegree.get(edge.target);
      if (next == null) continue;
      indegree.set(edge.target, next - 1);
      if (next - 1 === 0) queue.push(edge.target);
    }
  }
  if (nodes.length && visited !== nodes.length) issues.push("流程图里有环（第一版只支持 DAG）");
  return [...new Set(issues)];
}

export interface FlowDebugStep {
  id: string;
  type: string;
  label: string;
  inputs: string[];
  output: string;
  reasoning?: string;
}

// 调试预演用的节点顺序：Kahn 拓扑；有环时把剩下的按原顺序附在末尾，
// 让调试面板仍能展示“这里有环”，而不是整块空白。
export function flowNodeOrder(graph: FlowGraph): FlowNode[] {
  const nodes = graph.nodes ?? [];
  const edges = graph.edges ?? [];
  const indegree = new Map(nodes.map((n) => [n.id, 0]));
  for (const e of edges) if (indegree.has(e.target)) indegree.set(e.target, (indegree.get(e.target) ?? 0) + 1);
  const queue = nodes.filter((n) => (indegree.get(n.id) ?? 0) === 0).map((n) => n.id);
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const out: FlowNode[] = [];
  while (queue.length) {
    const id = queue.shift()!;
    const node = byId.get(id);
    if (node) out.push(node);
    for (const e of edges.filter((e) => e.source === id)) {
      const next = indegree.get(e.target);
      if (next == null) continue;
      indegree.set(e.target, next - 1);
      if (next - 1 === 0) queue.push(e.target);
    }
  }
  const seen = new Set(out.map((n) => n.id));
  for (const n of nodes) if (!seen.has(n.id)) out.push(n);
  return out;
}

// 结构预演：不调用模型，只把“上游输出”当占位文本往下传。
// 重点是看连线：LLM 的 reasoning / text 两个出口按 sourceHandle 分开走。
export function flowDebugSteps(graph: FlowGraph): FlowDebugStep[] {
  const nodes = flowNodeOrder(graph);
  const edges = graph.edges ?? [];
  const values = new Map<string, { text: string; reasoning: string }>();
  const labels: Record<string, string> = { input: "输入", history: "历史数据", trim: "裁剪 messages", llm: "调用 LLM", join: "拼接和复制", output: "输出" };
  const source = (edge: { source: string; sourceHandle?: string }): string => {
    const handle = edge.sourceHandle || "out";
    const value = values.get(`${edge.source}\0${handle}`) ?? values.get(`${edge.source}\0out`) ?? values.get(`${edge.source}\0text`);
    if (!value) return "（上游未产出）";
    return handle === "reasoning" ? (value.reasoning || "（无思考）") : value.text;
  };
  const steps: FlowDebugStep[] = [];
  for (const node of nodes) {
    const incoming = edges.filter((e) => e.target === node.id).map(source);
    let output = "";
    let reasoning = "";
    let label = labels[node.type] ?? node.type;
    if (node.type === "input") {
      output = "[本轮用户输入]";
    } else if (node.type === "history") {
      output = "[会话历史 messages]";
    } else if (node.type === "trim") {
      output = "[裁剪后 messages]";
    } else if (node.type === "llm") {
      label = "调用 LLM";
      output = "[LLM 正文]";
      reasoning = "[LLM 思考]";
    } else if (node.type === "join") {
      const separator = typeof node.data?.separator === "string" ? node.data.separator : DEFAULT_JOIN_SEPARATOR;
      output = incoming.length ? incoming.join(separator) : "[没有输入]";
    } else if (node.type === "output") {
      output = incoming.length ? incoming.join("\n---\n") : "[没有输入]";
    } else {
      output = "[未知节点]";
    }
    values.set(`${node.id}\0out`, { text: output, reasoning });
    if (node.type === "join") {
      const outputs = Array.isArray(node.data?.outputs) ? node.data.outputs : ["out-1"];
      for (const port of outputs) values.set(`${node.id}\0${port}`, { text: output, reasoning: "" });
    }
    if (node.type === "llm") {
      values.set(`${node.id}\0text`, { text: output, reasoning: "" });
      values.set(`${node.id}\0reasoning`, { text: "", reasoning });
    }
    steps.push({ id: node.id, type: node.type, label, inputs: incoming, output, reasoning: reasoning || undefined });
  }
  return steps;
}
