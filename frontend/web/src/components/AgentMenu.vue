<script setup lang="ts">
// 助手（Agent）选择入口（2026-11-10 用户定：从顶栏挪到输入栏上方，最左侧）。
// 只负责头像 + 选人菜单；真正的 PATCH 由 App 的 applyAgentValue 做。
import { computed } from "vue";
import { VAvatar, VListItem, VListItemTitle } from "vuetify/components";
import ToolbarMenu from "./ToolbarMenu.vue";
import type { Agent } from "../api/types.ts";

const props = withDefaults(defineProps<{
  agents?: Agent[];
  modelValue?: string;
}>(), { agents: () => [], modelValue: "" });
const emit = defineEmits<{ select: [id: string] }>();

interface AgentItem { id: string; label: string }
const items = computed<AgentItem[]>(() => {
  const list: AgentItem[] = (props.agents ?? [])
    .map((agent) => ({ id: agent.id, label: agent.name || agent.id }));
  if (props.modelValue && !list.some((agent) => agent.id === props.modelValue)) {
    list.unshift({ id: props.modelValue, label: `${props.modelValue}（不在列表）` });
  }
  return list;
});
const current = computed(() => items.value.find((agent) => agent.id === props.modelValue)?.label ?? (props.modelValue || "助手"));
function initial(label: string): string { return Array.from(label.trim())[0] ?? "?"; }
function avatarColor(id: string): string {
  let hue = 0;
  for (const char of id) hue = (hue * 31 + (char.codePointAt(0) ?? 0)) % 360;
  return `hsl(${hue} 42% 46%)`;
}
function pick(id: string): void {
  if (id !== props.modelValue) emit("select", id);
}
</script>

<template>
  <ToolbarMenu name="agent" title="切换助手" :width="240">
    <template #activator="{ props: activatorProps }">
      <button
        v-bind="activatorProps"
        id="agent-btn"
        class="agent-btn"
        type="button"
        :style="{ background: avatarColor(modelValue) }"
        :title="`当前助手：${current}（点击切换）`"
        aria-label="切换助手"
        :aria-description="`当前助手：${current}`"
      >{{ initial(current) }}</button>
    </template>
    <VListItem
      v-for="agent in items"
      :key="agent.id"
      role="option"
      :active="agent.id === modelValue"
      :aria-selected="agent.id === modelValue"
      @click="pick(agent.id)"
    >
      <template #prepend>
        <VAvatar :size="24" :style="{ background: avatarColor(agent.id) }" class="text-white" aria-hidden="true">
          {{ initial(agent.label) }}
        </VAvatar>
      </template>
      <VListItemTitle :title="agent.label">{{ agent.label }}</VListItemTitle>
      <template v-if="agent.id === modelValue" #append>
        <span aria-hidden="true" class="text-primary">✓</span>
      </template>
    </VListItem>
    <VListItem v-if="!items.length" disabled role="option" aria-selected="false" aria-disabled="true">
      <VListItemTitle>没有可选的助手</VListItemTitle>
    </VListItem>
  </ToolbarMenu>
</template>
