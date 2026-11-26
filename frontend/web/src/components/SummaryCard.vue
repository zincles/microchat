<script setup lang="ts">
// 摘要卡片：消息列里"一段被压过的对话"的样子 —— 四面圆角矩形（不是气泡、没有尾巴）。
// 头行 `Summary #a-b`（+ `脏` 徽标）；正文 = **摘要文本**（发给模型的就是这份，折叠态就看它）；
// `查看未压缩文本` 展开被盖住的原始气泡 —— 带着各自的操作（编辑/删除照常可用，
// 不然被折住的消息在界面上就没有入口了）。
import { computed, useTemplateRef, watch } from "vue";
import { VBtn } from "vuetify/components";
import ActionOverflow, { type OverflowAction } from "./ActionOverflow.vue";
import MessageBubble from "./MessageBubble.vue";
import RequestLoading from "./RequestLoading.vue";
import { ICONS } from "../assets/icons.ts";
import { renderMacroHtml, type MacroMap } from "../utils/macros.ts";
import { animateCandidate } from "../utils/candidateMotion.ts";
import type { ChatRow, ChatSummary } from "../utils/chatrows.ts";
import type { RerollView } from "../types/app.ts";

type SummaryLike = ChatSummary & {
  text?: string;
  dirty?: boolean;
  tokens?: number;
  undoable?: boolean;
};

const props = withDefaults(defineProps<{
  compactActions?: boolean;
  summary: SummaryLike;
  fromIdx?: number | string | null;
  toIdx?: number | string | null;
  rows?: ChatRow[] | null;
  loading?: boolean;
  rawError?: string;
  candidateLoading?: "refresh" | "previous" | "next" | null;
  undoLoading?: boolean;
  reroll?: RerollView | null;
  macros?: MacroMap;
}>(), {
  fromIdx: null,
  toIdx: null,
  rows: null,
  loading: false,
  rawError: "",
  candidateLoading: null,
  undoLoading: false,
  reroll: null,
  macros: () => ({}),
});
// 卡里的原文气泡**只读**：不给 messageId ⇒ MessageBubble 不画操作行（用户定：展开里不许编辑/修改）。
// refresh = 摇新一版（当前那版留在第 1 位，不丢）；edit = 应用内编辑器；version = ◀▶ 翻版（最右再按 = 再摇）。
const summaryHtml = computed(() => renderMacroHtml(props.summary.text ?? "", props.macros));
const versionBusy = computed(() => !!props.candidateLoading || !!props.reroll?.running);
const compactReroll = computed(() => props.compactActions && props.reroll?.active);
const body = useTemplateRef<HTMLElement>("body");
watch(() => props.reroll?.current_idx, (next, previous) => animateCandidate(body.value, next, previous), { flush: "post" });
const emit = defineEmits<{
  "toggle-raw": [open: boolean];
  refresh: [];
  edit: [];
  version: [delta: number];
  undo: [];
}>();
const summaryActions = computed<OverflowAction[]>(() => [
  { icon: ICONS.pencil, label: "编辑摘要", disabled: props.reroll?.running, run: () => emit("edit") },
  {
    icon: ICONS.trash,
    label: "撤销这次压缩",
    hint: props.summary.undoable ? "同层更晚的摘要会一并撤销" : "被上层摘要盖住：先撤上面那层",
    disabled: !props.summary.undoable || versionBusy.value,
    loading: props.undoLoading,
    run: () => emit("undo"),
  },
]);
</script>

<template>
  <div class="sum-block">
    <div class="sum-head">
      <span class="sum-name">Summary</span>
      <span class="sum-range">#{{ fromIdx }}-{{ toIdx }}</span>
      <span v-if="summary.dirty" class="sum-tag" title="所辖消息被编辑过：摘要可能过期（装配照用）">脏</span>
      <span class="sum-ops" :class="{ 'reroll-compact': compactReroll }">
        <template v-if="reroll">
          <VBtn icon variant="text" size="small" :loading="candidateLoading === 'previous'" :disabled="versionBusy" title="上一版" aria-label="上一版" @click="emit('version', -1)">◀</VBtn>
          <span class="sum-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
          <VBtn icon variant="text" size="small" :loading="candidateLoading === 'next'" :disabled="versionBusy" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="emit('version', 1)">▶</VBtn>
        </template>
        <VBtn icon variant="text" size="small" :loading="candidateLoading === 'refresh' || (!candidateLoading && !!reroll?.running)" :disabled="versionBusy" title="刷新（摇新一版；当前这版留在位次里，不丢）" aria-label="刷新摘要" @click="emit('refresh')">
          <span aria-hidden="true" v-html="ICONS.refresh" />
        </VBtn>
        <ActionOverflow :menu-label="compactReroll ? '更多摘要操作' : undefined" :actions="summaryActions" />
        <span v-if="reroll?.running" class="sum-busy" :class="{ 'd-sr-only': compactReroll }" role="status">刷新中…</span>
      </span>
    </div>
    <div ref="body" class="sum-text" v-html="summaryHtml"></div>
    <details class="sum-raw" @toggle="(e) => emit('toggle-raw', (e.target as HTMLDetailsElement).open)">
      <summary>查看未压缩文本</summary>
      <RequestLoading v-if="loading" class="sum-loading" label="正在加载未压缩文本…" />
      <p v-else-if="rawError" class="sum-loading" role="alert">{{ rawError }}</p>
      <div v-else-if="!rows" class="sum-loading">展开时按需拉取（省内存）</div>
      <div v-else class="sum-originals">
        <template v-for="r in rows" :key="r.key">
          <MessageBubble
            v-if="r.kind === 'msg'"
            :who="r.msg.who"
            :idx="r.displayIdx ?? undefined"
            :role="r.msg.role"
            :content="r.msg.content"
            :reasoning="r.msg.reasoning"
            :reasoning-ms="r.msg.reasoningMs ?? undefined"
            :tools="r.msg.tools"
            :macros="macros"
          />
        </template>
      </div>
    </details>
  </div>
</template>
