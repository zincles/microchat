<script setup lang="ts">
// FlowAgent 的第一版节点编辑器（Vue Flow）。
//
// 职责边界：它只产出/读入 `FlowGraph` JSON；不执行、不调模型。
// 节点先保持最小集合：input / llm / join / output。
// 两条属于 FlowAgent 的硬约束在数据层（utils/flow.ts）与这里一起做：
//   · 只能调用传统 Agent，不能嵌套 FlowAgent；
//   · 第一版只允许 DAG，且恰好一个输入、一个输出。
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { VueFlow, useVueFlow, type Connection } from "@vue-flow/core";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/controls/dist/style.css";
import { VBtn, VTextField } from "vuetify/components";
import { ICONS } from "./icons.ts";
import { sharedApi } from "../utils/prefs.ts";
import FlowNode from "./FlowNode.vue";
import { emptyFlowGraph, flowDebugSteps, flowIssues, flowSourceType, flowTargetType, normalizeFlowGraph } from "../utils/flow.ts";
import type { FlowEdge, FlowGraph, FlowNode as FlowNodeData } from "../api/types.ts";

const props = withDefaults(defineProps<{
  graph?: FlowGraph | null;
  agentId?: string;
}>(), {
  graph: null,
  agentId: "",
});
const emit = defineEmits<{ "update:graph": [graph: FlowGraph] }>();
const api = sharedApi();

// Vue Flow 的 NodeComponent 类型要求完整 NodeProps；我们的节点只声明实际用到的字段，
// 这里按 Vue Flow 的自定义节点约定交给运行时校验，别为了类型把无用 props 全抄一遍。
// 网页全屏：只把编辑器铺满 #app（不是浏览器 Fullscreen API）；画布高度随视口走。
const expanded = ref(false);
const { fitView } = useVueFlow();
// 尺寸变化不是下一 tick 就量准的：等两帧再 fit，否则 Vue Flow 会拿旧画布宽度算视口。
function fitAfterResize(): void {
  nextTick(() => requestAnimationFrame(() => requestAnimationFrame(() => fitView({ padding: 0.18 }))));
}
function toggleExpanded(): void {
  expanded.value = !expanded.value;
  // 画布尺寸变了，Vue Flow 不会自动重新 fit；不重算的话全屏后节点会跑到视口外。
  if (expanded.value) fitAfterResize();
}
const nodeTypes = { input: FlowNode, history: FlowNode, llm: FlowNode, join: FlowNode, output: FlowNode } as any;
const initial = normalizeFlowGraph(props.graph ?? emptyFlowGraph());
const nodes = ref<any[]>(initial.nodes.map((n) => ({ ...n, data: { ...(n.data ?? {}) } })));
const historyExists = computed(() => nodes.value.some((n) => n.type === "history"));
const edgeClass = (sourceId: string, nodeList: any[] = nodes.value): string =>
  nodeList.find((n) => n.id === sourceId)?.type === "history" ? "flow-edge-history" : "flow-edge-text";
const edges = ref<any[]>(initial.edges.map((e) => ({ ...e, class: edgeClass(e.source, initial.nodes) })));


const issues = computed(() => flowIssues(toGraph()));
const debugOpen = ref(false);
const debugSteps = computed(() => flowDebugSteps(toGraph()));
const testOpen = ref(false);
const testMessage = ref("");
const testStatus = ref("");
const testing = ref(false);
// 打开/关闭调试会让画布变窄/变宽；和全屏一样要重新 fit，否则节点会跑到画布外。
watch(debugOpen, fitAfterResize);
onMounted(fitAfterResize);
const inputExists = computed(() => nodes.value.some((n) => n.type === "input"));
const outputExists = computed(() => nodes.value.some((n) => n.type === "output"));
let seq = 0;

