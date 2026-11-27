<script setup lang="ts">
import { computed } from "vue";
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";

const { expanded = false } = defineProps<{ expanded?: boolean }>();
const label = computed(() => expanded ? "收起会话栏" : "打开会话栏");
</script>

<template>
  <VBtn type="button" class="icon-btn" icon variant="text" :title="label" :aria-label="label"
    :aria-expanded="expanded" aria-controls="sessions-bar">
    <span class="sidebar-toggle-glyph" aria-hidden="true" v-html="ICONS.panelLeft"></span>
  </VBtn>
</template>

<style scoped>
.sidebar-toggle-glyph { display: inline-flex; }
</style>
