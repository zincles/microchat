<script setup lang="ts">
// 输入卡的上下文与任务入口：只接收 App 提供的快照，不自己轮询。
import { computed, ref, watch } from "vue";
import { VBtn, VCard, VList, VListItem, VListSubheader, VOverlay, VProgressLinear, VTab, VTabs, VWindow, VWindowItem } from "vuetify/components";
import { useChatMenu } from "../composables/useChatMenu.ts";
import CloseButton from "./CloseButton.vue";
import RequestLoading from "./RequestLoading.vue";
import { contextArc, contextOverBudget, contextPercentText, contextTitle, formatDuration, formatStatusLine, formatTokens, phaseText } from "../utils/format.ts";
import type { ContextUsage, FlowActiveNode, MacroReport, Queue, TaskBoard, TaskRecord } from "../api/types.ts";

interface StatusInfo {
  conn?: string;
  agent?: string;
  phase?: string | null;
  elapsedMs?: number | null;
}
interface TaskRow extends TaskRecord {
  depth: number;
}

const props = defineProps<{
  flow?: boolean;
  flowCalls?: number;
  flowActiveNodes?: FlowActiveNode[];
  sessionId?: string;
  requestOverrides?: Record<string, unknown> | null;
  macrosLoading?: boolean;
  macrosError?: string;
  savingOverrides?: boolean;
  cancelingTasks?: ReadonlySet<string>;
  contextUsageLoading?: boolean;
  contextUsageError?: string;
}>();
const emit = defineEmits<{
  "open-workflow": [];
  "save-overrides": [overrides: Record<string, unknown>];
  "cancel-queue": [id: string];
  "cancel-task": [id: string];
  "refresh-macros": [];
}>();

const ctx = ref<ContextUsage | null>(null);
const ctxOpen = useChatMenu("context");
const ctxCard = ref<InstanceType<typeof VCard> | null>(null);
const ctxTab = ref<"context" | "tasks" | "overrides" | "macros">("context");
// 内容省略外层 {}：包装后再 JSON.parse；保存发给 App 做 PATCH。
const overridesSessionId = computed(() => props.sessionId ?? "");
const overridesText = ref("");
const overridesDirty = ref(false);
const overridesError = ref("");
const overridesNotice = ref("");