function toGraph(): FlowGraph {
  return {
    version: 1,
    nodes: nodes.value.map((n) => {
      const data = { ...(n.data ?? {}) };
      delete data.test; // 测试结果只活在编辑器里，绝不写回 Graph
      return {
        id: String(n.id),
        type: String(n.type ?? "llm"),
        position: { x: Number(n.position?.x ?? 0), y: Number(n.position?.y ?? 0) },
        data,
      };
    }),
    edges: edges.value.map((e) => ({
      id: String(e.id),
      source: String(e.source),
      target: String(e.target),
      ...(e.sourceHandle ? { sourceHandle: String(e.sourceHandle) } : {}),
      ...(e.targetHandle ? { targetHandle: String(e.targetHandle) } : {}),
    })),
  };
}

function addNode(type: "input" | "history" | "llm" | "join" | "output"): void {
  if (type === "input" && inputExists.value) return;
  if (type === "history" && historyExists.value) return;
  if (type === "output" && outputExists.value) return;
  seq += 1;
  const id = `${type}-${Date.now().toString(36)}-${seq}`;
  // 两列、按行递增：新节点不叠在已有节点上（LLM 节点比输入/输出宽，
  // 只按 x 递增很容易把输出盖住）。
  const index = nodes.value.length;
  nodes.value = [...nodes.value, {
    id,
    type,
    position: { x: 40 + (index % 2) * 240, y: 80 + Math.floor(index / 2) * 160 },
    data: type === "llm"
      ? { prompts: [{ id: `p-${Date.now().toString(36)}`, role: "system", content: "" }] }
      : type === "join" ? { inputs: ["in-1", "in-2"] } : {},
  }];
  fitAfterResize();
}

function isHandleUsed(direction: "source" | "target", nodeID: string, handle: string): boolean {
  return edges.value.some((e) => direction === "source"
    ? e.source === nodeID && (e.sourceHandle || "out") === handle
    : e.target === nodeID && (e.targetHandle || "in") === handle);
}
function isValidConnection(connection: Connection): boolean {
  if (!connection.source || !connection.target || connection.source === connection.target) return false;
  const source = nodes.value.find((n) => n.id === connection.source);
  const target = nodes.value.find((n) => n.id === connection.target);
  const sourceHandle = connection.sourceHandle || "out";
  const targetHandle = connection.targetHandle || "in";
  const st = flowSourceType(source, sourceHandle);
  const tt = flowTargetType(target, targetHandle);
  if (!st || !tt || st !== tt) return false;
  // 一个端点只允许一条线；需要多路输入时用 join 节点的多个输入孔。
  if (isHandleUsed("source", connection.source, sourceHandle)) return false;
  if (isHandleUsed("target", connection.target, targetHandle)) return false;
  return true;
}
function onConnect(connection: Connection): void {
  if (!isValidConnection(connection)) return;
  const id = `edge-${Date.now().toString(36)}-${++seq}`;
  edges.value = [...edges.value, {
    id,
    source: connection.source,
    target: connection.target,
    class: edgeClass(connection.source),
    ...(connection.sourceHandle ? { sourceHandle: connection.sourceHandle } : {}),
    ...(connection.targetHandle ? { targetHandle: connection.targetHandle } : {}),
  }];
}

