<script setup>
// 顶栏（2026-10-14 用户定，去壳版）：**全透明**，左上角 = **圆头像（当前 Agent，点开换）** + **当前模型平铺字**；
// 右侧只在某一侧栏收着时才出它的开关（栏开着 ⇒ 开关在栏自己头上，见 App 的 applySideToggle）。
//
// 头像（2026-10-14 用户定）：圆形小图标就是**新的 Agent 入口** —— 里头是名字首字、底色由 id 派生
// （同一个人永远同一个色）；点开掉出浮板选人。状态行小字里那个助手名管"显示"，头像管"选"。
// 会话抬头不在顶栏（左栏列表高亮 + 状态行都有它）。
import { ref, computed, nextTick } from "vue";
import { ICONS } from "./icons.js";
import { modelKey, parseModelKey, formatTokens, formatStatusLine, contextArc, contextPercentText, contextTitle, contextOverBudget } from "../utils/format.js";

defineProps({
  // leftHidden / rightHidden：那一侧栏收着 ⇒ 顶栏出它那枚开关；栏开着 ⇒ 开关在栏自己头上
  // （按钮跟着它控制的东西走）。
  leftHidden: { type: Boolean, default: false },
  rightHidden: { type: Boolean, default: false },
});
const emit = defineEmits(["toggle-left", "toggle-right", "select-model", "select-agent"]);

const modelGroups = ref([]); // [{provider, items:[{value,label}]}]
const curModel = ref("");
// 会话绑的模型不在列表里（渠道被删、模型下架）⇒ 补一条"不在列表"选项，别让下拉空着
// —— 空着看起来像"没选"，其实是有值（显示谎言）。
const orphanModel = ref(null);
// select 的选项是异步到的（/models 晚于进会话）：值在选项之前落进 DOM 时会掉（浏览器把
// 选中的 option 换掉后不自动重选，Vue 对"值没变"的绑定也不重放）⇒ 每次落值后补写一次。
const modelSel = ref(null);

const leftSvg = ICONS.panelLeft;
const rightSvg = ICONS.panelRight;

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

function reapplyValues() {
  nextTick(() => {
    if (modelSel.value && curModel.value) modelSel.value.value = curModel.value;
  });
}

function syncOrphanModel() {
  const cur = curModel.value;
  const known = cur && modelGroups.value.some((g) => (g.items ?? []).some((i) => i.value === cur));
  const parsed = cur ? parseModelKey(cur) : null;
  orphanModel.value = cur && !known ? { value: cur, label: `${parsed ? `${parsed.provider}/${parsed.model}` : cur}（不在列表）` } : null;
}
function setSession(info) {
  if (info.provider && info.model) curModel.value = modelKey(info.provider, info.model);
  if (info.agent_id) curAgent.value = info.agent_id;
  syncOrphanModel();
  reapplyValues();
}
function setModels(groups, cur) { modelGroups.value = groups; if (cur !== undefined) curModel.value = cur; syncOrphanModel(); reapplyValues(); }
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
// 弧长（含"用了就得看得见"的最小弧）在 `utils/format.js` 的 `contextArc` 里 —— 那边才测得到。
const ctxArc = computed(() => contextArc(ctx.value));
// dasharray="a b" 且 a+b=100 ⇒ 正好一道弧、没有第二道。
const ctxDash = computed(() => `${ctxArc.value} ${100 - ctxArc.value}`);
const ctxOver = computed(() => contextOverBudget(ctx.value));
const ctxRows = computed(() => {
  const c = ctx.value;
  if (!c) return [];
  const num = (v) => Number(v ?? 0).toLocaleString("en-US");
  const rows = [
    ["输入预算", `${num(c.budget_tokens)} = 上下文 ${num(c.ctx_len)} − 输出预留 ${num(c.max_output)}`],
    ["剩余", formatTokens(c.remaining_tokens)],
    ["压缩触发", c.trigger_tokens != null ? `${formatTokens(c.trigger_tokens)}（到这儿自动压）` : "没开（手动 /compact）"],
  ];
  // 上一次真发出去的 prompt_tokens（上游报的实数）与估算并排 —— 估算偏了看得见。
  if (c.last_prompt_tokens != null) rows.push(["上一发实测", `${formatTokens(c.last_prompt_tokens)}（这格是估的：${formatTokens(c.used_tokens)}）`]);
  return rows;
});
function toggleCtx() { ctxOpen.value = !ctxOpen.value; if (ctxOpen.value) agentOpen.value = false; }
function toggleAgent() { agentOpen.value = !agentOpen.value; if (agentOpen.value) ctxOpen.value = false; }
// setContext：App 每轮 refreshStatusline 喂一次（草稿态传 null）。
function setContext(c) { ctx.value = c ?? null; }