function formatOverrides(value?: Record<string, unknown> | null): string {
  const raw = JSON.stringify(value ?? {}, null, 2);
  return raw === "{}" ? "" : raw.slice(1, -1).trim();
}
function parseOverrides(text: string): Record<string, unknown> {
  const trimmed = text.trim();
  if (!trimmed) return {};
  // 省略外层花括号是界面约定；用户贴完整对象也照样接受。
  const wrapped = trimmed.startsWith("{") ? trimmed : `{${trimmed}}`;
  const value = JSON.parse(wrapped);
  if (!value || Array.isArray(value) || typeof value !== "object") {
    throw new Error("必须是一个 JSON 对象");
  }
  return value as Record<string, unknown>;
}
function validateOverrides(): Record<string, unknown> | null {
  try {
    const parsed = parseOverrides(overridesText.value);
    overridesError.value = "";
    overridesNotice.value = "JSON 合法";
    return parsed;
  } catch (err) {
    overridesError.value = `JSON 不合法：${err instanceof Error ? err.message : String(err)}`;
    return null;
  }
}
function saveOverrides(): void {
  if (!overridesSessionId.value) {
    overridesError.value = "先建立会话，才能保存请求体覆盖";
    return;
  }
  const parsed = validateOverrides();
  if (!parsed) return;
  emit("save-overrides", parsed);
}
function setOverridesSaved(value?: Record<string, unknown> | null): void {
  overridesText.value = formatOverrides(value);
  overridesDirty.value = false;
  overridesError.value = "";
  overridesNotice.value = "";
}
watch([overridesSessionId, () => props.requestOverrides], ([sid, overrides], previous) => {
  if (sid !== previous?.[0] || !overridesDirty.value) setOverridesSaved(overrides);
}, { immediate: true });
// 弧长（含"用了就得看得见"的最小弧）在 `utils/format.ts` 的 `contextArc` 里 —— 那边才测得到。
const ctxArc = computed(() => contextArc(ctx.value));
const ctxOver = computed(() => contextOverBudget(ctx.value));
const flowActiveNodes = computed(() => props.flow ? (props.flowActiveNodes ?? []) : []);
const flowCallCount = computed(() => props.flow ? Math.max(0, Number(props.flowCalls ?? 0), flowActiveNodes.value.length) : 0);
const ringCount = computed(() => props.flow ? flowCallCount.value : activeTaskCount.value);
// 有任务时给满环留一道可见的缺口。还没有占用数据的会话（含 Flow）用短弧表示活动。
const ringArc = computed(() => {
  const usage = props.flow ? 0 : ctxArc.value;
  if (!ringCount.value) return usage;
  return usage > 0 ? Math.min(usage, 92) : 18;
});
const ringCountText = computed(() => ringCount.value > 99 ? "99+" : String(ringCount.value));
const ctxRows = computed<Array<[string, string]>>(() => {
  const c = ctx.value;
  if (!c) return [];
  const rows: Array<[string, string]> = [
    ["剩余可用", `${formatTokens(c.remaining_tokens)} tokens`],
    ["自动压缩阈值", c.trigger_tokens != null ? `${formatTokens(c.trigger_tokens)} tokens` : "未启用"],
  ];
  // 上一次真发出去的 prompt_tokens（上游报的实数）—— 与本次估算值分开记。
  if (c.last_prompt_tokens != null) rows.push(["上次实际用量", `${formatTokens(c.last_prompt_tokens)} tokens`]);
  return rows;
});
// 预算这句是说给"为什么不是 100 万"的：只解释一次，不把算式塞进表格。
const ctxBudgetNote = computed(() => {
  const c = ctx.value;
  if (!c) return "";
  const num = (v?: number | null): string => Number(v ?? 0).toLocaleString("en-US");
  return `输入预算 ${num(c.budget_tokens)} tokens = 上下文上限 ${num(c.ctx_len)} − 输出预留 ${num(c.max_output)}。`;
});

watch(() => props.flow, () => { ctxOpen.value = false; });
// setContext：App 每轮 refreshStatusline 喂一次（草稿态传 null）。
function setContext(c?: ContextUsage | null): void { ctx.value = c ?? null; }
// “已注册宏”面板：后端把注册表 + 本会话全文使用次数一起算好（这里只管画）。
const macroReport = ref<MacroReport | null>(null);
function setMacros(report?: MacroReport | null): void { macroReport.value = report ?? null; }
watch([ctxOpen, ctxTab, () => props.flow, overridesSessionId], ([open, tab, flow, sid]) => {
  if (open && tab === "macros" && !flow && sid) emit("refresh-macros");
});
function macroLabel(name: string): string { return `{{${name}}}`; }

