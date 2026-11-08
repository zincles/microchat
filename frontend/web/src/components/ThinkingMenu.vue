<script setup lang="ts">
// 思考档位选择器（Vuetify 4 MD3）：VMenu + VBtn + VList。
// 2026-11-08 用户定：住在输入栏正上方的按钮区（不是顶栏）；显示未格式化的 ASCII 档位
// （default / low / medium / …），不要中文标签。菜单向上展开，避免盖住输入框。
import { computed } from "vue";
import { VBtn, VList, VListItem, VListItemTitle, VMenu } from "vuetify/components";
import type { ThinkingOption } from "../utils/thinking.ts";

const props = defineProps<{
  modelValue: string;
  options: ThinkingOption[];
}>();
const emit = defineEmits<{ "update:modelValue": [value: string] }>();

const current = computed(() => props.modelValue || "default");
const raw = (option: ThinkingOption): string => option.value || "default";

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
        :title="`thinking: ${current}`"
        :aria-label="`thinking: ${current}`"
      >
        <span class="thinking-label">{{ current }}</span>
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
        <VListItemTitle>{{ raw(option) }}</VListItemTitle>
      </VListItem>
    </VList>
  </VMenu>
</template>
