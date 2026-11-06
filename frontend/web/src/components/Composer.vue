<script setup lang="ts">
// 底栏输入（TG 底栏味）：textarea 回车换行 + 自增高 1..6 行 + 发送键三档 + 纸飞机圆钮。
// 生成中（running）⇒ 圆钮变停止键（■），点即停。挂载点还是 #composer（CSS 照旧命中）。
import { ref, watch, nextTick } from "vue";
import { ICONS } from "./icons.ts";
import ThinkingMenu from "./ThinkingMenu.vue";
import type { ThinkingOption } from "../utils/thinking.ts";

const emit = defineEmits<{
  "input-text": [text: string];
  keydown: [event: KeyboardEvent, text: string];
  submit: [text: string];
  stop: [];
  menu: [];
  thinking: [value: string];
}>();
// menuOn：命令菜单开不开（用户视图 → 输入；缺省关 —— 移动端那颗 ☰ 纯碍事）。
const props = withDefaults(defineProps<{
  menuOn?: boolean;
  thinking?: string;
  thinkingOptions?: ThinkingOption[];
}>(), {
  menuOn: false,
  thinking: "",
  thinkingOptions: () => [],
});
const text = ref("");
const running = ref(false);
function setRunning(v: unknown): void { running.value = !!v; }
const area = ref<HTMLTextAreaElement | null>(null);
const sendSvg = ICONS.send;
function autosize(): void {
  nextTick(() => {
    if (!area.value) return;
    const el = area.value;
    el.rows = 1; // 重置后 scrollHeight 才只反映内容高度（读它强制同步重排）
    const cs = getComputedStyle(el);
    const line = parseFloat(cs.lineHeight) || 22;
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    // scrollHeight 含上下 padding：不减掉的话每算一次多出近一行（0.8 行取整成 1），
    // 输入框只涨不缩（删除也不回退）——踩过的棘轮。
    const rows = Math.round((el.scrollHeight - pad) / line);
    el.rows = Math.min(6, Math.max(1, rows));
  });
}
watch(text, autosize);

function onInput(): void {
  emit("input-text", text.value);
}

function onKeydown(e: KeyboardEvent): void {
  emit("keydown", e, text.value);
}

function submit(): void {
  const t = text.value.trim();
  text.value = "";
  if (area.value) area.value.rows = 1;
  emit("submit", t);
}
function stop(): void {
  emit("stop");
}

function clear(): void {
  text.value = "";
  if (area.value) area.value.rows = 1;
}

// setText：空草稿的示例句点一下就填进来（App 调）。聚焦放在同一拍 ——
// 用户点的是"我要说这句"，下一步动作必然是补充或回车，不该再点一次输入框。
function setText(t?: string): void {
  text.value = String(t ?? "");
  nextTick(() => area.value?.focus());
}

defineExpose({ submit, clear, setText, text, setRunning });
</script>

<template>
  <form id="composer" @submit.prevent="running ? stop() : submit()">
    <div class="composer-box">
      <textarea
        id="input"
        ref="area"
        v-model="text"
        rows="1"
        autocomplete="off"
        placeholder="输入消息…"
        @input="onInput"
        @keydown="onKeydown"
      ></textarea>
      <div class="composer-actions">
        <button
          v-if="props.menuOn"
          type="button"
          id="menu-btn"
          title="命令菜单"
          aria-label="命令菜单"
          @click="$emit('menu')"
          v-html="ICONS.menu"
        ></button>
        <ThinkingMenu
          v-if="props.thinkingOptions.length"
          :model-value="props.thinking"
          :options="props.thinkingOptions"
          @update:model-value="$emit('thinking', $event)"
        />
        <button
          type="submit"
          id="send-btn"
          :class="{ stop: running }"
          :title="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
          :aria-label="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
          v-html="running ? ICONS.stop : text.trim() ? sendSvg : ICONS.cont"
        ></button>
      </div>
    </div>
  </form>
</template>
