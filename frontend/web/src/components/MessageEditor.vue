<script setup>
// 消息编辑器（应用内模态，2026-11-02 用户定）：编辑正文 / 思考时开新窗口，
// 不再把旧气泡就地换成 textarea（那会把原对话挤变形，也容易误以为改的就是气泡里那一版）。
//
// 只 PATCH 这一条消息的 content 或 reasoning；保存成功后由父组件重拉消息与状态。
import { ref, computed, nextTick, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";

const props = defineProps({
  sessionId: { type: String, required: true },
  message: { type: Object, required: true }, // {id, idx?, content?, reasoning?}
  kind: { type: String, default: "content" }, // content | reasoning
  rerollable: { type: Boolean, default: false }, // 是否最后一条助手回复（只有它能重摇）
});
const emit = defineEmits(["close", "saved"]);

const api = sharedApi();
const field = ref(null);
const original = props.kind === "reasoning" ? (props.message.reasoning ?? "") : (props.message.content ?? "");
const text = ref(original);
const status = ref("");
const busy = ref(false);
const isFullscreen = ref(false);
const changed = computed(() => text.value !== original);
const label = computed(() => (props.kind === "reasoning" ? "思考" : "正文"));
const heading = computed(() => {
  const n = props.message.idx;
  return `编辑${label.value}${n != null ? ` · #${n}` : ""}`;
});

async function leaveFullscreen() {
  if (document.fullscreenElement) {
    try { await document.exitFullscreen(); } catch { /* 浏览器拒绝退出时保持原样 */ }
  }
}
async function toggleFullscreen() {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else await document.documentElement.requestFullscreen();
  } catch (e) {
    status.value = `全屏失败：${e.message}`;
  }
}
function syncFullscreen() { isFullscreen.value = !!document.fullscreenElement; }
async function close() {
  await leaveFullscreen();
  emit("close");
}
async function save(reroll = false) {
  if (!changed.value) {
    if (reroll) {
      await leaveFullscreen();
      emit("saved", { reroll: true });
    } else {
      await close();
    }
    return;
  }
  busy.value = true;
  status.value = "";
  try {
    const body = props.kind === "reasoning" ? { reasoning: text.value } : { content: text.value };
    await api.editMessage(props.sessionId, props.message.id, body);
    await leaveFullscreen();
    emit("saved", { reroll });
  } catch (e) {
    status.value = `保存失败：${e.message}`;
  }
  busy.value = false;
}
function onKey(e) {
  if (e.key === "Escape") close();
}
onMounted(() => {
  document.addEventListener("keydown", onKey);
  document.addEventListener("fullscreenchange", syncFullscreen);
  nextTick(() => field.value?.focus());
});
onUnmounted(() => {
  document.removeEventListener("keydown", onKey);
  document.removeEventListener("fullscreenchange", syncFullscreen);
});
</script>

<template>
  <div class="modal" @click.self="close">
    <div class="modal-box message-edit-box">
      <div class="modal-head">
        <span>{{ heading }}</span>
        <span class="head-btns">
          <button class="icon-btn full-btn" type="button" :title="isFullscreen ? '退出全屏' : '全屏'"
            :aria-pressed="isFullscreen ? 'true' : 'false'" @click="toggleFullscreen">{{ isFullscreen ? "退出全屏" : "全屏" }}</button>
          <button class="icon-btn" type="button" title="关闭（不保存）" @click="close">×</button>
        </span>
      </div>
      <textarea
        ref="field"
        v-model="text"
        class="message-edit-field"
        rows="14"
        :placeholder="`${label}内容`"
      ></textarea>
      <div class="modal-foot">
        <span class="status">{{ status }}</span>
        <div class="modal-actions">
          <button type="button" @click="close">取消</button>
          <button type="button" class="primary" :disabled="busy" @click="save(false)">保存</button>
          <button v-if="rerollable" type="button" :disabled="busy" @click="save(true)">保存并重摇</button>
        </div>
      </div>
    </div>
  </div>
</template>
