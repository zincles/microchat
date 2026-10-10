<script setup>
// 右载荷栏：载荷 / 状态 / 压缩 三 tab。
// 载荷 = 真请求直放（method/url/headers/体 JSON）；状态 = 截至末条现演（tables 按表分组）；
// 压缩 = 压缩树（buildCompactTree 的产出：文件管理器那种看体积的树）。
import { ref, computed } from "vue";
import CompactTree from "./CompactTree.vue";
import { treeTotals } from "../utils/tree.js";

const props = defineProps({
  defaultBlocks: { type: Number, default: null }, // chat.compact_blocks（空输入框的默认，只用于提示）
});
const emit = defineEmits(["compact", "tab"]);

const tab = ref("payload");
// 切 tab 报给 App：压缩树看着才拉全量账本（窗口化的内存账，见 App.refreshTree）。
function selectTab(t) { tab.value = t; emit("tab", t); }
const blocks = ref(""); // 压缩面板上的块数输入（空 = 用配置默认）
function doCompact() {
  const n = Number(blocks.value);
  emit("compact", Number.isFinite(n) && n > 0 ? Math.floor(n) : null);
}
const wire = ref(null);
const tables = ref({});
const tree = ref(null); // null = 还没数据（不发请求，显示空话）

const payloadText = computed(() =>
  wire.value ? JSON.stringify(wire.value, null, 2) : "(空：打几个字，这里按真请求预演)",
);
const tableNames = computed(() => Object.keys(tables.value ?? {}).sort());
const totals = computed(() => (tree.value ? treeTotals(tree.value) : null));

function setWire(w) { wire.value = w ?? null; }
function setTables(t) { tables.value = t ?? {}; }
function setTree(t) { tree.value = t ?? null; }

defineExpose({ setWire, setTables, setTree });
</script>

<template>
  <aside id="payload-bar">
    <div class="bar-head bar-tabs">
      <button type="button" class="tab" :class="{ sel: tab === 'payload' }" @click="selectTab('payload')">载荷</button>
      <button type="button" class="tab" :class="{ sel: tab === 'state' }" @click="selectTab('state')">状态</button>
      <button type="button" class="tab" :class="{ sel: tab === 'tree' }" @click="selectTab('tree')">压缩</button>
    </div>
    <pre v-show="tab === 'payload'" id="outgoing-raw">{{ payloadText }}</pre>
    <div v-show="tab === 'state'" id="state-view">
      <div v-if="!tableNames.length" class="empty">（发一句话后这里显示世界状态）</div>
      <section v-for="name in tableNames" :key="name" class="state-table">
        <h4>{{ name }}</h4>
        <div v-for="(v, k) in tables[name]" :key="k" class="state-row">
          <span class="k">{{ k }}</span><span class="v">{{ v }}</span>
        </div>
        <div v-if="!Object.keys(tables[name] ?? {}).length" class="empty">（空）</div>
      </section>
    </div>
    <div v-show="tab === 'tree'" id="compact-tree">
      <div class="ctree-actions">
        <input
          v-model="blocks"
          type="number"
          min="1"
          :placeholder="defaultBlocks ? `块数（默认 ${defaultBlocks}）` : '块数（空=默认）'"
          @keydown.enter.prevent="doCompact"
        />
        <button type="button" class="primary" title="压缩 N 个块（空 = 用配置的 compact_blocks）" @click="doCompact">压缩</button>
      </div>
      <div v-if="!tree" class="empty">（发几句话后这里显示压缩树：目录 = 摘要，嵌套 = 嵌套压缩）</div>
      <template v-else>
        <div v-if="totals" class="ctree-total">原文 {{ totals.rawChars }} 字 · 装配后 ≈{{ totals.sent }} tok</div>
        <CompactTree :nodes="tree" />
        <div class="ctree-legend">▾ 折叠 · <i class="ctree-tag">发</i> 这一坨发出的就是它 · <i class="ctree-tag dirty">脏</i> 已过期</div>
      </template>
    </div>
  </aside>
</template>
