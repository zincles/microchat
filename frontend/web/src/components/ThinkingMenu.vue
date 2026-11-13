<script setup lang="ts">
// 思考档位选择器（Vuetify 4 MD3）：VMenu + VBtn + VList。
// 2026-11-08 用户定：住在输入栏正上方的按钮区（不是顶栏）。
// 按钮显示「推理等级：默认」这样的完整中文；菜单项显示「低 low / 中 medium / …」。
// 菜单向上展开，避免盖住输入框。
import { computed } from "vue";
import { VListItem, VListItemTitle } from "vuetify/components";
import ToolbarMenu from "./ToolbarMenu.vue";
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
  <ToolbarMenu
    :title="currentText"
    :label="currentText"
    :width="150"
    button-class="thinking-btn"
    label-class="thinking-label"
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
  </ToolbarMenu>
</template>
