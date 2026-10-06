<script setup>
// 浮层三件（命令面板 / picker / 确认框）：挂 #overlays-vue，DOM 照旧
// （.palette/.picker/.confirm + 各条目类），行为照 main.js。
import { ref } from "vue";
import { COMMAND_DESC } from "../utils/commands.js";

const emit = defineEmits(["run-command", "pick"]);

const palItems = ref([]);
const palSel = ref(0);
const pickerTitle = ref("");
const pickerItems = ref([]);
const pickerSel = ref(0);
const pickerOpen = ref(false);
const confirmText = ref("");
const confirmOpen = ref(false);

let pickerAction = null;
let confirmResolve = null;

function showPalette(items) {
  palItems.value = items ?? [];
  palSel.value = 0;
}
function movePalette(delta) {
  if (!palItems.value.length) return;
  palSel.value = (palSel.value + delta + palItems.value.length) % palItems.value.length;
}
function hidePalette() {
  palItems.value = [];
  palSel.value = 0;
}

function showPicker(title, items, action) {
  pickerTitle.value = title;
  pickerItems.value = items ?? [];
  pickerSel.value = 0;
  pickerOpen.value = true;
  pickerAction = action ?? null;
}
function movePicker(delta) {
  if (!pickerItems.value.length) return;
  pickerSel.value = (pickerSel.value + delta + pickerItems.value.length) % pickerItems.value.length;
}
function hidePicker() {
  pickerOpen.value = false;
  pickerAction = null;
}
function runPicker(it) {
  const a = pickerAction;
  hidePicker();
  if (it && a) emit("pick", a, it);
}

function confirmAsk(text) {
  return new Promise((resolve) => {
    confirmText.value = text;
    confirmOpen.value = true;
    confirmResolve = resolve;
  });
}
function confirmDone(v) {
  confirmOpen.value = false;
  const r = confirmResolve;
  confirmResolve = null;
  r?.(v);
}

defineExpose({
  showPalette, movePalette, hidePalette,
  showPicker, movePicker, hidePicker,
  confirmAsk,
  lift,
  isPaletteOpen: () => palItems.value.length > 0,
  isPickerOpen: () => pickerOpen.value,
  palSelected: () => palItems.value[palSel.value],
  pickerSelected: () => pickerItems.value[pickerSel.value],
  pickerAction: () => pickerAction,
});

// lift：把面板/ picker 顶到输入框上方（输入框长高多少就顶多少）。
// bottom = 输入框高 + 状态行 + 间隙，由调用方量好传进来（px）。
const liftPx = ref(0);
function lift(px) { liftPx.value = Math.max(0, px ?? 0); }
</script>

<template>
  <div id="overlays-vue">
    <div v-if="palItems.length" class="palette-backdrop" @click="hidePalette()"></div>
    <div class="palette" :class="{ hidden: !palItems.length }" :style="liftPx ? { bottom: `calc(5em + ${liftPx}px)` } : null">
      <div id="palette-list">
        <div
          v-for="(name, i) in palItems"
          :key="name"
          class="palette-item"
          :class="{ sel: i === palSel }"
          @click="$emit('run-command', name)"
        >
          <span class="cmd-name">/{{ name }}</span>
          <span class="cmd-desc">{{ COMMAND_DESC[name] ?? "" }}</span>
        </div>
      </div>
      <div class="palette-hint">Tab/↑↓ 选择，回车执行</div>
    </div>
    <div class="picker" :class="{ hidden: !pickerOpen }" :style="liftPx ? { bottom: `calc(5em + ${liftPx}px)` } : null">
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
    <div v-if="confirmOpen" class="confirm-backdrop" @click.self="confirmDone(false)">
      <div class="confirm">
        <div id="confirm-text">{{ confirmText }}</div>
        <div class="confirm-btns">
          <button id="confirm-yes" class="primary" type="button" @click="confirmDone(true)">确认</button>
          <button id="confirm-no" type="button" @click="confirmDone(false)">取消</button>
        </div>
      </div>
    </div>
  </div>
</template>
