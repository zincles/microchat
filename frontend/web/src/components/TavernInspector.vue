<script setup lang="ts">
// ST 预设的「内容」面板（2026-11-11 用户定：把原来的「查看」与「提取」合成一个弹窗两 tab）。
//
// 两个 tab 是同一份预设的两种看法：
//   - 原始 JSON：只读树（后端保存的原始字节，不重写）；
//   - 有效内容：按 prompt_order 执行 setvar/getvar/trim/注释后剩下的正文。
//
// 有效条目可以逐条「导入为片段」：落到 agents.json 的段库里，并带上 `preset_id`
// 与导入当时的文件名快照。带动态宏（如 {{lastUserMessage}}）的条目**不许**导入 ——
// 段是静态文本，导进去只会留下一个永远不替换的占位符。
import { computed, ref, shallowRef, watch } from "vue";
import { VBtn, VCard, VCardText, VCardTitle, VDialog, VProgressCircular } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { depthText } from "../utils/format.ts";
import { parseJsonText, type JsonValue } from "../utils/json.ts";
import { ICONS } from "./icons.ts";
import JsonNode from "./JsonNode.vue";
import type { Segment, TavernEffective, TavernEffectiveEntry, TavernPreset } from "../api/types.ts";

const props = withDefaults(defineProps<{
  preset: TavernPreset;
  segments?: Segment[];
}>(), {
  segments: () => [],
});
const emit = defineEmits<{ close: []; imported: [segment: Segment] }>();
const api = sharedApi();

const open = ref(true);
const tab = ref<"effective" | "raw">("effective");

// —— 原始 JSON（按需加载：1.5MB 的预设不该在打开有效内容时先 parse 一遍）——
const rawLoading = ref(false);
const rawLoaded = ref(false);
const rawError = ref("");
const tree = shallowRef<JsonValue>(null);
const treeKey = ref(0);

async function loadRaw(): Promise<void> {
  if (rawLoading.value || rawLoaded.value) return;
  rawLoading.value = true;
  rawError.value = "";
  try {
    const blob = await api.downloadTavernPreset(props.preset.id);
    tree.value = parseJsonText(await blob.text());
    rawLoaded.value = true;
  } catch (e) {
    rawError.value = e instanceof Error ? e.message : String(e);
  } finally {
    rawLoading.value = false;
  }
}
function collapseAll(): void { treeKey.value += 1; }

// —— 有效内容 ——
const loading = ref(true);
const error = ref("");
const data = shallowRef<TavernEffective | null>(null);

