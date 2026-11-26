<script setup lang="ts">
// 消息气泡：正文 markdown 渲染 + <state> 块独立容器（状态变化单列，不混进正文）。
import { ref, computed, watch, onMounted, onUnmounted, nextTick, inject } from "vue";
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import { assistantIdentity, formatThinkLabel, identityLabel, sourceLabel, toolRunHint, usageMetrics } from "../utils/format.ts";
import type { UsageLike } from "../utils/format.ts";
import { renderMarkdown, splitState } from "../utils/markdown.ts";
import { buildToolTimeline, hasToolPosition, inlineHostedTool } from "../utils/chatrows.ts";
import { renderMacroHtml, type MacroMap } from "../utils/macros.ts";
import { renderMermaidIn } from "../utils/mermaid.ts";
import { animateCandidate } from "../utils/candidateMotion.ts";
import type { RerollView } from "../types/app.ts";
import type { MessageTool } from "../api/types.ts";
import MessageToolCard from "./MessageToolCard.vue";
import ActionOverflow, { type OverflowAction } from "./ActionOverflow.vue";
import { issueLabel, type BubblePresentation } from "../utils/answerTranscript.ts";
import { copyMarkdownBody, writeClipboard } from "../utils/answerCopy.ts";

const props = withDefaults(defineProps<BubblePresentation & {
  compactActions?: boolean;
  who?: string;
  idx?: number | string | null; // 显示编号（真消息=服务端 idx；库外的按后缀顺延）
  role?: string;
  kind?: "chat" | "tool_run" | string | null;
  status?: "done" | "canceled" | "failed" | "interrupted" | string | null;
  content?: string;
  reasoning?: string;
  reasoningMs?: number | null;
  durationMs?: number | null; // 受理 → 收尾的总耗时，是输出速度的分母。
  messageId?: string | null;
  deleting?: boolean;
  promptId?: string | null; // 最新 AI 回复对应的用户输入（仅尾条 assistant 有）
  isLast?: boolean;
  streaming?: boolean; // 生成中（还没落库的那条）：发送与等待阶段显示星芒。
  // phase：sending = 正在向后端发送请求；waiting = 202 已受理、等 Provider 首字节。
  phase?: "sending" | "waiting" | string | null;
  // tool：当前服务端工具（xAI web_search / x_search / code_execution）；只服务生成中的提示。
  tool?: { kind: string; state: string } | null;
  sources?: { type?: string; url: string; title?: string }[];
  macros?: MacroMap;
  tools?: MessageTool[];
  reroll?: RerollView | null; // 重摇状态（本条是目标时才非 null；FlowAgent 不用）
  flow?: boolean;
  // usage：上游报的用量（归一化后的那几格；2026-10-20 起气泡脚注就用它）。
  // 只有聚合站（OpenRouter）会带 cost_usd；直连渠道没有 ⇒ 脚注不显示价格。
  usage?: UsageLike | null;
}>(), {
  answerView: "full",
  who: "",
  idx: null,
  role: "",
  kind: "chat",
  status: "done",
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
  macros: () => ({}),
  tools: () => [],
  reroll: null,
  flow: false,
  usage: null,
});
const emit = defineEmits<{
  edit: [messageId: string | null | undefined];
  delete: [messageId: string | null | undefined, isLast: boolean];
  reroll: [messageId: string | null | undefined];
  "reroll-go": [delta: number];
  regenerate: [messageId: string | null | undefined];
}>();

// 包括重摇在内，流式行上的用量和时长可能仍是上一版的，绝不能配给新正文。
const metrics = computed(() => props.streaming || props.role === "user" || props.answerView === "prose" ? [] : usageMetrics(props.usage, {
  reasoningMs: props.reasoningMs,
  durationMs: props.durationMs,
}));
// 用户身份（`你 #3`）：气泡**外面**那一行，CSS 里只在 hover 时显形。
const ident = computed(() => identityLabel(props.who, props.idx));
const assistantIdent = computed(() => assistantIdentity(props.who, props.idx));
const issue = computed(() => props.candidate ? "" : issueLabel(props.status ?? undefined));
const statusText = computed(() => {
  if (issue.value) return issue.value;
  if (props.candidate) return props.progressShown ? "" : "正在重新生成回答…";
  if (!props.progressShown && props.status === "done" && !props.streaming && props.kind !== "tool_run" && !props.content.trim()) return "无正文";
  return "";
});
const showToolDetails = computed(() => props.answerView === "full" && !props.candidate);

