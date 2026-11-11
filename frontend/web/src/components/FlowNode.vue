<script setup lang="ts">
// FlowAgent 编辑器的节点外壳：输入 / 历史 / 裁剪 / 调用 LLM / 拼接 / 输出。
// LLM 节点内部是一张 **messages 列表**：每个 Prompt 条目有自己的 role 和左侧输入孔。
import { computed, inject, ref, type Ref } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { VBtn, VSelect, VTextField, VTextarea } from "vuetify/components";
import type { ModelListItem } from "../api/types.ts";
import { DEFAULT_JOIN_SEPARATOR, TRIM_MODES, TRIM_ROLES, trimCountOf, trimModeOf, type TrimMode } from "../utils/flow.ts";

const props = defineProps<{
  id: string;
  type: string;
  selected?: boolean;
  data: Record<string, any>;
}>();

interface PromptEntry {
  id: string;
  kind?: "prompt" | "history";
  role: "system" | "user" | "assistant";
  content: string;
}

const hovered = ref(false);
// 删除回调由 FlowEditor 提供：它会在同一个动作里删节点 + 所有相连边。
const removeNode = inject<(id: string) => void>("flowRemoveNode", () => {});
const removePort = inject<(nodeId: string, handle: string, side: "source" | "target") => void>("flowRemovePort", () => {});
const models = inject<Ref<ModelListItem[]>>("flowModels", ref([]));
const label = computed(() => ({
  input: "输入",
  history: "历史数据",
  trim: "裁剪 messages",
  merge: "拼接 messages",
  llm: "调用 LLM",
  join: "拼接和复制",
  output: "输出",
}[props.type] ?? props.type));
const hint = computed(() => ({
  input: "裸字符串：本轮用户输入",
  history: "messages[]：当前会话历史",
  trim: "messages[] → messages[]：按条数裁剪",
  merge: "多路 messages[] 按端口顺序拼接",
  llm: "手动组装 messages",
  join: "多路拼接；只接一路时直接复制",
  output: "最终结果写入聊天",
}[props.type] ?? ""));
const prompts = computed<PromptEntry[]>(() => Array.isArray(props.data.prompts) ? props.data.prompts : []);
const modelOptions = computed(() => [
  { title: "跟随默认模型", value: "" },
  ...models.value.map((m) => ({
    title: `${m.provider} / ${m.display_name || m.upstream_id}`,
    value: `${m.provider}/${m.upstream_id}`,
  })),
]);
const modelValue = computed({
  get: () => props.data.provider && props.data.model ? `${props.data.provider}/${props.data.model}` : "",
  set: (value: string) => {
    const cut = value.indexOf("/");
    if (cut < 0) { props.data.provider = ""; props.data.model = ""; return; }
    props.data.provider = value.slice(0, cut);
    props.data.model = value.slice(cut + 1);
  },
});
const trimMode = computed<TrimMode>({
  get: () => trimModeOf(props.data),
  set: (value: TrimMode) => { props.data.mode = value; },
});
const trimCount = computed<string>({
  get: () => String(trimCountOf(props.data)),
  set: (value: string) => {
    const count = Number(value);
    props.data.count = Number.isInteger(count) && count > 0 ? count : 1;
  },
});
const trimRoleOptions: string[] = [...TRIM_ROLES];
const trimRoles = computed<string[]>({
  get: () => Array.isArray(props.data.roles) && props.data.roles.length
    ? props.data.roles.filter((role: unknown) => (TRIM_ROLES as readonly string[]).includes(String(role)))
    : [...TRIM_ROLES],
  set: (roles: string[]) => { props.data.roles = roles.length ? roles : [...TRIM_ROLES]; },
});
const trimSummary = computed(() => {
  const mode = TRIM_MODES.find((item) => item.value === trimMode.value)?.title ?? "保留最近 N 条";
  const roles = trimRoles.value.length === TRIM_ROLES.length ? "全部角色" : `只保留 ${trimRoles.value.join(" / ")}`;
  return `${mode.replace("N", trimCount.value)} · ${roles}`;
});
const mergeInputs = computed<string[]>(() => Array.isArray(props.data.inputs) && props.data.inputs.length
  ? props.data.inputs
  : ["in-1", "in-2"]);
