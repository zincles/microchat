<script setup lang="ts">
// 摘要卡片：消息列里"一段被压过的对话"的样子 —— 四面圆角矩形（不是气泡、没有尾巴）。
// 头行 `Summary #a-b`（+ `脏` 徽标）；正文 = **摘要文本**（发给模型的就是这份，折叠态就看它）；
// `查看未压缩文本` 展开被盖住的原始气泡 —— 带着各自的操作（编辑/删除照常可用，
// 不然被折住的消息在界面上就没有入口了）。
import MessageBubble from "./MessageBubble.vue";
import { ICONS } from "./icons.ts";

interface SummaryLike {
  id: string;
  text: string;
  dirty?: boolean;
  tokens?: number;
  undoable?: boolean;
}
interface SummaryRowMsg {
  id?: string;
  who?: string;
  idx?: number | null;
  role?: string;
  content?: string;
  reasoning?: string;
  reasoningMs?: number | null;
}
interface SummaryRow {
  msg: SummaryRowMsg;
  displayIdx: number | null;
}
interface SummaryReroll {
  count: number;
  current_idx: number;
  running?: boolean;
}

const props = withDefaults(defineProps<{
  summary: SummaryLike;
  fromIdx?: number | string | null;
  toIdx?: number | string | null;
  rows?: SummaryRow[] | null;
  loading?: boolean;
  reroll?: SummaryReroll | null;
}>(), {
  fromIdx: null,
  toIdx: null,
  rows: null,
  loading: false,
  reroll: null,
});
// 卡里的原文气泡**只读**：不给 messageId ⇒ MessageBubble 不画操作行（用户定：展开里不许编辑/修改）。
// refresh = 摇新一版（当前那版留在第 1 位，不丢）；edit = 应用内编辑器；version = ◀▶ 翻版（最右再按 = 再摇）。
const emit = defineEmits<{
  "toggle-raw": [open: boolean];
  refresh: [];
  edit: [];
  version: [delta: number];
  undo: [];
}>();
</script>

<template>
  <div class="sum-block">
    <div class="sum-head">
      <span class="sum-name">Summary</span>
      <span class="sum-range">#{{ fromIdx }}-{{ toIdx }}</span>
      <span v-if="summary.dirty" class="sum-tag" title="所辖消息被编辑过：摘要可能过期（装配照用）">脏</span>
      <span class="sum-ops">
        <template v-if="reroll">
          <button type="button" class="edit-btn" title="上一版" aria-label="上一版" @click="emit('version', -1)">◀</button>
          <span class="sum-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
          <button type="button" class="edit-btn" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="emit('version', 1)">▶</button>
        </template>
        <button
          type="button"
          class="edit-btn"
          :disabled="!!reroll && reroll.running"
          title="刷新（摇新一版；当前这版留在位次里，不丢）"
          aria-label="刷新摘要"
          @click="emit('refresh')"
          v-html="ICONS.refresh"
        ></button>
        <button
          type="button"
          class="edit-btn"
          :disabled="!!reroll && reroll.running"
          title="编辑摘要正文"
          aria-label="编辑摘要"
          @click="emit('edit')"
          v-html="ICONS.pencil"
        ></button>
        <!-- 撤销这次压缩（2026-11-01 用户定）：垃圾桶图标，**悬停说'撤销'**。
             顶层都可点；更早的会级联撤销同层更晚的摘要（App 会先弹警告）。
             不能撤的**留着但禁用** —— 让人看见"这儿本来能撤"，配上说明才不是死按钮。 -->
        <button
          type="button"
          class="edit-btn"
          :disabled="!summary.undoable || (!!reroll && reroll.running)"
          :title="summary.undoable ? '撤销这次压缩（同层更晚的会一并撤销）' : '被上层摘要盖住：先撤上面那层'"
          aria-label="撤销这次压缩"
          @click="emit('undo')"
          v-html="ICONS.trash"
        ></button>
        <span v-if="reroll && reroll.running" class="sum-busy">刷新中…</span>
      </span>
    </div>
    <div class="sum-text">{{ summary.text }}</div>
    <details class="sum-raw" @toggle="(e) => emit('toggle-raw', (e.target as HTMLDetailsElement).open)">
      <summary>查看未压缩文本</summary>
      <div v-if="loading" class="sum-loading">拉取未压缩文本…</div>
      <div v-else-if="!rows" class="sum-loading">展开时按需拉取（省内存）</div>
      <div v-else class="sum-originals">
        <MessageBubble
          v-for="r in rows"
          :key="r.msg.id"
          :who="r.msg.who"
          :idx="r.displayIdx ?? undefined"
          :role="r.msg.role"
          :content="r.msg.content"
          :reasoning="r.msg.reasoning"
          :reasoning-ms="r.msg.reasoningMs ?? undefined"
        />
      </div>
    </details>
  </div>
</template>
