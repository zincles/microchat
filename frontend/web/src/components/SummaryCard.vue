<script setup>
// 摘要卡片：消息列里"一段被压过的对话"的样子 —— 四面圆角矩形（不是气泡、没有尾巴）。
// 头行 `Summary #a-b`（+ `脏` 徽标）；正文 = **摘要文本**（发给模型的就是这份，折叠态就看它）；
// `查看未压缩文本` 展开被盖住的原始气泡 —— 带着各自的操作（编辑/删除照常可用，
// 不然被折住的消息在界面上就没有入口了）。
import MessageBubble from "./MessageBubble.vue";
import { ICONS } from "./icons.js";

const props = defineProps({
  summary: { type: Object, required: true }, // {id, text, dirty, tokens, …}
  fromIdx: { type: [Number, String], default: null },
  toIdx: { type: [Number, String], default: null },
  // rows：被盖住的原始消息 [{msg, displayIdx}] —— **null = 还没拉**（展开才拉，见 App.refreshSummaryMsgs；
  // 分页的意义就在这：折叠态一个字节的原文都不在浏览器里）。
  rows: { type: Array, default: null },
  loading: { type: Boolean, default: false },
  displayMode: { type: String, default: "chat" },
  // 摘要重摇状态（本条被摇时才非 null，与气泡的 reroll 同一套）：{count, current_idx, running}
  reroll: { type: Object, default: null },
});
// 卡里的原文气泡**只读**：不给 messageId ⇒ MessageBubble 不画操作行（用户定：展开里不许编辑/修改）。
// refresh = 摇新一版（当前那版留在第 1 位，不丢）；edit = 应用内编辑器；version = ◀▶ 翻版（最右再按 = 再摇）。
defineEmits(["toggle-raw", "refresh", "edit", "version"]);
</script>

<template>
  <div class="sum-block">
    <div class="sum-head">
      <span class="sum-name">Summary</span>
      <span class="sum-range">#{{ fromIdx }}-{{ toIdx }}</span>
      <span v-if="summary.dirty" class="sum-tag" title="所辖消息被编辑过：摘要可能过期（装配照用）">脏</span>
      <span class="sum-ops">
        <template v-if="reroll">
          <button type="button" class="edit-btn" title="上一版" aria-label="上一版" @click="$emit('version', -1)">◀</button>
          <span class="sum-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
          <button type="button" class="edit-btn" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="$emit('version', 1)">▶</button>
        </template>
        <button
          type="button"
          class="edit-btn"
          :disabled="!!reroll && reroll.running"
          title="刷新（摇新一版；当前这版留在位次里，不丢）"
          aria-label="刷新摘要"
          @click="$emit('refresh')"
          v-html="ICONS.refresh"
        ></button>
        <button
          type="button"
          class="edit-btn"
          :disabled="!!reroll && reroll.running"
          title="编辑摘要正文"
          aria-label="编辑摘要"
          @click="$emit('edit')"
          v-html="ICONS.pencil"
        ></button>
        <span v-if="reroll && reroll.running" class="sum-busy">刷新中…</span>
      </span>
    </div>
    <div class="sum-text">{{ summary.text }}</div>
    <details class="sum-raw" @toggle="(e) => $emit('toggle-raw', e.target.open)">
      <summary>查看未压缩文本</summary>
      <div v-if="loading" class="sum-loading">拉取未压缩文本…</div>
      <div v-else-if="!rows" class="sum-loading">展开时按需拉取（省内存）</div>
      <div v-else class="sum-originals">
        <MessageBubble
          v-for="r in rows"
          :key="r.msg.id"
          :who="r.msg.who"
          :idx="r.displayIdx"
          :role="r.msg.role"
          :content="r.msg.content"
          :reasoning="r.msg.reasoning"
          :reasoning-ms="r.msg.reasoningMs"
          :display-mode="displayMode"
        />
      </div>
    </details>
  </div>
</template>