// —— 后台任务（2026-11-01 用户定：「把后台任务也做成右上角那个圆环一样的东西……显示一个数字，
//     按下就在一个小面板里（类似那个绿色的上下文栏）显示有多少后台任务」）——
//
// 小圆里的数字 = **还在跑**的条数（`board.running`；`finished_at` 有值的不算）—— 与从前输入框
// 上方那行小字同一个口径。浮板里给明细：kind / 标题 / 跑了多久 / 成没成。
//
// ⚠ 这格是 `/tasks` 的**唯一**落点（从前输入框上方那行小字**已经删掉**）：同一个数字住两处，
//   只会让两处都可能过期 —— 那是 2026-10-28 状态行搬进环的浮板时定下的规矩，别再犯。
// ⚠ 0 个时环心不显示数字（2026-11-02 用户定），环本身仍在、不转；有任务才出现数字。
const tasks = ref<TaskBoard | null>(null);
const taskRunning = computed(() => tasks.value?.running ?? 0);
// 浮板只列**正在跑**的（2026-11-02 用户定：这个 tab 是「正在运行的任务」）。
// 后端已把跑着的排前面；最多 12 条，避免面板被历史记录撑长。
const TASK_ROWS = 12;
const taskRecords = computed<TaskRecord[]>(() => (tasks.value?.tasks ?? []).filter((t) => !t.finished_at).slice(0, TASK_ROWS));
// taskElapsed：跑了多久 / 一共跑了多久。`now` 用**后端**那一份（前端时钟与它不一致也照样对）。
function taskElapsed(t: TaskRecord): string {
  const now = tasks.value?.now ?? Date.now();
  const ms = (now - (t.started_at ?? now));
  return formatDuration(ms);
}
// —— 队列任务（2026-11-01 用户定）——
//
// 一个队列 = 一串**串行**跑的同种原子任务（现在只有压缩）。面板上它是**单独一栏**：
// 队列说"这一大批跑到第几步"，下面的原子任务说"此刻在跑哪一发" —— 两层东西，别塞一个列表。
const queues = computed<Queue[]>(() => tasks.value?.queues ?? []);
// 只留还没结束的队列：结束的跑完就消失，别在「正在运行」里冒充活人。
const runningQueues = computed(() => queues.value.filter((q) => !q.finished_at));
const queueRunning = computed(() => runningQueues.value.length);
// 转圈看的是"有没有活在跑"：原子任务、队列，任一都算（队列两步之间那几毫秒也算）。
const taskBusy = computed(() => taskRunning.value > 0 || queueRunning.value > 0);
// 环心数字：>99 收成 "99+"（1.8em 的圆装不下三位数；真数在 title 与浮板里）。
// 队列与它当前那一步原子任务是同一件事，用 max 避免重复计数；队列两步之间也算 1。
const activeTaskCount = computed(() => Math.max(taskRunning.value, queueRunning.value));
const taskCountText = computed(() => (activeTaskCount.value > 99 ? "99+" : String(activeTaskCount.value)));
// 环的 title / aria：把"在跑什么"和上下文占用一起说清。
const taskTitle = computed(() => {
  if (props.flow) {
    return flowCallCount.value > 0 ? `FlowAgent：${flowCallCount.value} 个 LLM 调用运行中` : "FlowAgent：当前没有 LLM 调用";
  }
  if (!taskBusy.value) return "后台任务：当前无运行任务";
  const q = queueRunning.value ? ` · ${queueRunning.value} 个队列` : "";
  return `后台任务：${taskRunning.value} 个运行中${q}`;
});

