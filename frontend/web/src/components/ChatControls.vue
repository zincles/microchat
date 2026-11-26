<script setup lang="ts">
// 左上角悬浮模型选择和侧栏开关，右上角切换聊天与工作区。
import { ref, computed, nextTick, watch, type Ref } from "vue";
import { ClickOutside as vClickOutside } from "vuetify/directives";
import { useChatMenu } from "../composables/useChatMenu.ts";
import { VBtn, VBtnToggle, VCard, VTab, VTabs } from "vuetify/components";
import ModelIcon from "./ModelIcon.vue";
import CloseButton from "./CloseButton.vue";
import { ICONS } from "../assets/icons.ts";
import {
  curModelLabelOf,
  filterModelGroups,
  groupModelChoices,
  modelKey,
  parseModelKey,
} from "../utils/format.ts";
import type { ModelBrowseMode, ModelChoice, ModelGroup } from "../utils/format.ts";
import type { Session } from "../api/types.ts";

const props = withDefaults(defineProps<{
  // leftHidden：左栏收着 ⇒ 顶栏出它那枚开关；栏开着 ⇒ 开关在栏自己头上（按钮跟着它控制的东西走）。
  // （右栏 2026-10-29 搬进上下文工作区 ⇒ 顶栏不再有 [◨] 那枚开关，`rightHidden` 跟着删。）
  leftHidden?: boolean;
  // 当前工作区仍由顶栏切换，状态圆环在输入卡。
  workspace?: string;
  // loading：会话正在加载（模型列表也还没就位）⇒ 不拿旧/裸 id 闪一下，直接骨架。
  loading?: boolean;
  modelSelectionPending?: boolean;
  savingModelDefault?: boolean;
  // FlowAgent 的第二个工作区是工作流。
  flow?: boolean;
}>(), {
  leftHidden: false,
  workspace: "chat",
  loading: false,
  flow: false,
});
const emit = defineEmits<{
  "toggle-left": [];
  "select-model": [value: string];
  "save-model-default": [];
  "select-workspace": [workspace: string];
}>();
function setWorkspace(v: unknown): void {
  if (v === "chat" || v === "context") emit("select-workspace", v);
}
const modelGroups = ref<ModelGroup[]>([]); // [{provider, items:[{value,label,provider_name,upstream_id}]}]
const curModel = ref("");
// 会话绑的模型不在列表里（渠道被删、模型下架）⇒ 补一条"不在列表"，别让它看起来像没选
// —— 那是有值，只是列表里没有（显示谎言比多一行丑得多）。
const orphanModel = ref<ModelChoice | null>(null);
// 搜索在两种分组方式间保留；分组页签和结果位次则分别从头开始。
const modelOpen = useChatMenu("model");
const modelQuery = ref("");
const modelBrowse = ref<ModelBrowseMode>("owner");
const modelGroupId = ref("");
const modelTabsEl = ref<HTMLElement | null>(null);
const modelSearchEl = ref<HTMLInputElement | null>(null);
const modelMenuEl = ref<HTMLElement | null>(null);
const modelButtonEl = ref<HTMLButtonElement | null>(null);
const modelActive = ref(-1); // 上下键选中的那一项（在**扁平化**的列表里的下标），-1 = 没选

const curModelLabel = computed(() => {
  if (props.loading) return "";
  return curModelLabelOf(modelGroups.value, curModel.value, orphanModel.value);
});

// 当前组合不依赖搜索或分组；切换分组只改变浏览方式。
const curModelChoice = computed<ModelChoice | null>(() => {
  for (const group of modelGroups.value) {
    const hit = (group.items ?? []).find((item) => item.value === curModel.value);
    if (hit) return hit;
  }
  return orphanModel.value;
});

const browseGroups = computed(() => groupModelChoices(modelGroups.value, modelBrowse.value));
const modelTabs = computed(() => [
  { id: "", label: "全部", count: browseGroups.value.reduce((n, g) => n + g.items.length, 0), icon: undefined },
  ...browseGroups.value.map(({ id, label, items }) => ({
    id, label, count: items.length,
    icon: modelBrowse.value === "owner" ? items.find((item) => item.owner && item.icon)?.icon : undefined,
  })),
]);
const selectedModelGroup = computed(() => browseGroups.value.find((g) => g.id === modelGroupId.value));
const selectedModelTab = computed(() => selectedModelGroup.value?.id ?? "");
const visibleGroups = computed(() => {
  const group = selectedModelGroup.value;
  return group
    ? filterModelGroups([group], modelQuery.value)
    : filterModelGroups(browseGroups.value, modelQuery.value, orphanModel.value);
});
const modelFlatCount = computed(() => visibleGroups.value.reduce((n, g) => n + g.items.length, 0));

