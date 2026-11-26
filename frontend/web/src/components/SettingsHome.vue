<script setup lang="ts">
import { computed, nextTick, shallowRef, useTemplateRef } from "vue";
import { VBtn, VTextField } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import { markdownTypes } from "../utils/prefs.ts";
import { searchSettings, SETTINGS_GROUPS, type SettingsLocation } from "../utils/settingsCatalog.ts";

type Entry = SettingsLocation & { label: string; desc: string; icon: keyof typeof ICONS };
const emit = defineEmits<{ select: [target: SettingsLocation] }>();
const query = shallowRef("");
const selectedGroup = shallowRef("");
const searchForm = useTemplateRef<HTMLFormElement>("searchForm");
const categoryList = useTemplateRef<HTMLUListElement>("categoryList");
const searching = computed(() => !!query.value.trim());
const results = computed(() => searchSettings(query.value, markdownTypes()));
const categories = SETTINGS_GROUPS.map(({ label, desc, icon }) => ({ label, desc, icon }));
const directory = SETTINGS_GROUPS.map(({ label, pages }) => ({
  label, entries: pages.map(({ id: page, ...entry }) => ({ page, ...entry })),
}));
const activeDirectory = computed(() => directory.find((group) => group.label === selectedGroup.value));

let lastEntry: HTMLElement | null = null;
function select(entry: SettingsLocation, event?: MouseEvent): void {
  lastEntry = (event?.currentTarget as HTMLElement | null) ?? null;
  selectedGroup.value = "";
  emit("select", entry);
}
function selectCategory(label: string, event: MouseEvent): void {
  lastEntry = event.currentTarget as HTMLElement;
  selectedGroup.value = label;
}
async function backToCategories(): Promise<void> {
  const label = selectedGroup.value;
  selectedGroup.value = "";
  await nextTick();
  if (label) categoryList.value?.querySelector<HTMLButtonElement>(`button[data-category="${CSS.escape(label)}"]`)?.focus();
}
function selectFirst(): void {
  if (results.value[0]) select(results.value[0]);
}
function clearSearch(event: KeyboardEvent): void {
  if (!query.value) return;
  event.stopPropagation();
  query.value = "";
}
function restoreFocus(): void {
  const entry = lastEntry?.isConnected ? lastEntry : searchForm.value?.querySelector("input");
  entry?.focus();
}
defineExpose({ restoreFocus });
</script>

<template>
  <section id="settings-home" class="panel">
    <form ref="searchForm" class="settings-search" role="search" @submit.prevent="selectFirst">
      <VTextField v-model="query" id="settings-query" type="search" variant="solo-filled" density="compact"
        flat rounded="lg" hide-details aria-label="搜索设置" placeholder="搜索设置，例如：字号、回车、自动压缩"
        @keydown.esc="clearSearch">
        <template #prepend-inner><span class="settings-search-icon" aria-hidden="true" v-html="ICONS.search"></span></template>
      </VTextField>
    </form>
    <template v-if="searching">
      <section class="settings-directory-group">
        <h3 class="settings-directory-label">搜索结果</h3>
        <p v-if="!results.length" class="settings-list-note" role="status">没有找到匹配的设置。试试其他关键词。</p>
        <ul v-else class="settings-list" aria-label="搜索结果">
          <li v-for="entry in results" :key="entry.anchor ?? entry.page" class="settings-list-row">
            <VBtn type="button" variant="text" class="settings-list-entry" :aria-label="entry.label" @click="select(entry, $event)">
              <span class="settings-list-icon" aria-hidden="true" v-html="ICONS[entry.icon]"></span>
              <span class="settings-list-copy">
                <strong>{{ entry.label }}</strong>
                <span class="settings-list-note">{{ entry.desc }}</span>
              </span>
              <span class="settings-list-chevron" aria-hidden="true" v-html="ICONS.chevronRight"></span>
            </VBtn>
          </li>
        </ul>
      </section>
    </template>
    <template v-else-if="activeDirectory">
      <section class="settings-directory-group">
        <div class="panel-head">
          <VBtn icon variant="text" size="small" type="button" aria-label="返回设置类别" title="返回设置类别" @click="backToCategories">
            <span aria-hidden="true" v-html="ICONS.arrowLeft"></span>
          </VBtn>
          <h3 class="settings-directory-label">{{ activeDirectory.label }}</h3>
        </div>
        <ul class="settings-list" :aria-label="activeDirectory.label">
          <li v-for="entry in activeDirectory.entries" :key="entry.page" class="settings-list-row">
            <VBtn type="button" variant="text" class="settings-list-entry" :aria-label="entry.label" @click="select(entry)">
              <span class="settings-list-icon" aria-hidden="true" v-html="ICONS[entry.icon]"></span>
              <span class="settings-list-copy">
                <strong>{{ entry.label }}</strong>
                <span class="settings-list-note">{{ entry.desc }}</span>
              </span>
              <span class="settings-list-chevron" aria-hidden="true" v-html="ICONS.chevronRight"></span>
            </VBtn>
          </li>
        </ul>
      </section>
    </template>
    <ul v-else ref="categoryList" class="settings-list" aria-label="设置类别">
      <li v-for="category in categories" :key="category.label" class="settings-list-row">
        <VBtn type="button" variant="text" class="settings-list-entry" :aria-label="category.label"
          :data-category="category.label" @click="selectCategory(category.label, $event)">
          <span class="settings-list-icon" aria-hidden="true" v-html="ICONS[category.icon]"></span>
          <span class="settings-list-copy">
            <strong>{{ category.label }}</strong>
            <span class="settings-list-note">{{ category.desc }}</span>
          </span>
          <span class="settings-list-chevron" aria-hidden="true" v-html="ICONS.chevronRight"></span>
        </VBtn>
      </li>
    </ul>
  </section>
</template>
