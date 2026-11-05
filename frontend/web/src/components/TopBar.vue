<script setup>
// 顶栏（2026-10-14 用户定，去壳版）：**全透明**，左上角 = **圆头像（当前 Agent，点开换）** + **当前模型平铺字**；
// 右侧只在某一侧栏收着时才出它的开关（栏开着 ⇒ 开关在栏自己头上，见 App 的 applySideToggle）。
//
// 模型选择器（2026-10-28 用户定）：从静默 `<select>` 换成自定义浮板（按渠道分组 + 搜索），
// 与 Agent 选人、上下文环那两个浮板同一族。
//
// 头像（2026-10-14 用户定）：圆形小图标就是**新的 Agent 入口** —— 里头是名字首字、底色由 id 派生
// （同一个人永远同一个色）；点开掉出浮板选人。状态行小字里那个助手名管"显示"，头像管"选"。
// 会话抬头不在顶栏（左栏列表高亮 + 状态行都有它）。
import { ref, computed, nextTick, watch } from "vue";
import { ICONS } from "./icons.js";
import { modelKey, parseModelKey, formatTokens, formatStatusLine, formatDuration, phaseText, contextArc, contextPercentText, contextTitle, contextOverBudget, curModelLabelOf, filterModelGroups } from "../utils/format.js";

const props = defineProps({
  // leftHidden：左栏收着 ⇒ 顶栏出它那枚开关；栏开着 ⇒ 开关在栏自己头上（按钮跟着它控制的东西走）。
  // （右栏 2026-10-29 搬进上下文工作区 ⇒ 顶栏不再有 [◨] 那枚开关，`rightHidden` 跟着删。）
  leftHidden: { type: Boolean, default: false },
  // workspace：这一下要显示哪块工作区（"chat" | "context"）。
  //
  // 2026-10-29 用户定：「每个对话分为俩部分：对话界面 和 上下文工作区 界面」，
  // 开关就落在右上角、**绿环左边**（"位于那个绿色圆环左侧"）。放在这一簇而不是别处，
  // 是因为它和环管的是同一件事的两面：环说"占了多少"，工作区说"占的是什么"。
  workspace: { type: String, default: "chat" },
  // loading：会话正在加载（模型列表也还没就位）⇒ 不拿旧/裸 id 闪一下，直接骨架。
  loading: { type: Boolean, default: false },
});
const emit = defineEmits(["toggle-left", "select-model", "select-agent", "select-workspace", "cancel-queue", "cancel-task"]);

const modelGroups = ref([]); // [{provider, items:[{value,label,provider_name,upstream_id}]}]
const curModel = ref("");
// 会话绑的模型不在列表里（渠道被删、模型下架）⇒ 补一条"不在列表"，别让它看起来像没选
// —— 那是有值，只是列表里没有（显示谎言比多一行丑得多）。
const orphanModel = ref(null);
// —— 模型选择器（2026-10-28 用户定）——
// 从前这儿是个静默 `<select>`：**原生选项**在 470 个模型的聚合站上是一条滚不完的长菜单，
// 同一渠道的模型还不挨在一起、也没法搜。现在点开一块与 Agent 选人同族的浮板：
// 搜索框 + 按渠道分组（组头带渠道名与条数）+ 当前项打勾 + 上下键与回车。
//
// ⚠ 顺带删掉了 `modelSel` / `reapplyValues` 那套补写：那是给 `<select>` 打的补丁 ——
//   选项异步到、`value` 先落进 DOM 会被浏览器换掉。既然是自己的面板，值就只是数据。
const modelOpen = ref(false);
const modelQuery = ref("");
const modelSearchEl = ref(null);
const modelActive = ref(-1); // 上下键选中的那一项（在**扁平化**的列表里的下标），-1 = 没选

const curModelLabel = computed(() => {
  if (props.loading) return "";
  return curModelLabelOf(modelGroups.value, curModel.value, orphanModel.value);
});

