<script setup lang="ts">
import { VProgressCircular } from "vuetify/components";

defineProps<{ label: string }>();
</script>

<template>
  <div class="request-loading" role="status">
    <VProgressCircular indeterminate size="20" width="2" color="primary" aria-hidden="true" />
    <span>{{ label }}</span>
  </div>
</template>

<style scoped>
.request-loading {
  display: flex;
  align-items: center;
  gap: var(--chat-space);
  min-height: var(--chat-control);
  color: var(--md-on-surface-variant);
  font-size: var(--md-type-body-medium);
}
</style>