// 归属和渠道共用同一条可横向拖动的页签栏。
// 触屏交给浏览器的原生横向滚动；鼠标拖动才自己接管（按下期间挂 window 监听，
// 不 setPointerCapture —— 捕获会把 click 的 target 换成容器，tab 就点不动了）。
let tabDrag: { pointerId: number; startX: number; startLeft: number; moved: boolean } | null = null;
let suppressTabClick = false;
function onTabsPointerDown(e: PointerEvent): void {
  if (e.pointerType === "touch" || e.button !== 0) return;
  const el = modelTabsEl.value;
  if (!el || el.scrollWidth <= el.clientWidth) return;
  tabDrag = { pointerId: e.pointerId, startX: e.clientX, startLeft: el.scrollLeft, moved: false };
  el.classList.add("dragging");
  const move = (ev: PointerEvent): void => {
    if (!tabDrag || ev.pointerId !== tabDrag.pointerId) return;
    const dx = ev.clientX - tabDrag.startX;
    if (!tabDrag.moved && Math.abs(dx) > 4) tabDrag.moved = true;
    if (!tabDrag.moved) return;
    el.scrollLeft = tabDrag.startLeft - dx;
    ev.preventDefault();
  };
  const end = (): void => {
    const moved = tabDrag?.moved ?? false;
    tabDrag = null;
    el.classList.remove("dragging");
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", end);
    window.removeEventListener("pointercancel", end);
    if (moved) {
      // 拖动结束后的 click 只是拖拽的尾巴，不是选页签。
      // click 在 pointerup 之后同步触发；超时兜底（松在空白处时没有 click）。
      suppressTabClick = true;
      setTimeout(() => { suppressTabClick = false; }, 0);
    }
  };
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", end);
  window.addEventListener("pointercancel", end);
}
function onTabsWheel(e: WheelEvent): void {
  const el = modelTabsEl.value;
  if (!el || el.scrollWidth <= el.clientWidth) return;
  // 普通滚轮给 deltaY，触控板横滑给 deltaX；哪个大用哪个。
  const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
  if (!delta) return;
  el.scrollLeft += delta;
  e.preventDefault();
}
function setModelBrowse(mode: unknown): void {
  if ((mode !== "owner" && mode !== "channel") || mode === modelBrowse.value) return;
  modelBrowse.value = mode;
  modelGroupId.value = "";
  if (modelTabsEl.value) modelTabsEl.value.scrollLeft = 0;
}
function selectModelTab(id: string): void {
  if (!suppressTabClick) modelGroupId.value = id;
}
function moveModelTab(delta: number): void {
  const tabs = modelTabs.value;
  const index = tabs.findIndex((tab) => tab.id === selectedModelTab.value);
  selectModelTab(tabs[(index + delta + tabs.length) % tabs.length].id);
  nextTick(() => {
    const active = modelTabsEl.value?.querySelector<HTMLButtonElement>('[aria-selected="true"]');
    active?.focus({ preventScroll: true });
    active?.scrollIntoView({ block: "nearest", inline: "nearest" });
  });
}
// 模型面板非模态。显式关闭回入口；点到别的控件则让焦点跟过去。
function panelDismissal(open: Ref<boolean>, trigger: Ref<HTMLButtonElement | null>) {
  function close(): void {
    if (!open.value) return;
    open.value = false;
    trigger.value?.focus({ preventScroll: true });
  }
  const outside = {
    include: () => trigger.value ? [trigger.value] : [],
    handler: () => { if (document.activeElement === document.body) close(); else open.value = false; },
  };
  function onFocusOut(e: FocusEvent): void {
    const next = e.relatedTarget;
    if (next instanceof Node && !trigger.value?.contains(next) && !(e.currentTarget as HTMLElement).contains(next)) open.value = false;
  }
  return { close, outside, onFocusOut };
}
const modelPanel = panelDismissal(modelOpen, modelButtonEl);

