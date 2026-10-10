<script setup>
// 摘要正文编辑器（应用内模态）：替掉 /editsum 与卡上 ✎ 的原生 window.prompt。
// 只改 text（PATCH /summaries/{id}：就地换正文，id / 区间 / 指针不动；空文本后端会 400，这里先拦）。
import { ref, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";

const props = defineProps({
  sessionId: { type: String, required: true },
  summary: { type: Object, required: true }, // {id, text, begin_idx?, end_idx?}
});
const emit = defineEmits(["close", "saved"]);
const api = sharedApi();
const text = ref(props.summary.text ?? "");
const status = ref("");
const busy = ref(false);
const range = props.summary.begin_idx && props.summary.end_idx
  ? ` #${props.summary.begin_idx}-${props.summary.end_idx}` : "";

async function save() {
  if (!text.value.trim()) { status.value = "不许空（空摘要没有意义）"; return; }
  busy.value = true;
  try {
    await api.editSummary(props.sessionId, props.summary.id, text.value);
    emit("saved");
  } catch (e) {
    status.value = `保存失败：${e.message}`;
  }
  busy.value = false;
}
function onKey(e) { if (e.key === "Escape") emit("close"); }
onMounted(() => document.addEventListener("keydown", onKey));
onUnmounted(() => document.removeEventListener("keydown", onKey));
</script>

<template>
  <div class="modal" @click.self="$emit('close')">
    <div class="modal-box compact">
      <div class="modal-head">
        <span>编辑摘要{{ range }}</span>
        <span class="head-btns">
          <button class="icon-btn" type="button" title="关闭（不保存）" @click="$emit('close')">×</button>
        </span>
      </div>
      <textarea v-model="text" class="sum-edit-field" rows="10" placeholder="摘要正文"></textarea>
      <div class="row">
        <button type="button" class="primary" :disabled="busy" @click="save">保存</button>
        <button type="button" @click="$emit('close')">取消</button>
        <span class="status">{{ status }}</span>
      </div>
    </div>
  </div>
</template>
