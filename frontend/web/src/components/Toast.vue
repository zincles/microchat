<script setup>
// Toast：右上通知（TG 安卓味）—— kind: "" 信息（蓝条）/ "ok" / "error"（红条）。
// 3s 自动消；点一下提前消。挂载点还是 #toasts（CSS 照旧命中），条目画法照 main.js。
import { ref } from "vue";

const items = ref([]);
let seq = 0;

function push(text, kind = "") {
  const id = ++seq;
  items.value.push({ id, text, kind });
  while (items.value.length > 4) items.value.shift(); // 最多攒 4 条，老的顶掉
  setTimeout(() => dismiss(id), 3000);
}

function dismiss(id) {
  const it = items.value.find((x) => x.id === id);
  if (!it) return;
  it.leaving = true;
  setTimeout(() => {
    items.value = items.value.filter((x) => x.id !== id);
  }, 220);
}

defineExpose({ push });
</script>

<template>
  <div id="toasts" aria-live="polite">
    <div
      v-for="t in items"
      :key="t.id"
      class="toast"
      :class="[t.kind, { out: t.leaving }]"
      @click="dismiss(t.id)"
    >
      {{ t.text }}
    </div>
  </div>
</template>
