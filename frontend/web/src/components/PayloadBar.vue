<script setup>
// 右载荷栏：载荷 / 状态 / 压缩 三 tab。
// 载荷 = 真请求直放（method/url/headers/体 JSON）；状态 = 截至末条现演（tables 按表分组）；
// 压缩 = 压缩树（buildCompactTree 的产出：文件管理器那种看体积的树）。
import { ref, computed, watch } from "vue";
import CompactTree from "./CompactTree.vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import { ICONS } from "./icons.js";
import { treeTotals } from "../utils/tree.js";
import { parseWireMessages, shouldCollapse, previewBlock } from "../utils/payload.js";
import { payloadVizOn, savePayloadViz } from "../utils/prefs.js";

defineProps({
  defaultBlocks: { type: Number, default: null }, // chat.compact_blocks（空输入框的默认，只用于提示）
});
const emit = defineEmits(["compact", "tab", "toggle-right", "compact-payload"]);

const tab = ref("payload");
// 切 tab 报给 App：压缩树看着才拉全量账本（窗口化的内存账，见 App.refreshTree）。
function selectTab(t) { tab.value = t; emit("tab", t); }
const blocks = ref(""); // 压缩面板上的块数输入（空 = 用配置默认）
function doCompact() {
  const n = Number(blocks.value);
  emit("compact", Number.isFinite(n) && n > 0 ? Math.floor(n) : null);
}
const wire = ref(null);
// 分块视图（用户点名 2026-10-11）：开关记忆在 localStorage；开着就把 body.messages 一块块画。
const viz = ref(payloadVizOn());
function setViz(v) { viz.value = !!v; savePayloadViz(viz.value); }
const vizData = computed(() => (viz.value ? parseWireMessages(wire.value) : null));
// 每块的开合（2026-10-11）：**长块默认折叠**（system prompt 那种巨块不撑爆面板）；
// 用户点过的以点过的为准。换一份载荷 ⇒ 状态清空、重按默认来。
const openMap = ref(new Map());
watch(vizData, () => { openMap.value = new Map(); });
function blockOpen(m, i) {
  return openMap.value.has(i) ? openMap.value.get(i) : !shouldCollapse(m.content);
}
function toggleBlock(m, i) {
  openMap.value.set(i, !blockOpen(m, i));
}
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

// —— 压缩那发的载荷（2026-10-28）——
// 用户问的是"能不能预览**即将被压缩的文本**"。答：能，而且给的是**整发请求** ——
// 后端的 `POST .../compact/payload` 与真压走同一段拼装（同一个 `wireFor`），所以这里画出来的
// 就是真会发出去的那一发（合同测试逐字节钉着）。App 拉回来经 `setCompactWire` 塞进来。
const compactInfo = ref({});
const compactWire = computed(() => compactInfo.value.payload ?? null);
const compactViz = computed(() => (compactWire.value ? parseWireMessages(compactWire.value) : null));
const compactMeta = computed(() => compactViz.value?.meta ?? {});
const compactMessages = computed(() => compactViz.value?.messages ?? []);
const compactRange = computed(() => {
  const info = compactInfo.value;
  if (!info.from_idx) return "";
  return `${info.merged ? "并" : "压"}第 ${info.from_idx}–${info.to_idx} 条（${info.blocks} 块）`;
});
// 材料里附没附世界状态 —— 附了就不是"纯文本那么小"，值得说一声。
const compactStateHint = computed(() => {
  const info = compactInfo.value;
  if (info.state_before && info.state_after) return "附了段首+段末状态";
  if (info.state_after) return "附了段末状态";
  if (info.state_before) return "附了段首状态";
  return "";
});
// 开合状态独立于上面"载荷"tab 那份（两边都在看同一种形状，但不要互相打断阅读）。
const cpOpenMap = ref(new Map());
function cpOpen(m, i) {
  return cpOpenMap.value.has(i) ? cpOpenMap.value.get(i) : !shouldCollapse(m.content);
}
function cpToggle(m, i) { cpOpenMap.value.set(i, !cpOpen(m, i)); }
function setCompactWire(result) {
  compactInfo.value = result ?? {};
  cpOpenMap.value = new Map(); // 换一发 ⇒ 开合回到默认（长块默认折叠）
}
// previewCompact：把面板上那个块数（空 = 用配置默认）交给 App 去问后端。
function previewCompact() {
  const n = Number(blocks.value);
  emit("compact-payload", Number.isFinite(n) && n > 0 ? Math.floor(n) : null);
}

defineExpose({ setWire, setTables, setTree, setCompactWire });
</script>

