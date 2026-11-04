<script setup>
// 分段页签：一条 nav + 一枚滑动的底片（设置视图的主栏与 Agent 编辑器里的二级页签**共用这一个**）。
//
// 为什么抽成组件：这东西在**两处**都要（2026-10-28 的教训 —— 同一个东西写两遍必然长歪，
// 那时段编辑器就是两边各一份，被用户一眼看出「不协调」）。
// 底片的 left / width 只能由 JS 量（按钮宽度跟着文字走，CSS 算不出来）；按钮按**下标**找
// （不用 id：两处同时挂在 DOM 上时 id 会撞，而 element id 是全局的）。
import { ref, watch, onMounted, onUnmounted, nextTick } from "vue";

const props = defineProps({
  tabs: { type: Array, required: true }, // [{ id, label }]（顺序 = 显示顺序）
  modelValue: { type: String, required: true },
});
const emit = defineEmits(["update:modelValue"]);

const navEl = ref(null);
const thumbLeft = ref(0);
const thumbWidth = ref(0);

function moveThumb() {
  nextTick(() => {
    const nav = navEl.value;
    if (!nav) return;
    const index = props.tabs.findIndex((t) => t.id === props.modelValue);
    if (index < 0) return;
    const btn = nav.querySelectorAll(".tab")[index];
    if (!btn) return;
    thumbLeft.value = btn.offsetLeft;
    thumbWidth.value = btn.offsetWidth;
  });
}

watch(() => props.modelValue, moveThumb);
watch(() => props.tabs.length, moveThumb);
onMounted(() => {
  moveThumb();
  // 窗口缩放 / 界面缩放滑杆都会让按钮变宽变窄（字大小变了），底片得跟着重新量。
  window.addEventListener("resize", moveThumb);
});
onUnmounted(() => window.removeEventListener("resize", moveThumb));
</script>

<template>
  <nav ref="navEl" class="settings-nav">
    <span class="tab-thumb" :style="{ left: thumbLeft + 'px', width: thumbWidth + 'px' }"></span>
    <button
      v-for="t in tabs"
      :key="t.id"
      type="button"
      class="tab"
      :class="{ sel: modelValue === t.id }"
      @click="emit('update:modelValue', t.id)"
    >
      {{ t.label }}
    </button>
  </nav>
</template>
