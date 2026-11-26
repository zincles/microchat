<script setup lang="ts">
import { VBtn, VList, VListItem, VMenu } from "vuetify/components";
import { ICONS } from "../assets/icons.ts";

export interface OverflowAction {
  icon: string;
  run: () => void;
  label: string;
  disabled?: boolean;
  hint?: string;
  loading?: boolean;
  destructive?: boolean;
}

const props = defineProps<{
  menuLabel?: string;
  actions: readonly OverflowAction[];
}>();
</script>

<template>
  <VMenu v-if="menuLabel" location="top end" :offset="4">
    <template #activator="{ props: activatorProps }">
      <VBtn
        v-bind="activatorProps"
        icon
        type="button"
        variant="text"
        size="small"
        :title="menuLabel"
        :aria-label="menuLabel"
        :loading="actions.some(action => action.loading)"
      >
        <span aria-hidden="true" v-html="ICONS.more" />
      </VBtn>
    </template>
    <VList role="menu" :aria-label="menuLabel" bg-color="surface-container-high" rounded="md">
      <VListItem v-for="action in actions" :key="action.label" role="menuitem"
        :title="action.label" :subtitle="action.hint" :disabled="action.disabled || action.loading"
        @click="action.run()" />
    </VList>
  </VMenu>
  <template v-else>
    <VBtn v-for="action in props.actions" :key="action.label" icon type="button" variant="text" size="small"
      :title="action.hint ? `${action.label}（${action.hint}）` : action.label" :aria-label="action.label" :class="{ 'msg-delete': action.destructive }"
      :loading="action.loading" :disabled="action.disabled || action.loading" @click="action.run()">
      <span aria-hidden="true" v-html="action.icon" />
    </VBtn>
  </template>
</template>
