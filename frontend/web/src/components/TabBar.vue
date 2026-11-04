<script setup>
// 分段页签（设置主栏 / Agent 编辑器二级页共用）。
//
// 2026-11-04 统一成**下划线导航**：不再有滑动底片。底片要 JS 量宽度、还要跟 resize /
// 界面缩放滑杆重新对齐；而下划线是纯 CSS，和上下文三 tab、顶栏聊天/上下文是同一套语言。
const props = defineProps({
  tabs: { type: Array, required: true }, // [{ id, label }]（顺序 = 显示顺序）
  modelValue: { type: String, required: true },
});
const emit = defineEmits(["update:modelValue"]);
</script>

<template>
  <nav class="settings-nav" role="tablist">
    <button
      v-for="t in tabs"
      :key="t.id"
      type="button"
      class="tab"
      role="tab"
      :aria-selected="modelValue === t.id ? 'true' : 'false'"
      :class="{ sel: modelValue === t.id }"
      @click="emit('update:modelValue', t.id)"
    >
      {{ t.label }}
    </button>
  </nav>
</template>
