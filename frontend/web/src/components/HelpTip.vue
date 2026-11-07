<script setup lang="ts">
// 问号提示：悬停或键盘聚焦触发器时显示注释。
// 用原生 button 当 activator（VTooltip 负责 tooltip 的可访问性/定位）。
import { VTooltip } from "vuetify/components";

withDefaults(defineProps<{
  text: string;
  label?: string;
}>(), { label: "说明" });
</script>

<template>
  <VTooltip :text="text" location="top" max-width="360" open-delay="120" close-delay="80">
    <template #activator="{ props }">
      <button v-bind="props" class="help-tip" type="button" :aria-label="label">?</button>
    </template>
  </VTooltip>
</template>

<style scoped>
.help-tip {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.25em;
  height: 1.25em;
  min-width: 0;
  min-height: 0;
  box-sizing: border-box;
  margin-left: 0.35em;
  padding: 0;
  border: 1px solid var(--md-outline);
  border-radius: 50%;
  background: none;
  color: var(--md-on-surface-variant);
  font: inherit;
  font-size: 0.7em;
  font-weight: 600;
  line-height: 1;
  vertical-align: 0.08em;
  cursor: help;
  transition: color var(--md-dur-150) var(--md-ease-standard), border-color var(--md-dur-150) var(--md-ease-standard);
}
.help-tip:hover {
  color: var(--md-on-surface);
  border-color: var(--md-on-surface);
}
</style>
