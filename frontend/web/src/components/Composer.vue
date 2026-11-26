<script setup lang="ts">
// 一张输入卡：上方写消息，下方调整选项并发送；新对话只改变卡的位置。
import { computed, ref } from "vue";
import { VBtn, VSheet, VTextarea } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import ThinkingMenu from "./ThinkingMenu.vue";
import ToolsMenu from "./ToolsMenu.vue";
import type { ThinkingOption } from "../utils/thinking.ts";

const emit = defineEmits<{
  "input-text": [text: string];
  keydown: [event: KeyboardEvent, text: string];
  submit: [text: string];
  stop: [];
  menu: [];
  "select-thinking": [value: string];
  "toggle-native-tool": [id: string, enabled: boolean];
}>();

const props = withDefaults(defineProps<{
  menuOn?: boolean;
  thinking?: string;
  thinkingOptions?: ThinkingOption[];
  nativeTools?: string[];
  nativeToolsDisabled?: string[];
  thinkingPending?: boolean;
  toolsPending?: boolean;
  stopping?: boolean;
  canContinue?: boolean;
  // flow：FlowAgent 暂不支持推理档位与工具选择 —— 底部小面板里直接藏起来。
  flow?: boolean;
}>(), {
  menuOn: false,
  thinking: "",
  thinkingOptions: () => [],
  nativeTools: () => [],
  nativeToolsDisabled: () => [],
  flow: false,
});

const text = defineModel<string>({ required: true });
const running = ref(false);
const hasText = computed(() => !!text.value.trim());
const continueMode = computed(() => !hasText.value && props.canContinue);
const actionLabel = computed(() => running.value ? "停止" : continueMode.value ? "续写" : "发送");

function setRunning(v: unknown): void {
  running.value = !!v;
}
function onValue(value: unknown): void {
  const input = String(value ?? "");
  text.value = input;
  emit("input-text", input);
}
function onKeydown(e: KeyboardEvent): void {
  emit("keydown", e, text.value);
}
function submit(): void {
  if (running.value || props.stopping) return;
  const content = text.value.trim();
  if (content || props.canContinue) emit("submit", content);
}
function stop(): void {
  emit("stop");
}
function selectThinking(value: string): void {
  emit("select-thinking", value);
}

defineExpose({ submit, setRunning });
</script>

<template>
  <form id="composer" class="composer-vuetify" @submit.prevent="submit">
    <VSheet class="composer-shell" color="surface-container-low">
      <VTextarea
        id="composer-input"
        class="composer-input"
        :model-value="text"
        variant="plain"
        density="compact"
        hide-details
        auto-grow
        :rows="1"
        :max-rows="6"
        placeholder="输入消息…"
        @update:model-value="onValue"
        @keydown="onKeydown"
      />
      <div class="composer-toolbar">
        <slot name="toolbar" />
        <div class="composer-options">
          <ThinkingMenu
            v-if="!flow && thinkingOptions.length"
            :model-value="thinking"
            :options="thinkingOptions"
            :pending="thinkingPending"
            @update:model-value="selectThinking"
          />
          <ToolsMenu
            v-if="!flow"
            :available="nativeTools"
            :disabled="nativeToolsDisabled"
            :pending="toolsPending"
            @toggle="(id, on) => $emit('toggle-native-tool', id, on)"
          />
        </div>
        <div class="composer-top-actions">
          <VBtn
            v-if="props.menuOn"
            type="button"
            icon
            variant="text"
            rounded="circle"
            class="composer-menu-btn"
            title="命令菜单"
            aria-label="命令菜单"
            @click="$emit('menu')"
          >
            <span v-html="ICONS.menu"></span>
          </VBtn>
          <slot name="status" />
          <VBtn
            id="send-btn"
            type="button"
            icon
            variant="flat"
            rounded="circle"
            :color="running ? 'error' : hasText || canContinue ? 'primary' : undefined"
            class="composer-send-btn"
            :loading="stopping"
            :disabled="stopping || (!running && !hasText && !canContinue)"
            :title="actionLabel"
            :aria-label="actionLabel"
            @click="running ? stop() : submit()"
          >
            <span aria-hidden="true" v-html="running ? ICONS.stop : continueMode ? ICONS.down : ICONS.send"></span>
          </VBtn>
        </div>
      </div>
    </VSheet>
  </form>
</template>

<style scoped>
/* 输入卡只用一层表面，圆角与下方两个圆形按钮共用一套几何。 */
.composer-shell {
  --composer-radius: 24px;
  --composer-border: 1px;
  /* 内缩从描边内侧量起，按钮圆心到两条外边的距离都等于卡片圆角半径。 */
  --composer-corner-inset: calc(var(--composer-radius) - var(--composer-border) - var(--chat-control) / 2);
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  width: 100%;
  min-width: 0;
  border: var(--composer-border) solid var(--md-outline-variant);
  border-radius: var(--composer-radius);
  transition: border-color var(--md-dur-150) var(--md-ease-standard);
}
.composer-shell:focus-within {
  border-color: var(--md-primary);
}
.composer-shell :deep(.v-field) {
  background: transparent;
  border-radius: inherit;
}
.composer-shell :deep(.v-field__input) {
  padding: var(--composer-inset);
  font-size: calc(16px * var(--app-text-scale, 1));
  line-height: 1.5;
}
.composer-shell :deep(.v-field__outline) {
  display: none;
}
@media (prefers-reduced-motion: reduce) {
  .composer-shell { transition: none; }
}
</style>
