<script setup>
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条：标题 + 设置（三横杠 ☰，改名/模型/agent）+ 关闭（×，全屏确认后删）。
import { ref } from "vue";
import { ICONS } from "./icons.js";
import { relativeTime } from "../utils/format.js";

defineEmits(["select", "settings", "close", "new", "open-settings", "toggle-left", "install"]);
defineProps({
  // 浏览器是否已给出 beforeinstallprompt（能一键装）；standalone = 已经是装好的窗口。
  installable: { type: Boolean, default: false },
  standalone: { type: Boolean, default: false },
});

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
          <!-- 第二行（2026-11-02）：条数 + 最近活动。翻十条只看得见标题时，
               根本分不出哪条是刚才那条 —— 时间比标题更能定位。 -->
          <span class="meta">{{ s.messages ?? 0 }} 条<span class="dot">·</span>{{ relativeTime(s.updated_at) }}</span>
        </span>
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
      <!-- 安装入口（2026-11-02）：浏览器给了 beforeinstallprompt 就是「一键装」；
           没给（iOS / 局域网 HTTP / 浏览器没入口）就是「安装说明」，点开把原因说清楚。
           放在设置上面：它是"把这个应用带走"的动作，不是设置。 -->
      <button
        v-if="!standalone"
        id="install-app"
        class="foot-btn"
        type="button"
        :title="installable ? '安装 MicroChat 到桌面' : '如何把 MicroChat 安装为应用'"
        @click="$emit('install')"
      >
        <span class="fb-icon" v-html="ICONS.download"></span>{{ installable ? "安装应用" : "安装说明" }}
      </button>
      <!-- 设置：2026-11-01 起**没有"root 专属"这回事**（账号砍了），谁都能进；
           外观那一区在同一天从删掉的用户视图搬了进来。 -->
      <button
        id="settings-open"
        class="foot-btn"
        type="button"
        title="设置（渠道 / 智能体 / 提示词 / 外观）"
        @click="$emit('open-settings')"
      >
        <span class="fb-icon" v-html="ICONS.settings"></span>设置
      </button>
    </div>
  </aside>
</template>
