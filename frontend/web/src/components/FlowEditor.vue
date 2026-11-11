<script setup lang="ts">
// FlowAgent 的第一版节点编辑器（Vue Flow）。
//
// 职责边界：它只产出/读入 `FlowGraph` JSON；不执行、不调模型。
// 节点先保持最小集合：input / agent / join / output。
// 两条属于 FlowAgent 的硬约束在数据层（utils/flow.ts）与这里一起做：
//   · 只能调用传统 Agent，不能嵌套 FlowAgent；
//   · 第一版只允许 DAG，且恰好一个输入、一个输出。
import { computed, provide, ref, watch } from "vue";
import { VueFlow, type Connection } from "@vue-flow/core";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/controls/dist/style.css";
import { VBtn } from "vuetify/components";
import FlowNode from "./FlowNode.vue";
import { emptyFlowGraph, flowIssues, normalizeFlowGraph } from "../utils/flow.ts";
import type { Agent, FlowEdge, FlowGraph, FlowNode as FlowNodeData } from "../api/types.ts";

const props = withDefaults(defineProps<{
  graph?: FlowGraph | null;
  agents?: Agent[];
}>(), {
  graph: null,
  agents: () => [],
});
const emit = defineEmits<{ "update:graph": [graph: FlowGraph] }>();

// Vue Flow 的 NodeComponent 类型要求完整 NodeProps；我们的节点只声明实际用到的字段，
// 这里按 Vue Flow 的自定义节点约定交给运行时校验，别为了类型把无用 props 全抄一遍。
const nodeTypes = { input: FlowNode, agent: FlowNode, join: FlowNode, output: FlowNode } as any;
const initial = normalizeFlowGraph(props.graph ?? emptyFlowGraph());
const nodes = ref<any[]>(initial.nodes.map((n) => ({ ...n, data: { ...(n.data ?? {}) } })));
const edges = ref<any[]>(initial.edges.map((e) => ({ ...e })));
// 节点里的 VSelect 要拿到可选的传统 Agent；inject 比把 agents 往每个 node.data 里塞干净。
provide("flowAgents", computed(() => props.agents ?? []));

const issues = computed(() => flowIssues(toGraph(), props.agents ?? []));
const inputExists = computed(() => nodes.value.some((n) => n.type === "input"));
const outputExists = computed(() => nodes.value.some((n) => n.type === "output"));
let seq = 0;

function toGraph(): FlowGraph {
  return {
    version: 1,
    nodes: nodes.value.map((n) => ({
      id: String(n.id),
      type: String(n.type ?? "agent"),
      position: { x: Number(n.position?.x ?? 0), y: Number(n.position?.y ?? 0) },
      data: { ...(n.data ?? {}) },
    })),
    edges: edges.value.map((e) => ({
      id: String(e.id),
      source: String(e.source),
      target: String(e.target),
      ...(e.sourceHandle ? { sourceHandle: String(e.sourceHandle) } : {}),
      ...(e.targetHandle ? { targetHandle: String(e.targetHandle) } : {}),
    })),
  };
}

function addNode(type: "input" | "agent" | "join" | "output"): void {
  if (type === "input" && inputExists.value) return;
  if (type === "output" && outputExists.value) return;
  seq += 1;
  const id = `${type}-${Date.now().toString(36)}-${seq}`;
  const offset = nodes.value.length * 26;
  nodes.value = [...nodes.value, {
    id,
    type,
    position: { x: 150 + offset, y: 80 + offset },
    data: type === "agent" ? { agent_id: "" } : {},
  }];
}

function allowed(sourceType?: string, targetType?: string): boolean {
  const map: Record<string, string[]> = {
    input: ["agent", "join"],
    agent: ["agent", "join", "output"],
    join: ["agent", "output"],
  };
  return !!sourceType && !!targetType && (map[sourceType]?.includes(targetType) ?? false);
}
function isValidConnection(connection: Connection): boolean {
  if (!connection.source || !connection.target || connection.source === connection.target) return false;
  const source = nodes.value.find((n) => n.id === connection.source);
  const target = nodes.value.find((n) => n.id === connection.target);
  return allowed(source?.type, target?.type);
}
function onConnect(connection: Connection): void {
  if (!isValidConnection(connection)) return;
  const id = `edge-${Date.now().toString(36)}-${++seq}`;
  edges.value = [...edges.value, {
    id,
    source: connection.source,
    target: connection.target,
    ...(connection.sourceHandle ? { sourceHandle: connection.sourceHandle } : {}),
    ...(connection.targetHandle ? { targetHandle: connection.targetHandle } : {}),
  }];
}

// 位置、连线、节点内 agent_id 都是深层数据；一有变化就把整张图交回 AgentEditor。
watch([nodes, edges], () => emit("update:graph", toGraph()), { deep: true });
</script>

<template>
  <section class="flow-editor">
    <div class="flow-toolbar">
      <span class="flow-toolbar-label">添加节点：</span>
      <VBtn variant="tonal" size="small" type="button" :disabled="inputExists" @click="addNode('input')">输入</VBtn>
      <VBtn variant="tonal" size="small" type="button" @click="addNode('agent')">调用智能体</VBtn>
      <VBtn variant="tonal" size="small" type="button" @click="addNode('join')">拼接</VBtn>
      <VBtn variant="tonal" size="small" type="button" :disabled="outputExists" @click="addNode('output')">输出</VBtn>
    </div>
    <div class="flow-canvas">
      <VueFlow
        v-model:nodes="nodes"
        v-model:edges="edges"
        :node-types="nodeTypes"
        :is-valid-connection="isValidConnection"
        :min-zoom="0.35"
        :max-zoom="1.8"
        fit-view-on-init
        @connect="onConnect"
      >
        <Background :gap="18" :size="1" />
        <Controls />
      </VueFlow>
    </div>
    <p class="hint">拖动节点移动位置，从右侧端口拖到左侧端口连线。删除节点/连线用选中后按 Delete。</p>
    <p v-if="issues.length" class="field-warning flow-issues">
      <b>还不能运行：</b>{{ issues.join("；") }}
    </p>
    <p v-else class="hint flow-ok">结构检查通过：恰好一个输入/输出，没有嵌套 FlowAgent。</p>
  </section>
</template>
