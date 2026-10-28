<script setup>
// 提示词段清单 —— **一个组件，两处用**（agent 的 base 后面 / 能力的模板后面）。
//
// 为什么抽出来（2026-10-28 用户：「不协调。总觉得和编辑系统提示词的有区别。」）：
// 同一种东西写两遍，第二遍就会悄悄长出别的样子 —— 标题一边是 `<h3>`、另一边是灰色小字
// 加一条底边线；预览一边叫「拼装预览」、另一边叫「这一发真会发出去的提示词」；
// 段正文截断 48 字 vs 40 字；"引用了库里没有的段"一边有汇总警告、另一边只有行内 ⚠。
// 这些都不是功能差异，纯粹是"忘了同步"。所以顺序 / 挑段 / 缺段警告 / 拼装预览
// **只留这一份实现**：改一处，两处一起变。
import { computed, ref } from "vue";
import { missingSegments } from "../utils/prompt.js";

const props = defineProps({
  ids: { type: Array, default: () => [] }, // 已挑的段 id（顺序 = 拼接顺序）
  segments: { type: Array, default: () => [] }, // 段库（随 GET /agents 一起给）
  assembled: { type: String, default: "" }, // 拼装结果（只做预览；权威在后端）
  heading: { type: String, required: true },
  hint: { type: String, default: "" }, // 一行补充说明（能力那块用；agent 级传空）
});
const emit = defineEmits(["update:ids"]);

const pick = ref("");
const segText = (id) => props.segments.find((s) => s.id === id)?.text ?? "";
const segMissing = (id) => !props.segments.some((s) => s.id === id);
const missing = computed(() => missingSegments(props.ids, props.segments));
const commit = (next) => emit("update:ids", next);

function add() {
  if (pick.value && !props.ids.includes(pick.value)) commit([...props.ids, pick.value]);
  pick.value = "";
}
function move(index, delta) {
  const target = index + delta;
  if (target < 0 || target >= props.ids.length) return;
  const next = [...props.ids];
  [next[index], next[target]] = [next[target], next[index]];
  commit(next);
}
function drop(index) { commit(props.ids.filter((_, at) => at !== index)); }
function optionLabel(segment) {
  const marks = `${segment.builtin ? "（内置）" : ""}${segment.imported ? "（导入）" : ""}`;
  return `${segment.id}${marks} —— ${segment.text.slice(0, 30)}`;
}
</script>

<template>
  <h3>{{ heading }}</h3>
  <p v-if="hint" class="status">{{ hint }}</p>
  <div v-if="!ids.length" class="status">（还没有段 —— 从下面挑一个加进来）</div>
  <div v-for="(sid, i) in ids" :key="sid" class="seg-row" :class="{ missing: segMissing(sid) }">
    <button type="button" class="icon-btn" title="上移" :disabled="i === 0" @click="move(i, -1)">↑</button>
    <button type="button" class="icon-btn" title="下移" :disabled="i === ids.length - 1" @click="move(i, 1)">↓</button>
    <span class="seg-id">{{ sid }}<template v-if="segMissing(sid)"> ⚠ 库里没有</template></span>
    <span class="seg-preview">{{ segText(sid).slice(0, 48) }}</span>
    <button type="button" class="icon-btn danger" title="移除（不删库里的段）" @click="drop(i)">✕</button>
  </div>
  <div class="row">
    <select v-model="pick" title="从段库挑一个">
      <option value="">从段库挑一个…</option>
      <option v-for="s in segments" :key="s.id" :value="s.id" :disabled="ids.includes(s.id)">{{ optionLabel(s) }}</option>
    </select>
    <button type="button" :disabled="!pick" @click="add">添加段</button>
    <span v-if="missing.length" class="status">引用了库里没有的段：{{ missing.join("、") }}（保存会被后端拒）</span>
  </div>
  <details class="seg-assembled">
    <summary>拼装预览（{{ assembled.length }} 字）</summary>
    <pre>{{ assembled }}</pre>
  </details>
</template>
