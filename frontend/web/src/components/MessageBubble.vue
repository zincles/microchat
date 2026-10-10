<script setup>
// 消息气泡：正文 markdown 渲染 + <state> 块独立容器（状态变化单列，不混进正文）。
import { ref, computed } from "vue";
import { ICONS } from "./icons.js";
import { formatThinkLabel } from "../utils/format.js";
import { renderMarkdown, splitState } from "../utils/markdown.js";
const props = defineProps({
  who: String,
  idx: { type: [Number, String], default: null }, // 显示编号（真消息=服务端 idx；库外的按后缀顺延）
  role: String,
  content: String,
  reasoning: String,
  reasoningMs: Number,
  messageId: String,
  isLast: Boolean,
  displayMode: { type: String, default: "chat" },
  streaming: Boolean, // 生成中（还没落库的那条）：等首字节时给三点呼吸动画
  reroll: { type: Object, default: null }, // 重摇状态（本条是目标时才非 null）：{active,count,current_idx,running}
});
const emit = defineEmits(["save", "delete", "reroll", "reroll-go"]);

// 流式期间还没正文 ⇒ 标签说"思考中…"；落库后才是"思考过程（2.1s）"（那会儿才有用时）。
const thinkLabel = computed(() =>
  props.streaming && !props.content ? "思考中…" : formatThinkLabel(props.reasoningMs),
);
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
        <span class="who">{{ who }}<span v-if="idx !== null && idx !== undefined" class="msg-idx">#{{ idx }}</span></span>
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
      <div v-if="streaming && !content && !reasoning" class="typing" aria-label="正在生成">
        <span></span><span></span><span></span>
      </div>
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
        title="重摇（摇新的一版并切过去）"
        aria-label="重摇（摇新的一版并切过去）"
        @click="$emit('reroll')"
        v-html="ICONS.refresh"
      ></button>
      <template v-if="role !== 'user' && isLast && reroll && reroll.active">
        <button type="button" class="edit-btn" title="上一版" aria-label="上一版" @click="$emit('reroll-go', -1)">◀</button>
        <span class="reroll-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
        <button type="button" class="edit-btn" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="$emit('reroll-go', 1)">▶</button>
      </template>
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