// 过滤 + 分组 + 扁平序号都在 `utils/format.js` 的 `filterModelGroups` 里（那边才测得到"搜到了什么"）。
const filteredGroups = computed(() => filterModelGroups(modelGroups.value, modelQuery.value, orphanModel.value));
const modelFlatCount = computed(() => filteredGroups.value.reduce((n, g) => n + g.items.length, 0));

function toggleModel() {
  modelOpen.value = !modelOpen.value;
  if (!modelOpen.value) return;
  agentOpen.value = false;
  ctxOpen.value = false;
  modelQuery.value = "";
  modelActive.value = -1;
  // 打开就聚焦搜索框：几百个模型，第一次动作必然是打字。
  nextTick(() => modelSearchEl.value?.focus());
}
function pickModel(value) {
  modelOpen.value = false;
  // 点当前那一项 = 什么都不做（别为了一次误点发一个 PATCH）。
  if (value && value !== curModel.value) emit("select-model", value);
}
function flatItemAt(index) {
  for (const g of filteredGroups.value) {
    for (const m of g.items) if (m.flat === index) return m;
  }
  return null;
}
function moveActive(delta) {
  const n = modelFlatCount.value;
  if (!n) return;
  const next = modelActive.value + delta;
  modelActive.value = next < 0 ? 0 : next >= n ? n - 1 : next;
}
function pickActive() {
  const hit = flatItemAt(modelActive.value);
  if (hit) pickModel(hit.value);
}
// 改了搜索词就丢掉上次的高亮（不然回车会选中一个新列表里不存在的位次）。
watch(modelQuery, () => { modelActive.value = -1; });

const leftSvg = ICONS.panelLeft;

// —— Agent 头像 + 选人菜单 ——
const agents = ref([]); // [{id,label}]
const curAgent = ref("");
const agentOpen = ref(false);
const agentItems = computed(() => {
  const list = [...agents.value];
  // 会话绑的 agent 不在列表里（改过名/删了）⇒ 补一条，别让头像"没有对应的人"。
  if (curAgent.value && !list.some((a) => a.id === curAgent.value)) list.unshift({ id: curAgent.value, label: `${curAgent.value}（不在列表）` });
  return list;
});
const curAgentLabel = computed(() => agentItems.value.find((a) => a.id === curAgent.value)?.label ?? curAgent.value ?? "助手");
const agentInitial = computed(() => Array.from(curAgentLabel.value.trim())[0] ?? "?");
// agentHue：id → 色相（同一 id 永远同一个色；纯派生，无存储）。
function agentHue(id) {
  let h = 0;
  for (const ch of String(id ?? "")) h = (h * 31 + ch.codePointAt(0)) % 360;
  return h;
}
function agentDotStyle(id) { return { background: `hsl(${agentHue(id)} 42% 46%)` }; }
function pickAgent(id) { agentOpen.value = false; emit("select-agent", id); }

function syncOrphanModel() {
  const cur = curModel.value;
  const known = cur && modelGroups.value.some((g) => (g.items ?? []).some((i) => i.value === cur));
  const parsed = cur ? parseModelKey(cur) : null;
  // 模型列表还没到时不要造 orphan（那会把 provider/upstream_id 裸串闪到顶栏）。
  orphanModel.value = modelGroups.value.length && cur && !known
    ? { value: cur, label: `${parsed ? `${parsed.provider}/${parsed.model}` : cur}（不在列表）` }
    : null;
}
function setSession(info) {
  if (info.provider && info.model) curModel.value = modelKey(info.provider, info.model);
  if (info.agent_id) curAgent.value = info.agent_id;
  syncOrphanModel();
}
function setModels(groups, cur) { modelGroups.value = groups; if (cur !== undefined) curModel.value = cur; syncOrphanModel(); }
function setAgents(list, cur) { agents.value = list ?? []; if (cur !== undefined) curAgent.value = cur; }

