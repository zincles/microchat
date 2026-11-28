<script setup lang="ts">
// 左会话栏：顶部新建、历史列表独立滚动，底部固定账户与设置入口。
// 收藏与三点菜单是标题之外的两个独立动作。
import { ref, shallowRef, computed, nextTick, onMounted, onUnmounted, useTemplateRef, watch } from "vue";
import { VBtn, VBtnToggle, VList, VListItem, VMenu, VNavigationDrawer } from "vuetify/components";
import SessionSearch from "./SessionSearch.vue";
import SidebarToggle from "./SidebarToggle.vue";
import { ICONS } from "../assets/icons.ts";
import { groupSessions } from "../utils/sessionGroups.ts";
import { sessionTitle } from "../utils/format.ts";
import { saveTheme, themePref, THEME_IDS } from "../utils/prefs.ts";
import type { SessionView } from "../api/types.ts";

const emit = defineEmits<{
  select: [id: string];
  settings: [id: string];
  copy: [id: string];
  export: [id: string];
  close: [id: string];
  new: [];
  "new-image": [];
  "open-settings": [];
  "open-account": [];
  "toggle-left": [event: MouseEvent];
  "open-update": [];
  prefetch: [id: string];
  "favorite-change": [id: string, favorite: boolean];
}>();

// busyIds：App 从 `/tasks` 那一拍算出来的"此刻有任务在跑的会话"（判定在 `utils/tasks.ts`）。
// 行内环只吃这一处 —— 别在组件里再 filter 一遍任务表（两处判据一分岔，同一时刻会显示两个答案）。
const props = withDefaults(defineProps<{
  collapsed?: boolean;
  busyIds?: string[];
  onlineClients?: number | null;
  pendingActions?: ReadonlyMap<string, string>;
  loading?: boolean;
  refreshing?: boolean;
  brandName?: string;
  brandIcon?: "spark" | "chat" | "bot" | "messagePlus";
}>(), {
  collapsed: false,
  busyIds: () => [],
  pendingActions: () => new Map(),
  loading: false,
  refreshing: false,
  brandName: "MicroChat",
  onlineClients: null,
});
const busySet = computed(() => new Set(props.busyIds ?? []));
// 骨架行的宽度（错落一点，别像一张表格）。
const skeletonWidths = ["72%", "54%", "64%", "46%", "58%", "50%"];

const sessions = ref<SessionView[]>([]);
// 左栏快捷键只循环颜色模式，不改界面风格。
const theme = ref(themePref());
const themeLabels: Record<string, string> = { light: "亮色", dark: "暗色", auto: "自动" };
const themeIcon = computed(() => (theme.value === "light" ? ICONS.sun : theme.value === "dark" ? ICONS.moon : ICONS.themeAuto));
const themeLabel = computed(() => themeLabels[theme.value] ?? "自动");
const newLabel = computed(() => (mode.value === "image" ? "新绘图任务" : mode.value === "project" ? "暂定" : "开启新对话"));
const newTitle = computed(() => (mode.value === "image" ? "新绘图任务（生图功能暂未接入）" : mode.value === "project" ? "暂定" : "开启新对话（草稿：首句才建会话）"));
function onNew(): void {
  if (mode.value === "chat") emit("new");
  else if (mode.value === "image") emit("new-image");
}
function cycleTheme(): void {
  const at = THEME_IDS.indexOf(theme.value);
  theme.value = THEME_IDS[(at + 1) % THEME_IDS.length] ?? "auto";
  saveTheme(theme.value);
}

const currentId = ref<string | null>(null);
// 对话显示会话列表；生图和项目暂为占位面板。
const mode = ref<"chat" | "image" | "project">("chat");
const newButton = useTemplateRef<HTMLButtonElement>("newButton");
const sessionList = useTemplateRef<HTMLDivElement>("sessionList");
const groupedSessions = computed(() => groupSessions(sessions.value));

function selectFoundSession(id: string): void {
  mode.value = "chat";
  emit("select", id);
}

