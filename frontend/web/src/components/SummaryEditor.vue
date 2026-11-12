<script setup lang="ts">
// 摘要正文编辑器：只 PATCH text（就地换正文，id / 区间 / 指针不动；空文本后端会 400，这里先拦）。
import { ref } from "vue";
import { VBtn, VTextarea } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import ModalShell from "./ModalShell.vue";
import type { Summary } from "../api/types.ts";

type EditableSummary = Pick<Summary, "id" | "text" | "begin_idx" | "end_idx">;

const props = defineProps<{ sessionId: string; summary: EditableSummary }>();
const emit = defineEmits<{ close: []; saved: [] }>();
const api = sharedApi();
const text = ref(props.summary.text ?? "");
const status = ref("");
const busy = ref(false);
const range = props.summary.begin_idx && props.summary.end_idx
  ? ` #${props.summary.begin_idx}-${props.summary.end_idx}` : "";

async function save(): Promise<void> {
  if (!text.value.trim()) { status.value = "不许空（空摘要没有意义）"; return; }
  busy.value = true;
  try {
    await api.editSummary(props.sessionId, props.summary.id, text.value);
    emit("saved");
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}
</script>

<template>
  <ModalShell :title="`编辑摘要${range}`" @close="emit('close')">
    <VTextarea v-model="text" rows="10" variant="outlined" hide-details placeholder="摘要正文" />
    <template #actions>
      <span class="status">{{ status }}</span>
      <VBtn variant="text" size="small" @click="emit('close')">取消</VBtn>
      <VBtn color="primary" variant="flat" size="small" :disabled="busy" @click="save">保存</VBtn>
    </template>
  </ModalShell>
</template>
