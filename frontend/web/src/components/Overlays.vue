<script setup lang="ts">
// 命令框与选择器跟随输入卡；确认对话框使用 Vuetify 的遮罩与焦点约束。
import { VBtn, VCard, VDialog } from "vuetify/components";
import CloseButton from "./CloseButton.vue";
import { ref, useTemplateRef, watch } from "vue";
import type { ConfirmOptions, PickerItem } from "../types/app.ts";

export interface PaletteItem {
  name: string;
  desc: string;
}
export type PickerAction = (item: PickerItem) => void;

const emit = defineEmits<{
  "run-command": [name: string];
  pick: [action: PickerAction, item: PickerItem];
}>();

const palItems = ref<PaletteItem[]>([]);
const palSel = ref(0);
const pickerTitle = ref("");
const pickerItems = ref<PickerItem[]>([]);
const pickerSel = ref(0);
const pickerOpen = ref(false);
const confirmText = ref("");
const confirmOpen = ref(false);
const confirmDestructive = ref(false);
const confirmAction = ref("");
const cancelButton = useTemplateRef<InstanceType<typeof VBtn>>("cancelButton");

let pickerAction: PickerAction | null = null;
let confirmResolve: ((value: boolean) => void) | null = null;
let confirmTrigger: HTMLElement | null = null;

// 面板受输入卡上方的可用高度限制；用方向键选到屏外条目时，只滚动列表，不移动焦点。
const panels = useTemplateRef<HTMLElement>("panels");
watch([palItems, palSel, pickerItems, pickerSel], () => {
  panels.value?.querySelector<HTMLElement>(".picker:not(.hidden) .sel, .palette:not(.hidden) .sel")?.scrollIntoView({ block: "nearest" });
}, { flush: "post" });

function showPalette(items?: PaletteItem[]): void {
  palItems.value = items ?? [];
  palSel.value = 0;
}
function movePalette(delta: number): void {
  if (!palItems.value.length) return;
  palSel.value = (palSel.value + delta + palItems.value.length) % palItems.value.length;
}
function hidePalette(): void {
  palItems.value = [];
  palSel.value = 0;
}

function showPicker(title: string, items?: PickerItem[], action?: PickerAction | null): void {
  pickerTitle.value = title;
  pickerItems.value = items ?? [];
  pickerSel.value = 0;
  pickerOpen.value = true;
  pickerAction = action ?? null;
}
function movePicker(delta: number): void {
  if (!pickerItems.value.length) return;
  pickerSel.value = (pickerSel.value + delta + pickerItems.value.length) % pickerItems.value.length;
}
function hidePicker(): void {
  pickerOpen.value = false;
  pickerAction = null;
}
function runPicker(it?: PickerItem): void {
  const a = pickerAction;
  hidePicker();
  if (it && a) emit("pick", a, it);
}

function confirmAsk(text: string, options: ConfirmOptions = {}): Promise<boolean> {
  confirmResolve?.(false);
  confirmTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  return new Promise<boolean>((resolve) => {
    confirmText.value = text;
    confirmDestructive.value = options.destructive ?? false;
    confirmAction.value = options.action ?? "";
    confirmOpen.value = true;
    confirmResolve = resolve;
  });
}
function confirmDone(v: boolean): void {
  confirmOpen.value = false;
  const r = confirmResolve;
  confirmResolve = null;
  r?.(v);
}
function focusCancel(): void { cancelButton.value?.$el.focus({ preventScroll: true }); }
function restoreConfirmFocus(): void {
  if (confirmTrigger?.isConnected) confirmTrigger.focus({ preventScroll: true });
  confirmTrigger = null;
}

defineExpose({
  showPalette,
  movePalette,
  hidePalette,
  showPicker,
  movePicker,
  hidePicker,
  confirmAsk,
  isPaletteOpen: (): boolean => palItems.value.length > 0,
  isPickerOpen: (): boolean => pickerOpen.value,
  palSelected: (): PaletteItem | undefined => palItems.value[palSel.value],
  pickerSelected: (): PickerItem | undefined => pickerItems.value[pickerSel.value],
  pickerAction: (): PickerAction | null => pickerAction,
});
</script>

<template>
  <div id="overlays-vue" ref="panels">
    <div v-if="palItems.length" class="palette-backdrop" @click="hidePalette()"></div>
    <div class="palette" :class="{ hidden: !palItems.length }">
      <div id="palette-list">
        <div
          v-for="(it, i) in palItems"
          :key="it.name"
          class="palette-item"
          :class="{ sel: i === palSel }"
          @click="emit('run-command', it.name)"
        >
          <span class="cmd-name">/{{ it.name }}</span>
          <span class="cmd-desc">{{ it.desc }}</span>
        </div>
      </div>
      <div class="palette-hint">Tab/↑↓ 选择，回车执行</div>
    </div>
    <div class="picker" :class="{ hidden: !pickerOpen }">
      <div id="picker-title">{{ pickerTitle }}</div>
      <div id="picker-list">
        <div
          v-for="(it, i) in pickerItems"
          :key="i"
          class="picker-item"
          :class="{ sel: i === pickerSel }"
          @click="runPicker(it)"
        >
          {{ it.label }}
        </div>
      </div>
    </div>
    <VDialog :model-value="confirmOpen" attach="#app" class="confirm-dialog" width="440" max-width="calc(100% - 24px)"
      aria-labelledby="confirm-title" aria-describedby="confirm-text" @update:model-value="(shown) => { if (!shown) confirmDone(false); }"
      @after-enter="focusCancel" @after-leave="restoreConfirmFocus">
      <VCard class="confirm-card" color="surface-container-high" @keydown.esc.stop="confirmDone(false)">
        <div class="confirm-heading">
          <h2 id="confirm-title">{{ confirmAction || '确认操作' }}</h2>
          <CloseButton @click="confirmDone(false)" />
        </div>
        <p id="confirm-text">{{ confirmText }}</p>
        <div class="confirm-actions">
          <VBtn ref="cancelButton" id="confirm-no" variant="text" size="small" @click="confirmDone(false)">取消</VBtn>
          <VBtn id="confirm-yes" :color="confirmDestructive ? 'error' : 'primary'" variant="flat" size="small" @click="confirmDone(true)">{{ confirmAction || '确认' }}</VBtn>
        </div>
      </VCard>
    </VDialog>
  </div>
</template>

<style scoped>
.confirm-card { padding: calc(2 * var(--chat-space)); border-radius: var(--md-shape-lg); }
.confirm-heading { display: flex; align-items: center; justify-content: space-between; gap: var(--chat-space); }
.confirm-heading h2 { margin: 0; font-size: var(--md-type-title-medium); font-weight: 600; }
#confirm-text { margin: var(--chat-space) 0 calc(2 * var(--chat-space)); color: var(--md-on-surface-variant); white-space: pre-wrap; overflow-wrap: anywhere; }
.confirm-actions { display: flex; justify-content: flex-end; gap: calc(var(--chat-space) / 2); }
#app .confirm-actions .v-btn { min-height: var(--chat-control); }
</style>
