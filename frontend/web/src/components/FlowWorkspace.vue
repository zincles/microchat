<script setup lang="ts">
// FlowAgent 的「工作流」页：直接编辑当前 Agent 的 FlowGraph，并自动写回 agents.json。
// 它是 AgentEditor 的流程页在会话工作区里的复用（同一个 FlowEditor，不另造一套）。
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import FlowEditor from "./FlowEditor.vue";
import { sharedApi } from "../utils/prefs.ts";
import { normalizeFlowGraph } from "../utils/flow.ts";
import type { Agent, FlowGraph } from "../api/types.ts";

const props = withDefaults(defineProps<{
  agent?: Agent | null;
  sessionId?: string;
}>(), { agent: null, sessionId: "" });
const emit = defineEmits<{ saved: [] }>();
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
  const snapshot = JSON.stringify(normalizeFlowGraph(next));
  if (snapshot === savedSnapshot.value) return;
  graph.value = next;
  dirty = true;
  status.value = "未保存";
  schedule();
}
async function pollActive(): Promise<void> {
  if (!props.sessionId) { activeNodeId.value = ""; nodeStates.value = {}; running.value = false; return; }
  try {
    const trace = await api.flowRun(props.sessionId);
    activeNodeId.value = trace?.active_node_id ?? "";
    nodeStates.value = trace?.node_states ?? {};
    running.value = trace?.status === "running";
  } catch {
    activeNodeId.value = "";
    nodeStates.value = {};
    running.value = false;
  }
}

async function save(): Promise<void> {
  cancelTimer();
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
  if (dirty) void save();
});
</script>

<template>
  <section id="flow-workspace" aria-label="FlowAgent 工作流">
    <div class="flow-workspace-head">
      <p class="hint">当前 Agent 的工作流；修改会防抖保存回 Agent 文件。</p>
      <span class="flow-workspace-status">{{ savedLabel }}</span>
    </div>
    <FlowEditor v-if="props.agent?.id" :graph="graph" :agent-id="props.agent.id"
      :active-node-id="activeNodeId" :node-states="nodeStates" :running="running" @update:graph="onGraph" />
    <p v-else class="ws-draft-note">这是草稿，还没有保存的 FlowAgent 可以编辑。</p>
  </section>
</template>
