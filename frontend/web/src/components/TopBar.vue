<script setup>
// 顶栏：左显隐会话栏 + Agent 下拉 + 右显隐载荷栏（连接/模型/会话名在左栏与状态行，不在这里）。
// 挂载后接管 header（id 照旧：toggle-left/session-agent/toggle-right）。
// 数据经事件与 main.js 互通（selectModel/selectAgent/patch 结果回填由父做）。
import { ref } from "vue";
import { ICONS } from "./icons.js";

const emit = defineEmits(["toggle-left", "toggle-right", "select-agent"]);

const agents = ref([]); // [{id,label}]
const curAgent = ref("");

const leftSvg = ICONS.panelLeft;
const rightSvg = ICONS.panelRight;

function setSession(info) {
  if (info.agent_id) curAgent.value = info.agent_id;
}
function setAgents(list, cur) { agents.value = list; if (cur !== undefined) curAgent.value = cur; }

defineExpose({ setSession, setAgents });
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
    <select
      id="session-agent"
      class="pill-select"
      title="改当前会话的 Agent"
      :value="curAgent"
      @change="$emit('select-agent', $event.target.value)"
    >
      <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.label }}</option>
    </select>
    <span class="head-spacer"></span>
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
