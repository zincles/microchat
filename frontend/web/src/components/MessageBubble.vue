<script setup>
// 消息气泡（TG 左右气泡）：user 右蓝 / assistant 左灰；头行署名；思考折叠；正文干净。
// 操作行在气泡下方（.msg-ops，deepseek/grok 味）：改正文 ✎ / 改思考 🧠 / 删除 🗑。
// 点改 ⇒ 行内 textarea + 保存/取消（保存/删除 emit 出去，PATCH/DELETE 由父做）。
import { ref, computed } from "vue";
import { ICONS } from "./icons.js";
import { formatThinkLabel } from "../utils/format.js";

const props = defineProps({
  who: String,
  role: String,
  content: String,
  reasoning: String,
  reasoningMs: Number,
  messageId: String,
  isLast: Boolean,
});
const emit = defineEmits(["save", "delete"]);

const thinkLabel = computed(() => formatThinkLabel(props.reasoningMs));
const canEdit = computed(() => !!props.messageId);

const editing = ref(null); // null | "content" | "reasoning"
const draft = ref("");

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
  <div :class="`msg-wrap ${role === 'user' ? 'user' : 'assistant'}`">
    <div :class="`msg ${role === 'user' ? 'user' : 'assistant'}`">
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
      <div v-show="editing !== 'content'" class="text">{{ content }}</div>
      <div v-if="editing === 'content'" class="inline-edit">
        <textarea v-model="draft" class="inline-input" :rows="editRows"></textarea>
        <div class="inline-btns">
          <button type="button" class="edit-btn" @click="save">保存</button>
          <button type="button" class="edit-btn" @click="cancel">取消</button>
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
