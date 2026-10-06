<script setup>
// 右载荷栏：真请求直放（method/url/headers/体 JSON）；没有就放空话。
import { ref, computed } from "vue";

const wire = ref(null);
const text = computed(() =>
  wire.value ? JSON.stringify(wire.value, null, 2) : "(还没有预演：输入框打字即问 (c))",
);

function setWire(w) { wire.value = w ?? null; }

defineExpose({ setWire });
</script>

<template>
  <aside id="payload-bar">
    <div class="bar-head"><span>即将发送的载荷</span></div>
    <pre id="outgoing-raw">{{ text }}</pre>
  </aside>
</template>
