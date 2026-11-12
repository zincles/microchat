<script setup lang="ts">
// 统一的轻量模态壳：VDialog + VCard，关闭/遮罩/Esc 都交给 Vuetify。
// 业务组件只提供 title / 正文 / actions，不再手写 .modal / .modal-box。
import { ref, watch } from "vue";
import { VBtn, VCard, VCardActions, VCardText, VCardTitle, VDialog } from "vuetify/components";

const props = withDefaults(defineProps<{
  title?: string;
  width?: string | number;
  maxWidth?: string | number;
  maxHeight?: string;
}>(), {
  title: "",
  width: 560,
  maxWidth: 560,
  maxHeight: "88vh",
});
const emit = defineEmits<{ close: [] }>();
const open = ref(true);
watch(open, (value) => { if (!value) emit("close"); });
</script>

<template>
  <VDialog v-model="open" attach="#app" :width="width" :max-width="maxWidth" scrim="rgba(0, 0, 0, 0.45)">
    <VCard class="modal-shell" elevation="3" :style="{ maxHeight }">
      <VCardTitle class="modal-shell-title">
        <slot name="title">{{ title }}</slot>
        <VBtn icon variant="text" size="small" title="关闭" aria-label="关闭" @click="open = false">×</VBtn>
      </VCardTitle>
      <VCardText class="modal-shell-body">
        <slot />
      </VCardText>
      <VCardActions v-if="$slots.actions" class="modal-shell-actions">
        <slot name="actions" />
      </VCardActions>
    </VCard>
  </VDialog>
</template>

<style scoped>
.modal-shell { display: flex; flex-direction: column; overflow: hidden; }
.modal-shell-title { display: flex; align-items: center; justify-content: space-between; gap: 0.8em; font-size: var(--md-type-title-medium); }
.modal-shell-body { overflow-y: auto; overscroll-behavior: contain; }
.modal-shell-actions { gap: 0.4em; }
</style>
