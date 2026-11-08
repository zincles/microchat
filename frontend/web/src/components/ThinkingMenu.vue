<script setup lang="ts">
// 思考档位选择器（Vuetify 4 MD3）：VMenu + VBtn + VList。
// 2026-11-08 用户定：住在输入栏正上方的按钮区（不是顶栏）。
// 按钮显示「推理等级：默认」这样的完整中文；菜单项显示「低 low / 中 medium / …」。
// 菜单向上展开，避免盖住输入框。
import { computed } from "vue";
import { VBtn, VList, VListItem, VListItemTitle, VMenu } from "vuetify/components";
import { thinkingLabel } from "../utils/thinking.ts";
import type { ThinkingOption } from "../utils/thinking.ts";

const props = defineProps<{
  modelValue: string;
  options: ThinkingOption[];
}>();
const emit = defineEmits<{ "update:modelValue": [value: string] }>();

const current = computed(() => thinkingLabel(props.modelValue));
const currentText = computed(() => `推理等级：${current.value}`);

function pick(value: string): void {
  if (value !== props.modelValue) emit("update:modelValue", value);
}
</script>

<template>
  <VMenu location="top end" origin="bottom end" :offset="[0, 8]" :width="150" max-height="50vh">
    <template #activator="{ props: activatorProps }">
      <VBtn
        v-bind="activatorProps"
        type="button"
        variant="text"
        rounded="xl"
        size="small"
        class="thinking-btn"
        :title="currentText"
        :aria-label="currentText"
      >
        <span class="thinking-label">{{ currentText }}</span>
      </VBtn>
    </template>
    <VList
      density="compact"
      color="on-surface"
      bg-color="surface-container"
      rounded="md"
      class="thinking-list py-1"
    >
      <VListItem
        v-for="option in options"
        :key="option.value || 'default'"
        :active="option.value === modelValue"
        @click="pick(option.value)"
      >
        <VListItemTitle>
          <span>{{ option.label }}</span>
          <span class="thinking-raw">{{ option.value || "default" }}</span>
        </VListItemTitle>
      </VListItem>
    </VList>
  </VMenu>
</template>