async function runTest(): Promise<void> {
  if (!props.agentId) { testStatus.value = "先保存 FlowAgent，再回来测试"; return; }
  if (!testMessage.value.trim()) { testStatus.value = "先输入一条测试消息"; return; }
  testing.value = true;
  testStatus.value = "测试中…";
  try {
    const result = await api.testFlow(props.agentId, testMessage.value.trim());
    const byID = new Map(result.steps.map((step) => [step.id, step]));
    nodes.value = nodes.value.map((n) => {
      const test = byID.get(String(n.id));
      const data = { ...(n.data ?? {}) };
      if (test) data.test = test; else delete data.test;
      return { ...n, data };
    });
    testStatus.value = result.output ? `完成：${result.output.slice(0, 80)}` : "完成";
    debugOpen.value = true;
  } catch (e) {
    testStatus.value = `测试失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    testing.value = false;
  }
}

// 位置、连线、节点内 Prompt 条目都是深层数据；一有变化就把整张图交回 AgentEditor。
watch([nodes, edges], () => emit("update:graph", toGraph()), { deep: true });
</script>

<template>
  <section class="flow-editor" :class="{ expanded }">
    <div class="flow-toolbar">
      <span class="flow-toolbar-label">添加节点：</span>
      <VBtn variant="tonal" size="small" type="button" :disabled="inputExists" @click="addNode('input')">输入</VBtn>
      <VBtn variant="tonal" size="small" type="button" color="warning" :disabled="historyExists" @click="addNode('history')">历史数据</VBtn>
      <VBtn variant="tonal" size="small" type="button" @click="addNode('llm')">调用 LLM</VBtn>
      <VBtn variant="tonal" size="small" type="button" @click="addNode('join')">拼接</VBtn>
      <VBtn variant="tonal" size="small" type="button" :disabled="outputExists" @click="addNode('output')">输出</VBtn>
      <span class="flow-toolbar-spacer"></span>
      <VBtn
        variant="text" size="small" type="button"
        :aria-pressed="testOpen ? 'true' : 'false'"
        :color="testOpen ? 'primary' : undefined"
        @click="testOpen = !testOpen"
      >测试</VBtn>
      <VBtn
        variant="text" size="small" type="button"
        :aria-pressed="debugOpen ? 'true' : 'false'"
        :color="debugOpen ? 'primary' : undefined"
        @click="debugOpen = !debugOpen"
      >调试</VBtn>
      <VBtn
        icon variant="text" size="small" type="button" class="flow-fullscreen-btn"
        :title="expanded ? '退出网页全屏' : '网页全屏'"
        :aria-label="expanded ? '退出网页全屏' : '网页全屏'"
        :aria-pressed="expanded ? 'true' : 'false'"
        @click="toggleExpanded" v-html="expanded ? ICONS.minimize : ICONS.maximize"
      />
    </div>
    <div v-if="testOpen" class="flow-test-bar">
      <VTextField
        v-model="testMessage"
        density="compact" variant="outlined" hide-details
        class="flow-test-input"
        placeholder="输入一条测试消息…"
        @keydown.enter.prevent="runTest"
      />
      <VBtn
        color="primary" variant="flat" size="small" type="button"
        :loading="testing" :disabled="testing || !agentId"
        @click="runTest"
      >发送测试</VBtn>
      <span class="status">{{ testStatus || (agentId ? "" : "先保存 FlowAgent，再回来测试") }}</span>
    </div>
    <div class="flow-main">
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
          @node-resize="fitAfterResize"
        >
          <Background :gap="18" :size="1" />
          <Controls position="bottom-right" />
        </VueFlow>
      </div>
      <aside v-if="debugOpen" class="flow-debug" aria-label="流程调试">
        <div class="flow-debug-head">
          <b>结构调试</b>
          <VBtn icon variant="text" size="small" type="button" title="关闭调试" aria-label="关闭调试" @click="debugOpen = false">×</VBtn>
        </div>
        <p class="hint">只做结构预演，不调用模型；用占位文本看连线和数据流。</p>
        <div v-if="issues.length" class="flow-debug-issues">
          <b>还不能运行：</b>
          <ul><li v-for="issue in issues" :key="issue">{{ issue }}</li></ul>
        </div>
        <div v-for="(step, i) in debugSteps" :key="step.id" class="flow-debug-step">
          <div class="flow-debug-step-head">
            <span class="flow-debug-index">{{ i + 1 }}</span>
            <b>{{ step.label }}</b>
            <code>{{ step.type }}</code>
          </div>
          <div v-if="step.inputs.length" class="flow-debug-inputs">
            <span>输入</span>
            <code v-for="(input, j) in step.inputs" :key="j">{{ input }}</code>
          </div>
          <div v-if="step.reasoning" class="flow-debug-reasoning">思考：{{ step.reasoning }}</div>
          <pre class="flow-debug-output">{{ step.output }}</pre>
        </div>
      </aside>
    </div>
    <p class="hint">拖动节点移动位置，从右侧端口拖到左侧端口连线。删除节点/连线用选中后按 Delete。</p>
    <p v-if="issues.length && !debugOpen" class="field-warning flow-issues">
      <b>还不能运行：</b>{{ issues.join("；") }}
    </p>
    <p v-else-if="!issues.length && !debugOpen" class="hint flow-ok">结构检查通过：恰好一个输入/输出，没有嵌套 FlowAgent。</p>
  </section>
</template>
