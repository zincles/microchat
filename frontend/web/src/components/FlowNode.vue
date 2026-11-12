<script setup lang="ts">
// FlowAgent 编辑器的节点外壳：输入 / 历史 / 裁剪 / 调用 LLM / 拼接 / 输出。
// LLM 节点内部是一张 **messages 列表**：每个 Prompt 条目有自己的 role 和左侧输入孔。
import { computed, inject, onUnmounted, ref, type Ref } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { VBtn, VSelect, VTextField, VTextarea } from "vuetify/components";
import type { ModelListItem } from "../api/types.ts";
import { DEFAULT_JOIN_SEPARATOR, TRIM_MODES, TRIM_ROLES, trimCountOf, trimModeOf, type FlowDataType, type TrimMode } from "../utils/flow.ts";
import { judgeAnswerType as judgeAnswerTypeOf, nodeBadgeFor, nodeHintFor, nodeLabelFor } from "../utils/flowSpecs.ts";

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
const runState = computed(() => String(props.data.runState ?? ""));
const runStateLabel = computed(() => ({ waiting: "等待", running: "进行", done: "完成" } as Record<string, string>)[runState.value] ?? "");
const nodeClass = computed(() => [`flow-node-${props.type}`, runState.value ? `flow-node-is-${runState.value}` : ""]);
// 删除回调由 FlowEditor 提供：它会在同一个动作里删节点 + 所有相连边。
const removeNode = inject<(id: string) => void>("flowRemoveNode", () => {});
const removePort = inject<(nodeId: string, handle: string, side: "source" | "target") => void>("flowRemovePort", () => {});
const models = inject<Ref<ModelListItem[]>>("flowModels", ref([]));
const label = computed(() => nodeLabelFor(props.type));
const badge = computed(() => nodeBadgeFor(props.type));
const hint = computed(() => nodeHintFor(props.type));
const portClass = (type: FlowDataType): string => `flow-port-${type}`;
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
const nodeStyle = computed(() => {
  if (props.type !== "llm") return {};
  const height = Number(props.data.height);
  return Number.isFinite(height) && height > 0 ? { height: `${height}px` } : {};
});
const resizing = ref(false);
let resizeStart = { y: 0, height: 0, zoom: 1 };
function resizeMove(event: PointerEvent): void {
  if (!resizing.value) return;
  // LLM 卡片宽度固定；缩放柄只改高度。
  // 鼠标位移是**屏幕像素**，height 是 **CSS 像素**；画布缩放 0.55 时直接相加会让
  // 卡片底边只走鼠标的一半，越拖偏得越远。先按缩放比换算。
  const delta = (event.clientY - resizeStart.y) / resizeStart.zoom;
  props.data.height = Math.max(120, Math.round(resizeStart.height + delta));
}
function resizeEnd(): void {
  resizing.value = false;
  window.removeEventListener("pointermove", resizeMove);
  window.removeEventListener("pointerup", resizeEnd);
  window.removeEventListener("pointercancel", resizeEnd);
}
function resizeStartHandler(event: PointerEvent): void {
  const node = (event.currentTarget as HTMLElement).closest(".flow-node") as HTMLElement | null;
  if (!node) return;
  // 以**按下那一刻的真实高度**为基准，并把基准立刻写回 data.height：
  // 否则第一帧会从旧的内容高度/旧 data.height 重算，表现成“一跳就变长”。
  const rect = node.getBoundingClientRect();
  if (rect.height <= 0) return;
  // 读 **CSS 像素** 高度，不读 getBoundingClientRect：后者被 Vue Flow 的画布缩放乘过，
  // 拿它当 height 写回会把节点瞬间缩掉一截（拖一下反而变短）。
  const cssHeight = Number.parseFloat(getComputedStyle(node).height);
  const base = Number.isFinite(cssHeight) && cssHeight > 0 ? cssHeight : rect.height;
  const zoom = Math.max(0.01, rect.height / base);
  // 让底边贴着鼠标：resizer 有自身高度，按在它中心时底边本来就在指针下面一点。
  const pointerToBottom = (event.clientY - rect.bottom) / zoom;
  resizeStart = { y: event.clientY, height: Math.max(120, Math.round(base + pointerToBottom)), zoom };
  props.data.height = resizeStart.height;
  resizing.value = true;
  window.addEventListener("pointermove", resizeMove);
  window.addEventListener("pointerup", resizeEnd);
  window.addEventListener("pointercancel", resizeEnd);
  event.preventDefault();
  event.stopPropagation();
}
onUnmounted(resizeEnd);
const maxRetry = computed<string>({
  get: () => String(Number.isInteger(Number(props.data.max_retry)) ? Number(props.data.max_retry) : 3),
  set: (value: string) => {
    const retry = Number(value);
    props.data.max_retry = Number.isInteger(retry) && retry >= 0 ? retry : 0;
  },
});
const textValue = computed({
  get: () => typeof props.data.text === "string" ? props.data.text : "",
  set: (value: string) => { props.data.text = value; },
});
const judgeProvider = computed({
  get: () => typeof props.data.provider === "string" ? props.data.provider : "",
  set: (value: string) => { props.data.provider = value; },
});
const judgeModel = computed({
  get: () => typeof props.data.model === "string" ? props.data.model : "",
  set: (value: string) => { props.data.model = value; },
});
const judgeQuestions = computed({
  get: () => typeof props.data.questions === "string" ? props.data.questions : JSON.stringify(props.data.questions ?? {}, null, 2),
  set: (value: string) => { props.data.questions = value; },
});
const judgeAnswerKey = computed({
  get: () => typeof props.data.answer_key === "string" ? props.data.answer_key : "",
  set: (value: string) => { props.data.answer_key = value; },
});
const judgeAnswerType = computed<FlowDataType>({
  get: () => judgeAnswerTypeOf(props as any),
  set: (value: FlowDataType) => { props.data.answer_type = value; },
});
const judgeThreshold = computed<string>({
  get: () => String(props.data.threshold ?? 0.5),
  set: (value: string) => {
    const number = Number(value);
    props.data.threshold = Number.isFinite(number) ? number : 0.5;
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
    :class="[nodeClass, { resizing }]"
    :style="nodeStyle"
    @mouseenter="hovered = true"
    @mouseleave="hovered = false"
  >
    <Handle v-if="type === 'trim'" id="in" type="target" :position="Position.Left" :class="portClass('messages')" />
    <Handle v-if="type === 'judge'" id="state" type="target" :position="Position.Left" :class="portClass('string')" />
    <div class="flow-node-head">
      <span class="flow-node-badge" aria-hidden="true">{{ badge }}</span>
      <div class="flow-node-heading">
        <span class="flow-node-title">{{ label }}</span>
        <span class="flow-node-hint">{{ hint }}</span>
      </div>
      <span v-if="runStateLabel" class="flow-node-state" :data-state="runState" :title="`节点状态：${runStateLabel}`">
        <span class="flow-node-state-dot" aria-hidden="true"></span>{{ runStateLabel }}
      </span>
      <button class="flow-node-delete nodrag nopan" type="button" title="删除节点" aria-label="删除节点"
        @click.stop="removeNode(id)">×</button>
    </div>
    <div v-if="type === 'llm'" class="flow-enabled-row">
      <Handle id="enabled" type="target" :position="Position.Left" :class="portClass('bool')" />
      <span>启用 · bool（可选）</span>
    </div>
    <label v-if="type === 'llm'" class="flow-llm-model nodrag nopan">
      <span>AI 模型</span>
      <VSelect v-model="modelValue" :items="modelOptions" density="compact" variant="outlined" hide-details />
    </label>
    <label v-if="type === 'llm'" class="flow-llm-retry nodrag nopan">
      <span>失败重试</span>
      <VTextField v-model="maxRetry" type="number" min="0" step="1" density="compact"
        variant="outlined" hide-details suffix="次" />
    </label>
    <div v-if="type === 'llm'" class="flow-prompt-list">
      <div v-for="prompt in prompts" :key="prompt.id"
        :class="prompt.kind === 'history' ? 'flow-prompt-history' : 'flow-prompt-row'">
        <Handle v-if="prompt.kind === 'history'" :id="`history-${prompt.id}`" type="target" :position="Position.Left" :style="{ top: '50%' }" :class="portClass('messages')" />
        <Handle v-else :id="`prompt-${prompt.id}`" type="target" :position="Position.Left" :style="{ top: '50%' }" :class="portClass('string')" />
        <template v-if="prompt.kind === 'history'">
          <span class="flow-history-label">历史 messages[]</span>
          <VBtn icon variant="text" size="x-small" type="button" class="flow-prompt-del nodrag nopan"
            title="删除这条历史" aria-label="删除这条历史" @click="removePrompt(prompt.id)">×</VBtn>
        </template>
        <template v-else>
          <div class="flow-prompt-head">
            <span class="flow-prompt-role-label">Role:</span>
            <select v-model="prompt.role" class="flow-prompt-role nodrag nopan"
              title="Prompt Role" aria-label="Prompt Role">
              <option v-for="role in roles" :key="role.value" :value="role.value">{{ role.title }}</option>
            </select>
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
    <div v-if="type === 'output'" class="flow-output-inputs">
      <div class="flow-output-row">
        <Handle id="text" type="target" :position="Position.Left" :class="portClass('string')" />
        <span>正文</span>
      </div>
      <div class="flow-output-row">
        <Handle id="reasoning" type="target" :position="Position.Left" :class="portClass('string')" />
        <span>思考</span>
      </div>
      <div class="flow-output-row">
        <Handle id="user" type="target" :position="Position.Left" :class="portClass('string')" />
        <span>用户消息</span>
      </div>
    </div>
    <div v-if="type === 'judge'" class="flow-judge-controls nodrag nopan">
      <label class="flow-judge-field">
        <span>渠道</span>
        <VTextField v-model="judgeProvider" density="compact" variant="outlined" hide-details
          placeholder="systemone 渠道 id" />
      </label>
      <label class="flow-judge-field">
        <span>决策模型</span>
        <VTextField v-model="judgeModel" density="compact" variant="outlined" hide-details
          placeholder="jev-latest" />
      </label>
      <label class="flow-judge-field">
        <span>答案类型</span>
        <VSelect v-model="judgeAnswerType" :items="['string', 'bool', 'number', 'choice']" density="compact"
          variant="outlined" hide-details />
      </label>
      <label v-if="judgeAnswerType === 'bool'" class="flow-judge-field">
        <span>布尔阈值</span>
        <VTextField v-model="judgeThreshold" type="number" step="0.05" min="0" max="1"
          density="compact" variant="outlined" hide-details />
      </label>
      <label class="flow-judge-field">
        <span>Questions JSON</span>
        <VTextarea v-model="judgeQuestions" rows="5" density="compact" variant="outlined" hide-details
          placeholder='{"involves": "是否涉及这个角色？"}' />
      </label>
      <label class="flow-judge-field">
        <span>Answer key（可选）</span>
        <VTextField v-model="judgeAnswerKey" density="compact" variant="outlined" hide-details
          placeholder="只输出某个答案；留空 = 整个 answers" />
      </label>
      <span class="flow-node-body">状态接左侧输入；输出是 JSON。低置信度/失败由下游裁决。</span>
    </div>
    <label v-if="type === 'text'" class="flow-text-value nodrag nopan">
      <VTextarea v-model="textValue" rows="3" density="compact" variant="outlined" hide-details
        placeholder="输入要输出的常量文本" />
      <span class="flow-node-body">没有输入口，只有右侧文本输出。</span>
    </label>
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
        <Handle :id="input" type="target" :position="Position.Left" :class="portClass('messages')" />
        <span>输入 {{ i + 1 }} · messages[]</span>
        <VBtn icon variant="text" size="x-small" type="button" class="flow-port-del nodrag nopan"
          :disabled="mergeInputs.length <= 1" title="移除这个输入" aria-label="移除这个输入"
          @click.stop="removeMergeInput(i)">−</VBtn>
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-merge nodrag nopan" @click="addMergeInput">＋ 添加输入</VBtn>
    </div>
    <div v-if="type === 'join'" class="flow-join-inputs">
      <div v-for="(input, i) in joinInputs" :key="input" class="flow-join-row">
        <Handle :id="input" type="target" :position="Position.Left" :class="portClass('string')" />
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
    <div v-if="(hovered || selected) && data.test" class="flow-node-test" role="tooltip" @wheel.stop>
      <b>测试 I/O</b>
      <span class="flow-node-test-label">输入</span>
      <pre>{{ (data.test.inputs ?? []).join("\n---\n") || "（无输入）" }}</pre>
      <span class="flow-node-test-label">思考</span>
      <pre>{{ data.test.reasoning || "（无思考）" }}</pre>
      <span class="flow-node-test-label">正文</span>
      <pre>{{ data.test.output || "（无输出）" }}</pre>
      <template v-if="data.test.user">
        <span class="flow-node-test-label">写入历史</span>
        <pre>{{ data.test.user }}</pre>
      </template>
    </div>
    <span v-if="type === 'llm'" class="flow-node-resizer nodrag nopan"
      title="拖动调整卡片高度" aria-label="调整卡片高度" @pointerdown="resizeStartHandler"></span>
    <div v-if="type === 'llm'" class="flow-llm-outputs">
      <div class="flow-llm-output-row">
        <span>思考</span>
        <Handle id="reasoning" type="source" :position="Position.Right" :class="portClass('string')" />
      </div>
      <div class="flow-llm-output-row">
        <span>正文</span>
        <Handle id="text" type="source" :position="Position.Right" :class="portClass('string')" />
      </div>
    </div>
    <div v-if="type === 'join'" class="flow-join-outputs">
      <div v-for="(output, i) in joinOutputs" :key="output" class="flow-join-output-row">
        <span>输出 {{ i + 1 }}</span>
        <VBtn icon variant="text" size="x-small" type="button" class="flow-port-del nodrag nopan"
          :disabled="joinOutputs.length <= 1" title="移除这个输出" aria-label="移除这个输出"
          @click.stop="removeJoinOutput(i)">−</VBtn>
        <Handle :id="output" type="source" :position="Position.Right" :class="portClass('string')" />
      </div>
      <VBtn variant="text" size="small" type="button" class="flow-add-join nodrag nopan" @click="addJoinOutput">＋ 添加输出</VBtn>
    </div>
    <Handle v-else-if="type !== 'output'" id="out" type="source" :position="Position.Right"
      :class="portClass(type === 'history' || type === 'trim' || type === 'merge' ? 'messages' : type === 'judge' ? judgeAnswerType : 'string')" />
  </div>
</template>
