<script setup lang="ts">
// 酒馆原始预设的只读 JSON 阅读器。
//
// 后端只保存原始字节，这里也不重写它：下载 → 去 BOM → JSON.parse → 树形展示。
// 打开时只展开根，子节点点一层开一层（1.5MB 的 ST 预设不能一次性铺满 DOM）。
import { ref, shallowRef, watch } from "vue";
import { VBtn, VCard, VCardText, VCardTitle, VDialog, VProgressCircular } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { parseJsonText, type JsonValue } from "../utils/json.ts";
import { ICONS } from "./icons.ts";
import JsonNode from "./JsonNode.vue";
import type { TavernPreset } from "../api/types.ts";

const props = defineProps<{ preset: TavernPreset }>();
const emit = defineEmits<{ close: [] }>();
const api = sharedApi();

const open = ref(true);
const loading = ref(true);
const loaded = ref(false);
const error = ref("");
const tree = shallowRef<JsonValue>(null);
const treeKey = ref(0);

watch(open, (value) => { if (!value) emit("close"); });

async function load(): Promise<void> {
  loading.value = true;
  loaded.value = false;
  error.value = "";
  try {
    const blob = await api.downloadTavernPreset(props.preset.id);
    const text = await blob.text();
    // shallowRef：1.5MB 的嵌套对象不要被 Vue 深度代理；这里只读，不需要深层响应式。
    tree.value = parseJsonText(text);
    loaded.value = true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}

// 重新挂载根节点 = 全部收起；根自己仍保持展开，否则连顶层键都看不见。
function collapseAll(): void {
  treeKey.value += 1;
}
function close(): void {
  open.value = false;
}

watch(() => props.preset.id, load, { immediate: true });
</script>

<template>
  <VDialog
    v-model="open"
    attach="#app"
    class="json-viewer-dialog"
    scrim="rgba(0, 0, 0, 0.45)"
  >
    <VCard class="json-viewer-card" elevation="3">
      <VCardTitle class="json-viewer-head">
        <div class="json-viewer-title">
          <span>JSON 阅读器</span>
          <span class="json-viewer-file">{{ preset.filename }}</span>
        </div>
        <div class="head-btns">
          <VBtn v-if="loaded" variant="text" size="small" type="button" @click="collapseAll">全部收起</VBtn>
          <VBtn icon variant="text" size="small" title="关闭" aria-label="关闭" @click="close" v-html="ICONS.x" />
        </div>
      </VCardTitle>

      <VCardText class="json-viewer-body">
        <div v-if="loading" class="json-viewer-state" role="status">
          <VProgressCircular indeterminate size="22" color="primary" />
          <span>读取中…</span>
        </div>
        <div v-else-if="error" class="json-viewer-state error" role="alert">{{ error }}</div>
        <div v-else-if="loaded" class="json-tree">
          <JsonNode :key="treeKey" :value="tree" open-initially />
        </div>
      </VCardText>
    </VCard>
  </VDialog>
</template>
