<script setup lang="ts">
// FlowAgent 的「工作流」页：直接编辑当前 Agent 的 FlowGraph，并自动写回 agents.json。
// 它是 AgentEditor 的流程页在会话工作区里的复用（同一个 FlowEditor，不另造一套）。
import { onBeforeUnmount, ref, watch } from "vue";
import FlowEditor from "./FlowEditor.vue";
import { sharedApi } from "../utils/prefs.ts";
import { normalizeFlowGraph } from "../utils/flow.ts";
import type { Agent, FlowGraph } from "../api/types.ts";

const props = withDefaults(defineProps<{
  agent?: Agent | null;
}>(), { agent: null });
const emit = defineEmits<{ saved: [] }>();
const api = sharedApi();

const graph = ref<FlowGraph>(normalizeFlowGraph(props.agent?.flow ?? null));
const status = ref("");
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
  graph.value = next;
  dirty = true;
  status.value = "未保存";
  schedule();
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
    status.value = "已保存";
    emit("saved");
  } catch (error) {
    if (id !== saveSeq) return;
    status.value = `保存失败：${error instanceof Error ? error.message : String(error)}`;
  }
}
watch(() => props.agent?.id, () => {
  cancelTimer();
  saveSeq += 1;
  dirty = false;
  graph.value = normalizeFlowGraph(props.agent?.flow ?? null);
  status.value = "";
});
onBeforeUnmount(() => {
  cancelTimer();
  if (dirty) void save();
});
</script>

<template>
  <section id="flow-workspace" aria-label="FlowAgent 工作流">
    <div class="flow-workspace-head">
      <p class="hint">当前 Agent 的工作流；修改会防抖保存回 Agent 文件。</p>
      <span class="flow-workspace-status">{{ status }}</span>
    </div>
    <FlowEditor v-if="props.agent?.id" v-model:graph="graph" :agent-id="props.agent.id" @update:graph="onGraph" />
    <p v-else class="ws-draft-note">这是草稿，还没有保存的 FlowAgent 可以编辑。</p>
  </section>
</template>
