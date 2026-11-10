<script setup lang="ts">
// SillyTavern 预设的**只读解析预览**：prompt_order 会变成什么，先摊开看，不写 Agent。
//
// 这一版不执行 setvar/getvar，也不把 relative 条目拼成提示词；只把条目、位置、跳过原因、
// 未知宏列出来。真正的 PromptEntry / Inject 运行时是下一步。
import { computed, ref, shallowRef, watch } from "vue";
import { VBtn, VCard, VCardText, VCardTitle, VDialog, VProgressCircular } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { depthText } from "../utils/format.ts";
import { ICONS } from "./icons.ts";
import type { TavernPreset, TavernPreview } from "../api/types.ts";

const props = defineProps<{ preset: TavernPreset }>();
const emit = defineEmits<{ close: [] }>();
const api = sharedApi();

const open = ref(true);
const loading = ref(true);
const error = ref("");
const preview = shallowRef<TavernPreview | null>(null);

watch(open, (value) => { if (!value) emit("close"); });

async function load(): Promise<void> {
  loading.value = true;
  error.value = "";
  preview.value = null;
  try {
    preview.value = await api.tavernPreview(props.preset.id);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}
function close(): void { open.value = false; }
function previewOf(text: string): string {
  const flat = String(text ?? "").replace(/\s+/g, " ").trim();
  return flat.length > 220 ? `${flat.slice(0, 220)}…` : flat;
}
const absoluteEntries = computed(() => (preview.value?.entries ?? []).filter((entry) => entry.position === "absolute"));
const relativeEntries = computed(() => (preview.value?.entries ?? []).filter((entry) => entry.position === "relative"));
const unknownMacros = computed(() => (preview.value?.macros ?? []).filter((item) =>
  !item.known && !item.name.startsWith("//") && item.name !== "trim"));
const knownMacros = computed(() => (preview.value?.macros ?? []).filter((item) => item.known));

watch(() => props.preset.id, load, { immediate: true });
</script>

<template>
  <VDialog v-model="open" attach="#app" class="tavern-preview-dialog" scrim="rgba(0, 0, 0, 0.45)">
    <VCard class="tavern-preview-card" elevation="3">
      <VCardTitle class="tavern-preview-head">
        <div class="tavern-preview-title">
          <span>解析预览</span>
          <span class="tavern-preview-file">{{ preset.filename }}</span>
        </div>
        <VBtn icon variant="text" size="small" title="关闭" aria-label="关闭" @click="close" v-html="ICONS.x" />
      </VCardTitle>

      <VCardText class="tavern-preview-body">
        <div v-if="loading" class="tavern-preview-state" role="status">
          <VProgressCircular indeterminate size="22" color="primary" />
          <span>解析中…</span>
        </div>
        <div v-else-if="error" class="tavern-preview-state error" role="alert">{{ error }}</div>
        <template v-else-if="preview">
          <p class="tavern-preview-note">
            只读预览：按 <code>prompt_order</code> 展开；不写 Agent、不执行 <code>setvar/getvar</code>。
          </p>

          <div class="tavern-preview-stats">
            <div><b>{{ preview.name }}</b><span>预设名</span></div>
            <div><b>{{ preview.prompt_count }}</b><span>prompts</span></div>
            <div><b>{{ preview.order_entries }}</b><span>order 条目</span></div>
            <div><b>{{ preview.enabled }}</b><span>启用</span></div>
            <div><b>{{ preview.with_content }}</b><span>有正文</span></div>
            <div><b>{{ preview.relative }} / {{ preview.absolute }}</b><span>relative / absolute</span></div>
          </div>

          <div v-if="preview.warnings?.length" class="tavern-preview-warnings">
            <div class="tavern-preview-section-title">提醒</div>
            <ul>
              <li v-for="(warning, index) in preview.warnings" :key="index">{{ warning }}</li>
            </ul>
          </div>

          <div class="tavern-preview-section-title">绝对注入 <span>{{ absoluteEntries.length }}</span></div>
          <p class="tavern-preview-depth-note">depth 从聊天历史末尾往前数：0 = 最后一条之后，1 = 最后一条之前，2 = 倒数第二条之前。</p>
          <p v-if="!absoluteEntries.length" class="tavern-preview-empty">没有 absolute 条目。</p>
          <article v-for="entry in absoluteEntries" :key="entry.identifier" class="tavern-preview-entry">
            <div class="tavern-preview-entry-head">
              <b>{{ entry.name || entry.identifier }}</b>
              <span class="tavern-preview-tag">{{ entry.role }}</span>
              <span class="tavern-preview-meta" :title="`depth ${entry.depth}：${depthText(entry.depth)}`">depth {{ entry.depth }} · {{ depthText(entry.depth) }} · order {{ entry.order }}</span>
            </div>
            <div class="tavern-preview-id">{{ entry.identifier }}<span v-if="entry.system_prompt"> · system_prompt</span></div>
            <p class="tavern-preview-content">{{ previewOf(entry.content) }}</p>
          </article>

          <div class="tavern-preview-section-title">相对条目 <span>{{ relativeEntries.length }}</span></div>
          <p v-if="!relativeEntries.length" class="tavern-preview-empty">没有 relative 条目。</p>
          <article v-for="entry in relativeEntries" :key="entry.identifier" class="tavern-preview-entry">
            <div class="tavern-preview-entry-head">
              <b>{{ entry.name || entry.identifier }}</b>
              <span class="tavern-preview-tag">{{ entry.role }}</span>
              <span v-if="entry.system_prompt" class="tavern-preview-meta">system_prompt</span>
            </div>
            <div class="tavern-preview-id">{{ entry.identifier }}</div>
            <p class="tavern-preview-content">{{ previewOf(entry.content) }}</p>
          </article>

          <div class="tavern-preview-section-title">未注册宏 <span>{{ unknownMacros.length }} 种</span></div>
          <p v-if="!unknownMacros.length" class="tavern-preview-empty">没有发现需要变量引擎的宏。</p>
          <div v-else class="tavern-preview-macros">
            <span v-for="item in unknownMacros" :key="item.name" class="tavern-preview-macro unknown">
              {{ item.name }} ×{{ item.count }}
            </span>
          </div>
          <p v-if="knownMacros.length" class="tavern-preview-note">
            已注册宏会在出站/显示时替换；未注册宏保持原文，后续由 ST 变量引擎处理。
          </p>

          <details v-if="preview.skipped.length" class="tavern-preview-skipped">
            <summary>跳过 {{ preview.skipped.length }} 条</summary>
            <div v-for="(item, index) in preview.skipped" :key="`${item.identifier}-${index}`" class="tavern-preview-skip">
              <b>{{ item.name || item.identifier || "（空 id）" }}</b>
              <span>{{ item.reason }}</span>
            </div>
          </details>
        </template>
      </VCardText>
    </VCard>
  </VDialog>
</template>