function titleButtons(): HTMLButtonElement[] {
  return Array.from(sessionList.value?.querySelectorAll<HTMLButtonElement>(".session-main") ?? []);
}
function onHistoryKey(event: KeyboardEvent): void {
  const target = event.target;
  if (event.isComposing || event.keyCode === 229) {
    if (target instanceof HTMLButtonElement) event.preventDefault();
    event.stopPropagation();
    return;
  }
  if (!(target instanceof HTMLButtonElement) || !target.matches(".session-main")) return;
  if (event.key === "Enter" && event.repeat) { event.preventDefault(); return; }
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  event.preventDefault();
  const rows = titleButtons(); // 实际 DOM 顺序包括日期分组，不依赖 v-for ref 数组的顺序。
  const down = event.key === "ArrowDown";
  const index = Math.max(0, Math.min(rows.length - 1, rows.indexOf(target) + (down ? 1 : -1)));
  rows[index]?.focus();
}

// 刷新后会话可能换组，原按钮会被卸载。焦点跟 ID 走，不跟原来的行号。
watch(groupedSessions, async () => {
  if (!sessionList.value) return;
  const active = document.activeElement;
  if (!(active instanceof HTMLButtonElement) || !active.dataset.sessionId || !sessionList.value.contains(active)) return;
  await nextTick();
  if (mode.value !== "chat" || props.collapsed || document.activeElement !== document.body) return;
  const row = titleButtons().find((button) => button.dataset.sessionId === active.dataset.sessionId);
  (row ?? newButton.value)?.focus();
});
const menu = shallowRef<{ id: string; anchor: HTMLButtonElement } | null>(null);
const menuOpen = shallowRef(false);
const menuList = useTemplateRef<InstanceType<typeof VList>>("menuList");
watch([() => props.collapsed, mode], () => { menuOpen.value = false; });

function setSessions(list: SessionView[] | null | undefined, cur?: string | null): void {
  sessions.value = list ?? [];
  currentId.value = cur ?? null;
}

function toggleMenu(id: string, event: MouseEvent): void {
  if (menuOpen.value) { menuOpen.value = false; return; }
  menu.value = { id, anchor: event.currentTarget as HTMLButtonElement };
  menuOpen.value = true;
}
function focusMenu(): void {
  if (!menuOpen.value || document.activeElement !== menu.value?.anchor) return;
  menuList.value?.$el.querySelector('[role="menuitem"]')?.focus({ preventScroll: true });
}
function closeMenuAndFocus(): void {
  if (!menuOpen.value) return;
  menuOpen.value = false;
  menu.value?.anchor.focus({ preventScroll: true });
}
function onMenuEscape(event: KeyboardEvent): void {
  if (!menuOpen.value) return;
  event.stopPropagation();
  event.preventDefault();
  closeMenuAndFocus();
}
function clearClosedMenu(): void {
  if (!menuOpen.value) menu.value = null;
}

// 预取（2026-11-05）：鼠标在会话行上**停 140ms** 才算"想点它"（扫过列表不算），
// 触屏 touchstart / 键盘 focus 立即 —— 这三种之后紧跟着的多半就是点击。
// 真正的去重、并发上限、缓存判断都在 App 的 prefetchSession 里。
let hoverTimer: ReturnType<typeof setTimeout> | null = null;
let hoverId: string | null = null;
function prefetchHover(s: SessionView, e?: PointerEvent): void {
  if ((e?.pointerType && e.pointerType !== "mouse") || hoverId === s.id) return;
  hoverId = s.id;
  clearTimeout(hoverTimer ?? undefined);
  hoverTimer = setTimeout(() => emit("prefetch", s.id), 140);
}
function prefetchNow(s: SessionView): void {
  clearTimeout(hoverTimer ?? undefined);
  emit("prefetch", s.id);
}
function prefetchEnd(): void {
  clearTimeout(hoverTimer ?? undefined);
  hoverId = null;
}

function syncTheme(): void { theme.value = themePref(); }
onMounted(() => {
  syncTheme();
  window.addEventListener("mc-theme-changed", syncTheme);
});
onUnmounted(() => {
  clearTimeout(hoverTimer ?? undefined);
  window.removeEventListener("mc-theme-changed", syncTheme);
});

defineExpose({ setSessions });
</script>

