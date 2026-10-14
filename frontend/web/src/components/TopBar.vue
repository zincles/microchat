<script setup>
// 顶栏（2026-10-14 用户定，去壳版）：**全透明**，左上角 = **圆头像（当前 Agent，点开换）** + **当前模型平铺字**；
// 右侧只在某一侧栏收着时才出它的开关（栏开着 ⇒ 开关在栏自己头上，见 App 的 applySideToggle）。
//
// 头像（2026-10-14 用户定）：圆形小图标就是**新的 Agent 入口** —— 里头是名字首字、底色由 id 派生
// （同一个人永远同一个色）；点开掉出浮板选人。状态行小字里那个助手名管"显示"，头像管"选"。
// 会话抬头不在顶栏（左栏列表高亮 + 状态行都有它）。
import { ref, computed, nextTick } from "vue";
import { ICONS } from "./icons.js";
import { modelKey, parseModelKey } from "../utils/format.js";

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

defineExpose({ setSession, setModels, setAgents });
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
      @click="agentOpen = !agentOpen"
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
  </header>
</template>
