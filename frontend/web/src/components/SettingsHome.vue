<script setup lang="ts">
import { computed, shallowRef, useTemplateRef } from "vue";
import { VBtn, VTextField } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";
import { markdownTypes } from "../utils/prefs.ts";
import { searchSettings, SETTINGS_GROUPS, type SettingsLocation } from "../utils/settingsCatalog.ts";

const emit = defineEmits<{ select: [target: SettingsLocation] }>();
const query = shallowRef("");
const searchForm = useTemplateRef<HTMLFormElement>("searchForm");
const searching = computed(() => !!query.value.trim());
const results = computed(() => searchSettings(query.value, markdownTypes()));
type Entry = SettingsLocation & { label: string; desc: string; icon: keyof typeof ICONS };
const directory: { label: string; entries: Entry[] }[] = SETTINGS_GROUPS.map(({ label, pages }) => ({
  label, entries: pages.map(({ id: page, ...entry }) => ({ page, ...entry })),
}));
const visibleGroups = computed(() => searching.value ? [{ label: "搜索结果", entries: results.value }] : directory);

let lastEntry: HTMLElement | null = null;
function select(entry: SettingsLocation, event?: MouseEvent): void {
  lastEntry = (event?.currentTarget as HTMLElement | null) ?? null;
  emit("select", entry);
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
    <p v-if="searching && !results.length" class="settings-list-note" role="status">没有找到匹配的设置。试试其他关键词。</p>
    <template v-else>
      <section v-for="group in visibleGroups" :key="group.label" class="settings-directory-group">
        <h3 class="settings-directory-label">{{ group.label }}</h3>
        <ul class="settings-list" :aria-label="group.label">
          <li v-for="entry in group.entries" :key="entry.anchor ?? entry.page" class="settings-list-row">
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
  </section>
</template>
