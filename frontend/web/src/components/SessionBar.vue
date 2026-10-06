<script setup>
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条：标题 + 设置（三横杠 ☰，改名/模型/agent）+ 关闭（×，全屏确认后删）。
import { ref } from "vue";
import { ICONS } from "./icons.js";

const emit = defineEmits(["select", "settings", "close", "new", "open-settings"]);

const sessions = ref([]);
const currentId = ref(null);

function setSessions(list, cur) { sessions.value = list ?? []; currentId.value = cur ?? null; }

defineExpose({ setSessions });
</script>

<template>
  <aside id="sessions-bar">
    <div class="bar-head">
      <span>会话</span>
      <button id="session-new" type="button" title="新建会话" @click="$emit('new')">＋</button>
    </div>
    <div id="session-list">
      <div v-if="!sessions.length" class="empty">还没有会话</div>
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
        id="settings-open"
        class="foot-btn"
        type="button"
        title="设置"
        @click="$emit('open-settings')"
      >
        ⚙ 设置
      </button>
    </div>
  </aside>
</template>
