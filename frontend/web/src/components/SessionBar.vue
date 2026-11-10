<script setup lang="ts">
// 左会话栏：列表 + 新建 + 置底设置按钮。
// 每条只有一枚「选项」（竖三点）：设置与删除都收进那块浮板 —— 行内不再挤两枚键。
import { ref, computed, nextTick, onMounted, onUnmounted } from "vue";
import { VBtn, VBtnToggle, VNavigationDrawer } from "vuetify/components";
import { ICONS } from "./icons.ts";
import { groupSessions } from "../utils/sessionGroups.ts";
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
  "toggle-left": [];
  "open-sessions": [];
  "open-providers": [];
  "open-update": [];
  prefetch: [id: string];
}>();

// busyIds：App 从 `/tasks` 那一拍算出来的"此刻有任务在跑的会话"（判定在 `utils/tasks.ts`）。
// 行内环只吃这一处 —— 别在组件里再 filter 一遍任务表（两处判据一分岔，同一时刻会显示两个答案）。
const props = withDefaults(defineProps<{
  collapsed?: boolean;
  busyIds?: string[];
  // loading：首次拿会话列表（boot）—— 画骨架，别显示"还没有会话"（那是假话）。
  loading?: boolean;
  // refreshing：后台在补一次列表（10 秒保鲜期外）—— 顶部一条 MD3 linear progress。
  refreshing?: boolean;
}>(), {
  collapsed: false,
  busyIds: () => [],
  loading: false,
  refreshing: false,
});
const busySet = computed(() => new Set(props.busyIds ?? []));
// 骨架行的宽度（错落一点，别像一张表格）。
const skeletonWidths = ["72%", "54%", "64%", "46%", "58%", "50%"];

const sessions = ref<SessionView[]>([]);
// 主题三态：亮色 / 暗色 / 自动。按钮只显示图标，按下立刻落 prefs + 改 data-theme。
const theme = ref(themePref());
const themeLabels: Record<string, string> = { light: "亮色", dark: "暗色", auto: "自动" };
const themeIcon = computed(() => (theme.value === "light" ? ICONS.sun : theme.value === "dark" ? ICONS.moon : ICONS.themeAuto));
const themeLabel = computed(() => themeLabels[theme.value] ?? "自动");
const newLabel = computed(() => (mode.value === "image" ? "新绘图任务" : mode.value === "inventory" ? "暂定" : "开启新对话"));
const newTitle = computed(() => (mode.value === "image" ? "新绘图任务（生图功能暂未接入）" : mode.value === "inventory" ? "暂定" : "开启新对话（草稿：首句才建会话）"));
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
// 左栏顶部的三个空白：对话 / 生图 / 库存。生图与库存暂未接 API，切过去只改选中态
// （未来这里放各自的列表）。
const mode = ref<"chat" | "image" | "inventory">("chat");
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

function syncTheme(): void { theme.value = themePref(); }
onMounted(() => {
  syncTheme();
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", closeMenu, true);
  window.addEventListener("mc-theme-changed", syncTheme);
});
onUnmounted(() => {
  clearTimeout(hoverTimer ?? undefined);
  document.removeEventListener("keydown", onKey);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("scroll", closeMenu, true);
  window.removeEventListener("mc-theme-changed", syncTheme);
});

defineExpose({ setSessions });
</script>

<template>
  <VNavigationDrawer
    id="sessions-bar"
    :model-value="true"
    permanent
    :width="280"
    :scrim="false"
    :touchless="true"
    class="sessions-drawer"
    :class="{ collapsed: props.collapsed }"
  >
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
        <span class="brand-mark" aria-hidden="true" v-html="ICONS.spark"></span>
        <span class="brand-name">MicroChat</span>
      </button>
      <span class="bar-head-actions">
        <button
          id="theme-toggle"
          class="icon-btn theme-toggle"
          type="button"
          :title="`切换主题（当前：${themeLabel}）`"
          :aria-label="`切换主题（当前：${themeLabel}）`"
          @click="cycleTheme"
          v-html="themeIcon"
        ></button>
        <button
          id="toggle-left"
          class="icon-btn"
          type="button"
          title="收起会话栏"
          aria-label="收起会话栏"
          @click="$emit('toggle-left')"
          v-html="ICONS.panelLeft"
        ></button>
      </span>
    </div>
    <div v-if="refreshing" class="md-linear-progress" role="progressbar" aria-label="正在刷新会话列表"><span></span></div>
    <button id="session-new" type="button" :title="newTitle" :disabled="mode === 'inventory'" @click="onNew">
      <span class="plus" aria-hidden="true" v-html="ICONS.plus"></span><span class="label">{{ newLabel }}</span>
    </button>
    <!-- 左栏模式切换，不是内容 tab：生图与库存面板还没接，切过去只是选中态。
         用 VBtnToggle 做全宽分段按钮；等它们有独立列表再升级成 VTabs。 -->
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
      <VBtn value="inventory" title="库存（暂未接入）" :aria-pressed="mode === 'inventory'">库存</VBtn>
    </VBtnToggle>
    <div v-if="mode === 'chat'" id="session-list" :aria-busy="loading && !sessions.length ? 'true' : 'false'">
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
    <div v-else-if="mode === 'image'" class="mode-panel" aria-label="生图">
      <div class="empty">生图<br /><span class="sub">功能尚未接入；这里将来放生图会话列表</span></div>
    </div>
    <div v-else class="mode-panel" aria-label="库存">
      <div class="empty">库存<br /><span class="sub">功能尚未接入；这里将来放库存列表</span></div>
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
      <VBtn
        id="update-open"
        class="bar-action"
        type="button"
        variant="text"
        color="on-surface-variant"
        stacked
        title="更新（git pull + 重启前后端）"
        @click="$emit('open-update')"
      >
        <template #prepend><span class="fb-icon" v-html="ICONS.refresh"></span></template>
        更新
      </VBtn>
    </div>
  </VNavigationDrawer>
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