<template>
  <aside id="payload-bar">
    <div class="bar-head bar-tabs">
      <button
        id="toggle-right"
        class="icon-btn"
        type="button"
        title="收起载荷栏"
        aria-label="收起载荷栏"
        @click="$emit('toggle-right')"
        v-html="ICONS.panelRight"
      ></button>
      <button type="button" class="tab" :class="{ sel: tab === 'payload' }" @click="selectTab('payload')">载荷</button>
      <button type="button" class="tab" :class="{ sel: tab === 'state' }" @click="selectTab('state')">状态</button>
      <button type="button" class="tab" :class="{ sel: tab === 'tree' }" @click="selectTab('tree')">压缩</button>

    </div>
    <div v-show="tab === 'payload'" id="payload-pane">
      <div class="pv-toolbar">
        <ToggleSwitch :model-value="viz" @update:model-value="setViz" />
        <span class="lbl">分块视图（按 message 拆开看）</span>
      </div>
      <div v-if="viz && vizData" id="payload-viz">
        <div class="pv-meta">
          <span class="pv-method">{{ vizData.meta.method }}</span>
          <span class="pv-url" :title="vizData.meta.url">{{ vizData.meta.url }}</span>
          <span v-if="vizData.meta.model" class="pv-chip">{{ vizData.meta.model }}</span>
          <span class="pv-chip">{{ vizData.messages.length }} 条</span>
          <span v-if="vizData.meta.tools" class="pv-chip">tools ×{{ vizData.meta.tools }}</span>
          <span v-if="vizData.meta.stream" class="pv-chip">stream</span>
        </div>
        <ol class="pv-list">
          <li v-for="(m, i) in vizData.messages" :key="i" class="pv-msg" :class="`role-${m.role}`">
            <!-- 整行可点开合（触摸友好：大目标）—— 长块默认折叠，点开才铺全文（面板整体滚，不做嵌套滚动） -->
            <button type="button" class="pv-head" :aria-expanded="blockOpen(m, i)" @click="toggleBlock(m, i)">
              <span class="pv-tw">{{ blockOpen(m, i) ? "▾" : "▸" }}</span>
              <span class="pv-role">{{ m.role }}</span>
              <span class="pv-idx">#{{ i + 1 }}</span>
              <span v-if="m.name" class="pv-chip">{{ m.name }}</span>
              <span class="pv-chars">{{ m.chars }} 字</span>
            </button>
            <div v-if="!blockOpen(m, i) && m.content" class="pv-preview">{{ previewBlock(m.content) }}</div>
            <div v-if="blockOpen(m, i) && m.reasoning" class="pv-extra">思考（reasoning_content）：{{ m.reasoning }}</div>
            <div v-if="blockOpen(m, i) && m.content" class="pv-content">{{ m.content }}</div>
            <div v-else-if="blockOpen(m, i) && !m.toolCalls" class="pv-content pv-empty">（空）</div>
            <div v-if="m.toolCalls" class="pv-extra">tool_calls：{{ m.toolCalls }}</div>
            <div v-if="m.toolCallId" class="pv-extra">tool_call_id：{{ m.toolCallId }}</div>
          </li>
        </ol>
      </div>
      <div v-else>
        <div v-if="viz && wire" class="empty">（这条载荷里没有 messages 数组可分块 —— 下面是原始 JSON）</div>
        <pre id="outgoing-raw">{{ payloadText }}</pre>
      </div>
    </div>
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
        <!-- 压缩那发的载荷预演（2026-10-28 用户点名要）：只算不发，看得见"即将被嚼的文本" -->
        <button type="button" title="预览这一次压缩真会发出去的请求（不联网、不落库）" @click="previewCompact">看载荷</button>
      </div>
      <div v-if="compactWire" id="compact-payload">
        <div class="cp-head">
          <span class="cp-title">这一发压缩的载荷</span>
          <button type="button" class="cp-close" title="收起" aria-label="收起" @click="compactWire = null">×</button>
        </div>
        <div class="pv-meta">
          <span class="pv-method">{{ compactMeta.method }}</span>
          <span class="pv-url" :title="compactMeta.url">{{ compactMeta.url }}</span>
          <span v-if="compactMeta.model" class="pv-chip">{{ compactMeta.model }}</span>
          <span v-if="compactRange" class="pv-chip">{{ compactRange }}</span>
          <span v-if="compactInfo.material_chars" class="pv-chip">材料 {{ compactInfo.material_chars }} 字</span>
          <span v-if="compactStateHint" class="pv-chip">{{ compactStateHint }}</span>
        </div>
        <ol class="pv-list">
          <li v-for="(m, i) in compactMessages" :key="i" class="pv-msg" :class="`role-${m.role}`">
            <button type="button" class="pv-head" :aria-expanded="cpOpen(m, i)" @click="cpToggle(m, i)">
              <span class="pv-tw">{{ cpOpen(m, i) ? "▾" : "▸" }}</span>
              <span class="pv-role">{{ m.role }}</span>
              <span class="pv-idx">#{{ i + 1 }}</span>
              <span class="pv-chars">{{ m.chars }} 字</span>
            </button>
            <div v-if="!cpOpen(m, i)" class="pv-preview">{{ previewBlock(m.content) }}</div>
            <div v-else class="pv-content">{{ m.content }}</div>
          </li>
        </ol>
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
