<script setup lang="ts">
// FlowAgent 的「工作流」页：会话工作区里**只读**展示当前对话的真实执行；
// 编辑入口跳到对应的智能体流程页（AgentEditor 的 FlowEditor，不在这里复制一份编辑器）。
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { VBtn } from "vuetify/components";
import FlowEditor from "./FlowEditor.vue";
import { sharedApi } from "../utils/prefs.ts";
import { normalizeFlowGraph } from "../utils/flow.ts";
import type { Agent, FlowGraph, FlowTestStep } from "../api/types.ts";

const props = withDefaults(defineProps<{
  agent?: Agent | null;
  sessionId?: string;
  readonly?: boolean;
}>(), { agent: null, sessionId: "", readonly: false });
const emit = defineEmits<{ saved: []; "edit-agent": [id: string] }>();
const api = sharedApi();

const graph = ref<FlowGraph>(normalizeFlowGraph(props.agent?.flow ?? null));
// 自动保存只对**真实修改**发请求。测试结果、Vue Flow 的初始化回写、以及
// 载入时就地做的老图迁移都会触发 update:graph，但它们不是用户改图。
const savedSnapshot = ref(JSON.stringify(graph.value));
const status = ref("");
const savedAt = ref<number | null>(null);
const nowMs = ref(Date.now());
const savedLabel = computed(() => {
  if (status.value !== "已保存") return status.value;
  if (!savedAt.value) return "已保存";
  const seconds = Math.max(0, Math.floor((nowMs.value - savedAt.value) / 1000));
  return `已保存（${seconds}秒之前）`;
});
const activeNodeId = ref("");
const nodeStates = ref<Record<string, string>>({});
const running = ref(false);
const traceLabel = ref("");
const traceSteps = ref<FlowTestStep[]>([]);
let pollTimer: number | null = null;
let tickTimer: number | null = null;
let timer: number | null = null;
let saveSeq = 0;
let dirty = false;

function cancelTimer(): void {
  if (timer !== null) { clearTimeout(timer); timer = null; }
}
function schedule(): void {
  cancelTimer();
  timer = window.setTimeout(() => { void save(); }, 700);
}
function onGraph(next: FlowGraph): void {
  if (props.readonly) return;
  const snapshot = JSON.stringify(normalizeFlowGraph(next));
  if (snapshot === savedSnapshot.value) return;
  graph.value = next;
  dirty = true;
  status.value = "未保存";
  schedule();
}
async function pollActive(): Promise<void> {
  if (!props.sessionId) { activeNodeId.value = ""; nodeStates.value = {}; running.value = false; traceLabel.value = ""; traceSteps.value = []; return; }
  try {
    const trace = await api.flowRun(props.sessionId);
    activeNodeId.value = trace?.active_node_id ?? "";
    nodeStates.value = trace?.node_states ?? {};
    traceSteps.value = trace?.steps ?? [];
    running.value = trace?.status === "running";
    traceLabel.value = trace?.status === "running" ? "运行中"
      : trace?.status === "done" ? "上次运行已完成"
      : trace?.status === "error" ? "上次运行失败"
      : trace?.status === "canceled" ? "上次运行已取消" : "";
  } catch {
    activeNodeId.value = "";
    nodeStates.value = {};
    running.value = false;
    traceSteps.value = [];
    traceLabel.value = "还没有执行记录";
  }
}

async function save(): Promise<void> {
  cancelTimer();
  if (props.readonly) return;
  const agent = props.agent;
  if (!agent?.id) { status.value = "先保存 Agent"; return; }
  const snapshot = graph.value;
  const id = ++saveSeq;
  status.value = "保存中…";
  try {
    await api.patchAgent(agent.id, { flow: snapshot });
    if (id !== saveSeq) return;
    dirty = false;
    savedSnapshot.value = JSON.stringify(normalizeFlowGraph(snapshot));
    savedAt.value = Date.now();
    nowMs.value = savedAt.value;
    status.value = "已保存";
    emit("saved");
  } catch (error) {
    if (id !== saveSeq) return;
    status.value = `保存失败：${error instanceof Error ? error.message : String(error)}`;
  }
}
watch([() => props.agent?.id, () => props.sessionId], () => {
  cancelTimer();
  saveSeq += 1;
  dirty = false;
  graph.value = normalizeFlowGraph(props.agent?.flow ?? null);
  savedSnapshot.value = JSON.stringify(graph.value);
  savedAt.value = null;
  nodeStates.value = {};
  running.value = false;
  traceLabel.value = "";
  status.value = "";
});
onMounted(() => {
  pollActive();
  pollTimer = window.setInterval(() => { void pollActive(); }, 700);
  tickTimer = window.setInterval(() => { nowMs.value = Date.now(); }, 1000);
});
onBeforeUnmount(() => {
  cancelTimer();
  if (pollTimer !== null) clearInterval(pollTimer);
  if (tickTimer !== null) clearInterval(tickTimer);
  if (dirty && !props.readonly) void save();
});
</script>

<template>
  <section id="flow-workspace" aria-label="FlowAgent 工作流">
    <div class="flow-workspace-head">
      <p class="hint">{{ readonly ? "当前对话的执行视图（只读）；看得到节点状态、输入输出与真实请求体。" : "当前 Agent 的工作流；修改会防抖保存回 Agent 文件。" }}</p>
      <span class="flow-workspace-status">{{ readonly ? traceLabel : savedLabel }}<template v-if="!readonly && traceLabel">{{ savedLabel ? " · " : "" }}{{ traceLabel }}</template></span>
      <VBtn v-if="readonly && props.agent?.id" variant="text" size="small" type="button"
        title="打开这个助手的流程编辑器" @click="emit('edit-agent', props.agent.id)">编辑助手流程</VBtn>
    </div>
    <FlowEditor v-if="props.agent?.id" :graph="graph" :agent-id="props.agent.id"
      :active-node-id="activeNodeId" :node-states="nodeStates" :running="running"
      :readonly="readonly" :trace-steps="traceSteps" @update:graph="onGraph" />
    <p v-else class="ws-draft-note">这是草稿，还没有保存的 FlowAgent 可以编辑。</p>
  </section>
</template>
