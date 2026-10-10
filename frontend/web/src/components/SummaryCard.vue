<script setup>
// 摘要卡片：消息列里"一段被压过的对话"的样子 —— 四面圆角矩形（不是气泡、没有尾巴）。
// 头行 `Summary #a-b`（+ `脏` 徽标）；正文 = **摘要文本**（发给模型的就是这份，折叠态就看它）；
// `查看未压缩文本` 展开被盖住的原始气泡 —— 带着各自的操作（编辑/删除照常可用，
// 不然被折住的消息在界面上就没有入口了）。
import MessageBubble from "./MessageBubble.vue";

const props = defineProps({
  summary: { type: Object, required: true }, // {id, text, dirty, tokens, …}
  fromIdx: { type: [Number, String], default: null },
  toIdx: { type: [Number, String], default: null },
  // rows：被盖住的原始消息 [{msg, displayIdx}] —— **null = 还没拉**（展开才拉，见 App.refreshSummaryMsgs；
  // 分页的意义就在这：折叠态一个字节的原文都不在浏览器里）。
  rows: { type: Array, default: null },
  loading: { type: Boolean, default: false },
  displayMode: { type: String, default: "chat" },
});
// 卡里的原文气泡**只读**：不给 messageId ⇒ MessageBubble 不画操作行（用户定：展开里不许编辑/修改）。
defineEmits(["toggle-raw"]);
</script>

<template>
  <div class="sum-block">
    <div class="sum-head">
      <span class="sum-name">Summary</span>
      <span class="sum-range">#{{ fromIdx }}-{{ toIdx }}</span>
      <span v-if="summary.dirty" class="sum-tag" title="所辖消息被编辑过：摘要可能过期（装配照用）">脏</span>
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