// —— 上下文占比环（2026-10-28 用户定）——
// 从前"上下文 344/1.04M"住在输入框上面那行状态小字里（紧贴着对话）；用户要求它搬这儿：
// 右上角一枚绿色小环，环长 = 已用 / 输入预算。点击掉一块浮板（与 Agent 选人同一族）看明细。
//
// ⚠ 只留**两档**颜色（绿 = 正常 / 红 = 超预算），不做琥珀中间档：环只有 1.8em ≈ 27px，
//   三档色在同一亮度下分不出深浅；而"超预算"这个信号从前在状态行上有，不能悄悄丢掉。
// ⚠ 弧长用 `pathLength="100"` 把周长**归一化**成 100 ⇒ dasharray 直接写百分数，
//   不必在 JS 里算 2πr（r 改了也不用改代码）。
const ctx = ref(null);
const ctxOpen = ref(false);
// 环面板的两个 tab（2026-11-02 用户定）：上下文状态 / 正在运行的任务。
const ctxTab = ref("context");
// 弧长（含"用了就得看得见"的最小弧）在 `utils/format.js` 的 `contextArc` 里 —— 那边才测得到。
const ctxArc = computed(() => contextArc(ctx.value));
// dasharray="a b" 且 a+b=100 ⇒ 正好一道弧、没有第二道。
const ctxDash = computed(() => `${ctxArc.value} ${100 - ctxArc.value}`);
const ctxOver = computed(() => contextOverBudget(ctx.value));
const ctxRows = computed(() => {
  const c = ctx.value;
  if (!c) return [];
  const rows = [
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
  const num = (v) => Number(v ?? 0).toLocaleString("en-US");
  return `输入预算 ${num(c.budget_tokens)} tokens = 上下文上限 ${num(c.ctx_len)} − 输出预留 ${num(c.max_output)}。`;
});
// closeMenus：Esc 一次收起所有顶栏浮板（MD3 menu 的常规行为；从前只有模型菜单接 Esc）。
function closeMenus() { modelOpen.value = false; agentOpen.value = false; ctxOpen.value = false; }
function toggleCtx() { ctxOpen.value = !ctxOpen.value; if (ctxOpen.value) { agentOpen.value = false; modelOpen.value = false; } }
function toggleAgent() { agentOpen.value = !agentOpen.value; if (agentOpen.value) { ctxOpen.value = false; modelOpen.value = false; } }
// setContext：App 每轮 refreshStatusline 喂一次（草稿态传 null）。
function setContext(c) { ctx.value = c ?? null; }

// —— 后台任务（2026-11-01 用户定：「把后台任务也做成右上角那个圆环一样的东西……显示一个数字，
//     按下就在一个小面板里（类似那个绿色的上下文栏）显示有多少后台任务」）——
//
// 小圆里的数字 = **还在跑**的条数（`board.running`；`finished_at` 有值的不算）—— 与从前输入框
// 上方那行小字同一个口径。浮板里给明细：kind / 标题 / 跑了多久 / 成没成。
//
// ⚠ 这格是 `/tasks` 的**唯一**落点（从前输入框上方那行小字**已经删掉**）：同一个数字住两处，
//   只会让两处都可能过期 —— 那是 2026-10-28 状态行搬进环的浮板时定下的规矩，别再犯。
// ⚠ 0 个时环心不显示数字（2026-11-02 用户定），环本身仍在、不转；有任务才出现数字。
const tasks = ref(null);
const taskRunning = computed(() => tasks.value?.running ?? 0);
// 浮板只列**正在跑**的（2026-11-02 用户定：这个 tab 是「正在运行的任务」）。
// 后端已把跑着的排前面；最多 12 条，避免面板被历史记录撑长。
const TASK_ROWS = 12;
const taskRecords = computed(() => (tasks.value?.tasks ?? []).filter((t) => !t.finished_at).slice(0, TASK_ROWS));
// taskElapsed：跑了多久 / 一共跑了多久。`now` 用**后端**那一份（前端时钟与它不一致也照样对）。
function taskElapsed(t) {
  const now = tasks.value?.now ?? Date.now();
  const ms = (now - (t.started_at ?? now));
  return formatDuration(ms);
}
// —— 队列任务（2026-11-01 用户定）——
//
// 一个队列 = 一串**串行**跑的同种原子任务（现在只有压缩）。面板上它是**单独一栏**：
// 队列说"这一大批跑到第几步"，下面的原子任务说"此刻在跑哪一发" —— 两层东西，别塞一个列表。
const queues = computed(() => tasks.value?.queues ?? []);
// 只留还没结束的队列：结束的跑完就消失，别在「正在运行」里冒充活人。
const runningQueues = computed(() => queues.value.filter((q) => !q.finished_at));
const queueRunning = computed(() => runningQueues.value.length);
// 转圈看的是"有没有活在跑"：原子任务、队列，任一都算（队列两步之间那几毫秒也算）。
const taskBusy = computed(() => taskRunning.value > 0 || queueRunning.value > 0);
// 环心数字：>99 收成 "99+"（1.8em 的圆装不下三位数；真数在 title 与浮板里）。
// 队列与它当前那一步原子任务是同一件事，用 max 避免重复计数；队列两步之间也算 1。
const activeTaskCount = computed(() => Math.max(taskRunning.value, queueRunning.value));
const taskCountText = computed(() => (activeTaskCount.value > 99 ? "99+" : String(activeTaskCount.value)));
// 环的可见弧：正常跟上下文占用；有任务在跑时至少留一小段，让"在转"看得见
//（否则占用很低时只有 2/100，或者草稿完全没有弧，转起来跟没转一样）。
const ringDash = computed(() => {
  if (!taskBusy.value) return ctxDash.value;
  // 有任务在跑时至少留一小段可见的弧：占用很低时原本只有 2/100，转起来像没转。
  const arc = Math.max(ctxArc.value, 18);
  return `${arc} ${100 - arc}`;
});
// 环的 title / aria：把"在跑什么"和上下文占用一起说清。
const taskTitle = computed(() => {
  if (!taskBusy.value) return "后台任务：当前无运行任务";
  const q = queueRunning.value ? ` · ${queueRunning.value} 个队列` : "";
  return `后台任务：${taskRunning.value} 个运行中${q}`;
});

// 大类的中文名 —— **只有这一份**。后端给 `group`（llm / plain），前端不按 kind 猜：
// 猜就会与 `task.Kind.Group()` 分岔，而且不报错，只是"某类任务挂到了错的组里"。
const GROUP_LABELS = { llm: "模型调用", plain: "其他任务" };
// 任务种类的人话名（原始 kind 是给机器看的，`refresh_models` 这种不该出现在面板里）。
const TASK_KIND_LABELS = {
  refresh_models: "刷新模型", turn: "对话", compact: "压缩", compact_job: "分段压缩",
  title: "起标题", reroll: "重摇", judgement: "判断",
};
// 复合作业的面板层级：父 Job 一行，子 Task 缩进挂在下面。
// 只按 parent_id 排；不做“猜测式”归并（父不在这一屏就把它当独立行，别丢）。
const taskRows = computed(() => {
  const list = taskRecords.value;
  const ids = new Set(list.map((t) => t.id));
  const children = new Map();
  for (const t of list) {
    if (t.parent_id && ids.has(t.parent_id)) {
      if (!children.has(t.parent_id)) children.set(t.parent_id, []);
      children.get(t.parent_id).push(t);
    }
  }
  const out = [];
  const walk = (t, depth) => {
    out.push({ ...t, depth });
    for (const child of children.get(t.id) ?? []) walk(child, depth + 1);
  };
  for (const t of list) {
    if (!t.parent_id || !ids.has(t.parent_id)) walk(t, 0);
  }
  return out;
});
function taskRoleText(t) {
  if (t.kind === "compact_job") return "Job";
  return t.parent_id ? "任务" : "";
}
// 标题常常已经以种类名开头（"刷新模型 · opendesign"）⇒ 别把同一个词在行首再印一遍。
function taskKindText(t) {
  const label = TASK_KIND_LABELS[t.kind] || "任务";
  return t.title && t.title.startsWith(label) ? "" : label;
}
const taskGroups = computed(() => {
  const buckets = new Map();
  for (const t of taskRows.value) {
    const key = t.group || "plain";
    if (!buckets.has(key)) buckets.set(key, { key, label: GROUP_LABELS[key] || key, items: [] });
    buckets.get(key).items.push(t);
  }
  // LLM 那组排前面：烧钱的那几发比"刷模型"更值得一眼看见。
  return [...buckets.values()].sort((a, b) => (a.key === "llm" ? 0 : 1) - (b.key === "llm" ? 0 : 1));
});

// setTasks：App 轮询 `/tasks` 喂进来（取不到就传 null ⇒ 帽子归 0、浮板说"读不到"）。
function setTasks(board) { tasks.value = board ?? null; }

// —— 连接 / 助手 / 相位（2026-10-28 用户定："已连接 v0.1.0｜默认助手｜idle 这个也塞那个环的面板里"）——
// 从前它是输入框上方那行小字。搬进面板之后**分三行**给（而不是拼成一句话）：
// 面板里每一格都是"标签 → 值"，拼成一行再拆出差错，也没必要（`formatStatusLine` 仍用来做悬停解释）。
const status = ref(null);
const statusRows = computed(() => {
  const s = status.value;
  if (!s) return [];
  const phase = `${phaseText(s.phase)}${s.elapsedMs != null ? ` · ${formatDuration(s.elapsedMs)}` : ""}`;
  return [["连接状态", s.conn || "未连接"], ["当前助手", s.agent || "?"], ["运行状态", phase]];
});
function setStatus(s) { status.value = s ?? null; }
// 悬停解释 = 状态一句 + 预算一句（两处都是"一眼看不清、要解释"的东西）。
const ringTitle = computed(() => [formatStatusLine(status.value ?? {}), contextTitle(ctx.value), taskTitle.value].filter(Boolean).join("\n"));

defineExpose({ setSession, setModels, setAgents, setContext, setStatus, setTasks });
</script>

<template>
  <header @keydown.esc="closeMenus">
    <button
      v-if="leftHidden"
      id="toggle-left"
      class="icon-btn"
      type="button"
      title="显示/隐藏会话栏"
      aria-label="显示/隐藏会话栏"
      @click="$emit('toggle-left')"
      v-html="leftSvg"
    ></button>
    <button
      id="agent-btn"
      class="agent-btn"
      type="button"
      :style="agentDotStyle(curAgent)"
      :title="`当前助手：${curAgentLabel}（点击切换）`"
      aria-label="切换 Agent"
      @click="toggleAgent"
    >{{ agentInitial }}</button>
    <!-- 当前模型（2026-10-28 用户定：换成自定义面板，按渠道分组）。
         外壳 `span` 只管定位（浮板要贴着按钮掉，而不是贴着栏的左内边距）。 -->
    <span class="model-anchor">
      <button
        id="model-btn"
        class="model-btn"
        :class="{ loading }"
        type="button"
        :disabled="loading"
        title="改当前会话的模型"
        aria-label="选择模型"
        :aria-expanded="modelOpen ? 'true' : 'false'"
        @click="toggleModel"
      >
        <span v-if="loading" class="model-loading" aria-hidden="true"></span>
        <span v-else class="model-btn-label">{{ curModelLabel }}</span>
        <svg class="model-caret" viewBox="0 0 10 6" aria-hidden="true"><path d="M0 0h10L5 6z" /></svg>
      </button>
      <template v-if="modelOpen">
        <div class="agent-menu-backdrop" @click="modelOpen = false"></div>
        <div class="agent-menu model-menu" role="dialog" aria-label="选择模型" @keydown.esc="modelOpen = false">
          <input
            ref="modelSearchEl"
            v-model="modelQuery"
            class="model-search"
            type="search"
            placeholder="搜索模型 / 渠道…"
            aria-label="搜索模型"
            @keydown.down.prevent="moveActive(1)"
            @keydown.up.prevent="moveActive(-1)"
            @keydown.enter.prevent="pickActive"
          />
          <div class="model-list">
            <template v-for="g in filteredGroups" :key="g.provider">
              <div class="model-group-head">
                <span class="model-group-name">{{ g.providerName }}</span>
                <span class="model-group-count">{{ g.items.length }}</span>
              </div>
              <button
                v-for="m in g.items"
                :key="m.value"
                type="button"
                class="model-item"
                :class="{ sel: m.value === curModel, active: m.flat === modelActive }"
                :title="`${g.providerName} / ${m.upstream_id || m.label}`"
                @click="pickModel(m.value)"
              >
                <span class="model-name">{{ m.label }}</span>
                <span v-if="m.value === curModel" class="model-check">✓</span>
              </button>
            </template>
            <p v-if="!filteredGroups.length" class="agent-empty">没有匹配的模型</p>
          </div>
        </div>
      </template>
    </span>
    <span class="header-right">
      <!-- 聊天 / 上下文（2026-10-29 用户定）：开关在环左边；环说"占了多少"，这块说"占的是什么"。 -->
      <span class="ws-switch" role="tablist" aria-label="工作区">
        <button
          type="button"
          class="ws-tab"
          role="tab"
          :class="{ sel: workspace === 'chat' }"
          :aria-selected="workspace === 'chat' ? 'true' : 'false'"
          title="对话本身"
          @click="$emit('select-workspace', 'chat')"
        >聊天</button>
        <button
          type="button"
          class="ws-tab"
          role="tab"
          :class="{ sel: workspace === 'context' }"
          :aria-selected="workspace === 'context' ? 'true' : 'false'"
          title="上下文工作区（压缩出来的前情提要、层级、状态、真发出去的载荷）"
          @click="$emit('select-workspace', 'context')"
        >上下文</button>
      </span>
      <!-- 顺序是**环在左、开关在右**（2026-10-28 用户定）：开关是"控制栏的按钮"，
           它是这一簇的锚，位置稳定；环是随状态变的仪表，挂在它左边不抢锚点。 -->
      <!-- 上下文占比环（2026-11-02 合并后台任务）：环长 = 已用 / 输入预算；绿正常、红超预算。
           圆心放**还在跑的后台任务数**；有任务时整个环转起来。点开是同一块面板，
           两个 tab：上下文数字 / 运行任务明细。 -->
      <button
        id="ctx-ring"
        class="ctx-ring"
        :class="{ over: ctxOver, idle: !ctx, busy: taskBusy }"
        type="button"
        :title="ringTitle"
        aria-label="上下文占用与后台任务"
        :aria-expanded="ctxOpen ? 'true' : 'false'"
        @click="toggleCtx"
      >
        <svg viewBox="0 0 32 32" aria-hidden="true">
          <circle class="ctx-ring-track" cx="16" cy="16" r="13" />
          <circle class="ctx-ring-fill" cx="16" cy="16" r="13" pathLength="100" :stroke-dasharray="ringDash" />
        </svg>
        <span v-if="activeTaskCount > 0" class="ctx-ring-n">{{ taskCountText }}</span>
      </button>
    </span>
    <template v-if="agentOpen">
      <div class="agent-menu-backdrop" @click="agentOpen = false"></div>
      <div class="agent-menu" role="menu">
        <button
          v-for="a in agentItems"
          :key="a.id"
          type="button"
          class="agent-item"
          :class="{ sel: a.id === curAgent }"
          @click="pickAgent(a.id)"
        >
          <span class="agent-dot" :style="agentDotStyle(a.id)">{{ Array.from(a.label.trim())[0] ?? "?" }}</span>
          <span class="agent-name">{{ a.label }}</span>
          <span v-if="a.id === curAgent" class="agent-check">✓</span>
        </button>
        <p v-if="!agentItems.length" class="agent-empty">没有可选的 Agent</p>
      </div>
    </template>
    <template v-if="ctxOpen">
      <div class="agent-menu-backdrop" @click="ctxOpen = false"></div>
      <div class="agent-menu ctx-menu" :class="{ over: ctxOver }" role="dialog" aria-label="上下文与任务">
        <div class="ws-switch ctx-tabs" role="tablist" aria-label="上下文与任务">
          <button type="button" class="ws-tab" role="tab" :aria-selected="ctxTab === 'context'"
            :class="{ sel: ctxTab === 'context' }" @click="ctxTab = 'context'">上下文</button>
          <button type="button" class="ws-tab" role="tab" :aria-selected="ctxTab === 'tasks'"
            :class="{ sel: ctxTab === 'tasks' }" @click="ctxTab = 'tasks'">任务<span
              v-if="activeTaskCount > 0" class="ctx-tab-count">{{ taskCountText }}</span></button>
        </div>
        <template v-if="ctxTab === 'context'">
        <template v-if="ctx">
          <div class="ctx-head">
            <b class="ctx-used">已用 {{ formatTokens(ctx.used_tokens) }} / {{ formatTokens(ctx.budget_tokens) }}</b>
            <b class="ctx-pct" :class="{ over: ctxOver }">{{ ctxOver ? "超出预算" : contextPercentText(ctx) }}</b>
          </div>
          <div class="ctx-bar"><i :style="{ width: `${ctxArc}%` }"></i></div>
          <dl class="ctx-rows">
            <div v-for="[k, v] in ctxRows" :key="k" class="ctx-row">
              <dt>{{ k }}</dt>
              <dd>{{ v }}</dd>
            </div>
          </dl>
          <p class="ctx-note">{{ ctxBudgetNote }}</p>
        </template>
        <p v-else class="agent-empty">当前会话暂无上下文数据。</p>
        <dl v-if="statusRows.length" class="ctx-rows ctx-rows-status">
          <div v-for="[k, v] in statusRows" :key="k" class="ctx-row">
            <dt>{{ k }}</dt>
            <dd>{{ v }}</dd>
          </div>
        </dl>
        </template>
        <template v-else>
        <!-- 正在运行的任务（2026-11-02 分 tab）：队列单独一栏，原子任务按大类分组。
             结束的立刻消失 —— 这个 tab 的名字就是"正在运行"。取消按钮只在还能停时画。 -->
        <div class="ctx-tasks">
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
            <template v-if="runningQueues.length">
              <div class="task-group-head">队列</div>
              <ul class="task-list">
                <li
                  v-for="q in runningQueues"
                  :key="q.id"
                  class="task-row task-queue"
                  :class="{ failed: !!q.error }"
                >
                  <span class="task-kind">{{ taskKindText(q) || "队列" }}</span>
                  <span class="task-label" :title="q.title || '队列'">
                    {{ q.title || "队列" }}
                    <b class="task-progress">第 {{ q.index }} / {{ q.total }} 步</b>
                    <span v-if="q.error" class="task-why">{{ q.error }}</span>
                  </span>
                  <button
                    type="button"
                    class="task-cancel"
                    title="取消这个队列（在飞的那一发也一起掐断）"
                    @click.stop="emit('cancel-queue', q.id)"
                  >取消</button>
                </li>
              </ul>
            </template>
            <template v-for="g in taskGroups" :key="g.key">
              <div class="task-group-head">{{ g.label }}</div>
              <ul class="task-list">
                <li
                  v-for="t in g.items"
                  :key="t.id"
                  class="task-row"
                  :class="{ failed: !!t.error, child: t.depth > 0 }"
                >
                  <span v-if="taskRoleText(t)" class="task-role">{{ taskRoleText(t) }}</span>
                  <span v-if="taskKindText(t)" class="task-kind">{{ taskKindText(t) }}</span>
                  <span class="task-label" :title="t.title">{{ t.title || "任务" }}</span>
                  <button
                    v-if="t.cancelable"
                    type="button"
                    class="task-cancel"
                    title="按停这个任务（已经写下的东西不受影响：取消 = 没做过）"
                    @click.stop="emit('cancel-task', t.id)"
                  >取消</button>
                  <span v-else class="task-time">{{ taskElapsed(t) }}</span>
                </li>
              </ul>
            </template>
          </template>
        </div>
        </template>
      </div>
    </template>
  </header>
</template>