<template>
  <VNavigationDrawer
    id="sessions-bar"
    :model-value="true"
    permanent
    :width="256"
    :scrim="false"
    :touchless="true"
    class="sessions-drawer"
    :class="{ collapsed: props.collapsed }"
    :inert="props.collapsed"
  >
    <div class="session-navigation">
    <div class="bar-head">
      <!-- Brand Title（2026-11-06 用户定）：原来放大 tab 的位置换作品牌；
           下面依次是新对话 → 对话/生图 → 会话列表（Gemini 式左侧栏）。 -->
      <button
        class="brand"
        type="button"
        title="关于 MicroChat 与版本更新"
        aria-label="关于 MicroChat 与版本更新"
        @click="$emit('open-update')"
      >
        <span class="brand-mark" aria-hidden="true" v-html="ICONS[props.brandIcon ?? 'spark']"></span>
        <span class="brand-name">{{ props.brandName || "MicroChat" }}</span>
      </button>
      <span class="bar-head-actions">
        <SessionSearch :sessions="sessions" :current-id="currentId" @select="selectFoundSession" />
        <button
          id="theme-toggle"
          class="icon-btn theme-toggle"
          type="button"
          :title="`切换颜色模式（当前：${themeLabel}）`"
          :aria-label="`切换颜色模式（当前：${themeLabel}）`"
          @click="cycleTheme"
          v-html="themeIcon"
        ></button>
        <SidebarToggle id="toggle-left" expanded @click="emit('toggle-left', $event)" />
      </span>
      <!-- 刷新条钉在栏头底边：它只是状态，不占布局 —— 曾放在栏头与新建按钮之间，
           进入站点时出现/消失会把下面整列（新建、模式切换、列表）顶动一次。 -->
      <div v-if="refreshing" class="md-linear-progress" role="progressbar" aria-label="正在刷新会话列表"><span></span></div>
    </div>
    <div class="session-actions">
      <button id="session-new" ref="newButton" type="button" :title="newTitle" :disabled="mode === 'project'" @click="onNew">
        <span class="plus" aria-hidden="true" v-html="ICONS.plus"></span>{{ newLabel }}
      </button>
    </div>
    <!-- 模式切换保留原有入口，不再用外框把三个按钮围成一块。 -->
    <VBtnToggle
      v-model="mode"
      class="session-mode-tabs ui-mode-switch"
      mandatory
      variant="text"
      role="group"
      aria-label="左栏模式"
    >
      <VBtn value="chat" title="对话" :aria-pressed="mode === 'chat'">对话</VBtn>
      <VBtn value="image" title="生图（暂未接入）" :aria-pressed="mode === 'image'">生图</VBtn>
      <VBtn value="project" title="项目（暂未接入）" :aria-pressed="mode === 'project'">项目</VBtn>
    </VBtnToggle>
    </div>
    <div v-if="mode === 'chat'" id="session-list" ref="sessionList" :aria-busy="loading && !sessions.length ? 'true' : 'false'" @keydown="onHistoryKey">
      <div v-if="loading && !sessions.length" class="session-skeleton">
        <div v-for="(w, i) in skeletonWidths" :key="i" class="skeleton-row" :style="{ '--w': w }"></div>
      </div>
      <div v-else-if="!sessions.length" class="empty">还没有会话<br /><span class="sub">点上面「＋ 开启新对话」开始</span></div>
      <div v-else-if="!groupedSessions.length" class="empty" role="status">没有匹配的会话</div>
      <template v-for="group in groupedSessions" :key="group.key">
        <div class="session-group-head">{{ group.label }}</div>
        <div
          v-for="s in group.items"
          :key="s.id"
          class="session-item"
          :class="{ sel: s.id === currentId }"
          @pointermove="prefetchHover(s, $event)"
          @pointerleave="prefetchEnd"
          @focusin="prefetchNow(s)"
          @touchstart.passive="prefetchNow(s)"
        >
          <button class="session-main" type="button" :data-session-id="s.id" :aria-current="s.id === currentId ? 'true' : undefined"
            @click="$emit('select', s.id)">
            <span class="title">{{ sessionTitle(s.title) }}</span>
          </button>
          <!-- 前台会话操作借用同一枚环，不把等待误报成模型任务。 -->
          <svg
            v-if="pendingActions.has(s.id) || busySet.has(s.id)"
            class="session-busy"
            viewBox="0 0 24 24"
            role="status"
            :aria-label="pendingActions.get(s.id) ?? '正在运行任务'"
          >
            <title>{{ pendingActions.get(s.id) ?? '正在运行任务' }}</title>
            <circle class="session-busy-track" cx="12" cy="12" r="9"></circle>
            <circle class="session-busy-arc" cx="12" cy="12" r="9"></circle>
          </svg>
          <button class="favorite-btn" type="button" :title="s.favorite ? '取消收藏' : '收藏'"
            :aria-label="s.favorite ? '取消收藏' : '收藏'" :aria-pressed="!!s.favorite"
            @click.stop="emit('favorite-change', s.id, !s.favorite)">{{ s.favorite ? '★' : '☆' }}</button>
          <button class="item-btn" type="button" title="会话选项" aria-label="会话选项" aria-haspopup="menu"
            :aria-expanded="menuOpen && menu?.id === s.id" :aria-controls="menuOpen && menu?.id === s.id ? 'session-options' : undefined"
            @click.stop="toggleMenu(s.id, $event)" @keydown.esc="onMenuEscape" v-html="ICONS.more"></button>
        </div>
      </template>
    </div>
    <div v-else-if="mode === 'image'" class="mode-panel" aria-label="生图">
      <div class="empty">生图<br /><span class="sub">功能尚未接入；这里将来放生图会话列表</span></div>
    </div>
    <div v-else class="mode-panel" aria-label="项目">
      <div class="empty">项目<br /><span class="sub">功能尚未接入；这里将来放项目列表</span></div>
    </div>
    <div class="sidebar-footer">
      <button type="button" class="account-button" :title="`账户；在线客户端 ${onlineClients ?? '未知'}`"
        :aria-label="`账户，在线客户端 ${onlineClients ?? '未知'}`" @click="emit('open-account')">
        <span class="account-icon" aria-hidden="true" v-html="ICONS.user"></span>
        <span>账户</span>
        <span v-if="onlineClients !== null" class="online-count" aria-label="在线客户端数量">{{ onlineClients }}</span>
      </button>
      <button id="settings-open" class="icon-btn" type="button" title="设置" aria-label="设置"
        @click="emit('open-settings')" v-html="ICONS.settings"></button>
    </div>
  </VNavigationDrawer>
  <VMenu id="session-options" v-model="menuOpen" :target="menu?.anchor" location="bottom end" :offset="8"
    :width="168" scroll-strategy="close" :close-on-content-click="false"
    @after-enter="focusMenu" @after-leave="clearClosedMenu">
    <VList v-if="menu" ref="menuList" role="menu" aria-label="会话选项" density="compact"
      color="on-surface" bg-color="surface-container" rounded="md" class="py-1"
      @click="closeMenuAndFocus" @keydown.esc="onMenuEscape">
      <VListItem role="menuitem" title="会话设置" @click="emit('settings', menu.id)" />
      <VListItem role="menuitem" title="创建副本" :disabled="pendingActions.has(menu.id)" @click="emit('copy', menu.id)" />
      <VListItem role="menuitem" title="导出 JSON" :disabled="pendingActions.has(menu.id)" @click="emit('export', menu.id)" />
      <VListItem role="menuitem" title="删除会话" class="text-error"
        :disabled="pendingActions.has(menu.id)" @click="emit('close', menu.id)" />
    </VList>
  </VMenu>
</template>
<style scoped>
.sidebar-footer { display: flex; flex: none; align-items: center; gap: calc(var(--chat-space) / 2); padding: var(--chat-space); border-top: 1px solid var(--md-outline-variant); }
.account-button { flex: 1 1 auto; min-width: 0; min-height: var(--chat-control); display: flex; align-items: center; gap: .5em; padding-inline: var(--chat-space); border: 0; border-radius: var(--md-shape-sm); color: var(--md-on-surface); background: transparent; text-align: start; font: inherit; }
 .account-icon { display: inline-flex; flex: none; width: 1.1em; height: 1.1em; }
 .account-icon :deep(svg) { width: 100%; height: 100%; }
 .online-count { margin-inline-start: auto; color: var(--md-on-surface-variant); font-size: var(--md-type-label-medium); }
#settings-open { flex: none; width: var(--chat-control); height: var(--chat-control); display: inline-flex; align-items: center; justify-content: center; border: 0; border-radius: 50%; color: var(--md-on-surface-variant); background: transparent; }
#settings-open svg { width: 18px; height: 18px; }
.account-button:focus-visible, #settings-open:focus-visible { outline: 2px solid var(--md-primary); outline-offset: -2px; }
</style>

