<script setup lang="ts">
// 顶部更新条：PWA 新壳 / GitHub 后端更新共用。**不自动消失**，由用户点。
import { computed, ref, watch } from "vue";
import { VAlert, VBtn } from "vuetify/components";

const props = withDefaults(defineProps<{
  pwa?: boolean;
  backend?: boolean;
  busy?: boolean;
}>(), { pwa: false, backend: false, busy: false });
const emit = defineEmits<{ update: []; "update-backend": [] }>();

const pwaBusy = ref(false);
const hidden = ref(false);
const busy = computed(() => pwaBusy.value || props.busy);
watch(() => props.backend, (on) => { if (on) hidden.value = false; });

function go(): void {
  if (busy.value) return;
  pwaBusy.value = true;
  emit("update");
}
function updateBackend(): void {
  if (busy.value) return;
  emit("update-backend");
}
defineExpose({ show: (): void => { pwaBusy.value = false; hidden.value = false; } });
</script>

<template>
  <VAlert
    v-if="backend || !hidden"
    id="update-bar"
    color="primary"
    variant="tonal"
    density="compact"
    rounded="0"
    role="status"
    class="update-bar"
  >
    <span class="ub-text">
      <template v-if="backend">GitHub 有新提交，可更新前后端</template>
      <template v-else>新版本已就绪，刷新页面应用</template>
      <template v-if="backend && pwa">；前端也有新版本</template>
      <template v-if="busy">，正在更新…</template>
    </span>
    <template #append>
      <VBtn v-if="backend" color="primary" variant="flat" size="small" :disabled="busy" @click="updateBackend">
        {{ busy ? "更新中…" : "更新" }}
      </VBtn>
      <VBtn v-else color="primary" variant="flat" size="small" :disabled="busy" @click="go">
        {{ busy ? "更新中…" : "刷新" }}
      </VBtn>
      <VBtn v-if="!backend" icon variant="text" size="small" title="先不更新" aria-label="先不更新" :disabled="busy" @click="hidden = true">×</VBtn>
    </template>
  </VAlert>
</template>

<style scoped>
#update-bar { flex: none; margin: 0; width: 100%; border-radius: 0; }
.ub-text { flex: 1; min-width: 0; line-height: 1.45; word-break: break-word; }
</style>