// 流式期间还没正文 ⇒ 标签说"思考中…"；落库后才是"思考过程（2.1s）"（那会儿才有用时）。
const typingHint = computed(() =>
  props.phase === "sending" ? "正在向后端发送请求…" : "等待 Provider 响应…",
);
const toolHint = computed(() => props.candidate ? "" : toolRunHint(props.tool));
const thinkLabel = computed(() =>
  props.streaming && !props.content ? "思考中…" : formatThinkLabel(props.reasoningMs),
);
const canEdit = computed(() => !!props.messageId);
const compactReroll = computed(() => props.compactActions && props.reroll?.active && props.isLast && !props.flow && props.role !== "user");
const toast = inject<(text: string, kind?: string) => void>("toast");
async function copyReply(): Promise<void> {
  try {
    await writeClipboard(copyMarkdownBody(parts.value.cleaned, props.macros));
    toast?.("已复制", "ok");
  } catch {
    toast?.("复制失败，请手动选取正文", "error");
  }
}

const replyActions = computed(() => {
  const extra: OverflowAction[] = [];
  if (props.copyAnswer && props.role === "assistant" && !props.streaming && parts.value.cleaned) {
    extra.push({ icon: ICONS.copy, label: "复制回答", run: copyReply });
  }
  const edit: OverflowAction = { icon: ICONS.pencil, label: "编辑消息", run: () => emit("edit", props.messageId) };
  if (!compactReroll.value) extra.push(edit);
  const promptId = props.promptId;
  if (props.role !== "user" && props.isLast && promptId) {
    extra.push({ icon: ICONS.notebookPen, label: "编辑最新 Prompt", hint: "编辑这条回复对应的用户输入", run: () => emit("edit", promptId) });
  }
  if (compactReroll.value) {
    extra.push({ icon: ICONS.refresh, label: "重摇", hint: "直接生成新版本", disabled: props.streaming, run: () => emit("reroll", props.messageId) });
    return { inline: [edit], overflow: extra };
  }
  return { inline: extra, overflow: [] };
});

