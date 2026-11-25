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
  "toggle-zen": [];
}>();

const props = withDefaults(defineProps<{
  menuOn?: boolean;
  thinking?: string;
  thinkingOptions?: ThinkingOption[];
  nativeTools?: string[];
  nativeToolsDisabled?: string[];
  // flow：FlowAgent 暂不支持推理档位与工具选择 —— 底部小面板里直接藏起来。
  flow?: boolean;
  // 专注模式：顶栏隐藏、输入栏空闲自动下移一半。
  zen?: boolean;
  zenHidden?: boolean;
}>(), {
  menuOn: false,
  thinking: "",
  thinkingOptions: () => [],
  nativeTools: () => [],
  nativeToolsDisabled: () => [],
  flow: false,
  zen: false,
  zenHidden: false,
});

const text = defineModel<string>({ required: true });
const running = ref(false);
const hasText = computed(() => !!text.value.trim());

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
  const content = text.value.trim();
  if (content) emit("submit", content);
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
  <form id="composer" class="composer-vuetify" :class="{ zen: zen, 'zen-hidden': zen && zenHidden }" @submit.prevent="submit">
    <VSheet class="composer-shell" color="surface-container-low" rounded="xl">
      <VTextarea
        id="composer-input"
        class="composer-input"
        :model-value="text"
        variant="plain"
        density="comfortable"
        hide-details
        auto-grow
        :rows="1"
        :max-rows="6"
        placeholder="输入消息…"
        @update:model-value="onValue"
        @keydown="onKeydown"
      />
      <div class="composer-toolbar">
        <div class="composer-options">
          <slot name="toolbar" />
          <ThinkingMenu
            v-if="!flow && thinkingOptions.length"
            :model-value="thinking"
            :options="thinkingOptions"
            @update:model-value="selectThinking"
          />
          <ToolsMenu
            v-if="!flow"
            :available="nativeTools"
            :disabled="nativeToolsDisabled"
            @toggle="(id, on) => $emit('toggle-native-tool', id, on)"
          />
          <VBtn
            v-if="!flow"
            type="button"
            :variant="zen ? 'flat' : 'text'"
            :color="zen ? 'secondary-container' : undefined"
            rounded="xl"
            size="small"
            class="zen-btn"
            :aria-pressed="zen"
            :title="zen ? '退出专注模式' : '专注模式：隐藏顶栏，输入栏空闲时自动收起一半'"
            @click="$emit('toggle-zen')"
          >专注</VBtn>
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
          <VBtn
            id="send-btn"
            type="button"
            icon
            variant="flat"
            rounded="circle"
            :color="running ? 'error' : 'primary-container'"
            class="composer-send-btn"
            :disabled="!running && !hasText"
            :title="running ? '停止' : '发送'"
            :aria-label="running ? '停止' : '发送'"
            @click="running ? stop() : submit()"
          >
            <span aria-hidden="true" v-html="running ? ICONS.stop : ICONS.send"></span>
          </VBtn>
        </div>
      </div>
    </VSheet>
  </form>
</template>

<style scoped>
/* 表面只在这里定义：VSheet 管圆角/底色，VTextarea 用 plain 只负责输入。
 * 这样不会再把 .v-field 的覆盖写到全局，设置页和编辑器不受影响。 */
.composer-shell {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  width: 100%;
  min-width: 0;
  border: 1px solid var(--md-outline-variant);
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
  min-height: calc(6 * var(--chat-space));
  padding: calc(1.5 * var(--chat-space)) var(--chat-inset);
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
