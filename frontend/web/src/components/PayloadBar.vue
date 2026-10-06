<script setup>
// 右载荷栏：载荷 / 状态双 tab。
// 载荷 = 真请求直放（method/url/headers/体 JSON）；状态 = 截至末条现演（tables 按表分组）。
import { ref, computed } from "vue";

const tab = ref("payload");
const wire = ref(null);
const tables = ref({});

const payloadText = computed(() =>
  wire.value ? JSON.stringify(wire.value, null, 2) : "(还没有预演：输入框打字即问 (c))",
);
const tableNames = computed(() => Object.keys(tables.value ?? {}).sort());

function setWire(w) { wire.value = w ?? null; }
function setTables(t) { tables.value = t ?? {}; }

defineExpose({ setWire, setTables });
</script>

<template>
  <aside id="payload-bar">
    <div class="bar-head bar-tabs">
      <button type="button" class="tab" :class="{ sel: tab === 'payload' }" @click="tab = 'payload'">载荷</button>
      <button type="button" class="tab" :class="{ sel: tab === 'state' }" @click="tab = 'state'">状态</button>
    </div>
    <pre v-show="tab === 'payload'" id="outgoing-raw">{{ payloadText }}</pre>
    <div v-show="tab === 'state'" id="state-view">
      <div v-if="!tableNames.length" class="empty">还没有状态</div>
      <section v-for="name in tableNames" :key="name" class="state-table">
        <h4>{{ name }}</h4>
        <div v-for="(v, k) in tables[name]" :key="k" class="state-row">
          <span class="k">{{ k }}</span><span class="v">{{ v }}</span>
        </div>
        <div v-if="!Object.keys(tables[name] ?? {}).length" class="empty">（空）</div>
      </section>
    </div>
  </aside>
</template>
