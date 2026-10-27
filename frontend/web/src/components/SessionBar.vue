<script setup>
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条：标题 + 设置（三横杠 ☰，改名/模型/agent）+ 关闭（×，全屏确认后删）。
import { ref } from "vue";
import { ICONS } from "./icons.js";

defineEmits(["select", "settings", "close", "new", "open-settings", "user", "toggle-left"]);
// canSettings：**设置 = root 专属**（用户定，2026-10-10）—— 普通用户这儿连按钮都不给。
const props = defineProps({ canSettings: { type: Boolean, default: true } });

const sessions = ref([]);
const currentId = ref(null);

function setSessions(list, cur) { sessions.value = list ?? []; currentId.value = cur ?? null; }

defineExpose({ setSessions });
</script>

<template>
  <aside id="sessions-bar">
    <div class="bar-head">
      <span>MicroChat</span>
      <button
        id="toggle-left"
        class="icon-btn"
        type="button"
        title="收起会话栏"
        aria-label="收起会话栏"
        @click="$emit('toggle-left')"
        v-html="ICONS.panelLeft"
      ></button>
    </div>
    <button id="session-new" type="button" title="开启新对话（草稿：首句才建会话）" @click="$emit('new')">
      <span class="plus">＋</span>开启新对话
    </button>
    <div id="session-list">
      <div v-if="!sessions.length" class="empty">还没有会话<br /><span class="sub">点上面「＋ 开启新对话」开始</span></div>
      <div
        v-for="s in sessions"
        :key="s.id"
        class="session-item"
        :class="{ sel: s.id === currentId }"
        @click="$emit('select', s.id)"
      >
        <span class="title">{{ s.title || "新会话" }}</span>
        <button
          class="item-btn"
          type="button"
          title="会话设置"
          aria-label="会话设置"
          @click.stop="$emit('settings', s.id)"
          v-html="ICONS.sliders"
        ></button>
        <button
          class="close"
          type="button"
          title="关闭会话"
          @click.stop="$emit('close', s.id)"
        >
          ×
        </button>
      </div>
    </div>
    <div class="bar-foot">
      <button
        id="user-open"
        class="foot-btn"
        type="button"
        title="用户（占位入口：点了会说还没做）"
        @click="$emit('user')"
      >
        <span class="fb-icon" v-html="ICONS.user"></span>用户
      </button>
      <button
        v-if="props.canSettings"
        id="settings-open"
        class="foot-btn"
        type="button"
        title="设置（root 专属）"
        @click="$emit('open-settings')"
      >
        <span class="fb-icon" v-html="ICONS.settings"></span>设置
      </button>
    </div>
  </aside>
</template>
