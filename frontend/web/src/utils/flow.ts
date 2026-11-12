// FlowAgent 图的前端纯数据层：空图、归一化、保存前检查。
//
// 这一层不认识 Vue Flow，也不认识运行时；它只认 `FlowGraph` 的 JSON。
// 节点类型先保持最小集合：input（字符串）/ history（messages[]）/ trim（messages[]）/
// llm / join / output。以后加 template / condition 时，先在这里扩白名单，再改编辑器与运行时。
import type { FlowEdge, FlowGraph, FlowNode } from "../api/types.ts";
import { DEFAULT_TRIM_COUNT, FLOW_NODE_SPECS, TRIM_MODES, TRIM_ROLES, flowInputPorts, flowNodeSpec, flowOutputPorts, judgeAnswerType, nodeLabelFor, type FlowNodeKind, type TrimMode } from "./flowSpecs.ts";
export { DEFAULT_TRIM_COUNT, TRIM_MODES, TRIM_ROLES } from "./flowSpecs.ts";
export type { TrimMode } from "./flowSpecs.ts";

export const FLOW_VERSION = 1;
export const DEFAULT_JOIN_SEPARATOR = "\n\n---\n\n";
export const FLOW_NODE_TYPES = Object.keys(FLOW_NODE_SPECS) as FlowNodeKind[];
export type FlowNodeType = FlowNodeKind;
export type FlowDataType = "string" | "messages" | "bool" | "number" | "choice";

const norm = (handle?: string, fallback = "out"): string => handle || fallback;

// 端口类型是连接校验的唯一依据：结构由 flowSpecs.ts 的 NodeSpec 提供。
// 这里只保留两个兼容映射：老 join 的 `out`、老 output 的 `in`。
export function flowSourceType(node: FlowNode | undefined, handle?: string): FlowDataType | null {
  if (!node) return null;
  const h = norm(handle, "out");
  const ports = flowOutputPorts(node);
  const port = ports.find((item) => item.handle === h);
  if (port) return port.type;
  if (h === "out" && node.type === "join") return ports[0]?.type ?? null;
  return null;
}

export function flowTargetType(node: FlowNode | undefined, handle?: string): FlowDataType | null {
  if (!node) return null;
  const h = norm(handle, "in");
  if (node.type === "output" && h === "in") return "string";
  return flowInputPorts(node).find((item) => item.handle === h)?.type ?? null;
}

export { judgeAnswerType };

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

// formatFlowText：图的纯文本视图。用 id + 类型 + 位置 + 每个端口的去向，
// 方便在没有画布的地方（日志、聊天、code review）核对连接。
export function formatFlowText(graph?: FlowGraph | null): string {
  const nodes = graph?.nodes ?? [];
  const edges = graph?.edges ?? [];
  const byID = new Map(nodes.map((node) => [node.id, node]));
  const lines: string[] = [`AgentFlow · ${nodes.length} nodes / ${edges.length} edges`];
  for (const node of nodes) {
    const label = nodeLabelFor(node);
    const x = Math.round(Number(node.position?.x ?? 0));
    const y = Math.round(Number(node.position?.y ?? 0));
    const height = Number(node.data?.height);
    const retry = Number(node.data?.max_retry);
    const size = Number.isFinite(height) && height > 0 ? ` h=${Math.round(height)}px` : "";
    const retryText = node.type === "llm" ? ` retry=${Number.isInteger(retry) && retry >= 0 ? retry : 3}` : "";
    lines.push(`${node.id} [${label}] @ (${x}, ${y})${size}${retryText}`);
    const outgoing = edges.filter((edge) => edge.source === node.id);
    if (!outgoing.length) {
      lines.push("  （无输出连接）");
      continue;
    }
    for (const edge of outgoing) {
      const target = byID.get(edge.target);
      const targetLabel = target ? nodeLabelFor(target) : "?";
      lines.push(`  ${edge.sourceHandle || "out"} → ${edge.target}:${edge.targetHandle || "in"} [${targetLabel}]`);
    }
  }
  return lines.join("\n");
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
    }))
    .map((e) => {
      // 老图的 output 只有一个 `in`；新图把正文/思考分成 text / reasoning，载入时就地迁移。
      // 迁移看**源端口**：reasoning 源接到旧 `in` ⇒ 落 reasoning，不能把它悄悄变成正文。
      const target = nodes.find((n) => n.id === e.target);
      if (target?.type !== "output" || e.targetHandle !== "in") return e;
      return { ...e, targetHandle: e.sourceHandle === "reasoning" ? "reasoning" : "text" };
    });
  // user_output 已并进 output 的 `user` 端口。老图载入时把它的入线搬到 output.user，
  // 然后把这个节点丢掉；否则老图会带着一个运行时已不认识的节点继续跑。
  const output = nodes.find((n) => n.type === "output");
  const legacyUsers = new Set(nodes.filter((n) => n.type === "user_output").map((n) => n.id));
  if (output && legacyUsers.size) {
    for (const edge of edges) {
      if (!legacyUsers.has(edge.target)) continue;
      edge.target = output.id;
      edge.targetHandle = "user";
    }
  }
  const cleanNodes = nodes.filter((n) => !legacyUsers.has(n.id));
  const cleanEdges = edges.filter((e) => !legacyUsers.has(e.source) && !legacyUsers.has(e.target));
  return { version: FLOW_VERSION, nodes: cleanNodes, edges: cleanEdges };
}

