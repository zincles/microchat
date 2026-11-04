<script setup>
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条只有一枚「选项」（竖三点）：设置与删除都收进那块浮板 —— 行内不再挤两枚键。
import { ref, computed, nextTick, onMounted, onUnmounted } from "vue";
import { ICONS } from "./icons.js";
import { groupSessions } from "../utils/sessionGroups.js";

const emit = defineEmits(["select", "settings", "copy", "export", "close", "new", "open-settings", "toggle-left", "open-sessions", "open-providers"]);

const sessions = ref([]);
const currentId = ref(null);
const groupedSessions = computed(() => groupSessions(sessions.value));
const menu = ref(null); // { id, left, top } —— Teleport 到 body 的浮板
const menuEl = ref(null);

function setSessions(list, cur) { sessions.value = list ?? []; currentId.value = cur ?? null; }

function closeMenu() { menu.value = null; }
function toggleMenu(s, event) {
  event.stopPropagation();
  if (menu.value?.id === s.id) { closeMenu(); return; }
  const rect = event.currentTarget.getBoundingClientRect();
  const width = 168;
  const height = 142;
  const left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 8));
  const top = rect.bottom + 4 + height > window.innerHeight - 8 ? rect.top - height - 4 : rect.bottom + 4;
  menu.value = { id: s.id, left, top };
  nextTick(() => menuEl.value?.querySelector("button")?.focus());
}
function pickSettings() {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("settings", id);
}
function pickCopy() {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("copy", id);
}
function pickExport() {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("export", id);
}
function pickDelete() {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("close", id);
}
function onKey(e) { if (e.key === "Escape") closeMenu(); }

onMounted(() => {
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", closeMenu, true);
});
onUnmounted(() => {
  document.removeEventListener("keydown", onKey);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("scroll", closeMenu, true);
});

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
      <template v-for="group in groupedSessions" :key="group.key">
        <div class="session-group-head">{{ group.label }}</div>
        <div
          v-for="s in group.items"
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
            title="会话选项"
            aria-label="会话选项"
            :aria-expanded="menu?.id === s.id ? 'true' : 'false'"
            @click="toggleMenu(s, $event)"
            v-html="ICONS.more"
          ></button>
        </div>
      </template>
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
  <Teleport to="body">
    <div v-if="menu" class="session-menu-backdrop" @click="closeMenu"></div>
    <div
      v-if="menu"
      ref="menuEl"
      class="session-menu"
      role="menu"
      :style="{ left: menu.left + 'px', top: menu.top + 'px' }"
      @click.stop
    >
      <button type="button" role="menuitem" @click="pickSettings">会话设置</button>
      <button type="button" role="menuitem" @click="pickCopy">创建副本</button>
      <button type="button" role="menuitem" @click="pickExport">导出 JSON</button>
      <button type="button" role="menuitem" class="danger" @click="pickDelete">删除会话</button>
    </div>
  </Teleport>
</template>
