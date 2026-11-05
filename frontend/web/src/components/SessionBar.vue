<script setup>
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条只有一枚「选项」（竖三点）：设置与删除都收进那块浮板 —— 行内不再挤两枚键。
import { ref, computed, nextTick, onMounted, onUnmounted } from "vue";
import { ICONS } from "./icons.js";
import { groupSessions } from "../utils/sessionGroups.js";

const emit = defineEmits(["select", "settings", "copy", "export", "close", "new", "open-settings", "toggle-left", "open-sessions", "open-providers", "prefetch"]);

// busyIds：App 从 `/tasks` 那一拍算出来的"此刻有任务在跑的会话"（判定在 `utils/tasks.js`）。
// 行内环只吃这一处 —— 别在组件里再 filter 一遍任务表（两处判据一分岔，同一时刻会显示两个答案）。
const props = defineProps({ busyIds: { type: Array, default: () => [] } });
const busySet = computed(() => new Set(props.busyIds ?? []));

const sessions = ref([]);
const currentId = ref(null);
// 左栏顶部的两个空白：对话 / 生图。生图暂未接 API，切过去只改选中态（未来这里放生图列表）。
const mode = ref("chat");
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

// 预取（2026-11-05）：鼠标在会话行上**停 140ms** 才算"想点它"（扫过列表不算），
// 触屏 touchstart / 键盘 focus 立即 —— 这三种之后紧跟着的多半就是点击。
// 真正的去重、并发上限、缓存判断都在 App 的 prefetchSession 里。
let hoverTimer = null;
function prefetchHover(s, e) {
  if (e?.pointerType && e.pointerType !== "mouse") return;
  clearTimeout(hoverTimer);
  hoverTimer = setTimeout(() => emit("prefetch", s.id), 140);
}
function prefetchNow(s) {
  clearTimeout(hoverTimer);
  emit("prefetch", s.id);
}
function prefetchEnd() { clearTimeout(hoverTimer); }

onMounted(() => {
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", closeMenu, true);
});
onUnmounted(() => {
  clearTimeout(hoverTimer);
  document.removeEventListener("keydown", onKey);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("scroll", closeMenu, true);
});

defineExpose({ setSessions });
</script>

<template>
  <aside id="sessions-bar">
    <div class="bar-head">
      <div class="session-mode-tabs" role="tablist" aria-label="左栏模式">
        <button type="button" class="session-mode-tab" :class="{ sel: mode === 'chat' }"
          role="tab" :aria-selected="mode === 'chat' ? 'true' : 'false'" @click="mode = 'chat'">对话</button>
        <button type="button" class="session-mode-tab" :class="{ sel: mode === 'image' }"
          role="tab" :aria-selected="mode === 'image' ? 'true' : 'false'" title="生图（暂未接入）"
          @click="mode = 'image'">生图</button>
      </div>
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
          @pointerenter="prefetchHover(s, $event)"
          @pointerleave="prefetchEnd"
          @focusin="prefetchNow(s)"
          @touchstart.passive="prefetchNow(s)"
        >
          <span class="session-main">
            <span class="title">{{ s.title || "新会话" }}</span>
          </span>
          <!-- 行内环：这条会话此刻有任务在跑（生成 / 压缩 / 重摇 / 起标题 / 队列踩到它）。
               与「选项」键并存 —— 它是状态，不是入口，所以不抢那枚按钮的位置。 -->
          <span
            v-if="busySet.has(s.id)"
            class="session-busy"
            role="status"
            title="正在运行任务"
            aria-label="正在运行任务"
          ></span>
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
