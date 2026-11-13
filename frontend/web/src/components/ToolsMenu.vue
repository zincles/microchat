<script setup lang="ts">
// 输入栏上方的工具选择（2026-11-09 用户定）：按钮显示当前**已启用**数量，
// 点开列出模型规则给出的 provider-native tools，可逐项取消/重新启用。
//
// 规则给的是“可用”；会话上存的是 disabled 列表 —— 新规则加进来的工具默认仍启用。
import { computed } from "vue";
import { VListItem, VListItemTitle } from "vuetify/components";
import ToolbarMenu from "./ToolbarMenu.vue";
import { nativeToolLabel, nativeToolsLabel, nativeToolsTitle } from "../utils/format.ts";

const props = withDefaults(defineProps<{
  available?: string[];
  disabled?: string[];
}>(), { available: () => [], disabled: () => [] });
const emit = defineEmits<{ toggle: [id: string, enabled: boolean] }>();

const enabled = computed(() => props.available.filter((id) => !props.disabled.includes(id)));
const title = computed(() => {
  if (!props.available.length) return "当前模型没有可用的内置工具（可在模型规则里追加）";
  if (!enabled.value.length) {
    return `已关闭全部内置工具：${props.available.map(nativeToolLabel).join("、")}`;
  }
  return nativeToolsTitle(enabled.value);
});
const label = computed(() => nativeToolsLabel(enabled.value));

function enabledOf(id: string): boolean { return !props.disabled.includes(id); }
function toggle(id: string, on: boolean | null): void { emit("toggle", id, !!on); }
</script>

<template>
  <ToolbarMenu
    :title="title"
    :label="label"
    :width="220"
    button-class="tools-btn"
    label-class="tools-label"
  >
    <template v-if="available.length">
      <VListItem
        v-for="id in available"
        :key="id"
        @click="toggle(id, !enabledOf(id))"
      >
        <template #prepend>
          <span
            class="tool-check"
            :class="{ on: enabledOf(id) }"
            role="checkbox"
            :aria-checked="enabledOf(id)"
          >
            <svg v-if="enabledOf(id)" viewBox="0 0 24 24" fill="none" stroke="currentColor"
              stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M20 6 9 17l-5-5" />
            </svg>
          </span>
        </template>
        <VListItemTitle>{{ nativeToolLabel(id) }}</VListItemTitle>
      </VListItem>
    </template>
    <VListItem v-else disabled>
      <VListItemTitle>当前模型没有可用工具</VListItemTitle>
    </VListItem>
  </ToolbarMenu>
</template>
