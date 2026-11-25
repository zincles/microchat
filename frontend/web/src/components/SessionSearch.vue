<script setup lang="ts">
import { computed, nextTick, shallowRef, useTemplateRef, watch } from "vue";
import { VCard, VDialog } from "vuetify/components";
import CloseButton from "./CloseButton.vue";
import { ICONS } from "../assets/icons.ts";
import type { SessionView } from "../api/types.ts";
import { sessionTitle } from "../utils/format.ts";
import { filterSessionTitles } from "../utils/sessionGroups.ts";

const props = defineProps<{ sessions: SessionView[]; currentId: string | null }>();
const emit = defineEmits<{ select: [id: string] }>();
const open = shallowRef(false);
const query = shallowRef("");
const active = shallowRef(-1);
const input = useTemplateRef<HTMLInputElement>("input");
const results = useTemplateRef<HTMLElement>("results");
const matches = computed(() => filterSessionTitles(props.sessions, query.value));

function focusSearch(): void {
  input.value?.focus({ preventScroll: true });
}
function select(id: string): void {
  open.value = false;
  emit("select", id);
}
function onSearchKey(event: KeyboardEvent): void {
  if (event.isComposing || event.keyCode === 229 || !matches.value.length) return;
  if (event.key === "Enter") {
    event.preventDefault();
    if (!event.repeat) select(matches.value[Math.max(active.value, 0)].id);
  } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    const delta = event.key === "ArrowDown" ? 1 : -1;
    const last = matches.value.length - 1;
    active.value = active.value < 0 && delta < 0 ? last : Math.max(0, Math.min(last, active.value + delta));
    nextTick(() => results.value?.children[active.value]?.scrollIntoView({ block: "nearest" }));
  }
}
watch(matches, () => {
  active.value = -1;
  if (results.value) results.value.scrollTop = 0;
});
watch(open, (shown) => {
  if (shown) focusSearch();
  else { query.value = ""; active.value = -1; }
}, { flush: "post" });
</script>

<template>
  <VDialog v-model="open" attach="#app" class="session-search-dialog" width="540" max-width="calc(100% - 24px)"
    aria-labelledby="session-search-title" @after-enter="focusSearch">
    <template #activator="{ props: activatorProps }">
      <button v-bind="activatorProps" class="icon-btn session-search-trigger" type="button" title="查找对话"
        aria-label="查找对话" :aria-expanded="open" v-html="ICONS.search"></button>
    </template>
    <VCard class="session-search-panel" color="surface-container">
      <div class="session-search-heading">
        <h2 id="session-search-title">查找对话</h2>
        <CloseButton label="关闭查找" @click="open = false" />
      </div>
      <div class="session-search-field">
        <input ref="input" v-model="query" type="search" role="combobox" autocomplete="off" enterkeyhint="go"
          aria-label="按标题查找对话" placeholder="按标题查找对话…" :aria-expanded="open" aria-autocomplete="list"
          aria-controls="session-search-results" :aria-activedescendant="active < 0 ? undefined : `session-result-${active}`" @keydown="onSearchKey" />
      </div>
      <div id="session-search-results" ref="results" class="session-search-results" role="listbox" aria-label="匹配的对话">
        <button v-for="(s, i) in matches" :id="`session-result-${i}`" :key="s.id" type="button" role="option" tabindex="-1"
          :aria-selected="i === active" @click="select(s.id)">
          <span aria-hidden="true" class="session-result-icon" v-html="ICONS.chat"></span>
          <span class="session-result-title">{{ sessionTitle(s.title) }}</span>
          <small v-if="s.id === currentId">当前</small>
        </button>
      </div>
      <p v-if="!matches.length" class="session-search-empty" role="status">{{ query.trim() ? "没有匹配的对话" : "还没有对话" }}</p>
    </VCard>
  </VDialog>
</template>

<style scoped>
.session-search-dialog :deep(.v-overlay__content) { align-self: flex-start; margin-block: min(8vh, 48px) 0; }
.session-search-panel { display: flex; flex-direction: column; max-height: min(31em, calc(var(--vvh, 100dvh) - min(8vh, 48px) - 12px)); }
.session-search-heading { display: flex; align-items: center; justify-content: space-between; gap: var(--chat-space); padding: var(--chat-space) var(--chat-space) 0 calc(2 * var(--chat-space)); }
.session-search-heading h2 { margin: 0; font-size: var(--md-type-title-medium); font-weight: 500; }
.session-search-field { padding: calc(var(--chat-space) / 2) calc(2 * var(--chat-space)) var(--chat-space); }
.session-search-field input { width: 100%; min-height: var(--chat-control); }
.session-search-results { flex: 1 1 auto; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 0 var(--chat-space); }
.session-search-results:not(:empty) { padding-bottom: var(--chat-space); }
.session-search-results button { display: flex; width: 100%; min-height: var(--chat-control); align-items: center; gap: var(--chat-space); padding: calc(var(--chat-space) / 2) var(--chat-space); border: 0; border-radius: var(--md-shape-sm); text-align: start; color: var(--md-on-surface); background: none; }
.session-search-results button:hover { background: var(--hover); }
.session-search-results button[aria-selected="true"], .session-search-results button:focus-visible { outline: 2px solid var(--md-primary); outline-offset: -2px; }
.session-result-icon { display: inline-flex; color: var(--md-on-surface-variant); }
.session-result-title { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.session-search-results small, .session-search-empty { color: var(--md-on-surface-variant); }
.session-search-empty { margin: 0; padding: var(--chat-space) calc(2 * var(--chat-space)) calc(2 * var(--chat-space)); }
</style>