function toggleModel(): void {
  if (modelOpen.value) { modelPanel.close(); return; }
  modelOpen.value = true;
  modelQuery.value = "";
  modelGroupId.value = "";
  modelActive.value = -1;
  // 打开就聚焦搜索框：几百个模型，第一次动作必然是打字。
  nextTick(() => { if (modelOpen.value) modelSearchEl.value?.focus({ preventScroll: true }); });
}
function pickModel(value?: string | null): void {
  modelPanel.close();
  // 点当前那一项 = 什么都不做（别为了一次误点发一个 PATCH）。
  if (value && value !== curModel.value) emit("select-model", value);
}
function modelItemDetail(model: ModelChoice): string {
  const upstream = model.upstream_id !== model.label ? model.upstream_id : "";
  if (modelBrowse.value === "channel") return upstream;
  const channel = model.alias || model.provider_name;
  return upstream ? `${channel} · ${upstream}` : channel;
}
function flatItemAt(index: number): ModelChoice | null {
  for (const g of visibleGroups.value) {
    for (const m of g.items) if (m.flat === index) return m;
  }
  return null;
}
function moveActive(delta: number): void {
  const n = modelFlatCount.value;
  if (!n) return;
  const next = modelActive.value < 0 ? (delta > 0 ? 0 : n - 1) : modelActive.value + delta;
  modelActive.value = Math.max(0, Math.min(n - 1, next));
}
function onModelKey(e: KeyboardEvent): void {
  if (e.isComposing || e.keyCode === 229) return;
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    if (modelOpen.value) moveActive(delta);
    else {
      toggleModel();
      // 重开时要先等原搜索/渠道的高亮重置，再定位第一项或最后一项。
      nextTick(() => { if (modelOpen.value) moveActive(delta); });
    }
  } else if (e.key === "Enter" && e.currentTarget === modelSearchEl.value) {
    e.preventDefault();
    const hit = flatItemAt(modelActive.value);
    if (hit) pickModel(hit.value);
  }
}
// 筛选或清单变化后从头看结果，不能让键盘指向上一份列表的下标。
watch([modelQuery, selectedModelGroup, browseGroups], () => {
  modelActive.value = -1;
  nextTick(() => { if (modelMenuEl.value) modelMenuEl.value.scrollTop = 0; });
});
watch(modelActive, () => {
  if (modelActive.value < 0) return;
  nextTick(() => modelMenuEl.value?.querySelector<HTMLElement>(`#model-option-${modelActive.value}`)
    ?.scrollIntoView({ block: "nearest" }));
});

const leftSvg = ICONS.panelLeft;

function syncOrphanModel(): void {
  const cur = curModel.value;
  const known = cur && modelGroups.value.some((g) => (g.items ?? []).some((i) => i.value === cur));
  const parsed = cur ? parseModelKey(cur) : null;
  // 模型列表还没到时不要造 orphan（那会把 provider/upstream_id 裸串闪到顶栏）。
  orphanModel.value = modelGroups.value.length && cur && !known
    ? {
        value: cur,
        label: `${parsed ? `${parsed.provider}/${parsed.model}` : cur}（不在列表）`,
        provider_name: parsed?.provider ?? "",
        upstream_id: parsed?.model ?? "",
      }
    : null;
}
function setSession(info: Partial<Session>): void {
  curModel.value = info.provider && info.model ? modelKey(info.provider, info.model) : "";
  syncOrphanModel();
}
function setModels(groups: ModelGroup[], cur?: string): void {
  modelGroups.value = groups;
  if (cur !== undefined) curModel.value = cur;
  syncOrphanModel();
}

function onMenuEscape(event: KeyboardEvent): void {
  if (event.isComposing || event.keyCode === 229 || !modelOpen.value) return;
  event.preventDefault();
  event.stopPropagation();
  modelPanel.close();
}

defineExpose({ setSession, setModels });
</script>