// 保存前检查：不阻止保存草稿，但编辑器要把问题摆出来。
// 每个节点的配置问题由 NodeSpec.validate 负责；这里只做全局结构与连线校验。
export function flowIssues(graph: FlowGraph): string[] {
  const issues: string[] = [];
  const nodes = graph.nodes ?? [];
  const edges = graph.edges ?? [];
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const counts = (type: string): number => nodes.filter((n) => n.type === type).length;
  if (counts("input") !== 1) issues.push("需要恰好一个输入节点");
  if (counts("output") !== 1) issues.push("需要恰好一个输出节点");
  for (const node of nodes) {
    const spec = flowNodeSpec(node.type);
    if (!spec) {
      issues.push(`不认识的节点类型：${node.type}`);
      continue;
    }
    if (node.type === "history" && !edges.some((e) => e.source === node.id)) {
      issues.push("历史数据节点还没有接出去");
    }
    issues.push(...(spec.validate?.(node) ?? []));
  }
  const usedSources = new Set<string>();
  const usedTargets = new Set<string>();
  for (const edge of edges) {
    if (!byId.has(edge.source) || !byId.has(edge.target)) issues.push("有连线指向不存在的节点");
    if (edge.source === edge.target) issues.push("节点不能连到自己");
    const sourceHandle = edge.sourceHandle || "out";
    const targetHandle = edge.targetHandle || "in";
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
    let label = nodeLabelFor(node);
    if (node.type === "input") {
      output = "[本轮用户输入]";
    } else if (node.type === "text") {
      output = String(node.data?.text ?? "");
    } else if (node.type === "history") {
      output = "[会话历史 messages]";
    } else if (node.type === "trim") {
      output = "[裁剪后 messages]";
    } else if (node.type === "merge") {
      output = "[拼接后 messages]";
    } else if (node.type === "llm") {
      label = "调用 LLM";
      output = "[LLM 正文]";
      reasoning = "[LLM 思考]";
    } else if (node.type === "join") {
      const separator = typeof node.data?.separator === "string" ? node.data.separator : DEFAULT_JOIN_SEPARATOR;
      const ports = Array.isArray(node.data?.inputs) && node.data.inputs.length ? node.data.inputs.map(String) : ["in-1", "in-2"];
      const ordered = ports.flatMap((port) => edges
        .filter((edge) => edge.target === node.id && (edge.targetHandle === port || (port === "in-1" && !edge.targetHandle)))
        .map(source));
      output = ordered.length ? ordered.join(separator) : "[没有输入]";
    } else if (node.type === "judge") {
      output = "[JEV answers]";
    } else if (node.type === "output") {
      const textInputs = edges.filter((e) => e.target === node.id && (e.targetHandle || "text") === "text").map(source);
      const reasoningInputs = edges.filter((e) => e.target === node.id && e.targetHandle === "reasoning").map(source);
      const userInputs = edges.filter((e) => e.target === node.id && e.targetHandle === "user").map(source);
      output = textInputs.length ? textInputs.join("\n---\n") : "[没有正文输入]";
      reasoning = reasoningInputs.length ? reasoningInputs.join("\n---\n") : "";
      if (userInputs.length) reasoning = [reasoning, `[写入历史] ${userInputs.join("\n---\n")}`].filter(Boolean).join("\n");
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
