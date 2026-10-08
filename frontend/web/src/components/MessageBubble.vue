<script setup>
// 消息气泡：正文 markdown 渲染 + <state> 块独立容器（状态变化单列，不混进正文）。
import { ref, computed } from "vue";
import { ICONS } from "./icons.js";
import { formatThinkLabel } from "../utils/format.js";
import { renderMarkdown, splitState } from "../utils/markdown.js";
const props = defineProps({
  who: String,
  role: String,
  content: String,
  reasoning: String,
  reasoningMs: Number,
  messageId: String,
  isLast: Boolean,
  displayMode: { type: String, default: "chat" },
});
const emit = defineEmits(["save", "delete", "refresh"]);

const thinkLabel = computed(() => formatThinkLabel(props.reasoningMs));
const canEdit = computed(() => !!props.messageId);

const editing = ref(null); // null | "content" | "reasoning"
const draft = ref("");

// 正文拆说话/状态：说话走 markdown，状态块进独立容器（只在非编辑态；编辑态看原文）。
const parts = computed(() => splitState(editing.value ? "" : props.content));
const bodyHtml = computed(() => renderMarkdown(parts.value.cleaned));
function openEditor(kind) {
  if (editing.value) return;
  editing.value = kind;
  draft.value = kind === "reasoning" ? (props.reasoning ?? "") : (props.content ?? "");
}

function cancel() {
  editing.value = null;
}

function save() {
  emit("save", props.messageId, editing.value, draft.value);
  editing.value = null;
}

const editRows = computed(() => Math.min(12, Math.max(3, draft.value.split("\n").length + 1)));
</script>

<template>
  <div :class="`msg-wrap ${role === 'user' ? 'user' : 'assistant'} ${displayMode === 'roleplay' && role !== 'user' ? 'prose' : ''}`">
    <div :class="`msg ${role === 'user' ? 'user' : 'assistant'} ${displayMode === 'roleplay' && role !== 'user' ? 'prose' : ''}`">
      <div class="msg-head">
        <span class="who">{{ who }}</span>
      </div>
      <details v-if="reasoning || editing === 'reasoning'" class="think" :open="editing === 'reasoning'">
        <summary>{{ thinkLabel }}</summary>
        <div v-show="editing !== 'reasoning'" class="think-body">{{ reasoning }}</div>
        <div v-if="editing === 'reasoning'" class="inline-edit">
          <textarea v-model="draft" class="inline-input" :rows="editRows"></textarea>
          <div class="inline-btns">
            <button type="button" class="edit-btn" @click="save">保存</button>
            <button type="button" class="edit-btn" @click="cancel">取消</button>
          </div>
        </div>
      </details>
      <div v-show="editing !== 'content'" class="text" v-html="bodyHtml"></div>
      <div v-if="editing === 'content'" class="inline-edit">
        <textarea v-model="draft" class="inline-input" :rows="editRows"></textarea>
        <div class="inline-btns">
          <button type="button" class="edit-btn" @click="save">保存</button>
          <button type="button" class="edit-btn" @click="cancel">取消</button>
        </div>
      </div>
      <div v-if="editing !== 'content' && parts.blocks.length" class="state-blocks">
        <div
          v-for="(b, i) in parts.blocks"
          :key="i"
          class="state-block"
          :class="{ current: b.current }"
        >
          <div class="state-table-name">{{ b.current ? `当前状态${b.table === "global" ? "" : ` · ${b.table}`}` : b.table }}</div>
          <div v-for="(ln, j) in b.lines" :key="j" class="state-line">{{ ln }}</div>
        </div>
      </div>
    </div>
    <div v-if="canEdit" class="msg-ops">
      <button
        type="button"
        class="edit-btn"
        title="改正文"
        aria-label="改正文"
        @click="openEditor('content')"
        v-html="ICONS.pencil"
      ></button>
      <button
        v-if="role !== 'user'"
        type="button"
        class="edit-btn"
        title="改思考"
        aria-label="改思考"
        @click="openEditor('reasoning')"
        v-html="ICONS.brain"
      ></button>
      <button
        v-if="role !== 'user' && isLast"
        type="button"
        class="edit-btn"
        title="刷新（重发历史，再跑一轮）"
        aria-label="刷新（重发历史，再跑一轮）"
        @click="$emit('refresh')"
        v-html="ICONS.refresh"
      ></button>
      <button
        type="button"
        class="edit-btn danger"
        title="删除这条及之后"
        aria-label="删除这条及之后"
        @click="$emit('delete', messageId, isLast)"
        v-html="ICONS.trash"
      ></button>
    </div>
  </div>
</template>
