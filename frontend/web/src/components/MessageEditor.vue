<script setup lang="ts">
// 消息编辑器（应用内模态，2026-11-02 用户定）：编辑正文 / 思考时开新窗口，
// 不再把旧气泡就地换成 textarea（那会把原对话挤变形，也容易误以为改的就是气泡里那一版）。
//
// 2026-11-09：改用 Vuetify `VDialog` —— 原来的 `.modal` z-index 只有 20，会被
// `VNavigationDrawer`（MD3 层级 1000+）压在下面；VDialog 的 overlay 层级与焦点陷阱
// 由 Vuetify 统一管，顺便不再手写 Esc / 遮罩逻辑。
//
// 切换编辑字段时保留两边的草稿，保存只 PATCH 有改动的字段。
import { ref, reactive, computed, nextTick, onMounted, watch } from "vue";
import { VBtn, VBtnToggle, VCard, VCardActions, VCardText, VCardTitle, VDialog, VTextarea } from "vuetify/components";
import CloseButton from "./CloseButton.vue";
import { messageEditorPrefs, saveMessageEditorPrefs, sharedApi } from "../utils/prefs.ts";
import { ICONS } from "../assets/icons.ts";
import type { EditMessageRequest, Message } from "../api/types.ts";

interface EditableMessage {
  id: string;
  idx?: number | null;
  role?: string;
  content?: string;
  reasoning?: string;
}

const props = withDefaults(defineProps<{
  sessionId: string;
  message: EditableMessage;
  startWithReasoning?: boolean;
  rerollable?: boolean;
  flow?: boolean;
}>(), {
  rerollable: false,
  flow: false,
});
const emit = defineEmits<{
  close: [];
  saved: [payload: { reroll: boolean; message?: Message }];
}>();

const api = sharedApi();
const open = ref(true);
const field = ref<{ focus: () => void } | null>(null);
const canEditReasoning = props.message.role === "assistant";
const prefs = messageEditorPrefs();
const part = ref<"content" | "reasoning">(canEditReasoning && (props.startWithReasoning || prefs.reasoning) ? "reasoning" : "content");
const original = { content: props.message.content ?? "", reasoning: props.message.reasoning ?? "" };
const draft = reactive({ ...original });
const status = ref("");
const busy = ref<"save" | "reroll" | null>(null);
const expanded = ref(prefs.fullscreen);
const label = computed(() => part.value === "reasoning" ? "思考" : props.message.role === "user" ? "Prompt" : "正文");
const heading = props.message.role === "user" ? "编辑 Prompt" : "编辑消息";

function selectPart(value: unknown): void {
  if ((value !== "content" && value !== "reasoning") || value === part.value) return;
  part.value = value;
  saveMessageEditorPrefs({ reasoning: value === "reasoning" });
}

// 关闭统一走 VDialog 的 v-model：点 × / 遮罩 / Esc 都会让它变 false，再由这里发 close。
watch(open, (value) => { if (!value) emit("close"); });

// 网页全屏：只是把编辑框铺满 #app（不是浏览器 Fullscreen API，不占用户整块屏幕）。
function toggleExpanded(): void {
  expanded.value = !expanded.value;
  saveMessageEditorPrefs({ fullscreen: expanded.value });
}
function close(): void { open.value = false; }
async function save(reroll = false): Promise<void> {
  if (busy.value) return;
  const body: EditMessageRequest = {};
  if (draft.content !== original.content) body.content = draft.content;
  if (canEditReasoning && draft.reasoning !== original.reasoning) body.reasoning = draft.reasoning;
  if (body.content === undefined && body.reasoning === undefined) {
    if (reroll) emit("saved", { reroll: true });
    else close();
    return;
  }
  busy.value = reroll ? "reroll" : "save";
  status.value = "";
  try {
    const updated = await api.editMessage(props.sessionId, props.message.id, body);
    emit("saved", { reroll, message: updated });
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    busy.value = null;
  }
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
    aria-labelledby="message-edit-heading"
    :class="{ expanded }"
    scrim="rgba(0, 0, 0, 0.45)"
    :fullscreen="expanded"
  >
    <VCard class="message-edit-card" elevation="3">
      <VCardTitle class="message-edit-title">
        <div class="message-edit-heading">
          <h2 id="message-edit-heading">{{ heading }}</h2>
          <span v-if="props.message.idx != null">#{{ props.message.idx }}</span>
        </div>
        <span class="head-btns">
          <VBtn icon variant="text" size="small" class="full-btn"
            :title="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-label="expanded ? '退出网页全屏' : '网页全屏'"
            :aria-pressed="expanded ? 'true' : 'false'"
            @click="toggleExpanded" v-html="expanded ? ICONS.minimize : ICONS.maximize" />
          <CloseButton title="关闭（不保存）" @click="close" />
        </span>
      </VCardTitle>
      <div v-if="canEditReasoning" class="message-edit-mode">
        <VBtnToggle class="ui-mode-switch" :model-value="part" mandatory variant="text"
          aria-label="编辑内容" @update:model-value="selectPart">
          <VBtn value="content" :aria-pressed="part === 'content'">正文</VBtn>
          <VBtn value="reasoning" :aria-pressed="part === 'reasoning'">思考</VBtn>
        </VBtnToggle>
      </div>
      <VCardText class="message-edit-body">
        <VTextarea
          ref="field"
          v-model="draft[part]"
          class="message-edit-field"
          rows="14"
          variant="plain"
          hide-details
          :aria-label="label"
          :placeholder="`${label}内容`"
        />
      </VCardText>
      <VCardActions class="message-edit-foot">
        <span class="status" role="status">{{ status }}</span>
        <VBtn variant="text" size="small" @click="close">取消</VBtn>
        <VBtn color="primary" variant="flat" size="small" :disabled="!!busy" :loading="busy === 'save'" @click="save(false)">保存</VBtn>
        <VBtn v-if="rerollable" variant="tonal" size="small" :disabled="!!busy" :loading="busy === 'reroll'" @click="save(true)">{{ flow ? "保存并重新生成" : "保存并重摇" }}</VBtn>
      </VCardActions>
    </VCard>
  </VDialog>
</template>