<style scoped>
/* 左栏模式 tab 的垂直几何钉死（2026-11-09）。
 * 病灶有两层，都在测量里看得到：
 *  1) VBtnGroup 的 compact 规则给了整条胶囊 `height: 36px`，而按钮自己的
 *     `min-height: 2.667em`（label-large 下 = 37.33px）比它更高 —— 按钮永远溢出
 *     1.33px，靠 `overflow: hidden` 裁掉，所以"按钮高度"和"胶囊高度"本来就对不上。
 *  2) 抽屉内容是 column flex，这条胶囊是 `flex: 0 1 auto` 的普通子项，会被挤。
 *     对话模式下列表内容更高 → 胶囊被压到 25.14px；生图/库存是占位面板 → 不压，36px。
 *     于是切换模式时同一个控件高矮变了 10.9px。
 * 修法：胶囊不参与伸缩（flex: none）、高度交给按钮（height: auto），
 * 按钮统一 min-height = 2.667em + border-box。三格都走同一条规则，选中/焦点只改
 * 颜色与 outline，不再碰尺寸。 */
/* 注意：这里不能用 `:deep()` —— SessionBar 的根是 Fragment（抽屉 + Teleport），
 * 作用域属性不会挂到抽屉根上，`:deep()` 编译出的 `[data-v-x] .x` 找不到祖先。
 * 但 VBtnToggle / VBtn 的根元素自己带着本组件的 `data-v-x`，普通作用域选择器就够。 */
.session-mode-tabs {
  flex: none;
  height: auto;
  box-sizing: border-box;
  align-items: stretch;
}
.session-mode-tabs .v-btn {
  height: auto;
  min-height: 2.667em;
  max-height: none;
  box-sizing: border-box;
  align-self: stretch;
}

/* 字面墨迹居中（2026-11-09 逐像素复测，取代 main.css 的 `-0.16em`）。
 * 旧的 `-0.16em` 是在**被裁掉**的胶囊上量的：那时可见带只有 25.14px、窗口偏上，
 * 字往下压才显得居中；胶囊高度修好后按钮 37.33px 完整可见，同一偏移就偏高。
 * 实测（截屏取字面 ink 亮度质心，非行盒中心）：不偏移时墨迹中心本来就在按钮中心
 * **上方 0.5px 左右**（字体固有，Noto Sans CJK 的 ink 比行盒几何中心高）；旧 `-0.16em`
 * 再往上推 2.24px，于是实际高约 2.7–3.3px —— 这就是"字偏上"的全部来源。
 * 目标：墨迹质心 = 按钮中心 → 需要**往下**约 0.7px，即 +0.05em（13.9995px 字号下 = 0.70px）。
 * 注意 Chrome 把字形竖直位置吸附到设备像素，所以不同 dpr 的残差是 0.8/1/0.5px 一档：
 *   dpr 1.25 / 2 / 4 实测质心残差 +0.04…+0.34 / -0.24…+0.06 / +0.06…+0.42px；
 *   dpr 1 只能落在 ±0.5px 的档上（+0.33…+0.70px）。三个 tab 同值，选中/未选中一致。
 * 选 0.05em 而不是恰好补到 0（0.0757em）：后者在 dpr 4 会掉到下一档，残差反而 +0.5…+0.7px；
 * 0.05em 在四个 dpr 上都是最靠中的那一档。
 * 选择器必须盖过 main.css 的 `#sessions-bar .session-mode-tabs .v-btn__content`
 * （1 id + 2 类）：带上 id 再 `:deep()` → `#sessions-bar .session-mode-tabs
 * .v-btn[data-v] .v-btn__content`（1 id + 4 类）。作用域属性挂在按钮根上（slot 内容
 * 归父组件所有），不能锚在抽屉根上（它是 Fragment，没有本组件的作用域属性）。 */
#sessions-bar .session-mode-tabs .v-btn :deep(.v-btn__content) {
  transform: translateY(0.05em);
}
</style>