// —— 连接 / 助手 / 相位（2026-10-28 用户定："已连接 v0.1.0｜默认助手｜idle 这个也塞那个环的面板里"）——
// 从前它是输入框上方那行小字。搬进面板之后**分三行**给（而不是拼成一句话）：
// 面板里每一格都是"标签 → 值"，拼成一行再拆出差错，也没必要（`formatStatusLine` 仍用来做悬停解释）。
const status = ref(null);
const statusRows = computed(() => {
  const s = status.value;
  if (!s) return [];
  const phase = `${s.phase ?? "idle"}${s.elapsedMs != null ? ` · ${s.elapsedMs}ms` : ""}`;
  return [["连接", s.conn || "未连接"], ["助手", s.agent || "?"], ["运行", phase]];
});
function setStatus(s) { status.value = s ?? null; }
// 悬停解释 = 状态一句 + 预算一句（两处都是"一眼看不清、要解释"的东西）。
const ringTitle = computed(() => [formatStatusLine(status.value ?? {}), contextTitle(ctx.value)].filter(Boolean).join("\n"));

defineExpose({ setSession, setModels, setAgents, setContext, setStatus });
</script>

<template>
  <header>
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
    <select
      id="session-model"
      ref="modelSel"
      class="flat-select"
      title="改当前会话的模型"
      :value="curModel"
      @change="$emit('select-model', $event.target.value)"
    >
      <option v-if="orphanModel" :value="orphanModel.value">{{ orphanModel.label }}</option>
      <optgroup v-for="g in modelGroups" :key="g.provider" :label="g.provider">
        <option v-for="m in g.items" :key="m.value" :value="m.value">{{ m.label }}</option>
      </optgroup>
    </select>
    <span class="header-right">
      <!-- 顺序是**环在左、开关在右**（2026-10-28 用户定）：开关是"控制栏的按钮"，
           它是这一簇的锚，位置稳定；环是随状态变的仪表，挂在它左边不抢锚点。 -->
      <!-- 上下文占比环：环长 = 已用 / 输入预算；绿正常、红超预算。点开看明细。 -->
      <button
        id="ctx-ring"
        class="ctx-ring"
        :class="{ over: ctxOver, idle: !ctx }"
        type="button"
        :title="ringTitle"
        aria-label="上下文占用"
        :aria-expanded="ctxOpen ? 'true' : 'false'"
        @click="toggleCtx"
      >
        <svg viewBox="0 0 32 32" aria-hidden="true">
          <circle class="ctx-ring-track" cx="16" cy="16" r="13" />
          <circle class="ctx-ring-fill" cx="16" cy="16" r="13" pathLength="100" :stroke-dasharray="ctxDash" />
        </svg>
      </button>
      <button
        v-if="rightHidden"
        id="toggle-right"
        class="icon-btn"
        type="button"
        title="显示/隐藏载荷栏"
        aria-label="显示/隐藏载荷栏"
        @click="$emit('toggle-right')"
        v-html="rightSvg"
      ></button>
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
      <div class="agent-menu ctx-menu" :class="{ over: ctxOver }" role="dialog" aria-label="上下文与运行状态">
        <template v-if="ctx">
          <div class="ctx-head">
            <b class="ctx-used">{{ formatTokens(ctx.used_tokens) }}<span class="ctx-sep">/</span>{{ formatTokens(ctx.budget_tokens) }}</b>
            <b class="ctx-pct" :class="{ over: ctxOver }">{{ contextPercentText(ctx) }}</b>
          </div>
          <div class="ctx-bar"><i :style="{ width: `${ctxArc}%` }"></i></div>
          <dl class="ctx-rows">
            <div v-for="[k, v] in ctxRows" :key="k" class="ctx-row">
              <dt>{{ k }}</dt>
              <dd>{{ v }}</dd>
            </div>
          </dl>
          <p class="ctx-note">分母是输入预算（上下文减去输出预留），不是模型名义上下文。</p>
        </template>
        <p v-else class="agent-empty">这是新对话，还没有可统计的上下文。</p>
        <dl v-if="statusRows.length" class="ctx-rows ctx-rows-status">
          <div v-for="[k, v] in statusRows" :key="k" class="ctx-row">
            <dt>{{ k }}</dt>
            <dd>{{ v }}</dd>
          </div>
        </dl>
      </div>
    </template>
  </header>
</template>
