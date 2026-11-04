<script setup>
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条：标题 + 设置（三横杠 ☰，改名/模型/agent）+ 关闭（×，全屏确认后删）。
import { ref } from "vue";
import { ICONS } from "./icons.js";

defineEmits(["select", "settings", "close", "new", "open-settings", "toggle-left", "open-sessions", "open-providers"]);

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
        <span class="session-main">
          <span class="title">{{ s.title || "新会话" }}</span>
        </span>
        <button
          class="item-btn"
          type="button"
          title="会话设置"
          aria-label="会话设置"
          @click.stop="$emit('settings', s.id)"
          v-html="ICONS.settings"
        ></button>
        <button
          class="close"
          type="button"
          title="关闭会话"
          @click.stop="$emit('close', s.id)"
          v-html="ICONS.x"
        ></button>
      </div>
    </div>
    <div class="bar-foot">
      <!-- 管理入口（2026-11-02 用户定）：会话 / 渠道从设置里拆出来，各有一个独立入口；
           它们与「设置」共用同一个视图，只是不经过设置 tab。 -->
      <button
        id="sessions-manage"
        class="foot-btn"
        type="button"
        title="会话管理（新建默认值 / 列表 / 导入）"
        @click="$emit('open-sessions')"
      >
        <span class="fb-icon" v-html="ICONS.chat"></span>会话
      </button>
      <button
        id="providers-manage"
        class="foot-btn"
        type="button"
        title="渠道管理（端点 / 密钥 / 模型筛选）"
        @click="$emit('open-providers')"
      >
        <span class="fb-icon" v-html="ICONS.server"></span>渠道
      </button>
      <!-- 设置：2026-11-01 起**没有"root 专属"这回事**（账号砍了），谁都能进；
           外观那一区在同一天从删掉的用户视图搬了进来。 -->
      <button
        id="settings-open"
        class="foot-btn"
        type="button"
        title="设置（聊天 / 智能体 / 提示词 / 外观）"
        @click="$emit('open-settings')"
      >
        <span class="fb-icon" v-html="ICONS.settings"></span>设置
      </button>
    </div>
  </aside>
</template>
