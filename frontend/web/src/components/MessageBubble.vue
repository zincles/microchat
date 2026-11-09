<script setup lang="ts">
// 消息气泡：正文 markdown 渲染 + <state> 块独立容器（状态变化单列，不混进正文）。
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from "vue";
import { VBtn } from "vuetify/components";
import { ICONS } from "./icons.ts";
import { assistantIdentity, formatThinkLabel, identityLabel, nativeToolLabel, sourceLabel, toolRunHint, usageFoot, usageFootTitle } from "../utils/format.ts";
import type { UsageLike } from "../utils/format.ts";
import { renderMarkdown, splitState } from "../utils/markdown.ts";
import { renderMermaidIn } from "../utils/mermaid.ts";

interface RerollLike {
  active?: boolean;
  count: number;
  current_idx: number;
  running?: boolean;
}

const props = withDefaults(defineProps<{
  who?: string;
  idx?: number | string | null; // 显示编号（真消息=服务端 idx；库外的按后缀顺延）
  role?: string;
  kind?: "chat" | "tool_run" | string | null;
  content?: string;
  reasoning?: string;
  reasoningMs?: number | null;
  durationMs?: number | null; // 这一发的总耗时（受理 → 收尾），与 reasoningMs 一起算速度
  messageId?: string | null;
  promptId?: string | null; // 最新 AI 回复对应的用户输入（仅尾条 assistant 有）
  isLast?: boolean;
  streaming?: boolean; // 生成中（还没落库的那条）：等首字节时给三点呼吸动画
  // phase：sending = 正在向后端发送请求；waiting = 202 已受理、等 Provider 首字节。
  phase?: "sending" | "waiting" | string | null;
  // tool：当前服务端工具（xAI web_search / x_search / code_execution）；只服务生成中的提示。
  tool?: { kind: string; state: string } | null;
  sources?: { type?: string; url: string; title?: string }[];
  tools?: {
    id?: string;
    source?: string;
    tool_name?: string;
    queries?: string[];
    sources?: { type?: string; url: string; title?: string }[];
    status?: string;
  }[];
  reroll?: RerollLike | null; // 重摇状态（本条是目标时才非 null）：{active,count,current_idx,running}
  // usage：上游报的用量（归一化后的那几格；2026-10-20 起气泡脚注就用它）。
  // 只有聚合站（OpenRouter）会带 cost_usd；直连渠道没有 ⇒ 脚注不显示价格。
  usage?: UsageLike | null;
}>(), {
  who: "",
  idx: null,
  role: "",
  kind: "chat",
  content: "",
  reasoning: "",
  reasoningMs: null,
  durationMs: null,
  messageId: null,
  promptId: null,
  isLast: false,
  streaming: false,
  phase: null,
  tool: null,
  sources: () => [],
  tools: () => [],
  reroll: null,
  usage: null,
});
const emit = defineEmits<{
  edit: [messageId: string | null | undefined, kind: "content" | "reasoning"];
  "edit-prompt": [promptId: string];
  delete: [messageId: string | null | undefined, isLast: boolean];
  reroll: [messageId: string | null | undefined];
  "reroll-go": [delta: number];
}>();

// 脚注 = 身份 + 用量 + 首字延迟 + 速度（纯函数在 utils/format.ts，契约测试钉着）。
// 各段有才写；什么都没有（生成中 / 库外那条）⇒ 空串 ⇒ 整行不渲染。
//
// 生成中（含重摇候选流）只留编号：那一发还没落库，行上挂的 `usage` / 时长是**上一版**的，
// 拿它算延迟/速度等于给新候选配旧账 —— 宁可空着。落库重拉之后这些数才属于当前这一版。
const footMeta = computed(() => ({
  idx: props.idx,
  reasoningMs: props.reasoningMs,
  durationMs: props.durationMs,
}));
const footUsage = computed(() => (props.streaming ? null : props.usage));
const footTiming = computed(() => (props.streaming ? { idx: props.idx } : footMeta.value));
const foot = computed(() => usageFoot(footUsage.value, footTiming.value));
const footTitle = computed(() => usageFootTitle(footUsage.value, footTiming.value));
// 用户身份（`你 #3`）：气泡**外面**那一行，CSS 里只在 hover 时显形。
const ident = computed(() => identityLabel(props.who, props.idx));
const assistantIdent = computed(() => assistantIdentity(props.who, props.kind, props.idx));

