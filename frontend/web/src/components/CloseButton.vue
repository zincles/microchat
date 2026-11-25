<script setup lang="ts">
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";

withDefaults(defineProps<{ label?: string; title?: string }>(), { label: "关闭" });
const emit = defineEmits<{ click: [event: MouseEvent] }>();
</script>

<template>
  <VBtn class="close-button" icon rounded="circle" size="small" variant="tonal" color="secondary" type="button"
    :aria-label="label" :title="title || label" @click="emit('click', $event)">
    <span class="close-icon" aria-hidden="true" v-html="ICONS.x"></span>
  </VBtn>
</template>

<style scoped>
:is(#app, .v-overlay-container) .close-button { flex: none; width: var(--chat-control, 32px); height: var(--chat-control, 32px); padding: 0; }
.close-icon { display: inline-flex; }
.close-button .close-icon :deep(svg) { width: 20px; height: 20px; }
.close-button:focus-visible { outline: 2px solid var(--md-primary); outline-offset: -2px; }
</style>