const joinInputs = computed<string[]>(() => Array.isArray(props.data.inputs) && props.data.inputs.length
  ? props.data.inputs
  : ["in-1", "in-2"]);
const joinOutputs = computed<string[]>(() => Array.isArray(props.data.outputs) && props.data.outputs.length
  ? props.data.outputs
  : ["out-1"]);
const joinSeparator = computed({
  get: () => typeof props.data.separator === "string" ? props.data.separator : DEFAULT_JOIN_SEPARATOR,
  set: (value: string) => { props.data.separator = value; },
});
const roles = [
  { title: "system", value: "system" },
  { title: "user", value: "user" },
  { title: "assistant", value: "assistant" },
];
function addPrompt(): void {
  if (!Array.isArray(props.data.prompts)) props.data.prompts = [];
  props.data.prompts.push({ id: `p-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`, kind: "prompt", role: "system", content: "" });
}
function addHistory(): void {
  if (!Array.isArray(props.data.prompts)) props.data.prompts = [];
  props.data.prompts.push({ id: `h-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`, kind: "history", role: "system", content: "" });
}
function removePrompt(id: string): void {
  props.data.prompts = (props.data.prompts ?? []).filter((p: PromptEntry) => p.id !== id);
}
function removeMergeInput(index: number): void {
  if (mergeInputs.value.length <= 1) return;
  const [removed] = mergeInputs.value.slice(index, index + 1);
  props.data.inputs = mergeInputs.value.filter((_, i) => i !== index);
  if (removed) removePort(props.id, removed, "target");
}
function addMergeInput(): void {
  const inputs = [...mergeInputs.value];
  let n = inputs.length + 1;
  while (inputs.includes(`in-${n}`)) n++;
  props.data.inputs = [...inputs, `in-${n}`];
}
function removeJoinInput(index: number): void {
  if (joinInputs.value.length <= 1) return;
  const [removed] = joinInputs.value.slice(index, index + 1);
  props.data.inputs = joinInputs.value.filter((_, i) => i !== index);
  if (removed) removePort(props.id, removed, "target");
}
function removeJoinOutput(index: number): void {
  if (joinOutputs.value.length <= 1) return;
  const [removed] = joinOutputs.value.slice(index, index + 1);
  props.data.outputs = joinOutputs.value.filter((_, i) => i !== index);
  if (removed) removePort(props.id, removed, "source");
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
    <Handle v-if="type === 'output' || type === 'trim'" id="in" type="target" :position="Position.Left" />
    <div class="flow-node-head">
      <span class="flow-node-title">{{ label }}</span>
      <button class="flow-node-delete nodrag nopan" type="button" title="删除节点" aria-label="删除节点"
        @click.stop="removeNode(id)">×</button>
      <span class="flow-node-hint">{{ hint }}</span>
    </div>
    <label v-if="type === 'llm'" class="flow-llm-model nodrag nopan">
      <span>AI 模型</span>
      <VSelect v-model="modelValue" :items="modelOptions" density="compact" variant="outlined" hide-details />
    </label>
    <div v-if="type === 'llm'" class="flow-prompt-list">
      <div v-for="prompt in prompts" :key="prompt.id"
        :class="prompt.kind === 'history' ? 'flow-prompt-history' : 'flow-prompt-row'">
        <Handle v-if="prompt.kind === 'history'" :id="`history-${prompt.id}`" type="target" :position="Position.Left" :style="{ top: '50%' }" />
        <Handle v-else :id="`prompt-${prompt.id}`" type="target" :position="Position.Left" :style="{ top: '50%' }" />
        <template v-if="prompt.kind === 'history'">
          <span class="flow-history-label">历史 messages[]</span>
          <VBtn icon variant="text" size="x-small" type="button" class="flow-prompt-del nodrag nopan"
            title="删除这条历史" aria-label="删除这条历史" @click="removePrompt(prompt.id)">×</VBtn>
        </template>
        <template v-else>
          <div class="flow-prompt-head">
            <VSelect v-model="prompt.role" class="flow-prompt-role nodrag nopan"
              :items="roles" density="compact" variant="outlined" hide-details />
            <VBtn icon variant="text" size="x-small" type="button" class="flow-prompt-del nodrag nopan"
              title="删除这条 Prompt" aria-label="删除这条 Prompt" @click="removePrompt(prompt.id)">×</VBtn>
          </div>
          <VTextarea v-model="prompt.content" class="flow-prompt-content nodrag nopan"
            rows="2" density="compact" variant="outlined" hide-details
            placeholder="手写这段 Prompt（也可以只接上游）" />
        </template>
      </div>
      <div class="flow-prompt-add-row">
        <VBtn variant="text" size="small" type="button" class="flow-add-prompt nodrag nopan" @click="addPrompt">＋ 添加 Prompt</VBtn>
        <VBtn variant="text" size="small" type="button" class="flow-add-prompt nodrag nopan" @click="addHistory">＋ 添加历史</VBtn>
      </div>
    </div>
    <div v-if="type === 'history'" class="flow-node-body">来自真实会话的历史 messages</div>
    <div v-if="type === 'trim'" class="flow-trim-controls nodrag nopan">
      <VSelect v-model="trimMode" :items="TRIM_MODES" density="compact" variant="outlined" hide-details />
      <VTextField v-model="trimCount" type="number" min="1" step="1" density="compact"
        variant="outlined" hide-details suffix="条" />
      <VSelect v-model="trimRoles" :items="trimRoleOptions" multiple chips density="compact"
        variant="outlined" hide-details label="角色" />
      <span class="flow-node-body">{{ trimSummary }}</span>
    </div>
    <div v-if="type === 'merge'" class="flow-merge-inputs">
      <div v-for="(input, i) in mergeInputs" :key="input" class="flow-merge-row">
        <Handle :id="input" type="target" :position="Position.Left" />
        <span>输入 {{ i + 1 }} · messages[]</span>
        <VBtn icon variant="text" size="x-small" type="button" class="flow-port-del nodrag nopan"
          :disabled="mergeInputs.length <= 1" title="移除这个输入" aria-label="移除这个输入"
          @click.stop="removeMergeInput(i)">−</VBtn>
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-merge nodrag nopan" @click="addMergeInput">＋ 添加输入</VBtn>
    </div>
    <div v-if="type === 'join'" class="flow-join-inputs">
      <div v-for="(input, i) in joinInputs" :key="input" class="flow-join-row">
        <Handle :id="input" type="target" :position="Position.Left" />
        <span>输入 {{ i + 1 }}</span>
        <VBtn icon variant="text" size="x-small" type="button" class="flow-port-del nodrag nopan"
          :disabled="joinInputs.length <= 1" title="移除这个输入" aria-label="移除这个输入"
          @click.stop="removeJoinInput(i)">−</VBtn>
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-join nodrag nopan" @click="addJoinInput">＋ 添加输入</VBtn>
      <label class="flow-join-sep nodrag nopan">
        <span>分隔符</span>
        <VTextarea v-model="joinSeparator" rows="1" density="compact" variant="outlined" hide-details
          placeholder="留空 = 直接相连" />
      </label>
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
        <VBtn icon variant="text" size="x-small" type="button" class="flow-port-del nodrag nopan"
          :disabled="joinOutputs.length <= 1" title="移除这个输出" aria-label="移除这个输出"
          @click.stop="removeJoinOutput(i)">−</VBtn>
        <Handle :id="output" type="source" :position="Position.Right" />
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-join nodrag nopan" @click="addJoinOutput">＋ 添加输出</VBtn>
    </div>
    <Handle v-else-if="type !== 'output'" id="out" type="source" :position="Position.Right" />
  </div>
</template>
