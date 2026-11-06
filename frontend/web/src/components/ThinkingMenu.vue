<script setup lang="ts">
// 输入框左侧的思考档位选择器（2026-11-06 用户定：Session 级七档 + 面板切换）。
//
// 为什么不塞在发送键左边：输入行右侧已经有发送/停止，再叠一颗会变成"按钮墙"；
// 左侧只有输入文字，放一枚紧凑药丸（脑图标 + 1~2 个汉字）最省地方。
// 菜单向上弹（输入行贴着屏幕底），点外部/选中即收。
import { computed, onMounted, onUnmounted, ref } from "vue";
import { ICONS } from "./icons.ts";
import { thinkingLabel } from "../utils/thinking.ts";
import type { ThinkingOption } from "../utils/thinking.ts";

const props = defineProps<{
  modelValue: string;
  options: ThinkingOption[];
}>();
const emit = defineEmits<{ "update:modelValue": [value: string] }>();

const open = ref(false);
const root = ref<HTMLElement | null>(null);
const current = computed(
  () => props.options.find((o) => o.value === props.modelValue) ?? { value: props.modelValue, label: thinkingLabel(props.modelValue) },
);

function toggle(): void {
  open.value = !open.value;
}
function pick(value: string): void {
  open.value = false;
  if (value !== props.modelValue) emit("update:modelValue", value);
}
function onDocPointer(event: PointerEvent): void {
  if (!open.value) return;
  const target = event.target as Node | null;
  if (root.value && target && !root.value.contains(target)) open.value = false;
}
onMounted(() => document.addEventListener("pointerdown", onDocPointer));
onUnmounted(() => document.removeEventListener("pointerdown", onDocPointer));
</script>

<template>
  <div ref="root" class="thinking-anchor">
    <button
      type="button"
      class="thinking-btn"
      :class="{ on: !!modelValue }"
      :title="`思考等级：${current.label}`"
      :aria-label="`思考等级：${current.label}`"
      aria-haspopup="menu"
      :aria-expanded="open"
      @click.stop="toggle"
    >
      <span class="thinking-icon" v-html="ICONS.brain"></span>
      <span class="thinking-label">{{ current.label }}</span>
    </button>
    <div v-if="open" class="thinking-menu" role="menu" aria-label="思考等级">
      <div class="thinking-menu-title">思考等级</div>
      <button
        v-for="option in options"
        :key="option.value || 'default'"
        type="button"
        class="thinking-item"
        :class="{ sel: option.value === modelValue }"
        role="menuitemradio"
        :aria-checked="option.value === modelValue"
        @click.stop="pick(option.value)"
      >
        <span>{{ option.label }}</span>
        <span v-if="option.value === modelValue" class="thinking-check">✓</span>
      </button>
    </div>
  </div>
</template>
