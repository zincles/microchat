<script setup lang="ts">
// FlowAgent 的第一版节点编辑器（Vue Flow）。
//
// 职责边界：它只产出/读入 `FlowGraph` JSON；不执行、不调模型。
// 节点先保持最小集合：input / history / trim / llm / join / output。
// 两条属于 FlowAgent 的硬约束在数据层（utils/flow.ts）与这里一起做：
//   · 只能调用传统 Agent，不能嵌套 FlowAgent；
//   · 第一版只允许 DAG，且恰好一个输入、一个输出。
import { computed, nextTick, onMounted, provide, ref, watch } from "vue";
import { ConnectionMode, VueFlow, useVueFlow, type Connection, type OnConnectStartParams } from "@vue-flow/core";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/controls/dist/style.css";
import { VBtn, VList, VListItem, VMenu, VTextField } from "vuetify/components";
import { ICONS } from "./icons.ts";
import { sharedApi } from "../utils/prefs.ts";
import FlowNode from "./FlowNode.vue";
import { emptyFlowGraph, flowDebugSteps, flowIssues, flowSourceType, flowTargetType, normalizeFlowGraph } from "../utils/flow.ts";
import type { FlowEdge, FlowGraph, FlowNode as FlowNodeData, ModelListItem } from "../api/types.ts";

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
const nodeTypes = { input: FlowNode, history: FlowNode, trim: FlowNode, merge: FlowNode, llm: FlowNode, join: FlowNode, output: FlowNode } as any;
const initial = normalizeFlowGraph(props.graph ?? emptyFlowGraph());
const nodes = ref<any[]>(initial.nodes.map((n) => ({ ...n, data: { ...(n.data ?? {}) } })));
const historyExists = computed(() => nodes.value.some((n) => n.type === "history"));
const edgeClass = (edge: { source: string; sourceHandle?: string }, nodeList: any[] = nodes.value): string =>
  flowSourceType(nodeList.find((n) => n.id === edge.source), edge.sourceHandle) === "messages"
    ? "flow-edge-history" : "flow-edge-text";
const edges = ref<any[]>(initial.edges.map((e) => ({ ...e, class: edgeClass(e, initial.nodes) })));


const issues = computed(() => flowIssues(toGraph()));
const panelOpen = ref(false);
const panelTab = ref<"test" | "debug" | "nodes">("debug");
const debugSteps = computed(() => flowDebugSteps(toGraph()));
const testMessage = ref("");
const testStatus = ref("");
const testing = ref(false);
const models = ref<ModelListItem[]>([]);
provide("flowModels", models);
const nodeRows = computed(() => nodes.value.map((n) => ({
  id: String(n.id),
  type: String(n.type),
  label: ({ input: "输入", history: "历史", trim: "裁剪", merge: "拼接 messages", llm: "LLM", join: "拼接和复制", output: "输出" } as Record<string, string>)[String(n.type)] ?? String(n.type),
  badge: ({ input: "IN", history: "H", trim: "TR", merge: "MG", llm: "AI", join: "J", output: "OUT" } as Record<string, string>)[String(n.type)] ?? "·",
  incoming: edges.value.filter((e) => e.target === n.id),
  outgoing: edges.value.filter((e) => e.source === n.id),
})));
function nodeLabel(id: string): string {
  const node = nodes.value.find((n) => n.id === id);
  return node ? (({ input: "输入", history: "历史", trim: "裁剪", merge: "拼接 messages", llm: "LLM", join: "拼接和复制", output: "输出" } as Record<string, string>)[String(node.type)] ?? String(node.type)) : id;
}
function openPanel(tab: "test" | "debug" | "nodes"): void {
  if (panelOpen.value && panelTab.value === tab) { panelOpen.value = false; return; }
  panelTab.value = tab;
  panelOpen.value = true;
}
function selectNode(id: string): void {
  nodes.value = nodes.value.map((n) => ({ ...n, selected: n.id === id }));
}
function removeEdge(id: string): void {
  edges.value = edges.value.filter((e) => e.id !== id);
}
// 删除节点时**同一条事务里**把相连的边一起删掉，避免留下“指向不存在节点”的悬空边。
function removeNode(id: string): void {
  edges.value = edges.value.filter((e) => e.source !== id && e.target !== id);
  nodes.value = nodes.value.filter((n) => n.id !== id);
}
// 删端口必须和删节点一样把相连的边一起删掉，否则会留下指向不存在端口的悬空线。
function removePort(nodeId: string, handle: string, side: "source" | "target"): void {
  edges.value = edges.value.filter((e) => {
    if (side === "target") {
      return !(e.target === nodeId && (e.targetHandle || (handle === "in-1" ? "in-1" : "in")) === handle);
    }
    return !(e.source === nodeId && (e.sourceHandle || (handle === "out-1" ? "out-1" : "out")) === handle);
  });
}
// 面板浮在画布上：Vue Flow 的平移滚轮/手势可能抢事件。面板自己滚时明确截住。
function onPanelWheel(event: WheelEvent): void {
  const el = event.currentTarget as HTMLElement | null;
  if (!el || el.scrollHeight <= el.clientHeight) return;
  el.scrollTop += event.deltaY;
  event.preventDefault();
  event.stopPropagation();
}
provide("flowRemoveNode", removeNode);
provide("flowRemovePort", removePort);
onMounted(async () => {
  fitAfterResize();
  try { models.value = await api.models(); } catch { /* 模型清单拿不到时只留“跟随默认模型” */ }
});
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

