<script setup lang="ts">
// 底栏输入（2026-11-06 起：Vuetify 4 MD3 组件，渐进替换的第一块）。
//
// 行为不变：回车/发送键发送、生成中变停止、自增高 1..6 行、命令菜单、思考档位。
// 外观交给 Vuetify 的 `md3` blueprint（VTextarea / VBtn），不再由手写 CSS 画输入框和按钮。
import { ref, nextTick } from "vue";
import { VBtn, VTextarea } from "vuetify/components";
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

const text = ref("");
const running = ref(false);
const area = ref<InstanceType<typeof VTextarea> | null>(null);
const sendSvg = ICONS.send;

function setRunning(v: unknown): void {
  running.value = !!v;
}
function onValue(value: unknown): void {
  text.value = String(value ?? "");
  emit("input-text", text.value);
}
function onKeydown(e: KeyboardEvent): void {
  emit("keydown", e, text.value);
}
function submit(): void {
  const t = text.value.trim();
  text.value = "";
  emit("submit", t);
}
function stop(): void {
  emit("stop");
}
function selectThinking(value: string): void {
  emit("select-thinking", value);
}
function clear(): void {
  text.value = "";
}
function setText(t?: string): void {
  text.value = String(t ?? "");
  nextTick(() => area.value?.focus());
}

defineExpose({ submit, clear, setText, text, setRunning });
</script>

<template>
  <form id="composer" class="composer-vuetify" @submit.prevent="running ? stop() : submit()">
    <div class="composer-toolbar">
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
    </div>
    <div class="composer-row">
    <VTextarea
      id="composer-input"
      ref="area"
      class="composer-input"
      :model-value="text"
      variant="solo-filled"
      density="comfortable"
      bg-color="surface-container"
      flat
      hide-details
      auto-grow
      :rows="1"
      :max-rows="6"
      rounded="xl"
      placeholder="输入消息…"
      @update:model-value="onValue"
      @keydown="onKeydown"
    />
    <div class="composer-actions">
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
        :title="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
        :aria-label="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
        @click="running ? stop() : submit()"
      >
        <span v-html="running ? ICONS.stop : text.trim() ? sendSvg : ICONS.down"></span>
      </VBtn>
    </div>
    </div>
  </form>
</template>
