<script setup lang="ts">
// FlowAgent 编辑器的节点外壳：输入 / 调用 LLM / 拼接 / 输出。
// LLM 节点内部是一张 **messages 列表**：每个 Prompt 条目有自己的 role 和左侧输入孔。
import { computed, inject, ref } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { VBtn, VSelect, VTextarea } from "vuetify/components";

const props = defineProps<{
  id: string;
  type: string;
  selected?: boolean;
  data: Record<string, any>;
}>();

interface PromptEntry {
  id: string;
  role: "system" | "user" | "assistant";
  content: string;
}

const hovered = ref(false);
// 删除回调由 FlowEditor 提供：它会在同一个动作里删节点 + 所有相连边。
const removeNode = inject<(id: string) => void>("flowRemoveNode", () => {});
const label = computed(() => ({
  input: "输入",
  history: "历史数据",
  llm: "调用 LLM",
  join: "拼接和复制",
  output: "输出",
}[props.type] ?? props.type));
const hint = computed(() => ({
  input: "裸字符串：本轮用户输入",
  history: "messages[]：当前会话历史",
  llm: "手动组装 messages",
  join: "多路拼接；只接一路时直接复制",
  output: "最终结果写入聊天",
}[props.type] ?? ""));
const prompts = computed<PromptEntry[]>(() => Array.isArray(props.data.prompts) ? props.data.prompts : []);
const joinInputs = computed<string[]>(() => Array.isArray(props.data.inputs) && props.data.inputs.length
  ? props.data.inputs
  : ["in-1", "in-2"]);
const joinOutputs = computed<string[]>(() => Array.isArray(props.data.outputs) && props.data.outputs.length
  ? props.data.outputs
  : ["out-1"]);
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
function addJoinInput(): void {
  const inputs = [...joinInputs.value];
  let n = inputs.length + 1;
  while (inputs.includes(`in-${n}`)) n++;
  props.data.inputs = [...inputs, `in-${n}`];
}
function addJoinOutput(): void {
  const outputs = [...joinOutputs.value];
  let n = outputs.length + 1;
  while (outputs.includes(`out-${n}`)) n++;
  props.data.outputs = [...outputs, `out-${n}`];
}
</script>

<template>
  <div
    class="flow-node"
    :class="[`flow-node-${type}`]"
    @mouseenter="hovered = true"
    @mouseleave="hovered = false"
  >
    <Handle v-if="type === 'output'" id="in" type="target" :position="Position.Left" />
    <div class="flow-node-head">
      <span class="flow-node-title">{{ label }}</span>
      <button class="flow-node-delete nodrag nopan" type="button" title="删除节点" aria-label="删除节点"
        @click.stop="removeNode(id)">×</button>
      <span class="flow-node-hint">{{ hint }}</span>
    </div>
    <div v-if="type === 'llm'" class="flow-history-input">
      <Handle id="history" type="target" :position="Position.Left" />
      <span>历史消息</span>
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
    <div v-if="type === 'history'" class="flow-node-body">来自真实会话的历史 messages</div>
    <div v-if="type === 'join'" class="flow-join-inputs">
      <div v-for="(input, i) in joinInputs" :key="input" class="flow-join-row">
        <Handle :id="input" type="target" :position="Position.Left" />
        <span>输入 {{ i + 1 }}</span>
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-join nodrag nopan" @click="addJoinInput">＋ 添加输入</VBtn>
    </div>
    <div v-if="(hovered || selected) && data.test" class="flow-node-test" role="tooltip">
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
    <div v-if="type === 'join'" class="flow-join-outputs">
      <div v-for="(output, i) in joinOutputs" :key="output" class="flow-join-output-row">
        <span>输出 {{ i + 1 }}</span>
        <Handle :id="output" type="source" :position="Position.Right" />
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-join nodrag nopan" @click="addJoinOutput">＋ 添加输出</VBtn>
    </div>
    <Handle v-else-if="type !== 'output'" id="out" type="source" :position="Position.Right" />
  </div>
</template>
