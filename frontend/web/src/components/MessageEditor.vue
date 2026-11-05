<script setup lang="ts">
// 消息编辑器（应用内模态，2026-11-02 用户定）：编辑正文 / 思考时开新窗口，
// 不再把旧气泡就地换成 textarea（那会把原对话挤变形，也容易误以为改的就是气泡里那一版）。
//
// 只 PATCH 这一条消息的 content 或 reasoning；保存成功后由父组件重拉消息与状态。
import { ref, computed, nextTick, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";
import { ICONS } from "./icons.js";
import type { EditMessageRequest, Message } from "../api/types.ts";

interface EditableMessage {
  id: string;
  idx?: number | null;
  content?: string;
  reasoning?: string;
}

const props = withDefaults(defineProps<{
  sessionId: string;
  message: EditableMessage;
  kind?: "content" | "reasoning";
  rerollable?: boolean;
}>(), {
  kind: "content",
  rerollable: false,
});
const emit = defineEmits<{
  close: [];
  saved: [payload: { reroll: boolean; message?: Message }];
}>();

const api = sharedApi();
const field = ref<HTMLTextAreaElement | null>(null);
const original = props.kind === "reasoning" ? (props.message.reasoning ?? "") : (props.message.content ?? "");
const text = ref(original);
const status = ref("");
const busy = ref(false);
const expanded = ref(false);
const changed = computed(() => text.value !== original);
const label = computed(() => (props.kind === "reasoning" ? "思考" : "正文"));
const heading = computed(() => {
  const n = props.message.idx;
  return `编辑${label.value}${n != null ? ` · #${n}` : ""}`;
});

// 网页全屏：只是把编辑框铺满 #app（不是浏览器 Fullscreen API，不占用户整块屏幕）。
function toggleExpanded(): void { expanded.value = !expanded.value; }
function close(): void { emit("close"); }
async function save(reroll = false): Promise<void> {
  if (!changed.value) {
    if (reroll) emit("saved", { reroll: true });
    else close();
    return;
  }
  busy.value = true;
  status.value = "";
  try {
    const body: EditMessageRequest = props.kind === "reasoning" ? { reasoning: text.value } : { content: text.value };
    const updated = await api.editMessage(props.sessionId, props.message.id, body);
    emit("saved", { reroll, message: updated });
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
  busy.value = false;
}
function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape") close();
}
onMounted(() => {
  document.addEventListener("keydown", onKey);
  nextTick(() => field.value?.focus());
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
</script>

<template>
  <div class="modal" @click.self="close">
    <div class="modal-box message-edit-box" :class="{ expanded }">
      <div class="modal-head">
        <span>{{ heading }}</span>
        <span class="head-btns">
          <button class="icon-btn full-btn" type="button"
            :title="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-label="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-pressed="expanded ? 'true' : 'false'"
            @click="toggleExpanded" v-html="expanded ? ICONS.minimize : ICONS.maximize"></button>
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
