<script setup lang="ts">
import { VBtn } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";

withDefaults(defineProps<{ label?: string }>(), { label: "返回列表" });
const emit = defineEmits<{ back: [] }>();
</script>

<template>
  <VBtn class="settings-back" variant="tonal" color="secondary" size="small" type="button" @click="emit('back')">
    <template #prepend><span aria-hidden="true" v-html="ICONS.arrowLeft"></span></template>
    {{ label }}
  </VBtn>
</template>

<style scoped>
#app .settings-back { flex: none; min-height: var(--chat-control); padding-inline: var(--chat-space); font-size: var(--md-type-label-large); letter-spacing: normal; }
.settings-back span { display: inline-flex; }
#app .settings-back:focus-visible { outline: 2px solid var(--md-primary); outline-offset: -2px; }
</style>