// 流式期间还没正文 ⇒ 标签说"思考中…"；落库后才是"思考过程（2.1s）"（那会儿才有用时）。
const typingHint = computed(() =>
  props.phase === "sending" ? "正在向后端发送请求…" : "等待 Provider 响应…",
);
const toolHint = computed(() => toolRunHint(props.tool));
const thinkLabel = computed(() =>
  props.streaming && !props.content ? "思考中…" : formatThinkLabel(props.reasoningMs),
);
const canEdit = computed(() => !!props.messageId);

// 正文拆说话/状态：说话走 markdown，状态块进独立容器。
const parts = computed(() => splitState(props.content));
const bodyHtml = computed(() => renderMarkdown(parts.value.cleaned));
// 公式比列宽时给右缘一道内阴影（2026-11-02）：手机上原本是"一刀切"，看不出还能往右滑。
// `overflow-x: auto` 之外再标一个类 —— 只有真的溢出才提示，短公式不受影响。
const textEl = ref<HTMLElement | null>(null);
const msgEl = ref<HTMLElement | null>(null);
let cursorEl: HTMLSpanElement | null = null;
function markMathOverflow(): void {
  const root = textEl.value;
  if (!root) return;
  for (const el of root.querySelectorAll<HTMLElement>(".katex-display")) {
    el.classList.toggle("scroll-x", el.scrollWidth > el.clientWidth + 1);
  }
}
// Mermaid：流式期间不画（代码块每帧都在长，画一次废一次）；收流/挂载后再交给延迟加载的 mermaid。
function paintMermaid(): void {
  if (props.streaming || !textEl.value) return;
  void renderMermaidIn(textEl.value);
}
// 流式期间的终端光标：v-html 每帧会重写正文，所以每帧在**最后一个有字的文本节点**后面
// 插一个光标（不塞进 Markdown 源，免得落进代码块/公式被转义）。它只是装饰，aria-hidden。
function clearStreamCursor(): void {
  cursorEl?.remove();
  cursorEl = null;
}
function placeStreamCursor(): void {
  const box = msgEl.value;
  if (!props.streaming || !textEl.value || !box) { clearStreamCursor(); return; }
  const walker = document.createTreeWalker(textEl.value, NodeFilter.SHOW_TEXT, {
    acceptNode(node: Node): number {
      return node.nodeValue && node.nodeValue.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
    },
  });
  let last: Node | null = null;
  while (walker.nextNode()) last = walker.currentNode;
  if (!last) { clearStreamCursor(); return; }
  // 光标挂在 `.msg`（不在 v-html 里）⇒ Markdown 每帧重写正文也不会把动画重建成"常亮"；
  // 这里只把它的绝对坐标移到最后一个文字节点的末尾。
  if (!cursorEl || !cursorEl.isConnected) {
    cursorEl = document.createElement("span");
    cursorEl.className = "stream-cursor";
    cursorEl.setAttribute("aria-hidden", "true");
    box.appendChild(cursorEl);
  }
  const range = document.createRange();
  const end = last.nodeValue?.length ?? 0;
  range.selectNodeContents(last);
  range.setStart(last, end);
  range.setEnd(last, end);
  const rect = range.getBoundingClientRect();
  const host = box.getBoundingClientRect();
  if (!rect.height) { clearStreamCursor(); return; }
  cursorEl.style.left = `${rect.right - host.left}px`;
  cursorEl.style.top = `${rect.top - host.top}px`;
  cursorEl.style.height = `${rect.height}px`;
}
onMounted(() => {
  nextTick(() => { markMathOverflow(); placeStreamCursor(); paintMermaid(); });
  window.addEventListener("mc-theme-changed", paintMermaid);
});
onUnmounted(() => window.removeEventListener("mc-theme-changed", paintMermaid));
// 流式期间正文每来一段就重算一次 —— 那时不扫公式（每帧扫一遍是白烧 CPU）；
// 但光标要跟着最后一个字走。收尾那一拍去掉光标、扫公式并画 Mermaid。
watch(bodyHtml, () => {
  if (props.streaming) nextTick(placeStreamCursor);
  else nextTick(() => { markMathOverflow(); paintMermaid(); });
});
watch(() => props.streaming, (s) => nextTick(() => {
  if (s) placeStreamCursor();
  else { clearStreamCursor(); markMathOverflow(); paintMermaid(); }
}));
</script>

