<script setup lang="ts">
// FlowAgent 编辑器的节点外壳：输入 / 调用智能体 / 拼接 / 输出。
// 节点内部用 Vuetify 控件；画布、连线、拖拽由 Vue Flow 负责。
import { computed, inject, ref, type Ref } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { VSelect } from "vuetify/components";

interface FlowAgentOption {
  id: string;
  name?: string;
  kind?: string;
}

const props = defineProps<{
  id: string;
  type: string;
  data: Record<string, any>;
}>();
const agents = inject<Ref<FlowAgentOption[]>>("flowAgents", ref([]));
const agentOptions = computed(() => agents.value
  .filter((a) => a.kind !== "flow")
  .map((a) => ({ title: a.name || a.id, value: a.id })));
const label = computed(() => ({
  input: "输入",
  agent: "调用智能体",
  join: "拼接",
  output: "输出",
}[props.type] ?? props.type));
const hint = computed(() => ({
  input: "本轮用户消息 + 会话上下文",
  agent: "只允许传统智能体",
  join: "把多路输出合成一段文本",
  output: "最终结果写入聊天",
}[props.type] ?? ""));
</script>

<template>
  <div class="flow-node" :class="[`flow-node-${type}`]">
    <Handle v-if="type !== 'input'" id="in" type="target" :position="Position.Left" />
    <div class="flow-node-head">
      <span class="flow-node-title">{{ label }}</span>
      <span class="flow-node-hint">{{ hint }}</span>
    </div>
    <VSelect
      v-if="type === 'agent'"
      v-model="data.agent_id"
      class="flow-node-select nodrag nopan"
      :items="agentOptions"
      item-title="title"
      item-value="value"
      density="compact"
      variant="outlined"
      hide-details
      placeholder="选择一个传统智能体…"
    />
    <div v-if="type === 'join'" class="flow-node-body">多路文本 → 一段文本</div>
    <Handle v-if="type !== 'output'" id="out" type="source" :position="Position.Right" />
  </div>
</template>