// 正文拆说话/状态：说话走 markdown，状态块进独立容器。
const inlineTools = computed(() => props.candidate ? [] : props.tools.filter(showToolDetails.value ? hasToolPosition : inlineHostedTool));
const legacyTools = computed(() => showToolDetails.value ? props.tools.filter((tool) => !hasToolPosition(tool)) : []);
const parts = computed(() => splitState(props.content, inlineTools.value.some((tool) => tool.body_offset !== undefined)));
const bodyHtml = computed(() => renderMarkdown(parts.value.cleaned, { macros: props.macros }));
const toolTimeline = computed(() => buildToolTimeline(parts.value.cleaned, inlineTools.value, parts.value.projection, props.streaming).map((part) => (
  part.kind === "text" ? { ...part, html: renderMarkdown(part.text, { macros: props.macros }) } : part
)));
const reasoningHtml = computed(() => renderMacroHtml(props.reasoning, props.macros));
// 公式确实比列宽时给右缘一道提示；字体加载/排版完成前不开放横滚，避免短暂白条。
const textEl = ref<HTMLElement | null>(null);
const msgEl = ref<HTMLElement | null>(null);
function markPendingImages(): void {
  for (const image of textEl.value?.querySelectorAll("img") ?? []) {
    if (!image.complete) image.setAttribute("aria-busy", "true");
  }
}
function settleImage(event: Event): void {
  if (event.target instanceof HTMLImageElement) event.target.removeAttribute("aria-busy");
}
watch([textEl, bodyHtml, toolTimeline], markPendingImages, { flush: "post" });
watch(() => props.reroll?.current_idx, (next, previous) => animateCandidate(msgEl.value, next, previous), { flush: "post" });
let cursorEl: HTMLSpanElement | null = null;
let cursorPrinting = false;
let cursorWaitFrame = 0;
let cursorUpdate = 0;
function markMathOverflow(): void {
  const root = textEl.value;
  if (!root) return;
  for (const el of root.querySelectorAll<HTMLElement>(".katex-display")) {
    el.classList.toggle("scroll-x", el.scrollWidth > el.clientWidth + 1);
  }
}
function settleMathOverflow(): void {
  // KaTeX 字体使公式宽度在插入后还会变化；等待当前字体集稳定再决定是否启用横滚。
  void document.fonts.ready.then(() => nextTick(markMathOverflow));
}
// Mermaid：流式期间不画（代码块每帧都在长，画一次废一次）；收流/挂载后再交给延迟加载的 mermaid。
function paintMermaid(): void {
  if (props.streaming || !textEl.value) return;
  void renderMermaidIn(textEl.value);
}
// 流式期间的终端光标：v-html 每帧会重写正文，所以每帧在**最后一个有字的文本节点**后面
// 插一个光标（不塞进 Markdown 源，免得落进代码块/公式被转义）。它只是装饰，aria-hidden。
function clearStreamCursor(): void {
  cursorPrinting = false;
  cursorUpdate++;
  if (cursorWaitFrame) cancelAnimationFrame(cursorWaitFrame);
  cursorWaitFrame = 0;
  cursorEl?.remove();
  cursorEl = null;
}
function placeStreamCursor(): void {
  const box = msgEl.value;
  if (!props.streaming || !textEl.value || !box) { clearStreamCursor(); return; }
  const walker = document.createTreeWalker(textEl.value, NodeFilter.SHOW_TEXT, {
    acceptNode(node: Node): number {
      return node.nodeValue?.trim() && !node.parentElement?.closest(".tool-card") ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
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
  cursorEl.classList.toggle("is-printing", cursorPrinting);
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
function markCursorPrinting(): void {
  if (!props.streaming) return;
  cursorPrinting = true;
  const update = ++cursorUpdate;
  if (cursorWaitFrame) cancelAnimationFrame(cursorWaitFrame);
  cursorWaitFrame = 0;
  cursorEl?.classList.add("is-printing");
  void nextTick(() => {
    if (update !== cursorUpdate) return;
    placeStreamCursor();
    cursorWaitFrame = requestAnimationFrame(() => {
      if (update !== cursorUpdate) return;
      cursorPrinting = false;
      cursorEl?.classList.remove("is-printing");
      cursorWaitFrame = 0;
    });
  });
}
onMounted(() => {
  settleMathOverflow(); placeStreamCursor(); paintMermaid();
  window.addEventListener("mc-theme-changed", paintMermaid);
});
onUnmounted(() => { window.removeEventListener("mc-theme-changed", paintMermaid); clearStreamCursor(); });
// 流式期间正文每来一段就重算一次 —— 那时不扫公式（每帧扫一遍是白烧 CPU）；
// 但光标要跟着最后一个字走。收尾那一拍去掉光标、扫公式并画 Mermaid。
watch([() => props.content, toolTimeline, () => props.answerView], ([content], [previousContent]) => {
  if (props.streaming) {
    if (content.length > previousContent.length) markCursorPrinting();
    else nextTick(placeStreamCursor);
  } else nextTick(() => { settleMathOverflow(); paintMermaid(); });
});
watch(() => props.streaming, (s) => nextTick(() => {
  if (s) placeStreamCursor();
  else { clearStreamCursor(); settleMathOverflow(); paintMermaid(); }
}));
</script>

<template>
  <!-- 只有一种形态（2026-10-27 用户定）：**用户 = 气泡，AI = 正文**。
       从前这里按 `display_mode` 切两套 class（两边气泡 / 用户气泡+AI 纯文本）；现在后者是唯一的样子，
       所以不再有条件类 —— 形态本身写在 CSS 的 `.msg.assistant` / `.msg-wrap.assistant` 里。 -->
  <div :class="`msg-wrap ${role === 'user' ? 'user' : 'assistant'}`">
    <div v-if="answerView === 'full' || content || toolTimeline.length" ref="msgEl" :class="`msg ${role === 'user' ? 'user' : 'assistant'}`"
      @load.capture="settleImage" @error.capture="settleImage">
      <div v-if="role !== 'user' && answerView === 'full' && (assistantIdent || statusText)" class="msg-head">
        {{ assistantIdent }}<span v-if="statusText" :class="{ 'msg-status': issue }">{{ assistantIdent ? ' · ' : '' }}{{ statusText }}</span>
      </div>
      <details v-if="answerView === 'full' && reasoning" class="think">
        <summary>{{ thinkLabel }}</summary>
        <div class="think-body" v-html="reasoningHtml"></div>
      </details>
      <div v-if="!progressShown && streaming && toolHint && !tools.length" class="tool-status" aria-live="polite">
        <span class="tool-dot" aria-hidden="true"></span>
        <i>{{ toolHint }}</i>
      </div>
      <template v-if="showToolDetails">
        <div v-if="legacyTools.length" class="tool-cards" aria-label="未记录位置的工具调用">
          <MessageToolCard v-for="(tool, i) in legacyTools" :key="tool.id || i" :tool="tool" />
        </div>
        <details v-else-if="!tools.length && sources.length" class="tool-sources" aria-label="搜索结果">
          <summary class="tool-sources-title">来源 {{ sources.length }}</summary>
          <a v-for="source in sources" :key="source.url" :href="source.url" target="_blank" rel="noopener noreferrer">
            {{ sourceLabel(source.url, source.title) }}
          </a>
        </details>
      </template>
      <div v-if="toolTimeline.length" ref="textEl" class="text text-timeline">
        <template v-for="(part, i) in toolTimeline" :key="part.kind === 'tool' ? `tool-${part.tool.source}-${part.tool.item_id || part.tool.id || i}` : `text-${i}`">
          <template v-if="part.kind === 'text'">
            <div v-if="part.html" class="text-segment" v-html="part.html"></div>
          </template>
          <MessageToolCard v-else :tool="part.tool" :live="streaming" inline />
        </template>
      </div>
      <div v-else ref="textEl" class="text" v-html="bodyHtml"></div>
      <div v-if="!progressShown && streaming && !content && !reasoning && !toolHint && !tools.length" class="typing" role="status">
        <span class="typing-orbit" aria-hidden="true" v-html="ICONS.spark"></span>
        <span>{{ typingHint }}</span>
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
    </div>
    <div v-if="answerView !== 'prose' && (canEdit || metrics.length)" class="msg-footer">
      <div v-if="canEdit" class="msg-ops" :class="{ 'reroll-compact': compactReroll }">
        <span v-if="role === 'user' && ident" class="msg-ident">{{ ident }}</span>
        <div class="msg-action-group" role="group" aria-label="消息操作">
          <ActionOverflow :actions="replyActions.inline" />
        </div>
        <div v-if="role !== 'user' && isLast" class="msg-action-group" role="group" aria-label="重新生成与版本">
          <VBtn v-if="!compactReroll" icon variant="text" size="small"
            :title="flow ? '重新生成（用当前历史重跑流程）' : '重摇（摇新的一版并切过去）'"
            :aria-label="flow ? '重新生成（用当前历史重跑流程）' : '重摇（摇新的一版并切过去）'"
            @click="flow ? emit('regenerate', messageId) : emit('reroll', messageId)" v-html="ICONS.refresh" />
          <template v-if="!flow && reroll?.active">
            <VBtn icon variant="text" size="small" title="上一版" aria-label="上一版" @click="emit('reroll-go', -1)">◀</VBtn>
            <span class="reroll-pos">{{ reroll.running ? `${reroll.current_idx}→${reroll.count}` : `${reroll.current_idx}/${reroll.count}` }}</span>
            <VBtn icon variant="text" size="small" title="下一版（到最右再按 = 再摇一版）" aria-label="下一版" @click="emit('reroll-go', 1)">▶</VBtn>
          </template>
        </div>
        <VBtn icon variant="text" size="small" class="msg-delete" :loading="deleting" :disabled="deleting"
          title="删除这条及之后" aria-label="删除这条及之后" @click="emit('delete', messageId, isLast)">
          <span aria-hidden="true" v-html="ICONS.trash" />
        </VBtn>
        <ActionOverflow v-if="compactReroll" menu-label="更多消息操作" :actions="replyActions.overflow" />
      </div>
      <details v-if="metrics.length" class="msg-foot">
        <summary title="回答用量与耗时明细">
          <span v-for="metric in metrics" :key="metric.text" class="msg-metric" :title="metric.detail">{{ metric.text }}</span>
        </summary>
        <ul><li v-for="metric in metrics" :key="metric.text">{{ metric.detail }}</li></ul>
      </details>
    </div>
  </div>
</template>