<template>
  <div id="chat-controls" @keydown.esc="onMenuEscape">
    <button
      id="chat-sidebar-toggle"
      class="icon-btn"
      type="button"
      :title="leftHidden ? '打开会话栏' : '收起会话栏'"
      :aria-label="leftHidden ? '打开会话栏' : '收起会话栏'"
      :aria-expanded="!leftHidden"
      aria-controls="sessions-bar"
      @click="$emit('toggle-left')"
      v-html="leftSvg"
    ></button>
    <!-- 浮板随模型按钮向下展开。 -->
    <span class="model-anchor">
      <button
        id="model-btn"
        ref="modelButtonEl"
        class="model-btn"
        :class="{ loading }"
        type="button"
        :disabled="loading"
        :title="curModelLabel ? `${curModelLabel}｜改当前会话的模型` : '改当前会话的模型'"
        aria-label="选择模型"
        aria-haspopup="dialog"
        :aria-controls="modelOpen ? 'chat-model-picker' : undefined"
        :aria-expanded="modelOpen ? 'true' : 'false'"
        @click="toggleModel"
        @keydown="onModelKey"
      >
        <span v-if="loading" class="model-loading" aria-hidden="true"></span>
        <template v-else>
          <ModelIcon v-if="curModelChoice" :icon="curModelChoice.icon" :brand="curModelChoice.brand" size="lg" />
          <span class="model-btn-label">{{ curModelLabel }}</span>
        </template>
        <svg class="model-caret" viewBox="0 0 10 6" aria-hidden="true"><path d="M0 0h10L5 6z" /></svg>
      </button>
      <Transition name="model-picker">
        <VCard v-if="modelOpen" id="chat-model-picker" v-click-outside="modelPanel.outside" class="agent-menu model-menu" role="dialog" aria-labelledby="model-menu-title" @focusout="modelPanel.onFocusOut">
          <div class="model-menu-head">
            <h2 id="model-menu-title">选择模型</h2>
            <VBtnToggle class="ui-mode-switch model-browse" :model-value="modelBrowse" mandatory variant="text"
              role="group" aria-label="模型分组方式" @update:model-value="setModelBrowse">
              <VBtn value="owner" :aria-pressed="modelBrowse === 'owner'">按归属</VBtn>
              <VBtn value="channel" :aria-pressed="modelBrowse === 'channel'">按渠道</VBtn>
            </VBtnToggle>
            <CloseButton label="关闭模型选择器" @click="modelPanel.close" />
          </div>
          <input
            ref="modelSearchEl"
            v-model="modelQuery"
            class="model-search"
            type="search"
            placeholder="搜索模型、归属或渠道…"
            aria-label="搜索模型"
            role="combobox"
            aria-expanded="true"
            aria-controls="model-options"
            aria-autocomplete="list"
            :aria-activedescendant="modelActive >= 0 ? `model-option-${modelActive}` : undefined"
            @keydown="onModelKey"
          />
          <div
            ref="modelTabsEl"
            class="model-group-tabs"
            role="tablist"
            :aria-label="modelBrowse === 'owner' ? '按归属筛选模型' : '按渠道筛选模型'"
            @pointerdown="onTabsPointerDown"
            @wheel="onTabsWheel"
            @dragstart.prevent
          >
            <button
              v-for="tab in modelTabs"
              :key="tab.id"
              type="button"
              role="tab"
              class="model-group-tab"
              :class="{ active: selectedModelTab === tab.id }"
              :aria-selected="selectedModelTab === tab.id"
              aria-controls="model-group-panel"
              :title="`${tab.label}（${tab.count} 个模型）`"
              :tabindex="selectedModelTab === tab.id ? 0 : -1"
              @keydown.right.prevent="moveModelTab(1)"
              @keydown.left.prevent="moveModelTab(-1)"
              @click="selectModelTab(tab.id)"
            >
              <ModelIcon v-if="tab.icon" :icon="tab.icon" size="sm" />
              {{ tab.label }}
            </button>
          </div>
          <div id="model-group-panel" class="model-results" role="tabpanel" :aria-label="selectedModelGroup?.label ?? '全部模型'">
            <div id="model-options" ref="modelMenuEl" class="model-list" role="listbox" aria-label="可用模型">
              <template v-for="g in visibleGroups" :key="g.id">
                <div v-if="!selectedModelTab" class="model-group-head">
                  <span class="model-group-name">{{ g.label }}</span>
                  <span class="model-group-count">{{ g.items.length }}</span>
                </div>
                <button
                  v-for="m in g.items"
                  :key="m.value"
                  :id="`model-option-${m.flat}`"
                  role="option"
                  :aria-selected="m.value === curModel"
                  tabindex="-1"
                  type="button"
                  class="model-item"
                  :class="{ sel: m.value === curModel, active: m.flat === modelActive }"
                  :title="`${m.alias || m.provider_name} / ${m.upstream_id || m.label}`"
                  @click="pickModel(m.value)"
                >
                  <ModelIcon :icon="m.icon" :brand="m.brand" />
                  <span class="model-item-copy">
                    <span class="model-name">{{ m.label }}</span>
                    <span v-if="modelBrowse === 'owner' || (m.upstream_id && m.upstream_id !== m.label)" class="model-item-id">{{ modelItemDetail(m) }}</span>
                  </span>
                  <span v-if="m.value === curModel" class="model-check">✓ 当前</span>
                </button>
              </template>
              <div v-if="!visibleGroups.length" class="model-empty" role="status">
                <strong>没有匹配的模型</strong>
                <span>试试其他名称、上游 ID，或选择其他分组。</span>
              </div>
            </div>
          </div>
          <div class="model-menu-foot">
            <div class="model-menu-meta">
              <span role="status">{{ modelFlatCount }} 个模型</span>
              <span class="model-key-hint">↑ ↓ 选择 · Enter 切换</span>
            </div>
            <VBtn
              class="model-default-btn" variant="tonal" color="primary" size="small" type="button"
              :disabled="!curModel || loading || modelSelectionPending"
              :loading="savingModelDefault"
              title="将当前模型和渠道设为新会话默认"
              @click="$emit('save-model-default')"
            >设当前为新会话默认</VBtn>
          </div>
        </VCard>
      </Transition>
    </span>
    <VTabs
      :model-value="workspace"
      class="workspace-tabs"
      height="var(--chat-nav-size)"
      color="primary"
      aria-label="工作区"
      @update:model-value="setWorkspace"
    >
      <VTab value="chat" title="对话本身">聊天</VTab>
      <VTab value="context" :title="flow ? '当前助手的工作流' : '上下文工作区'">{{ flow ? "工作流" : "上下文" }}</VTab>
    </VTabs>
  </div>
</template>
