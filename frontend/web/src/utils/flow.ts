// FlowAgent 图的前端纯数据层：空图、归一化、保存前检查。
//
// 这一层不认识 Vue Flow，也不认识运行时；它只认 `FlowGraph` 的 JSON。
// 节点类型先保持最小集合：input / agent / join / output。
// 以后加 template / condition 时，先在这里扩白名单，再改编辑器与运行时。
import type { FlowEdge, FlowGraph, FlowNode } from "../api/types.ts";

export const FLOW_VERSION = 1;
export const FLOW_NODE_TYPES = ["input", "agent", "join", "output"] as const;
export type FlowNodeType = (typeof FLOW_NODE_TYPES)[number];

export function emptyFlowGraph(): FlowGraph {
  return {
    version: FLOW_VERSION,
    nodes: [
      { id: "input-1", type: "input", position: { x: 80, y: 120 }, data: {} },
      { id: "output-1", type: "output", position: { x: 380, y: 120 }, data: {} },
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
      type: String(n.type ?? "agent"),
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
// 第一条硬规矩：**FlowAgent 不允许再调用 FlowAgent** —— 这里的 agent 节点只能指向传统 Agent。
export function flowIssues(graph: FlowGraph, agents: Array<{ id: string; name?: string; kind?: string }>): string[] {
  const issues: string[] = [];
  const nodes = graph.nodes ?? [];
  const edges = graph.edges ?? [];
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const counts = (type: string): number => nodes.filter((n) => n.type === type).length;
  if (counts("input") !== 1) issues.push("需要恰好一个输入节点");
  if (counts("output") !== 1) issues.push("需要恰好一个输出节点");
  for (const node of nodes) {
    if (!FLOW_NODE_TYPES.includes(node.type as FlowNodeType)) issues.push(`不认识的节点类型：${node.type}`);
    if (node.type === "agent") {
      const id = String(node.data?.agent_id ?? "").trim();
      if (!id) issues.push("有 Agent 节点还没选智能体");
      else {
        const target = agents.find((a) => a.id === id);
        if (!target) issues.push(`Agent 节点指向不存在的智能体：${id}`);
        else if (target.kind === "flow") issues.push("FlowAgent 不能调用另一个 FlowAgent");
      }
    }
  }
  for (const edge of edges) {
    if (!byId.has(edge.source) || !byId.has(edge.target)) issues.push("有连线指向不存在的节点");
    if (edge.source === edge.target) issues.push("节点不能连到自己");
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
