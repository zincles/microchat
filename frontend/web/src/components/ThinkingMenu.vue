<script setup lang="ts">
// 思考档位选择器（Vuetify 4 MD3）：VMenu + VBtn + VList，菜单/焦点/键盘/点外关闭都由
// Vuetify 处理；这里只保留"当前档位"与"选中哪个"的业务逻辑。
import { computed } from "vue";
import { VBtn, VList, VListItem, VListItemTitle, VMenu } from "vuetify/components";
import { ICONS } from "./icons.ts";
import { thinkingLabel } from "../utils/thinking.ts";
import type { ThinkingOption } from "../utils/thinking.ts";

const props = defineProps<{
  modelValue: string;
  options: ThinkingOption[];
}>();
const emit = defineEmits<{ "update:modelValue": [value: string] }>();

const current = computed(
  () => props.options.find((o) => o.value === props.modelValue) ?? { value: props.modelValue, label: thinkingLabel(props.modelValue) },
);

function pick(value: string): void {
  if (value !== props.modelValue) emit("update:modelValue", value);
}
</script>

<template>
  <VMenu
    location="top end"
    origin="bottom end"
    :offset="[0, 8]"
    :width="180"
    max-height="50vh"
  >
    <template #activator="{ props: activatorProps }">
      <VBtn
        v-bind="activatorProps"
        variant="text"
        rounded="xl"
        class="thinking-btn"
        :title="`思考等级：${current.label}`"
        :aria-label="`思考等级：${current.label}`"
      >
        <span class="thinking-icon" v-html="ICONS.brain"></span>
        <span class="thinking-label">{{ current.label }}</span>
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
        <VListItemTitle>{{ option.label }}</VListItemTitle>
      </VListItem>
    </VList>
  </VMenu>
</template>
