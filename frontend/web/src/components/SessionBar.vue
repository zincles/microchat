<script setup lang="ts">
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条只有一枚「选项」（竖三点）：设置与删除都收进那块浮板 —— 行内不再挤两枚键。
import { ref, computed, nextTick, onMounted, onUnmounted } from "vue";
import { VBtn, VBtnToggle } from "vuetify/components";
import { ICONS } from "./icons.ts";
import { groupSessions } from "../utils/sessionGroups.ts";
import type { SessionView } from "../api/types.ts";

const emit = defineEmits<{
  select: [id: string];
  settings: [id: string];
  copy: [id: string];
  export: [id: string];
  close: [id: string];
  new: [];
  "open-settings": [];
  "toggle-left": [];
  "open-sessions": [];
  "open-providers": [];
  prefetch: [id: string];
}>();

// busyIds：App 从 `/tasks` 那一拍算出来的"此刻有任务在跑的会话"（判定在 `utils/tasks.ts`）。
// 行内环只吃这一处 —— 别在组件里再 filter 一遍任务表（两处判据一分岔，同一时刻会显示两个答案）。
const props = withDefaults(defineProps<{
  busyIds?: string[];
  // loading：首次拿会话列表（boot）—— 画骨架，别显示"还没有会话"（那是假话）。
  loading?: boolean;
  // refreshing：后台在补一次列表（10 秒保鲜期外）—— 顶部一条 MD3 linear progress。
  refreshing?: boolean;
}>(), {
  busyIds: () => [],
  loading: false,
  refreshing: false,
});
const busySet = computed(() => new Set(props.busyIds ?? []));
// 骨架行的宽度（错落一点，别像一张表格）。
const skeletonWidths = ["72%", "54%", "64%", "46%", "58%", "50%"];

const sessions = ref<SessionView[]>([]);
const currentId = ref<string | null>(null);
// 左栏顶部的两个空白：对话 / 生图。生图暂未接 API，切过去只改选中态（未来这里放生图列表）。
const mode = ref<"chat" | "image">("chat");
const groupedSessions = computed(() => groupSessions(sessions.value));
const menu = ref<{ id: string; left: number; top: number } | null>(null); // Teleport 到 body 的浮板
const menuEl = ref<HTMLElement | null>(null);

function setSessions(list: SessionView[] | null | undefined, cur?: string | null): void {
  sessions.value = list ?? [];
  currentId.value = cur ?? null;
}

function closeMenu(): void { menu.value = null; }
function toggleMenu(s: SessionView, event: MouseEvent): void {
  event.stopPropagation();
  if (menu.value?.id === s.id) { closeMenu(); return; }
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  const width = 168;
  const height = 142;
  const left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 8));
  const top = rect.bottom + 4 + height > window.innerHeight - 8 ? rect.top - height - 4 : rect.bottom + 4;
  menu.value = { id: s.id, left, top };
  nextTick(() => menuEl.value?.querySelector<HTMLButtonElement>("button")?.focus());
}
function pickSettings(): void {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("settings", id);
}
function pickCopy(): void {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("copy", id);
}
function pickExport(): void {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("export", id);
}
function pickDelete(): void {
  const id = menu.value?.id;
  closeMenu();
  if (id) emit("close", id);
}
function onKey(e: KeyboardEvent): void { if (e.key === "Escape") closeMenu(); }

// 预取（2026-11-05）：鼠标在会话行上**停 140ms** 才算"想点它"（扫过列表不算），
// 触屏 touchstart / 键盘 focus 立即 —— 这三种之后紧跟着的多半就是点击。
// 真正的去重、并发上限、缓存判断都在 App 的 prefetchSession 里。
let hoverTimer: ReturnType<typeof setTimeout> | null = null;
function prefetchHover(s: SessionView, e?: PointerEvent): void {
  if (e?.pointerType && e.pointerType !== "mouse") return;
  clearTimeout(hoverTimer ?? undefined);
  hoverTimer = setTimeout(() => emit("prefetch", s.id), 140);
}
function prefetchNow(s: SessionView): void {
  clearTimeout(hoverTimer ?? undefined);
  emit("prefetch", s.id);
}
function prefetchEnd(): void { clearTimeout(hoverTimer ?? undefined); }

