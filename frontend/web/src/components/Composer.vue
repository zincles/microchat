<script setup>
// 底栏输入（TG 底栏味）：textarea 回车换行 + 自增高 1..6 行 + 发送键三档 + 纸飞机圆钮。
// 生成中（running）⇒ 圆钮变停止键（■），点即停。挂载点还是 #composer（CSS 照旧命中）。
import { ref, watch, nextTick } from "vue";
import { ICONS } from "./icons.js";

const emit = defineEmits(["input-text", "keydown", "submit", "stop", "menu"]);
// menuOn：命令菜单开不开（用户视图 → 输入；缺省关 —— 移动端那颗 ☰ 纯碍事）。
const props = defineProps({ menuOn: { type: Boolean, default: false } });
const text = ref("");
const taskText = ref(""); // 输入框上方、状态行头上那行小字（后台任务；空 = 藏）
function setTaskLine(t) { taskText.value = t ?? ""; }
// 状态行只有一句"连接 + 助手 + 相位"。**上下文占用不在这儿**（2026-10-28 用户定）：
// 它住顶栏右上角那枚环（`TopBar.vue` 的 `#ctx-ring`），点开才是明细。
// 所以这里不接 over/title —— 环形自己会变红，别留一份永远为假的"超预算"状态。
const statusText = ref("连接中｜未知模型｜idle");
function setStatus(t) { statusText.value = t ?? ""; }
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

defineExpose({ submit, clear, text, setStatus, setRunning, setTaskLine });
</script>

<template>
  <form id="composer" @submit.prevent="running ? $emit('stop') : submit()">
    <div v-if="taskText" class="taskline">{{ taskText }}</div>
    <div id="statusline" class="statusline">{{ statusText }}</div>
    <button
      v-if="props.menuOn"
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
      placeholder="输入消息…"
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
