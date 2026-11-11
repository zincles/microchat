<script setup lang="ts">
// FlowAgent 编辑器的节点外壳：输入 / 调用 LLM / 拼接 / 输出。
// LLM 节点内部是一张 **messages 列表**：每个 Prompt 条目有自己的 role 和左侧输入孔。
import { computed, ref } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { VBtn, VSelect, VTextarea } from "vuetify/components";

const props = defineProps<{
  id: string;
  type: string;
  data: Record<string, any>;
}>();

interface PromptEntry {
  id: string;
  role: "system" | "user" | "assistant";
  content: string;
}

const hovered = ref(false);
const label = computed(() => ({
  input: "输入",
  llm: "调用 LLM",
  join: "拼接",
  output: "输出",
}[props.type] ?? props.type));
const hint = computed(() => ({
  input: "本轮用户消息 + 会话上下文",
  llm: "手动组装 messages",
  join: "把多路输出合成一段文本",
  output: "最终结果写入聊天",
}[props.type] ?? ""));
const prompts = computed<PromptEntry[]>(() => Array.isArray(props.data.prompts) ? props.data.prompts : []);
const roles = [
  { title: "system", value: "system" },
  { title: "user", value: "user" },
  { title: "assistant", value: "assistant" },
];
function addPrompt(): void {
  if (!Array.isArray(props.data.prompts)) props.data.prompts = [];
  props.data.prompts.push({ id: `p-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`, role: "system", content: "" });
}
function removePrompt(id: string): void {
  props.data.prompts = (props.data.prompts ?? []).filter((p: PromptEntry) => p.id !== id);
}
</script>

<template>
  <div
    class="flow-node"
    :class="[`flow-node-${type}`]"
    @mouseenter="hovered = true"
    @mouseleave="hovered = false"
  >
    <Handle v-if="type === 'join' || type === 'output'" id="in" type="target" :position="Position.Left" />
    <div class="flow-node-head">
      <span class="flow-node-title">{{ label }}</span>
      <span class="flow-node-hint">{{ hint }}</span>
    </div>
    <div v-if="type === 'llm'" class="flow-prompt-list">
      <div v-for="prompt in prompts" :key="prompt.id" class="flow-prompt-row">
        <Handle
          :id="`prompt-${prompt.id}`"
          type="target"
          :position="Position.Left"
          :style="{ top: '50%' }"
        />
        <div class="flow-prompt-head">
          <VSelect
            v-model="prompt.role"
            class="flow-prompt-role nodrag nopan"
            :items="roles"
            density="compact"
            variant="outlined"
            hide-details
          />
          <VBtn icon variant="text" size="x-small" type="button" class="flow-prompt-del nodrag nopan"
            title="删除这条 Prompt" aria-label="删除这条 Prompt" @click="removePrompt(prompt.id)">×</VBtn>
        </div>
        <VTextarea
          v-model="prompt.content"
          class="flow-prompt-content nodrag nopan"
          rows="2"
          density="compact"
          variant="outlined"
          hide-details
          placeholder="手写这段 Prompt（也可以只接上游）"
        />
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-prompt nodrag nopan" @click="addPrompt">＋ 添加 Prompt</VBtn>
    </div>
    <div v-if="type === 'join'" class="flow-node-body">多路文本 → 一段文本</div>
    <div v-if="hovered && data.test" class="flow-node-test" role="tooltip">
      <b>测试 I/O</b>
      <span class="flow-node-test-label">输入</span>
      <pre>{{ (data.test.inputs ?? []).join("\n---\n") || "（无输入）" }}</pre>
      <span class="flow-node-test-label">思考</span>
      <pre>{{ data.test.reasoning || "（无思考）" }}</pre>
      <span class="flow-node-test-label">正文</span>
      <pre>{{ data.test.output || "（无输出）" }}</pre>
    </div>
    <div v-if="type === 'llm'" class="flow-llm-outputs">
      <div class="flow-llm-output-row">
        <span>思考</span>
        <Handle id="reasoning" type="source" :position="Position.Right" />
      </div>
      <div class="flow-llm-output-row">
        <span>正文</span>
        <Handle id="text" type="source" :position="Position.Right" />
      </div>
    </div>
    <Handle v-else-if="type !== 'output'" id="out" type="source" :position="Position.Right" />
  </div>
</template>
