<script setup lang="ts">
// 输入栏选择器共用浮层、触发器的无障碍关系和选中后的焦点归还。
import { nextTick, useId, useTemplateRef, watch } from "vue";
import { VBtn, VList, VMenu } from "vuetify/components";
import { useChatMenu } from "../composables/useChatMenu.ts";

const props = withDefaults(defineProps<{
  name: "agent" | "thinking" | "tools";
  title: string;
  label?: string;
  width?: number;
  buttonClass?: string;
  labelClass?: string;
}>(), {
  width: 180,
  buttonClass: "",
  labelClass: "",
});
const open = useChatMenu(props.name);
const listId = `${useId()}-choices`;
const menu = useTemplateRef<{ activatorEl?: HTMLElement }>("menu");

// 受控 VMenu 打开时先更新 model，再挂载并定位列表。焦点若已去了别的入口就不抢回来。
function focusSelection(): void {
  const list = document.getElementById(listId);
  if (!open.value || !list || document.activeElement !== menu.value?.activatorEl) return;
  const enabled = '[role="option"]:not([aria-disabled="true"])';
  const option = list.querySelector<HTMLElement>(`${enabled}[aria-selected="true"]`) ?? list.querySelector<HTMLElement>(enabled);
  option?.focus();
}
watch(open, (show) => { if (show) nextTick(focusSelection); });

// 只在本菜单内选择或按 Escape 时归还焦点，外点/切换/卸载交给 VMenu。
function closeAndFocus(): void {
  if (!open.value) return;
  open.value = false;
  menu.value?.activatorEl?.focus({ preventScroll: true });
}
function onEscape(event: KeyboardEvent): void {
  if (event.key !== "Escape" || !open.value) return;
  event.stopPropagation();
  event.preventDefault();
  closeAndFocus();
}
</script>

<template>
  <VMenu
    ref="menu"
    v-model="open"
    :activator-props="{ 'aria-haspopup': 'listbox', 'aria-controls': listId, 'aria-owns': listId, onKeydown: onEscape }"
    :location="name === 'agent' ? 'top start' : 'top end'"
    :offset="8"
    :width="width"
    max-height="50vh"
    :close-on-content-click="false"
    @after-enter="focusSelection"
  >
    <template #activator="{ props: activatorProps }">
      <slot name="activator" :props="activatorProps">
        <VBtn
          v-bind="activatorProps"
          type="button"
          variant="outlined"
          rounded="xl"
          size="small"
          :class="buttonClass"
          :title="title"
          :aria-label="title"
        >
          <span :class="labelClass">{{ label }}</span>
          <span class="toolbar-chevron" aria-hidden="true"></span>
        </VBtn>
      </slot>
    </template>
    <VList
      :id="listId"
      role="listbox"
      :aria-label="title"
      :aria-multiselectable="name === 'tools' || undefined"
      density="compact"
      color="on-surface"
      bg-color="surface-container"
      rounded="md"
      class="py-1"
      @click="closeAndFocus"
      @keydown.esc="onEscape"
    >
      <slot />
    </VList>
  </VMenu>
</template>

<style scoped>
/* 箭头只提示这里能展开，读屏仍使用按钮原有的完整标签。 */
.toolbar-chevron {
  flex: none;
  width: 0.45em;
  height: 0.45em;
  margin: -0.2em 0.1em 0 0.4em;
  border: solid currentColor;
  border-width: 0 1.5px 1.5px 0;
  transform: rotate(45deg);
}
</style>
