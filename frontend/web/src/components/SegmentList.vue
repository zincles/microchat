<script setup lang="ts">
// 提示词段清单 —— **一个组件，多处用**（系统提示词后 / 最新用户消息 / 能力模板后）。
//
// 同一种选择与排序逻辑分散多写几遍，很快就会长出不一致的标题、预览和缺段警告。
// 顺序 / 挑段 / 缺段警告 / 拼装预览只在这里实现；片段编辑由父级显式处理 `edit-segment`。
// 这让三种装配位置共用同一组行为，同时不把片段库或页面导航耦合进本组件。
//
// 修改这里时要保留 `ids` 的 v-model 和 `edit-segment` 事件契约。
import { computed, ref } from "vue";
import { VBtn, VSelect } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
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
const emit = defineEmits<{ "update:ids": [ids: string[]]; "edit-segment": [id: string] }>();

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
    <VBtn icon variant="text" size="small" type="button" :title="`编辑片段 ${sid}`" :aria-label="`编辑片段 ${sid}`" @click="emit('edit-segment', sid)"><span aria-hidden="true" v-html="ICONS.pencil"></span></VBtn>
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