// 大类的中文名 —— **只有这一份**。后端给 `group`（llm / plain），前端不按 kind 猜：
// 猜就会与 `task.Kind.Group()` 分岔，而且不报错，只是"某类任务挂到了错的组里"。
const GROUP_LABELS: Record<string, string> = { llm: "模型调用", plain: "其他任务" };
// 任务种类的人话名（原始 kind 是给机器看的，`refresh_models` 这种不该出现在面板里）。
const TASK_KIND_LABELS: Record<string, string> = {
  refresh_models: "刷新模型", turn: "对话", compact: "压缩", compact_job: "分段压缩",
  title: "起标题", reroll: "重摇", judgement: "判断",
};
// 复合作业的面板层级：父 Job 一行，子 Task 缩进挂在下面。
// 只按 parent_id 排；不做“猜测式”归并（父不在这一屏就把它当独立行，别丢）。
const taskRows = computed<TaskRow[]>(() => {
  const list = taskRecords.value;
  const ids = new Set(list.map((t) => t.id));
  const children = new Map<string, TaskRecord[]>();
  for (const t of list) {
    if (t.parent_id && ids.has(t.parent_id)) {
      if (!children.has(t.parent_id)) children.set(t.parent_id, []);
      children.get(t.parent_id)!.push(t);
    }
  }
  const out: TaskRow[] = [];
  const walk = (t: TaskRecord, depth: number): void => {
    out.push({ ...t, depth });
    for (const child of children.get(t.id) ?? []) walk(child, depth + 1);
  };
  for (const t of list) {
    if (!t.parent_id || !ids.has(t.parent_id)) walk(t, 0);
  }
  return out;
});
function taskRoleText(t: TaskRecord): string {
  if (t.kind === "compact_job") return "Job";
  return t.parent_id ? "任务" : "";
}
// 标题常常已经以种类名开头（"刷新模型 · opendesign"）⇒ 别把同一个词在行首再印一遍。
function taskKindText(t: { kind: string; title?: string }): string {
  const label = TASK_KIND_LABELS[t.kind] || "任务";
  return t.title && t.title.startsWith(label) ? "" : label;
}
const taskGroups = computed<Array<{ key: string; label: string; items: TaskRow[] }>>(() => {
  const buckets = new Map<string, { key: string; label: string; items: TaskRow[] }>();
  for (const t of taskRows.value) {
    const key = t.group || "plain";
    if (!buckets.has(key)) buckets.set(key, { key, label: GROUP_LABELS[key] || key, items: [] });
    buckets.get(key)!.items.push(t);
  }
  // LLM 那组排前面：烧钱的那几发比"刷模型"更值得一眼看见。
  return [...buckets.values()].sort((a, b) => (a.key === "llm" ? 0 : 1) - (b.key === "llm" ? 0 : 1));
});

// setTasks：App 轮询 `/tasks` 喂进来（取不到就传 null ⇒ 帽子归 0、浮板说"读不到"）。
function setTasks(board?: TaskBoard | null): void { tasks.value = board ?? null; }

// —— 连接 / 助手 / 相位（2026-10-28 用户定："已连接 v0.1.0｜默认助手｜idle 这个也塞那个环的面板里"）——
// 从前它是输入框上方那行小字。搬进面板之后**分三行**给（而不是拼成一句话）：
// 面板里每一格都是"标签 → 值"，拼成一行再拆出差错，也没必要（`formatStatusLine` 仍用来做悬停解释）。
const status = ref<StatusInfo | null>(null);
const statusRows = computed<Array<[string, string]>>(() => {
  const s = status.value;
  if (!s) return [];
  const phase = `${phaseText(s.phase)}${s.elapsedMs != null ? ` · ${formatDuration(s.elapsedMs)}` : ""}`;
  return [["连接状态", s.conn || "未连接"], ["当前助手", s.agent || "?"], ["运行状态", phase]];
});
function setStatus(s?: StatusInfo | null): void { status.value = s ?? null; }
// 悬停解释 = 状态一句 + 预算一句（两处都是"一眼看不清、要解释"的东西）。
const ringTitle = computed(() => {
  if (props.flow) return taskTitle.value;
  return [formatStatusLine(status.value ?? {}), contextTitle(ctx.value), taskTitle.value].filter(Boolean).join("\n");
});
function focusPanel(): void {
  if (!ctxOpen.value) return;
  const el: HTMLElement | undefined = ctxCard.value?.$el;
  (el?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]') ?? el?.querySelector("button"))?.focus({ preventScroll: true });
}
function openWorkflow(): void {
  ctxOpen.value = false;
  emit("open-workflow");
}
defineExpose({ setContext, setStatus, setTasks, setOverridesSaved, setMacros });
</script>

