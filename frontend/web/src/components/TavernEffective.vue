<script setup lang="ts">
// ST 预设的“有效部分”：按 prompt_order 执行 setvar/getvar/trim/注释后，只保留有正文的条目。
// 与“解析预览”不同：这里展示的是求值后的内容；仍然只读、不写 Agent。
import { computed, ref, shallowRef, watch } from "vue";
import { VBtn, VCard, VCardText, VCardTitle, VDialog, VProgressCircular } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { depthText } from "../utils/format.ts";
import { ICONS } from "./icons.ts";
import type { TavernEffective, TavernEffectiveEntry, TavernPreset } from "../api/types.ts";

const props = defineProps<{ preset: TavernPreset }>();
const emit = defineEmits<{ close: [] }>();
const api = sharedApi();
const open = ref(true);
const loading = ref(true);
const error = ref("");
const data = shallowRef<TavernEffective | null>(null);

watch(open, (value) => { if (!value) emit("close"); });
async function load(): Promise<void> {
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
function close(): void { open.value = false; }
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
watch(() => props.preset.id, load, { immediate: true });
</script>

<template>
  <VDialog v-model="open" attach="#app" class="tavern-preview-dialog" scrim="rgba(0, 0, 0, 0.45)">
    <VCard class="tavern-preview-card" elevation="3">
      <VCardTitle class="tavern-preview-head">
        <div class="tavern-preview-title">
          <span>提取有效内容</span>
          <span class="tavern-preview-file">{{ preset.filename }}</span>
        </div>
        <VBtn icon variant="text" size="small" title="关闭" aria-label="关闭" @click="close" v-html="ICONS.x" />
      </VCardTitle>

      <VCardText class="tavern-preview-body">
        <div v-if="loading" class="tavern-preview-state" role="status">
          <VProgressCircular indeterminate size="22" color="primary" />
          <span>求值中…</span>
        </div>
        <div v-else-if="error" class="tavern-preview-state error" role="alert">{{ error }}</div>
        <template v-else-if="data">
          <p class="tavern-preview-note">
            按 <code>prompt_order</code> 执行 <code>setvar/getvar/trim/注释</code> 后的结果；空条目已丢弃。
            <code v-text="'{{lastUserMessage}}'"></code> 没有给具体输入时保留原样。
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
          <article v-for="entry in data.entries" :key="entry.identifier" class="tavern-effective-entry">
            <div class="tavern-preview-entry-head">
              <b>{{ entry.name || entry.identifier }}</b>
              <span class="tavern-preview-tag">{{ entry.role }}</span>
              <span class="tavern-preview-meta">{{ meta(entry) }}</span>
            </div>
            <div class="tavern-preview-id">{{ entry.identifier }}</div>
            <pre class="tavern-effective-content">{{ entry.content }}</pre>
          </article>

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
      </VCardText>
    </VCard>
  </VDialog>
</template>
