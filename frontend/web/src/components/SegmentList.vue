<script setup lang="ts">
// 提示词段清单 —— **一个组件，两处用**（agent 的 base 后面 / 能力的模板后面）。
//
// 为什么抽出来（2026-10-28 用户：「不协调。总觉得和编辑系统提示词的有区别。」）：
// 同一种东西写两遍，第二遍就会悄悄长出别的样子 —— 标题一边是 `<h3>`、另一边是灰色小字
// 加一条底边线；预览一边叫「拼装预览」、另一边叫「这一发真会发出去的提示词」；
// 段正文截断 48 字 vs 40 字；"引用了库里没有的段"一边有汇总警告、另一边只有行内 ⚠。
// 这些都不是功能差异，纯粹是"忘了同步"。所以顺序 / 挑段 / 缺段警告 / 拼装预览
// **只留这一份实现**：改一处，两处一起变。
import { computed, ref } from "vue";
import { VBtn, VSelect } from "vuetify/components";
import { missingSegments } from "../utils/prompt.ts";

export interface SegmentItem {
  id: string;
  text: string;
  builtin?: boolean;
  imported?: boolean;
}

const props = withDefaults(defineProps<{
  ids?: string[];
  segments?: SegmentItem[];
  assembled?: string;
  heading: string;
  hint?: string;
}>(), {
  ids: () => [],
  segments: () => [],
  assembled: "",
  hint: "",
});
const emit = defineEmits<{ "update:ids": [ids: string[]] }>();

const pick = ref("");
const segText = (id: string): string => props.segments.find((s) => s.id === id)?.text ?? "";
const segMissing = (id: string): boolean => !props.segments.some((s) => s.id === id);
const missing = computed(() => missingSegments(props.ids, props.segments));
const pickOptions = computed(() => props.segments
  .filter((s) => !props.ids.includes(s.id))
  .map((s) => ({ label: optionLabel(s), value: s.id })));
const commit = (next: string[]): void => { emit("update:ids", next); };

function add(): void {
  if (pick.value && !props.ids.includes(pick.value)) commit([...props.ids, pick.value]);
  pick.value = "";
}
function move(index: number, delta: number): void {
  const target = index + delta;
  if (target < 0 || target >= props.ids.length) return;
  const next = [...props.ids];
  [next[index], next[target]] = [next[target], next[index]];
  commit(next);
}
function drop(index: number): void { commit(props.ids.filter((_, at) => at !== index)); }
function optionLabel(segment: SegmentItem): string {
  const marks = `${segment.builtin ? "（内置）" : ""}${segment.imported ? "（导入）" : ""}`;
  return `${segment.id}${marks} —— ${segment.text.slice(0, 30)}`;
}
</script>

<template>
  <h3>{{ heading }}</h3>
  <p v-if="hint" class="status">{{ hint }}</p>
  <div v-if="!ids.length" class="status">（还没有片段 —— 从下面挑一个加进来）</div>
  <div v-for="(sid, i) in ids" :key="sid" class="seg-row" :class="{ missing: segMissing(sid) }">
    <VBtn icon variant="text" size="small" type="button" title="上移" :disabled="i === 0" @click="move(i, -1)">↑</VBtn>
    <VBtn icon variant="text" size="small" type="button" title="下移" :disabled="i === ids.length - 1" @click="move(i, 1)">↓</VBtn>
    <span class="seg-id">{{ sid }}<template v-if="segMissing(sid)"> ⚠ 不存在</template></span>
    <span class="seg-preview">{{ segText(sid).slice(0, 48) }}</span>
    <VBtn icon variant="text" size="small" type="button" title="移除（不会删除片段本身）" @click="drop(i)">✕</VBtn>
  </div>
  <div class="row">
    <VSelect v-model="pick" :items="pickOptions" item-title="label" item-value="value" placeholder="从提示词片段里挑一个…" density="compact" variant="outlined" hide-details class="seg-pick" />
    <VBtn variant="text" size="small" type="button" :disabled="!pick" @click="add">添加片段</VBtn>
    <span v-if="missing.length" class="status">引用了不存在的片段：{{ missing.join("、") }}（保存会被拒绝）</span>
  </div>
  <details class="seg-assembled">
    <summary>拼好的提示词预览（{{ assembled.length }} 字）</summary>
    <pre>{{ assembled }}</pre>
  </details>
</template>
