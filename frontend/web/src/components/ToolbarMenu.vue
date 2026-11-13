<script setup lang="ts">
// 输入栏上方的 MD3 工具菜单壳：VMenu + 触发器按钮 + VList。
// 思考档位 / 工具选择只是内容不同，壳不该各写一遍。
import { VBtn, VList, VMenu } from "vuetify/components";

withDefaults(defineProps<{
  title: string;
  label: string;
  width?: number;
  buttonClass?: string;
  labelClass?: string;
  listClass?: string;
}>(), {
  width: 180,
  buttonClass: "",
  labelClass: "",
  listClass: "",
});
</script>

<template>
  <VMenu location="top end" origin="bottom end" :offset="[0, 8]" :width="width" max-height="50vh">
    <template #activator="{ props: activatorProps }">
      <VBtn
        v-bind="activatorProps"
        type="button"
        variant="text"
        rounded="xl"
        size="small"
        :class="buttonClass"
        :title="title"
        :aria-label="title"
      >
        <span :class="labelClass">{{ label }}</span>
      </VBtn>
    </template>
    <VList
      density="compact"
      color="on-surface"
      bg-color="surface-container"
      rounded="md"
      :class="['py-1', listClass]"
    >
      <slot />
    </VList>
  </VMenu>
</template>
