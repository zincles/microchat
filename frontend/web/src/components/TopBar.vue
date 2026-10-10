<script setup>
// 顶栏：会话抬头（新加，左）+ 模型/Agent 下拉（右）+ 左右栏显隐（两端）。
// 抬头是"我在哪条会话"的唯一门面：草稿 ⇒「新对话」、还没起名 ⇒「新会话」、超长省略（hover 全名）。
// 渲染 header（id 照旧：toggle-left/session-model/session-agent/toggle-right）。
// 数据经事件与 App.vue 互通（selectModel/selectAgent/patch 结果回填由父做）。
import { ref, nextTick } from "vue";
import { ICONS } from "./icons.js";
import { modelKey, parseModelKey, sessionTitle } from "../utils/format.js";

defineEmits(["toggle-left", "toggle-right", "select-model", "select-agent"]);

const modelGroups = ref([]); // [{provider, items:[{value,label}]}]
const agents = ref([]); // [{id,label}]
const curModel = ref("");
const curAgent = ref("");
const title = ref("新对话");
const titleFull = ref("");
// 会话绑的模型/Agent 不在列表里（渠道被删、模型下架、agent 没了）⇒ 补一条"不在列表"选项，
// 别让下拉空着 —— 空着看起来像"没选"，其实是有值（显示谎言）。
const orphanModel = ref(null);
const orphanAgent = ref(null);
// select 的选项是异步到的（/models 晚于进会话）：值在选项之前落进 DOM 时会掉（浏览器把
// 选中的 option 换掉后不自动重选，Vue 对"值没变"的绑定也不重放）⇒ 每次落值后补写一次。
const modelSel = ref(null);
const agentSel = ref(null);

const leftSvg = ICONS.panelLeft;
const rightSvg = ICONS.panelRight;

function reapplyValues() {
  nextTick(() => {
    if (modelSel.value && curModel.value) modelSel.value.value = curModel.value;
    if (agentSel.value && curAgent.value) agentSel.value.value = curAgent.value;
  });
}

function syncOrphanModel() {
  const cur = curModel.value;
  const known = cur && modelGroups.value.some((g) => (g.items ?? []).some((i) => i.value === cur));
  const parsed = cur ? parseModelKey(cur) : null;
  orphanModel.value = cur && !known ? { value: cur, label: `${parsed ? `${parsed.provider}/${parsed.model}` : cur}（不在列表）` } : null;
}
function syncOrphanAgent() {
  const cur = curAgent.value;
  const known = cur && agents.value.some((a) => a.id === cur);
  orphanAgent.value = cur && !known ? { value: cur, label: `${cur}（不在列表）` } : null;
}
function setSession(info, isDraft) {
  if (info.provider && info.model) curModel.value = modelKey(info.provider, info.model);
  if (info.agent_id) curAgent.value = info.agent_id;
  title.value = sessionTitle(info.title, isDraft);
  titleFull.value = (info.title ?? "").trim();
  syncOrphanModel();
  syncOrphanAgent();
  reapplyValues();
}
function setModels(groups, cur) { modelGroups.value = groups; if (cur !== undefined) curModel.value = cur; syncOrphanModel(); reapplyValues(); }
function setAgents(list, cur) { agents.value = list; if (cur !== undefined) curAgent.value = cur; syncOrphanAgent(); reapplyValues(); }

defineExpose({ setSession, setModels, setAgents });
</script>

<template>
  <header>
    <button
      id="toggle-left"
      class="icon-btn"
      type="button"
      title="显示/隐藏会话栏"
      aria-label="显示/隐藏会话栏"
      @click="$emit('toggle-left')"
      v-html="leftSvg"
    ></button>
    <span id="session-title" class="session-title" :title="titleFull">{{ title }}</span>
    <select
      id="session-model"
      ref="modelSel"
      class="pill-select"
      title="改当前会话的模型"
      :value="curModel"
      @change="$emit('select-model', $event.target.value)"
    >
      <option v-if="orphanModel" :value="orphanModel.value">{{ orphanModel.label }}</option>
      <optgroup v-for="g in modelGroups" :key="g.provider" :label="g.provider">
        <option v-for="m in g.items" :key="m.value" :value="m.value">{{ m.label }}</option>
      </optgroup>
    </select>
    <select
      id="session-agent"
      ref="agentSel"
      class="pill-select"
      title="改当前会话的 Agent"
      :value="curAgent"
      @change="$emit('select-agent', $event.target.value)"
    >
      <option v-if="orphanAgent" :value="orphanAgent.value">{{ orphanAgent.label }}</option>
      <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.label }}</option>
    </select>
    <button
      id="toggle-right"
      class="icon-btn"
      type="button"
      title="显示/隐藏载荷栏"
      aria-label="显示/隐藏载荷栏"
      @click="$emit('toggle-right')"
      v-html="rightSvg"
    ></button>
  </header>
</template>
