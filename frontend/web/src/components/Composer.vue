<script setup>
// 底栏输入（TG 底栏味）：textarea 回车换行 + 自增高 1..6 行 + 发送键三档 + 纸飞机圆钮。
// 挂载点还是 #composer（CSS 照旧命中）。事件全 emit 出去，main.js 原样接。
import { ref, computed, watch, nextTick } from "vue";
import { ICONS } from "./icons.js";

const emit = defineEmits(["input-text", "keydown", "submit"]);
const text = ref("");
const statusText = ref("上下文 ?/?｜未知模型｜idle");
const statusOver = ref(false);
function setStatus(t, over) { statusText.value = t; statusOver.value = !!over; }
const area = ref(null);
const sendSvg = ICONS.send;

const hint = computed(() => {
  const mode = localStorage.getItem("mc_send") || "button";
  return mode === "enter" ? "输入消息（回车发送，Shift+回车换行）；/ 开头进命令"
    : mode === "shift-enter" ? "输入消息（Shift+回车发送，回车换行）；/ 开头进命令"
    : "输入消息，点发送发出（回车换行）；/ 开头进命令";
});

function autosize() {
  nextTick(() => {
    if (!area.value) return;
    area.value.rows = 1;
    const line = parseFloat(getComputedStyle(area.value).lineHeight) || 22;
    area.value.rows = Math.min(6, Math.max(1, Math.round(area.value.scrollHeight / line)));
  });
}
watch(text, autosize);

function onInput(e) {
  emit("input-text", text.value);
}

function onKeydown(e) {
  emit("keydown", e, text.value);
}

function submit() {
  const t = text.value.trim();
  text.value = "";
  if (area.value) area.value.rows = 1;
  emit("submit", t);
}

function clear() {
  text.value = "";
  if (area.value) area.value.rows = 1;
}

defineExpose({ submit, clear, text, setStatus });
</script>

<template>
  <form id="composer" @submit.prevent="submit">
    <div id="statusline" class="statusline" :class="{ over: statusOver }">{{ statusText }}</div>
    <textarea
      id="input"
      ref="area"
      v-model="text"
      rows="1"
      autocomplete="off"
      :placeholder="hint"
      @input="onInput"
      @keydown="onKeydown"
    ></textarea>
    <button type="submit" id="send-btn" title="发送" aria-label="发送" v-html="sendSvg"></button>
  </form>
</template>