<template>
  <!-- 只有一种形态（2026-10-27 用户定）：**用户 = 气泡，AI = 正文**。
       从前这里按 `display_mode` 切两套 class（两边气泡 / 用户气泡+AI 纯文本）；现在后者是唯一的样子，
       所以不再有条件类 —— 形态本身写在 CSS 的 `.msg.assistant` / `.msg-wrap.assistant` 里。 -->
  <div :class="`msg-wrap ${role === 'user' ? 'user' : 'assistant'}`">
    <div ref="msgEl" :class="`msg ${role === 'user' ? 'user' : 'assistant'}`">
      <div v-if="role !== 'user' && assistantIdent" class="msg-head">{{ assistantIdent }}</div>
      <details v-if="reasoning" class="think">
        <summary>{{ thinkLabel }}</summary>
        <div class="think-body">{{ reasoning }}</div>
      </details>
      <div ref="textEl" class="text" v-html="bodyHtml"></div>
      <div v-if="streaming && toolHint" class="tool-status" aria-live="polite">
        <span class="tool-dot" aria-hidden="true"></span>
        <i>{{ toolHint }}</i>
      </div>
      <div v-if="streaming && !content && !reasoning && !toolHint" class="typing" aria-label="正在生成">
        <span></span><span></span><span></span>
        <i class="typing-hint">{{ typingHint }}</i>
      </div>
      <div v-if="tools.length" class="tool-cards" aria-label="工具调用">
        <div v-for="(tool, i) in tools" :key="tool.id || i" class="tool-card">
          <span class="tool-card-name">{{ nativeToolLabel(tool.tool_name) }}</span>
          <span v-if="tool.source" class="tool-card-source">{{ tool.source === "client" ? "本地" : "服务端" }}</span>
          <span v-if="tool.queries?.length" class="tool-card-query">{{ tool.queries.join(" / ") }}</span>
          <span v-if="tool.sources?.length" class="tool-card-count">{{ tool.sources.length }} 条来源</span>
        </div>
      </div>
      <div v-if="sources.length" class="tool-sources" aria-label="搜索结果">
        <div class="tool-sources-title">来源 {{ sources.length }}</div>
        <a v-for="source in sources" :key="source.url" :href="source.url" target="_blank" rel="noopener noreferrer">
          {{ sourceLabel(source.url, source.title) }}
        </a>
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
      <!-- 脚注（2026-11-09 用户定）：编号 + 用量 + 首字延迟 + 速度合成一行，贴着正文底部。
           只在 assistant 这边；用户消息的身份在气泡外（下面那行，hover 才显形）。
           生成中 / 库外那条没有这些数 ⇒ `foot` 为空 ⇒ 整行不渲染。 -->
      <div v-if="role !== 'user' && foot" class="msg-foot" :title="footTitle">{{ foot }}</div>
    </div>
    <div v-if="canEdit" class="msg-ops">
      <!-- 用户身份（2026-11-09 用户定）：从气泡里挪到**气泡外的动作行**，CSS 里只在 hover 时显形。
           放在这里而不是单独一行：单独占位会在每条你说的话下面永远留一道空行。 -->
      <span v-if="role === 'user' && ident" class="msg-ident">{{ ident }}</span>
      <VBtn icon variant="text" size="small" title="改正文" aria-label="改正文" @click="emit('edit', messageId, 'content')" v-html="ICONS.pencil" />
      <VBtn v-if="role !== 'user'" icon variant="text" size="small" title="改思考" aria-label="改思考" @click="emit('edit', messageId, 'reasoning')" v-html="ICONS.brain" />
      <VBtn v-if="role !== 'user' && isLast && promptId" icon variant="text" size="small" color="primary" class="prompt-edit-btn" title="编辑最新 Prompt（编辑这条回复对应的用户输入）" aria-label="编辑最新 Prompt" @click="emit('edit-prompt', promptId)" v-html="ICONS.notebookPen" />
      <VBtn v-if="role !== 'user' && isLast" icon variant="text" size="small" title="重摇（摇新的一版并切过去）" aria-label="重摇（摇新的一版并切过去）" @click="emit('reroll', messageId)" v-html="ICONS.refresh" />
      <template v-if="role !== 'user' && isLast && reroll && reroll.active">
        <VBtn icon variant="text" size="small" title="上一版" aria-label="上一版" @click="emit('reroll-go', -1)">◀</VBtn>
        <span class="reroll-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
        <VBtn icon variant="text" size="small" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="emit('reroll-go', 1)">▶</VBtn>
      </template>
      <VBtn icon variant="text" size="small" color="error" title="删除这条及之后" aria-label="删除这条及之后" @click="emit('delete', messageId, isLast)" v-html="ICONS.trash" />
    </div>
  </div>
</template>
