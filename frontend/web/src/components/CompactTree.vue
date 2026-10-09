<script setup>
// CompactTree：压缩树（文件管理器那种一眼看体积的树）—— 缩进行 + 行底容量条。
// 行元素：▸/▾ 折叠 | 行底 bar（宽度 = 子树原文占比，du 式）| 标签（区间/角色 + 标记）| 右侧体积。
// 标记：`发` = 装配真的跳到它（这一坨就发这份）；`脏` = 摘要被覆盖的消息改过、已过期（装配照用）。
import { ref, computed } from "vue";

const props = defineProps({
  nodes: { type: Array, default: () => [] }, // buildCompactTree 的产出
});

const collapsed = ref(new Set());
function toggle(id) {
  const s = new Set(collapsed.value);
  if (s.has(id)) s.delete(id);
  else s.add(id);
  collapsed.value = s;
}

// 展平成行（带 depth）：渲染与"bar 基准"共用一份遍历。
const rows = computed(() => {
  const out = [];
  const walk = (ns, depth) => {
    for (const n of ns) {
      out.push({ n, depth });
      if (!collapsed.value.has(n.id)) walk(n.children, depth + 1);
    }
  };
  walk(props.nodes, 0);
  return out;
});
const maxRaw = computed(() => {
  let m = 1;
  const walk = (ns) => {
    for (const n of ns) {
      m = Math.max(m, n.rawChars);
      walk(n.children);
    }
  };
  walk(props.nodes);
  return m;
});
// 缩进：**每层 1.8em**（1em 时 340px 面板里几乎看不出层级，2026-10-10 用户点名要更明显）。
const INDENT_EM = 1.8;
function indentStyle(depth) {
  return { paddingLeft: `calc(0.15em + ${depth * INDENT_EM}em)` };
}
// 容量条从**缩进起点**画起（都从最左起会把层级感抹平）：宽度 = 行宽的占比 × 剩余空间。
function barStyle(n, depth) {
  const pct = Math.max(0.02, n.rawChars / maxRaw.value);
  const indent = depth * INDENT_EM;
  return {
    left: `calc(0.15em + ${indent}em)`,
    width: `calc((100% - ${indent}em - 0.5em) * ${pct})`,
  };
}
</script>

<template>
  <div class="ctree">
    <div
      v-for="(r, i) in rows"
      :key="r.n.id ?? i"
      class="ctree-row"
      :class="{ sum: r.n.kind === 'summary', used: r.n.used }"
      :style="indentStyle(r.depth)"
    >
      <span class="ctree-bar" :style="barStyle(r.n, r.depth)"></span>
      <button
        v-if="r.n.children.length"
        class="ctree-tw"
        type="button"
        :title="collapsed.has(r.n.id) ? '展开' : '折叠'"
        @click="toggle(r.n.id)"
      >{{ collapsed.has(r.n.id) ? "▸" : "▾" }}</button>
      <span v-else class="ctree-tw"></span>
      <span class="ctree-label" :title="r.n.kind === 'summary' ? `摘要 ${r.n.id}` : `消息 ${r.n.id}`">
        <template v-if="r.n.kind === 'summary'">#{{ r.n.fromIdx }}–{{ r.n.toIdx }} 摘要×{{ r.n.blocks }}</template>
        <template v-else>#{{ r.n.idx }} {{ r.n.role === "user" ? "你" : "助手" }}</template>
      </span>
      <i v-if="r.n.used" class="ctree-tag" title="装配真的跳到这里（这一坨发出的就是这份）">发</i>
      <i v-if="r.n.dirty" class="ctree-tag dirty" title="被盖的消息改过：摘要已过期（装配照用）">脏</i>
      <span class="ctree-size" :title="r.n.kind === 'summary' ? `压缩后 ≈${r.n.tokens} tok（原文 ${r.n.rawChars} 字）` : `原文 ${r.n.rawChars} 字`">
        {{ r.n.kind === "summary" ? "→" : "≈" }}{{ r.n.tokens }}
      </span>
    </div>
  </div>
</template>
