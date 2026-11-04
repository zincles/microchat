<script setup>
// 底栏输入（TG 底栏味）：textarea 回车换行 + 自增高 1..6 行 + 发送键三档 + 纸飞机圆钮。
// 生成中（running）⇒ 圆钮变停止键（■），点即停。挂载点还是 #composer（CSS 照旧命中）。
import { ref, watch, nextTick } from "vue";
import { ICONS } from "./icons.js";

const emit = defineEmits(["input-text", "keydown", "submit", "stop", "menu"]);
// menuOn：命令菜单开不开（用户视图 → 输入；缺省关 —— 移动端那颗 ☰ 纯碍事）。
const props = defineProps({ menuOn: { type: Boolean, default: false } });
const text = ref("");
const running = ref(false);
function setRunning(v) { running.value = !!v; }
const area = ref(null);
const fsArea = ref(null);
const expanded = ref(false);
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
  expanded.value = false;
  emit("submit", t);
}
function stop() {
  expanded.value = false;
  emit("stop");
}
function openFullscreen() {
  expanded.value = true;
  nextTick(() => fsArea.value?.focus());
}
function closeFullscreen() {
  expanded.value = false;
  nextTick(() => area.value?.focus());
}
function onFsKeydown(e) {
  if (e.key === "Escape") { e.preventDefault(); closeFullscreen(); return; }
  onKeydown(e);
}

function clear() {
  text.value = "";
  if (area.value) area.value.rows = 1;
}

// setText：空草稿的示例句点一下就填进来（App 调）。聚焦放在同一拍 ——
// 用户点的是"我要说这句"，下一步动作必然是补充或回车，不该再点一次输入框。
function setText(t) {
  text.value = String(t ?? "");
  nextTick(() => (expanded.value ? fsArea.value : area.value)?.focus());
}

defineExpose({ submit, clear, setText, text, setRunning });
</script>

<template>
  <form id="composer" @submit.prevent="running ? stop() : submit()">
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
      type="button"
      id="expand-btn"
      title="全屏输入"
      aria-label="全屏输入"
      @click="openFullscreen"
      v-html="ICONS.maximize"
    ></button>
    <button
      type="submit"
      id="send-btn"
      :class="{ stop: running }"
      :title="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
      :aria-label="running ? '停止' : text.trim() ? '发送' : '续写（重发历史）'"
      v-html="running ? ICONS.stop : text.trim() ? sendSvg : ICONS.cont"
    ></button>
  </form>
  <Teleport to="#app">
    <section v-if="expanded" class="composer-fs" aria-label="全屏输入">
      <div class="composer-fs-head">
        <span>输入</span>
        <button type="button" class="composer-fs-close" title="退出全屏（Esc）" aria-label="退出全屏" @click="closeFullscreen" v-html="ICONS.minimize"></button>
      </div>
      <textarea
        ref="fsArea"
        v-model="text"
        class="composer-fs-input"
        placeholder="输入消息…"
        @input="onInput"
        @keydown="onFsKeydown"
      ></textarea>
      <div class="composer-fs-foot">
        <button type="button" id="fs-send-btn" :class="{ stop: running }" @click="running ? stop() : submit()">
          {{ running ? "停止" : "发送" }}
        </button>
      </div>
    </section>
  </Teleport>
</template>