const addNodeItems = computed<Array<{
  type: "input" | "history" | "trim" | "merge" | "llm" | "join" | "output";
  label: string;
  disabled: boolean;
}>>(() => [
  { type: "input", label: "输入", disabled: inputExists.value },
  { type: "history", label: "历史数据", disabled: historyExists.value },
  { type: "trim", label: "裁剪 messages", disabled: false },
  { type: "merge", label: "拼接 messages", disabled: false },
  { type: "llm", label: "调用 LLM", disabled: false },
  { type: "join", label: "拼接和复制", disabled: false },
  { type: "output", label: "输出", disabled: outputExists.value },
]);

function addNode(type: "input" | "history" | "trim" | "merge" | "llm" | "join" | "output"): void {
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
      : type === "join" ? { inputs: ["in-1", "in-2"], outputs: ["out-1"], separator: "\n\n---\n\n" }
      : type === "trim" ? { mode: "tail", count: 10 }
      : type === "merge" ? { inputs: ["in-1", "in-2"] } : {},
  }];
  fitAfterResize();
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
  // 端点占用不在这里拒绝：拖到已占用的端点应当**替换旧线**。
  return true;
}
const pendingReconnect = ref<string | null>(null);
const connectSucceeded = ref(false);

// 从一个已连接的口子开始拖：先记住旧边；放开到空白就删掉它，落到别处则在 onConnect 里替换。
function onConnectStart(params: OnConnectStartParams): void {
  connectSucceeded.value = false;
  const handle = params.handleId || "";
  const edge = edges.value.find((e) => params.handleType === "source"
    ? e.source === params.nodeId && (e.sourceHandle || "out") === handle
    : e.target === params.nodeId && (e.targetHandle || "in") === handle);
  pendingReconnect.value = edge?.id ?? null;
}
function onConnect(connection: Connection): void {
  if (!isValidConnection(connection)) return;
  connectSucceeded.value = true;
  const sourceHandle = connection.sourceHandle || "out";
  const targetHandle = connection.targetHandle || "in";
  // 替换语义：同一个出口或入口上的旧线先删掉，再落这条新线。
  edges.value = edges.value.filter((e) =>
    !(e.source === connection.source && (e.sourceHandle || "out") === sourceHandle)
    && !(e.target === connection.target && (e.targetHandle || "in") === targetHandle));
  const id = `edge-${Date.now().toString(36)}-${++seq}`;
  edges.value = [...edges.value, {
    id,
    source: connection.source,
    target: connection.target,
    class: edgeClass({ source: connection.source, sourceHandle: connection.sourceHandle ?? undefined }),
    ...(connection.sourceHandle ? { sourceHandle: connection.sourceHandle } : {}),
    ...(connection.targetHandle ? { targetHandle: connection.targetHandle } : {}),
  }];
  pendingReconnect.value = null;
}
function onConnectEnd(): void {
  // 拖回空白：没有产生新连接 ⇒ 把开始拖的那条旧线删掉。
  if (pendingReconnect.value && !connectSucceeded.value) {
    removeEdge(pendingReconnect.value);
  }
  pendingReconnect.value = null;
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
    panelTab.value = "test";
    panelOpen.value = true;
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
      <VMenu location="bottom start" :close-on-content-click="true">
        <template #activator="{ props: menuProps }">
          <VBtn v-bind="menuProps" variant="tonal" size="small" type="button">＋ 添加节点</VBtn>
        </template>
        <VList class="flow-add-menu" density="compact">
          <VListItem v-for="item in addNodeItems" :key="item.type"
            :disabled="item.disabled" @click="addNode(item.type)">
            {{ item.label }}
          </VListItem>
        </VList>
      </VMenu>
      <span class="flow-toolbar-spacer"></span>
      <VBtn variant="text" size="small" type="button"
        :aria-pressed="panelOpen && panelTab === 'test' ? 'true' : 'false'"
        :color="panelOpen && panelTab === 'test' ? 'primary' : undefined"
        @click="openPanel('test')">测试</VBtn>
      <VBtn variant="text" size="small" type="button"
        :aria-pressed="panelOpen && panelTab === 'debug' ? 'true' : 'false'"
        :color="panelOpen && panelTab === 'debug' ? 'primary' : undefined"
        @click="openPanel('debug')">调试</VBtn>
      <VBtn variant="text" size="small" type="button"
        :aria-pressed="panelOpen && panelTab === 'nodes' ? 'true' : 'false'"
        :color="panelOpen && panelTab === 'nodes' ? 'primary' : undefined"
        @click="openPanel('nodes')">节点</VBtn>
      <VBtn
        icon variant="text" size="small" type="button" class="flow-fullscreen-btn"
        :title="expanded ? '退出网页全屏' : '网页全屏'"
        :aria-label="expanded ? '退出网页全屏' : '网页全屏'"
        :aria-pressed="expanded ? 'true' : 'false'"
        @click="toggleExpanded" v-html="expanded ? ICONS.minimize : ICONS.maximize"
      />
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
          :connection-mode="ConnectionMode.Loose"
          @connect-start="onConnectStart"
          @connect="onConnect"
          @connect-end="onConnectEnd"
          @node-resize="fitAfterResize"
        >
          <Background :gap="18" :size="1" />
          <Controls position="bottom-right" />
        </VueFlow>
      </div>
      <aside v-if="panelOpen" class="flow-debug" aria-label="流程面板" @wheel="onPanelWheel">
        <div class="flow-panel-head">
          <div class="flow-panel-tabs" role="tablist">
            <button type="button" :class="{ active: panelTab === 'test' }" @click="panelTab = 'test'">测试</button>
            <button type="button" :class="{ active: panelTab === 'debug' }" @click="panelTab = 'debug'">调试</button>
            <button type="button" :class="{ active: panelTab === 'nodes' }" @click="panelTab = 'nodes'">节点</button>
          </div>
          <VBtn icon variant="text" size="small" type="button" title="关闭面板" aria-label="关闭面板" @click="panelOpen = false">×</VBtn>
        </div>

        <div v-if="panelTab === 'test'" class="flow-panel-test">
          <p class="hint">输入一条测试消息；真实调用 LLM 后，悬停节点可看每步输入输出。</p>
          <VTextField v-model="testMessage" density="compact" variant="outlined" hide-details
            placeholder="输入一条测试消息…" @keydown.enter.prevent="runTest" />
          <div class="row">
            <VBtn color="primary" variant="flat" size="small" type="button"
              :loading="testing" :disabled="testing || !agentId" @click="runTest">发送测试</VBtn>
            <span class="status">{{ testStatus || (agentId ? "" : "先保存 FlowAgent，再回来测试") }}</span>
          </div>
        </div>

        <template v-else-if="panelTab === 'debug'">
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
        </template>

        <template v-else>
          <p class="hint">像图层一样查看节点和连接；断开或删除都只改当前图。</p>
          <div v-for="row in nodeRows" :key="row.id" class="flow-node-row">
            <div class="flow-node-row-head">
              <span class="flow-node-row-id"><span class="flow-node-badge" aria-hidden="true">{{ row.badge }}</span><b>{{ row.label }}</b><code>{{ row.id }}</code></span>
              <VBtn icon variant="text" size="x-small" type="button" title="删除节点" aria-label="删除节点"
                @click="removeNode(row.id)"><span v-html="ICONS.trash"></span></VBtn>
            </div>
            <div v-if="row.incoming.length" class="flow-conn-section">
              <span class="flow-conn-label">输入</span>
              <div v-for="edge in row.incoming" :key="edge.id" class="flow-conn-row">
                <span :title="edge.id">{{ nodeLabel(edge.source) }} · {{ edge.sourceHandle || "out" }} → {{ edge.targetHandle || "in" }}</span>
                <button type="button" class="flow-conn-x" @click="removeEdge(edge.id)">断开</button>
              </div>
            </div>
            <div v-if="row.outgoing.length" class="flow-conn-section">
              <span class="flow-conn-label">输出</span>
              <div v-for="edge in row.outgoing" :key="edge.id" class="flow-conn-row">
                <span :title="edge.id">{{ edge.sourceHandle || "out" }} → {{ nodeLabel(edge.target) }} · {{ edge.targetHandle || "in" }}</span>
                <button type="button" class="flow-conn-x" @click="removeEdge(edge.id)">断开</button>
              </div>
            </div>
            <div v-if="!row.incoming.length && !row.outgoing.length" class="flow-conn-empty">（还没有连接）</div>
          </div>
          <p v-if="!nodeRows.length" class="flow-conn-empty">（图里还没有节点）</p>
        </template>
      </aside>
    </div>
    <p class="hint">拖动节点移动位置，从右侧端口拖到左侧端口连线。删除节点/连线用选中后按 Delete。</p>
    <p v-if="issues.length && !panelOpen" class="field-warning flow-issues">
      <b>还不能运行：</b>{{ issues.join("；") }}
    </p>
    <p v-else-if="!issues.length && !panelOpen" class="hint flow-ok">结构检查通过：恰好一个输入/输出，没有嵌套 FlowAgent。</p>
  </section>
</template>