onMounted(() => {
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", closeMenu, true);
});
onUnmounted(() => {
  clearTimeout(hoverTimer ?? undefined);
  document.removeEventListener("keydown", onKey);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("scroll", closeMenu, true);
});

defineExpose({ setSessions });
</script>

<template>
  <aside id="sessions-bar">
    <div class="bar-head">
      <!-- Brand Title（2026-11-06 用户定）：原来放大 tab 的位置换作品牌；
           下面依次是新对话 → 对话/生图 → 会话列表（Gemini 式左侧栏）。 -->
      <span class="brand">
        <span class="brand-mark" aria-hidden="true" v-html="ICONS.spark"></span>
        <span class="brand-name">MicroChat</span>
      </span>
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
    <div v-if="refreshing" class="md-linear-progress" role="progressbar" aria-label="正在刷新会话列表"><span></span></div>
    <button id="session-new" type="button" title="开启新对话（草稿：首句才建会话）" @click="$emit('new')">
      <span class="plus" aria-hidden="true" v-html="ICONS.plus"></span><span class="label">开启新对话</span>
    </button>
    <!-- 左栏模式切换，不是内容 tab：生图面板还没接，切过去只是选中态。
         用 VBtnToggle 做全宽分段按钮；等生图有独立列表再升级成 VTabs。 -->
    <VBtnToggle
      v-model="mode"
      class="session-mode-tabs"
      mandatory
      divided
      density="compact"
      color="primary"
      role="group"
      aria-label="左栏模式"
    >
      <VBtn value="chat" title="对话" :aria-pressed="mode === 'chat'">对话</VBtn>
      <VBtn value="image" title="生图（暂未接入）" :aria-pressed="mode === 'image'">生图</VBtn>
    </VBtnToggle>
    <div id="session-list" :aria-busy="loading && !sessions.length ? 'true' : 'false'">
      <div v-if="loading && !sessions.length" class="session-skeleton">
        <div v-for="(w, i) in skeletonWidths" :key="i" class="skeleton-row" :style="{ '--w': w }"></div>
      </div>
      <div v-else-if="!sessions.length" class="empty">还没有会话<br /><span class="sub">点上面「＋ 开启新对话」开始</span></div>
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
          <svg
            v-if="busySet.has(s.id)"
            class="session-busy"
            viewBox="0 0 24 24"
            role="status"
            aria-label="正在运行任务"
          >
            <title>正在运行任务</title>
            <circle class="session-busy-track" cx="12" cy="12" r="9"></circle>
            <circle class="session-busy-arc" cx="12" cy="12" r="9"></circle>
          </svg>
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
      <VBtn
        id="sessions-manage"
        class="bar-action"
        type="button"
        variant="text"
        color="on-surface-variant"
        stacked
        title="会话管理（新建默认值 / 列表 / 导入）"
        @click="$emit('open-sessions')"
      >
        <template #prepend><span class="fb-icon" v-html="ICONS.chat"></span></template>
        会话
      </VBtn>
      <VBtn
        id="providers-manage"
        class="bar-action"
        type="button"
        variant="text"
        color="on-surface-variant"
        stacked
        title="渠道管理（端点 / 密钥 / 模型筛选）"
        @click="$emit('open-providers')"
      >
        <template #prepend><span class="fb-icon" v-html="ICONS.server"></span></template>
        渠道
      </VBtn>
      <!-- 设置：2026-11-01 起**没有"root 专属"这回事**（账号砍了），谁都能进；
           外观那一区在同一天从删掉的用户视图搬了进来。 -->
      <VBtn
        id="settings-open"
        class="bar-action"
        type="button"
        variant="text"
        color="on-surface-variant"
        stacked
        title="设置（聊天 / 智能体 / 提示词 / 外观）"
        @click="$emit('open-settings')"
      >
        <template #prepend><span class="fb-icon" v-html="ICONS.settings"></span></template>
        设置
      </VBtn>
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