async function loadEffective(): Promise<void> {
  loading.value = true;
  error.value = "";
  data.value = null;
  try {
    data.value = await api.tavernEffective(props.preset.id);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}
function meta(entry: TavernEffectiveEntry): string {
  const bits = [entry.role];
  if (entry.position === "absolute") {
    bits.push(`depth ${entry.depth ?? 0}（${depthText(entry.depth)}）`);
    if (entry.order != null) bits.push(`order ${entry.order}`);
  }
  if (entry.system_prompt) bits.push("system_prompt");
  return bits.join(" · ");
}
const unknownMacros = computed(() => (data.value?.macros ?? []).filter((m) => !m.known && m.name !== "lastUserMessage"));
const variablesText = computed(() => {
  const vars = data.value?.variables ?? {};
  const keys = Object.keys(vars).sort();
  if (!keys.length) return "";
  return keys.map((key) => `${key} = ${vars[key]}`).join("\n");
});

// —— 逐条导入为提示词片段 ——
// 静态段不能承载动态宏：只要正文里还有 {{...}} 就拒绝（有效提取通常会解析掉已知宏，
// 留下的就是 lastUserMessage 这类运行时才替换的）。
const dynamicMacro = (text: string): boolean => /\{\{[^{}]+\}\}/.test(text);
const importedKeys = ref<string[]>([]);
const importedIDs = ref<string[]>([]);
const importingKeys = ref<string[]>([]);
const importStatus = ref("");

const entryKey = (entry: TavernEffectiveEntry, index: number): string =>
  `${entry.identifier || entry.name || "entry"}#${index}`;
const isImported = (entry: TavernEffectiveEntry, index: number): boolean =>
  importedKeys.value.includes(entryKey(entry, index))
  || props.segments.some((s) => s.preset_id === props.preset.id && s.text === entry.content);
const isImporting = (entry: TavernEffectiveEntry, index: number): boolean =>
  importingKeys.value.includes(entryKey(entry, index));
const canImport = (entry: TavernEffectiveEntry): boolean =>
  !!entry.content.trim() && !dynamicMacro(entry.content);

function fitID(raw: string): string {
  const cleaned = raw.trim().replace(/\s+/g, "-") || "st-entry";
  return Array.from(cleaned).slice(0, 64).join("");
}
function nextSegmentID(entry: TavernEffectiveEntry, index: number): string {
  const base = fitID(entry.identifier || entry.name || `entry-${index + 1}`);
  const used = new Set([...props.segments.map((s) => s.id), ...importedIDs.value]);
  if (!used.has(base)) return base;
  for (let n = 2; n < 1000; n += 1) {
    const suffix = `-${n}`;
    const candidate = Array.from(base).slice(0, 64 - suffix.length).join("") + suffix;
    if (!used.has(candidate)) return candidate;
  }
  return `${Array.from(base).slice(0, 55).join("")}-${Date.now()}`;
}

async function importEntry(entry: TavernEffectiveEntry, index: number): Promise<void> {
  const key = entryKey(entry, index);
  if (isImported(entry, index) || isImporting(entry, index)) return;
  if (!canImport(entry)) {
    importStatus.value = `「${entry.name || entry.identifier}」含动态宏，不能导入为静态片段`;
    return;
  }
  const id = nextSegmentID(entry, index);
  importingKeys.value = [...importingKeys.value, key];
  importStatus.value = "";
  try {
    const segment = await api.createSegment({
      id,
      text: entry.content,
      imported: true,
      preset_id: props.preset.id,
    });
    importedKeys.value = [...importedKeys.value, key];
    importedIDs.value = [...importedIDs.value, segment.id];
    importStatus.value = `已导入片段「${segment.id}」`;
    emit("imported", segment);
  } catch (e) {
    importStatus.value = `导入失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    importingKeys.value = importingKeys.value.filter((k) => k !== key);
  }
}

function close(): void { open.value = false; }
watch(open, (value) => { if (!value) emit("close"); });
watch(tab, (value) => { if (value === "raw") void loadRaw(); });
watch(() => props.preset.id, () => {
  importedKeys.value = [];
  importedIDs.value = [];
  importingKeys.value = [];
  importStatus.value = "";
  rawLoaded.value = false;
  rawLoading.value = false;
  rawError.value = "";
  tree.value = null;
  void loadEffective();
  if (tab.value === "raw") void loadRaw();
}, { immediate: true });
</script>

<template>
  <VDialog v-model="open" attach="#app" class="tavern-inspector-dialog" scrim="rgba(0, 0, 0, 0.45)">
    <VCard class="tavern-preview-card" elevation="3">
      <VCardTitle class="tavern-preview-head">
        <div class="tavern-preview-title">
          <span>预设内容</span>
          <span class="tavern-preview-file">{{ preset.filename }}</span>
        </div>
        <div class="head-btns">
          <VBtn v-if="tab === 'raw' && rawLoaded" variant="text" size="small" type="button" @click="collapseAll">全部收起</VBtn>
          <VBtn icon variant="text" size="small" title="关闭" aria-label="关闭" @click="close" v-html="ICONS.x" />
        </div>
      </VCardTitle>

      <div class="tavern-inspector-tabs" role="tablist" aria-label="预设内容视图">
        <button
          type="button" role="tab" class="tavern-inspector-tab"
          :class="{ active: tab === 'effective' }" :aria-selected="tab === 'effective'"
          @click="tab = 'effective'"
        >有效内容</button>
        <button
          type="button" role="tab" class="tavern-inspector-tab"
          :class="{ active: tab === 'raw' }" :aria-selected="tab === 'raw'"
          @click="tab = 'raw'"
        >原始 JSON</button>
      </div>

      <VCardText class="tavern-preview-body">
        <template v-if="tab === 'raw'">
          <div v-if="rawLoading" class="tavern-preview-state" role="status">
            <VProgressCircular indeterminate size="22" color="primary" />
            <span>读取中…</span>
          </div>
          <div v-else-if="rawError" class="tavern-preview-state error" role="alert">{{ rawError }}</div>
          <div v-else-if="rawLoaded" class="json-tree">
            <JsonNode :key="treeKey" :value="tree" open-initially />
          </div>
        </template>

        <template v-else>
          <div v-if="loading" class="tavern-preview-state" role="status">
            <VProgressCircular indeterminate size="22" color="primary" />
            <span>求值中…</span>
          </div>
          <div v-else-if="error" class="tavern-preview-state error" role="alert">{{ error }}</div>
          <template v-else-if="data">
            <p class="tavern-preview-note">
              按 <code>prompt_order</code> 执行 <code>setvar/getvar/trim/注释</code> 后的结果；空条目已丢弃。
              <code v-text="'{{lastUserMessage}}'"></code> 没有给具体输入时保留原样。
              导入为片段时，带动态宏的条目不可导入。
            </p>
            <div class="tavern-preview-stats">
              <div><b>{{ data.name }}</b><span>预设名</span></div>
              <div><b>{{ data.entries.length }}</b><span>有效条目</span></div>
              <div><b>{{ data.dropped.length }}</b><span>求值后为空</span></div>
              <div><b>{{ Object.keys(data.variables).length }}</b><span>变量</span></div>
              <div><b>{{ unknownMacros.length }}</b><span>未解析宏</span></div>
            </div>

            <div class="tavern-preview-section-title">有效条目 <span>{{ data.entries.length }}</span></div>
            <p v-if="!data.entries.length" class="tavern-preview-empty">没有留下任何正文。</p>
            <article v-for="(entry, index) in data.entries" :key="entry.identifier" class="tavern-effective-entry">
              <div class="tavern-preview-entry-head">
                <b>{{ entry.name || entry.identifier }}</b>
                <span class="tavern-preview-tag">{{ entry.role }}</span>
                <span class="tavern-preview-meta">{{ meta(entry) }}</span>
              </div>
              <div class="tavern-preview-id">{{ entry.identifier }}</div>
              <pre class="tavern-effective-content">{{ entry.content }}</pre>
              <div class="tavern-inspector-actions">
                <VBtn
                  v-if="canImport(entry)"
                  variant="text" size="small" type="button"
                  :disabled="isImported(entry, index) || isImporting(entry, index)"
                  :title="`导入为提示词片段（来源：${preset.filename}）`"
                  @click="importEntry(entry, index)"
                >{{ isImported(entry, index) ? "已导入" : isImporting(entry, index) ? "导入中…" : "导入为片段" }}</VBtn>
                <span v-else class="tavern-preview-meta">含动态宏，不能导入为静态片段</span>
              </div>
            </article>
            <p v-if="importStatus" class="status" role="status">{{ importStatus }}</p>

            <div v-if="unknownMacros.length" class="tavern-preview-section-title">未解析宏 <span>{{ unknownMacros.length }} 种</span></div>
            <div v-if="unknownMacros.length" class="tavern-preview-macros">
              <span v-for="m in unknownMacros" :key="m.name" class="tavern-preview-macro unknown">
                {{ m.name }} ×{{ m.count }}
              </span>
            </div>

            <details v-if="variablesText" class="tavern-preview-skipped">
              <summary>变量 {{ Object.keys(data.variables).length }} 个</summary>
              <pre class="tavern-effective-vars">{{ variablesText }}</pre>
            </details>

            <details v-if="data.dropped.length" class="tavern-preview-skipped">
              <summary>丢弃 {{ data.dropped.length }} 条</summary>
              <div v-for="(item, index) in data.dropped" :key="`${item.identifier}-${index}`" class="tavern-preview-skip">
                <b>{{ item.name || item.identifier || "（空 id）" }}</b>
                <span>{{ item.reason }}</span>
              </div>
            </details>
          </template>
        </template>
      </VCardText>
    </VCard>
  </VDialog>
</template>
