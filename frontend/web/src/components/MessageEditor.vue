<script setup lang="ts">
// 消息编辑器（应用内模态，2026-11-02 用户定）：编辑正文 / 思考时开新窗口，
// 不再把旧气泡就地换成 textarea（那会把原对话挤变形，也容易误以为改的就是气泡里那一版）。
//
// 2026-11-09：改用 Vuetify `VDialog` —— 原来的 `.modal` z-index 只有 20，会被
// `VNavigationDrawer`（MD3 层级 1000+）压在下面；VDialog 的 overlay 层级与焦点陷阱
// 由 Vuetify 统一管，顺便不再手写 Esc / 遮罩逻辑。
//
// 只 PATCH 这一条消息的 content 或 reasoning；保存成功后由父组件重拉消息与状态。
import { ref, computed, nextTick, onMounted, watch } from "vue";
import { VBtn, VCard, VCardActions, VCardText, VCardTitle, VDialog, VTextarea } from "vuetify/components";
import { sharedApi } from "../utils/prefs.ts";
import { ICONS } from "./icons.ts";
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
const open = ref(true);
const field = ref<{ focus: () => void } | null>(null);
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

// 关闭统一走 VDialog 的 v-model：点 × / 遮罩 / Esc 都会让它变 false，再由这里发 close。
watch(open, (value) => { if (!value) emit("close"); });

// 网页全屏：只是把编辑框铺满 #app（不是浏览器 Fullscreen API，不占用户整块屏幕）。
function toggleExpanded(): void { expanded.value = !expanded.value; }
function close(): void { open.value = false; }
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
onMounted(() => {
  nextTick(() => field.value?.focus());
});
</script>

<template>
  <VDialog
    v-model="open"
    attach="#app"
    class="message-edit-dialog"
    :class="{ expanded }"
    scrim="rgba(0, 0, 0, 0.45)"
    :fullscreen="expanded"
  >
    <VCard class="message-edit-card" elevation="3">
      <VCardTitle class="message-edit-title">
        <span>{{ heading }}</span>
        <span class="head-btns">
          <VBtn icon variant="text" size="small" class="full-btn"
            :title="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-label="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-pressed="expanded ? 'true' : 'false'"
            @click="toggleExpanded" v-html="expanded ? ICONS.minimize : ICONS.maximize" />
          <VBtn icon variant="text" size="small" title="关闭（不保存）" aria-label="关闭"
            @click="close" v-html="ICONS.x" />
        </span>
      </VCardTitle>
      <VCardText class="message-edit-body">
        <VTextarea
          ref="field"
          v-model="text"
          class="message-edit-field"
          rows="14"
          variant="outlined"
          hide-details
          :placeholder="`${label}内容`"
        />
      </VCardText>
      <VCardActions class="message-edit-foot">
        <span class="status">{{ status }}</span>
        <VBtn variant="text" size="small" @click="close">取消</VBtn>
        <VBtn color="primary" variant="flat" size="small" :disabled="busy" @click="save(false)">保存</VBtn>
        <VBtn v-if="rerollable" variant="tonal" size="small" :disabled="busy" @click="save(true)">保存并重摇</VBtn>
      </VCardActions>
    </VCard>
  </VDialog>
</template>
