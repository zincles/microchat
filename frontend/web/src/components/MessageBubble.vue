<script setup>
// 消息气泡：正文 markdown 渲染 + <state> 块独立容器（状态变化单列，不混进正文）。
import { ref, computed, watch, onMounted, nextTick } from "vue";
import { ICONS } from "./icons.js";
import { formatThinkLabel, usageFoot, usageFootTitle } from "../utils/format.js";
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
  streaming: Boolean, // 生成中（还没落库的那条）：等首字节时给三点呼吸动画
  reroll: { type: Object, default: null }, // 重摇状态（本条是目标时才非 null）：{active,count,current_idx,running}
  // usage：上游报的用量（归一化后的那几格；2026-10-20 起气泡脚注就用它）。
  // 只有聚合站（OpenRouter）会带 cost_usd；直连渠道没有 ⇒ 脚注不显示价格。
  usage: { type: Object, default: null },
});
defineEmits(["edit", "delete", "reroll", "reroll-go"]);

// 脚注：token 用量 + 成本（纯函数在 utils/format.js，契约测试钉着）。
// 没用量（生成中 / 库外那条 / 上游没报）⇒ 空串 ⇒ 整行不渲染。
const foot = computed(() => usageFoot(props.usage));
const footTitle = computed(() => usageFootTitle(props.usage));

// 流式期间还没正文 ⇒ 标签说"思考中…"；落库后才是"思考过程（2.1s）"（那会儿才有用时）。
const thinkLabel = computed(() =>
  props.streaming && !props.content ? "思考中…" : formatThinkLabel(props.reasoningMs),
);
const canEdit = computed(() => !!props.messageId);

// 正文拆说话/状态：说话走 markdown，状态块进独立容器。
const parts = computed(() => splitState(props.content));
const bodyHtml = computed(() => renderMarkdown(parts.value.cleaned));
// 公式比列宽时给右缘一道内阴影（2026-11-02）：手机上原本是"一刀切"，看不出还能往右滑。
// `overflow-x: auto` 之外再标一个类 —— 只有真的溢出才提示，短公式不受影响。
const textEl = ref(null);
function markMathOverflow() {
  const root = textEl.value;
  if (!root) return;
  for (const el of root.querySelectorAll(".katex-display")) {
    el.classList.toggle("scroll-x", el.scrollWidth > el.clientWidth + 1);
  }
}
onMounted(() => nextTick(markMathOverflow));
// 流式期间正文每来一段就重算一次 —— 那时不扫（每帧扫一遍 DOM 是白烧 CPU）；
// 收尾那一拍再扫。
watch(bodyHtml, () => { if (!props.streaming) nextTick(markMathOverflow); });
watch(() => props.streaming, (s) => { if (!s) nextTick(markMathOverflow); });
</script>

<template>
  <!-- 只有一种形态（2026-10-27 用户定）：**用户 = 气泡，AI = 正文**。
       从前这里按 `display_mode` 切两套 class（两边气泡 / 用户气泡+AI 纯文本）；现在后者是唯一的样子，
       所以不再有条件类 —— 形态本身写在 CSS 的 `.msg.assistant` / `.msg-wrap.assistant` 里。 -->
  <div :class="`msg-wrap ${role === 'user' ? 'user' : 'assistant'}`">
    <div :class="`msg ${role === 'user' ? 'user' : 'assistant'}`">
      <div class="msg-head">
        <span class="who">{{ who }}<span v-if="idx !== null && idx !== undefined" class="msg-idx">#{{ idx }}</span></span>
      </div>
      <details v-if="reasoning" class="think">
        <summary>{{ thinkLabel }}</summary>
        <div class="think-body">{{ reasoning }}</div>
      </details>
      <div ref="textEl" class="text" v-html="bodyHtml"></div>
      <div v-if="streaming && !content && !reasoning" class="typing" aria-label="正在生成">
        <span></span><span></span><span></span>
        <i class="typing-hint">正在输出…</i>
      </div>
      <div v-if="parts.blocks.length" class="state-blocks">
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
      <!-- 脚注（2026-10-20）：这一发实际用了多少 token、花了多少钱（上游报的）。
           只在**落库后的**消息上有（生成中那条没有用量）；直连渠道不报 cost ⇒ 这里只有 token。 -->
      <div v-if="foot" class="msg-foot" :title="footTitle">{{ foot }}</div>
    </div>
    <div v-if="canEdit" class="msg-ops">
      <button
        type="button"
        class="edit-btn hover-only"
        title="改正文"
        aria-label="改正文"
        @click="$emit('edit', messageId, 'content')"
        v-html="ICONS.pencil"
      ></button>
      <button
        v-if="role !== 'user'"
        type="button"
        class="edit-btn hover-only"
        title="改思考"
        aria-label="改思考"
        @click="$emit('edit', messageId, 'reasoning')"
        v-html="ICONS.brain"
      ></button>
      <button
        v-if="role !== 'user' && isLast"
        type="button"
        class="edit-btn"
        title="重摇（摇新的一版并切过去）"
        aria-label="重摇（摇新的一版并切过去）"
        @click="$emit('reroll', messageId)"
        v-html="ICONS.refresh"
      ></button>
      <template v-if="role !== 'user' && isLast && reroll && reroll.active">
        <button type="button" class="edit-btn" title="上一版" aria-label="上一版" @click="$emit('reroll-go', -1)">◀</button>
        <span class="reroll-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
        <button type="button" class="edit-btn" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="$emit('reroll-go', 1)">▶</button>
      </template>
      <button
        type="button"
        class="edit-btn danger hover-only"
        title="删除这条及之后"
        aria-label="删除这条及之后"
        @click="$emit('delete', messageId, isLast)"
        v-html="ICONS.trash"
      ></button>
    </div>
  </div>
</template>
