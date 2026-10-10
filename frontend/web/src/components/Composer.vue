<script setup>
// 底栏输入（TG 底栏味）：textarea 回车换行 + 自增高 1..6 行 + 发送键三档 + 纸飞机圆钮。
// 生成中（running）⇒ 圆钮变停止键（■），点即停。挂载点还是 #composer（CSS 照旧命中）。
import { ref, watch, nextTick } from "vue";
import { ICONS } from "./icons.js";
import { sendHintText } from "../utils/prefs.js";

const emit = defineEmits(["input-text", "keydown", "submit", "stop", "menu"]);
const text = ref("");
const statusText = ref("上下文 ?/?｜未知模型｜idle");
const statusOver = ref(false);
function setStatus(t, over) { statusText.value = t; statusOver.value = !!over; }
const running = ref(false);
function setRunning(v) { running.value = !!v; }
const area = ref(null);
const sendSvg = ICONS.send;
function autosize() {
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

defineExpose({ submit, clear, text, setStatus, setRunning });
</script>

<template>
  <form id="composer" @submit.prevent="running ? $emit('stop') : submit()">
    <div id="statusline" class="statusline" :class="{ over: statusOver }">{{ statusText }}</div>
    <button
      type="button"
      id="menu-btn"
      title="命令菜单"
      aria-label="命令菜单"
      @click="$emit('menu')"
      v-html="ICONS.menu"
    ></button>
    <textarea
      id="input"
      ref="area"
      v-model="text"
      rows="1"
      autocomplete="off"
      :placeholder="sendHintText()"
      @input="onInput"
      @keydown="onKeydown"
    ></textarea>
    <button
      type="submit"
      id="send-btn"
      :class="{ stop: running }"
      :title="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
      :aria-label="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
      v-html="running ? ICONS.stop : text.trim() ? sendSvg : ICONS.cont"
    ></button>
  </form>
</template>
