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
import { FLOW_NODE_TYPES, emptyFlowGraph, flowDebugSteps, flowIssues, flowSourceType, flowTargetType, formatFlowText, normalizeFlowGraph, type FlowNodeType } from "../utils/flow.ts";
import { FLOW_NODE_SPECS, nodeBadgeFor, nodeDefaults, nodeLabelFor } from "../utils/flowSpecs.ts";
import type { FlowEdge, FlowGraph, FlowNode as FlowNodeData, ModelListItem } from "../api/types.ts";

const props = withDefaults(defineProps<{
  graph?: FlowGraph | null;
  agentId?: string;
  activeNodeId?: string;
  nodeStates?: Record<string, string>;
  running?: boolean;
}>(), {
  graph: null,
  agentId: "",
  activeNodeId: "",
  nodeStates: () => ({}),
  running: false,
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
const nodeTypes = Object.fromEntries(FLOW_NODE_TYPES.map((type) => [type, FlowNode])) as any;
const initial = normalizeFlowGraph(props.graph ?? emptyFlowGraph());
const nodes = ref<any[]>(initial.nodes.map((n) => ({ ...n, data: { ...(n.data ?? {}) } })));
const historyExists = computed(() => nodes.value.some((n) => n.type === "history"));
const edgeClass = (edge: { source: string; sourceHandle?: string }, nodeList: any[] = nodes.value): string => {
  const type = flowSourceType(nodeList.find((n) => n.id === edge.source), edge.sourceHandle);
  if (type === "messages") return "flow-edge-history";
  if (type === "bool") return "flow-edge-bool";
  if (type === "number") return "flow-edge-number";
  if (type === "choice") return "flow-edge-choice";
  return "flow-edge-text";
};
const edges = ref<any[]>(initial.edges.map((e) => ({ ...e, class: edgeClass(e, initial.nodes) })));


const issues = computed(() => flowIssues(toGraph()));
// 右侧节点侧栏 / 底部测试调试栏都是**浮在画布上的**，不参与画布宽度分配。
const nodesOpen = ref(false);
const sidebarTab = ref<"nodes" | "text">("nodes");
const bottomOpen = ref(false);
const bottomTab = ref<"test" | "debug">("debug");
const selectedNodeId = ref("");
const debugSteps = computed(() => flowDebugSteps(toGraph()));
const flowText = computed(() => formatFlowText(toGraph()));
const testMessage = ref("");
const testStatus = ref("");
const testing = ref(false);
const locked = computed(() => props.running);
const models = ref<ModelListItem[]>([]);
provide("flowModels", models);
const nodeRows = computed(() => nodes.value.map((n) => ({
  id: String(n.id),
  type: String(n.type),
  label: nodeLabelFor(String(n.type)),
  badge: nodeBadgeFor(String(n.type)),
  incoming: edges.value.filter((e) => e.target === n.id),
  outgoing: edges.value.filter((e) => e.source === n.id),
})));
function nodeLabel(id: string): string {
  const node = nodes.value.find((n) => n.id === id);
  return node ? nodeLabelFor(node as any) : id;
}
function toggleSidebar(tab: "nodes" | "text"): void {
  if (nodesOpen.value && sidebarTab.value === tab) { nodesOpen.value = false; return; }
  sidebarTab.value = tab;
  nodesOpen.value = true;
}
function toggleNodes(): void { toggleSidebar("nodes"); }
function toggleBottom(tab: "test" | "debug"): void {
  if (bottomOpen.value && bottomTab.value === tab) { bottomOpen.value = false; return; }
  bottomTab.value = tab;
  bottomOpen.value = true;
}
function selectNode(id: string): void {
  selectedNodeId.value = id;
  nodes.value = nodes.value.map((n) => ({ ...n, selected: n.id === id }));
}
function removeEdge(id: string): void {
  if (locked.value) return;
  edges.value = edges.value.filter((e) => e.id !== id);
}
// 删除节点时**同一条事务里**把相连的边一起删掉，避免留下“指向不存在节点”的悬空边。
function removeNode(id: string): void {
  if (locked.value) return;
  edges.value = edges.value.filter((e) => e.source !== id && e.target !== id);
  nodes.value = nodes.value.filter((n) => n.id !== id);
}
// 删端口必须和删节点一样把相连的边一起删掉，否则会留下指向不存在端口的悬空线。
function removePort(nodeId: string, handle: string, side: "source" | "target"): void {
  if (locked.value) return;
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
      delete data.runState; // 运行状态只活在界面上
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
  type: FlowNodeType;
  label: string;
  disabled: boolean;
}>>(() => Object.values(FLOW_NODE_SPECS).map((spec) => ({
  type: spec.type,
  label: spec.label,
  disabled: (spec.type === "input" && inputExists.value)
    || (spec.type === "history" && historyExists.value)
    || (spec.type === "output" && outputExists.value),
})));

function addNode(type: FlowNodeType): void {
  if (locked.value) return;
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
    data: nodeDefaults(type),
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
  if (locked.value) return;
  connectSucceeded.value = false;
  const handle = params.handleId || "";
  const edge = edges.value.find((e) => params.handleType === "source"
    ? e.source === params.nodeId && (e.sourceHandle || "out") === handle
    : e.target === params.nodeId && (e.targetHandle || "in") === handle);
  pendingReconnect.value = edge?.id ?? null;
}
function onConnect(connection: Connection): void {
  if (locked.value || !isValidConnection(connection)) return;
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
  if (locked.value) return;
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
      if (test) {
        data.test = test;
        // 测试是同步跑完的：结束后把每个节点标成完成/跳过，别让编辑器看起来什么都没发生。
        data.runState = test.status === "skipped" ? "done" : "done";
      } else {
        delete data.test;
        delete data.runState;
      }
      return { ...n, data };
    });
    testStatus.value = result.output ? `完成：${result.output.slice(0, 80)}` : "完成";
    bottomTab.value = "test";
    bottomOpen.value = true;
  } catch (e) {
    testStatus.value = `测试失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    testing.value = false;
  }
}

// 位置、连线、节点内 Prompt 条目都是深层数据；一有变化就把整张图交回 AgentEditor。
watch([nodes, edges], () => emit("update:graph", toGraph()), { deep: true });
// 运行状态是独立的展示层数据：不进 Graph、不触发自动保存。
watch([() => props.nodeStates, () => props.activeNodeId], ([states, activeId]) => {
  const map = states ?? {};
  nodes.value = nodes.value.map((n) => {
    const data = { ...(n.data ?? {}) };
    // node_states 是权威；老 trace 没有这一格时，至少把 activeNodeId 标成进行中。
    const state = map[String(n.id)] || (String(n.id) === activeId ? "running" : "");
    if (state) data.runState = state; else delete data.runState;
    return { ...n, data };
  });
}, { deep: true, immediate: true });
</script>

<template>
  <section class="flow-editor" :class="{ expanded, 'is-locked': locked }">
    <div class="flow-main">
      <div class="flow-canvas" :class="{ 'nodes-open': nodesOpen, 'bottom-open': bottomOpen }">
        <VueFlow
          v-model:nodes="nodes"
          v-model:edges="edges"
          :node-types="nodeTypes"
          :is-valid-connection="isValidConnection"
          :min-zoom="0.35"
          :max-zoom="1.8"
          fit-view-on-init
          :connection-mode="ConnectionMode.Loose"
          :nodes-draggable="!locked"
          :nodes-connectable="!locked"
          :edges-updatable="!locked"
          @connect-start="onConnectStart"
          @connect="onConnect"
          @connect-end="onConnectEnd"
          @node-resize="fitAfterResize"
        >
          <Background :gap="18" :size="1" />
          <Controls position="bottom-right" />
        </VueFlow>

        <div v-if="locked" class="flow-lock-badge" role="status">运行中 · 只读</div>
        <!-- 左上角：添加节点（浮在画布上，不挤画布）。 -->
        <div class="flow-canvas-add">
          <VMenu :close-on-content-click="true">
            <template #activator="{ props: menuProps }">
              <VBtn v-bind="menuProps" variant="tonal" size="small" type="button" :disabled="locked"
                prepend-icon="mdi-plus" title="添加节点">添加节点</VBtn>
            </template>
            <VList class="flow-add-menu" density="compact">
              <VListItem v-for="item in addNodeItems" :key="item.type"
                :disabled="item.disabled || locked" @click="addNode(item.type)">
                {{ item.label }}
              </VListItem>
            </VList>
          </VMenu>
        </div>

        <!-- 右上角：侧栏 / 底栏 / 全屏开关。 -->
        <div class="flow-canvas-actions">
          <VBtn class="flow-canvas-action" :class="{ active: bottomOpen && bottomTab === 'test' }"
            variant="text" size="small" type="button" title="测试" @click="toggleBottom('test')">测试</VBtn>
          <VBtn class="flow-canvas-action" :class="{ active: bottomOpen && bottomTab === 'debug' }"
            variant="text" size="small" type="button" title="调试" @click="toggleBottom('debug')">调试</VBtn>
          <VBtn class="flow-canvas-action" :class="{ active: nodesOpen && sidebarTab === 'nodes' }"
            variant="text" size="small" type="button" title="节点与连接" @click="toggleNodes">节点</VBtn>
          <VBtn class="flow-canvas-action" :class="{ active: nodesOpen && sidebarTab === 'text' }"
            variant="text" size="small" type="button" title="文本视图" @click="toggleSidebar('text')">文本</VBtn>
          <VBtn
            icon variant="text" size="small" type="button" class="flow-fullscreen-btn flow-canvas-action"
            :title="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-label="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-pressed="expanded ? 'true' : 'false'"
            @click="toggleExpanded" v-html="expanded ? ICONS.minimize : ICONS.maximize"
          />
        </div>

        <!-- 右侧：节点与连接（PS 图层面板式，浮在画布上）。 -->
        <aside v-if="nodesOpen" class="flow-node-sidebar" aria-label="节点与连接" @wheel="onPanelWheel">
          <div class="flow-panel-head">
            <div class="flow-panel-tabs" role="tablist">
              <button type="button" :class="{ active: sidebarTab === 'nodes' }" @click="sidebarTab = 'nodes'">节点与连接</button>
              <button type="button" :class="{ active: sidebarTab === 'text' }" @click="sidebarTab = 'text'">文本</button>
            </div>
            <VBtn icon variant="text" size="small" type="button" title="关闭节点面板"
              aria-label="关闭节点面板" @click="nodesOpen = false">×</VBtn>
          </div>
          <template v-if="sidebarTab === 'text'">
            <p class="hint">用文本核对节点、端口和连接；内容随当前图实时更新。</p>
            <pre class="flow-text-outline">{{ flowText }}</pre>
          </template>
          <template v-else>
          <p class="hint">像图层一样查看节点和连接；断开或删除都只改当前图。</p>
          <div v-for="row in nodeRows" :key="row.id" class="flow-node-row"
            :class="{ selected: row.id === selectedNodeId, active: row.id === activeNodeId }" @click="selectNode(row.id)">
            <div class="flow-node-row-head">
              <span class="flow-node-row-id"><span class="flow-node-badge" aria-hidden="true">{{ row.badge }}</span><b>{{ row.label }}</b><code>{{ row.id }}</code></span>
              <VBtn icon variant="text" size="x-small" type="button" title="删除节点" aria-label="删除节点"
                :disabled="locked" @click.stop="removeNode(row.id)"><span v-html="ICONS.trash"></span></VBtn>
            </div>
            <div v-if="row.incoming.length" class="flow-conn-section">
              <span class="flow-conn-label">输入</span>
              <div v-for="edge in row.incoming" :key="edge.id" class="flow-conn-row">
                <span :title="edge.id">{{ nodeLabel(edge.source) }} · {{ edge.sourceHandle || "out" }} → {{ edge.targetHandle || "in" }}</span>
                <button type="button" class="flow-conn-x" :disabled="locked" @click.stop="removeEdge(edge.id)">断开</button>
              </div>
            </div>
            <div v-if="row.outgoing.length" class="flow-conn-section">
              <span class="flow-conn-label">输出</span>
              <div v-for="edge in row.outgoing" :key="edge.id" class="flow-conn-row">
                <span :title="edge.id">{{ edge.sourceHandle || "out" }} → {{ nodeLabel(edge.target) }} · {{ edge.targetHandle || "in" }}</span>
                <button type="button" class="flow-conn-x" :disabled="locked" @click.stop="removeEdge(edge.id)">断开</button>
              </div>
            </div>
            <div v-if="!row.incoming.length && !row.outgoing.length" class="flow-conn-empty">（还没有连接）</div>
          </div>
          <p v-if="!nodeRows.length" class="flow-conn-empty">（图里还没有节点）</p>
          </template>
        </aside>

        <!-- 底部：测试 / 调试，浮在画布上。 -->
        <aside v-if="bottomOpen" class="flow-bottom" aria-label="测试与调试" @wheel="onPanelWheel">
          <div class="flow-panel-head">
            <div class="flow-panel-tabs" role="tablist">
              <button type="button" :class="{ active: bottomTab === 'test' }" @click="bottomTab = 'test'">测试</button>
              <button type="button" :class="{ active: bottomTab === 'debug' }" @click="bottomTab = 'debug'">调试</button>
            </div>
            <VBtn icon variant="text" size="small" type="button" title="关闭底栏"
              aria-label="关闭底栏" @click="bottomOpen = false">×</VBtn>
          </div>

          <div v-if="bottomTab === 'test'" class="flow-panel-test">
            <p class="hint">输入一条测试消息；真实调用 LLM 后，悬停节点可看每步输入输出。</p>
            <VTextField v-model="testMessage" density="compact" variant="outlined" hide-details
              placeholder="输入一条测试消息…" @keydown.enter.prevent="runTest" />
            <div class="row">
              <VBtn color="primary" variant="flat" size="small" type="button"
                :loading="testing" :disabled="testing || !agentId" @click="runTest">发送测试</VBtn>
              <span class="status">{{ testStatus || (agentId ? "" : "先保存 FlowAgent，再回来测试") }}</span>
            </div>
          </div>

          <template v-else>
            <p class="hint">只做结构预演，不调用模型；用占位文本看连线和数据流。</p>
            <div v-if="issues.length" class="flow-debug-issues">
              <b>还不能运行：</b>
              <ul><li v-for="issue in issues" :key="issue">{{ issue }}</li></ul>
            </div>
            <div class="flow-debug-steps">
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
            </div>
          </template>
        </aside>
      </div>
    </div>
    <p class="hint">拖动节点移动位置，从右侧端口拖到左侧端口连线。删除节点/连线用选中后按 Delete。</p>
    <p v-if="issues.length && !bottomOpen" class="field-warning flow-issues">
      <b>还不能运行：</b>{{ issues.join("；") }}
    </p>
    <p v-else-if="!issues.length && !bottomOpen" class="hint flow-ok">结构检查通过：恰好一个输入/输出，没有嵌套 FlowAgent。</p>
  </section>
</template>