<template>
  <VOverlay v-model="ctxOpen" attach="#app" :scrim="false" location-strategy="connected" location="top end"
    scroll-strategy="reposition" :offset="8" min-width="0" width="min(30em, calc(100vw - 24px))" height="23.5em"
    @after-enter="focusPanel">
    <template #activator="{ props: activatorProps }">
      <button
        v-bind="activatorProps"
        id="ctx-ring"
        aria-haspopup="dialog"
        :aria-controls="ctxOpen ? 'chat-context-panel' : undefined"
        :class="{ over: !flow && ctxOver }"
        type="button"
        :title="ringTitle"
        :aria-label="flow ? '正在运行的 LLM 调用' : '上下文占用与后台任务'"
        :aria-expanded="ctxOpen"
      >
        <svg viewBox="0 0 36 36" aria-hidden="true">
          <circle class="ctx-ring-track" cx="18" cy="18" r="12.5" />
          <circle v-if="ringArc > 0" class="ctx-ring-fill" :class="{ spinning: ringCount > 0 }" cx="18" cy="18" r="12.5"
            pathLength="100" :stroke-dasharray="`${ringArc} ${100 - ringArc}`" />
        </svg>
        <span v-if="ringCount > 0" class="ctx-ring-n">{{ ringCountText }}</span>
      </button>
    </template>
    <template v-if="flow">
      <VCard id="chat-context-panel" ref="ctxCard" color="surface-container" class="ctx-menu flow-calls-menu" role="dialog" aria-label="并行 LLM 调用">
        <div class="flow-calls-head">
          <b>并行 LLM 调用</b>
          <span class="flow-calls-count">{{ flowCallCount }} 个运行中</span>
          <CloseButton label="关闭并行 LLM 调用" @click="ctxOpen = false" />
        </div>
        <p v-if="!flowActiveNodes.length" class="agent-empty">当前没有运行中的 LLM 调用。</p>
        <VList v-else density="compact" class="flow-calls-list">
          <VListItem v-for="node in flowActiveNodes" :key="node.id" class="flow-call-row">
            <template #prepend><span class="flow-call-dot" aria-hidden="true"></span></template>
            <div class="flow-call-text">
              <b>{{ node.label }}</b>
              <span>{{ node.id }}</span>
              <small v-if="node.provider || node.model">{{ node.provider }}/{{ node.model }}</small>
            </div>
          </VListItem>
        </VList>
        <VBtn variant="text" size="small" type="button" class="flow-calls-open" @click="openWorkflow">打开工作流</VBtn>
      </VCard>
    </template>
    <template v-else>
      <VCard id="chat-context-panel" ref="ctxCard" color="surface-container" class="ctx-menu" :class="{ over: ctxOver }" role="dialog" aria-label="上下文与任务">
        <VTabs
          v-model="ctxTab"
          class="ctx-tabs"
          density="compact"
          color="primary"
          align-tabs="start"
          aria-label="上下文与任务"
        >
          <VTab value="context">上下文</VTab>
          <VTab value="tasks">任务<span v-if="activeTaskCount > 0" class="ctx-tab-count">{{ taskCountText }}</span></VTab>
          <VTab value="overrides">请求体覆盖</VTab>
          <VTab value="macros">已注册宏</VTab>
        </VTabs>
        <VWindow v-model="ctxTab" class="ctx-window">
          <VWindowItem value="context">
            <div class="ctx-pane">
              <RequestLoading v-if="contextUsageLoading" label="正在加载上下文占用…" />
              <p v-else-if="contextUsageError" class="agent-empty" role="alert">{{ contextUsageError }}</p>
              <template v-else-if="ctx">
                <div class="ctx-head">
                  <b class="ctx-used">已用 {{ formatTokens(ctx.used_tokens) }} / {{ formatTokens(ctx.budget_tokens) }}</b>
                  <b class="ctx-pct" :class="{ over: ctxOver }">{{ ctxOver ? "超出预算" : contextPercentText(ctx) }}</b>
                </div>
                <VProgressLinear
                  class="ctx-progress"
                  :model-value="ctxArc"
                  :color="ctxOver ? 'error' : 'success'"
                  rounded
                  height="6"
                />
                <VList density="compact" class="ctx-list">
                  <VListItem v-for="[k, v] in ctxRows" :key="k" class="ctx-row">
                    <div class="ctx-row-inner"><span class="ctx-key">{{ k }}</span><span class="ctx-val">{{ v }}</span></div>
                  </VListItem>
                </VList>
                <p class="ctx-note">{{ ctxBudgetNote }}</p>
              </template>
              <p v-else class="agent-empty">当前会话暂无上下文数据。</p>
              <VList v-if="statusRows.length" density="compact" class="ctx-list ctx-list-status">
                <VListItem v-for="[k, v] in statusRows" :key="k" class="ctx-row">
                  <div class="ctx-row-inner"><span class="ctx-key">{{ k }}</span><span class="ctx-val">{{ v }}</span></div>
                </VListItem>
              </VList>
            </div>
          </VWindowItem>
          <VWindowItem value="tasks">
            <div class="ctx-pane ctx-tasks">
              <div class="task-head">
                <b>{{ taskBusy ? "运行中" : "当前无运行任务" }}</b>
                <span v-if="taskBusy" class="task-total">
                  {{ taskRunning }} 个任务<template v-if="queueRunning"> · {{ queueRunning }} 个队列</template>运行中
                </span>
              </div>
              <p v-if="!tasks" class="agent-empty">无法读取任务状态（服务未响应）。</p>
              <p v-else-if="!taskBusy" class="agent-empty">
                对话生成、上下文压缩、模型列表刷新等任务会显示在这里。
              </p>
              <template v-else>
                <VList v-if="runningQueues.length" density="compact" class="task-list">
                  <VListSubheader>队列</VListSubheader>
                  <VListItem
                    v-for="q in runningQueues"
                    :key="q.id"
                    class="task-row task-queue"
                    :class="{ failed: !!q.error }"
                  >
                    <div class="task-row-inner">
                      <span class="task-kind">{{ taskKindText(q) || "队列" }}</span>
                      <span class="task-label" :title="q.title || '队列'">
                        {{ q.title || "队列" }}
                        <b class="task-progress">第 {{ q.index }} / {{ q.total }} 步</b>
                        <span v-if="q.error" class="task-why">{{ q.error }}</span>
                      </span>
                    </div>
                    <template #append>
                      <VBtn
                        class="task-cancel"
                        variant="text"
                        color="error"
                        size="x-small"
                        :loading="cancelingTasks?.has(q.id)"
                        :disabled="cancelingTasks?.has(q.id)"
                        title="取消这个队列（在飞的那一发也一起掐断）"
                        @click.stop="emit('cancel-queue', q.id)"
                      >取消</VBtn>
                    </template>
                  </VListItem>
                </VList>
                <template v-for="g in taskGroups" :key="g.key">
                  <div class="task-group-head">{{ g.label }}</div>
                  <VList density="compact" class="task-list">
                    <VListItem
                      v-for="t in g.items"
                      :key="t.id"
                      class="task-row"
                      :class="{ failed: !!t.error, child: t.depth > 0 }"
                    >
                      <div class="task-row-inner">
                        <span v-if="taskRoleText(t)" class="task-role">{{ taskRoleText(t) }}</span>
                        <span v-if="taskKindText(t)" class="task-kind">{{ taskKindText(t) }}</span>
                        <span class="task-label" :title="t.title">{{ t.title || "任务" }}</span>
                      </div>
                      <template #append>
                        <VBtn
                          v-if="t.cancelable"
                          class="task-cancel"
                          variant="text"
                          color="error"
                          size="x-small"
                          :loading="cancelingTasks?.has(t.id)"
                          :disabled="cancelingTasks?.has(t.id)"
                          title="按停这个任务（已经写下的东西不受影响：取消 = 没做过）"
                          @click.stop="emit('cancel-task', t.id)"
                        >取消</VBtn>
                        <span v-else class="task-time">{{ taskElapsed(t) }}</span>
                      </template>
                    </VListItem>
                  </VList>
                </template>
              </template>
            </div>
          </VWindowItem>
          <VWindowItem value="overrides">
            <div class="ctx-pane ctx-overrides">
              <p class="ctx-note">顶层 JSON 键值，省略外层花括号；保存后在每次对话请求体最后写入（允许覆盖任何 key）。</p>
              <textarea
                v-model="overridesText"
                class="ctx-overrides-input"
                spellcheck="false"
                :disabled="savingOverrides"
                placeholder='"think": false'
                @input="overridesDirty = true; overridesError = ''; overridesNotice = ''"
              ></textarea>
              <div class="ctx-overrides-actions">
                <VBtn type="button" variant="text" size="small" @click="validateOverrides">验证</VBtn>
                <VBtn type="button" variant="flat" color="primary" size="small" :loading="savingOverrides" :disabled="!overridesSessionId || savingOverrides" @click="saveOverrides">保存</VBtn>
                <span class="ctx-overrides-state" :class="{ error: !!overridesError }">{{ overridesError || overridesNotice || (overridesDirty ? "未保存" : "已保存") }}</span>
              </div>
            </div>
          </VWindowItem>
          <VWindowItem value="macros">
            <div class="ctx-pane ctx-macros">
              <p class="macro-note">存储保留 <code v-text="'{{…}}'"></code> 原文；只有后端出站与界面显示时替换。</p>
              <RequestLoading v-if="macrosLoading" label="正在加载已注册宏…" />
              <p v-else-if="macrosError" class="agent-empty" role="alert">{{ macrosError }}</p>
              <p v-else-if="!sessionId" class="agent-empty">建立会话后可查看已注册宏与使用情况。</p>
              <template v-else-if="macroReport">

              <div class="macro-section-title">
                <span>可用宏</span>
                <span class="macro-section-count">{{ macroReport?.macros?.length ?? 0 }}</span>
              </div>
              <table class="macro-table macro-table-available">
                <thead><tr><th>宏</th><th>当前值</th><th>说明</th></tr></thead>
                <tbody>
                  <tr v-for="m in macroReport?.macros ?? []" :key="m.name">
                    <td>
                      <code class="macro-name" v-text="macroLabel(m.name)"></code>
                      <span v-if="m.aliases?.length" class="macro-alias">{{ m.aliases.join(" / ") }}</span>
                    </td>
                    <td><span class="macro-value">{{ m.value || "—" }}</span></td>
                    <td class="macro-description">{{ m.description }}</td>
                  </tr>
                </tbody>
              </table>

              <div class="macro-section-title">
                <span>本会话使用</span>
                <span class="macro-section-count">{{ macroReport?.total ?? 0 }} 次</span>
              </div>
              <div v-if="!macroReport?.usage?.length" class="macro-empty">
                <span class="macro-empty-icon" aria-hidden="true">∅</span>
                <div>
                  <b>还没有使用宏</b>
                  <span>在提示词或消息里写 <code v-text="'{{user_name}}'"></code> 就会出现在这里。</span>
                </div>
              </div>
              <table v-else class="macro-table macro-table-usage">
                <thead><tr><th>宏</th><th>次数</th><th>状态</th></tr></thead>
                <tbody>
                  <tr v-for="u in macroReport.usage" :key="u.name">
                    <td><code class="macro-name" v-text="macroLabel(u.name)"></code></td>
                    <td class="macro-count">×{{ u.count }}</td>
                    <td><span v-if="!u.known" class="macro-unknown">未注册</span><span v-else class="macro-known">已注册</span></td>
                  </tr>
                </tbody>
              </table>
              </template>
            </div>
          </VWindowItem>
        </VWindow>
      </VCard>
    </template>
  </VOverlay>
</template>
