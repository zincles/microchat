<script setup lang="ts">
// 顶部更新条（2026-10-20 用户定 PWA；2026-11-09 加后端 GitHub 更新）。
//
// 两种来源：
//   - pwa：浏览器里旧的 Service Worker 发现新壳，需要「刷新」；
//   - backend：自动检查发现本地落后 GitHub，需要「更新」（git pull + 重启前后端）。
// **不自动消失**：这是要求用户行动的通知；消失等于没通知。
import { computed, ref, watch } from "vue";
import { VBtn } from "vuetify/components";

const props = withDefaults(defineProps<{
  pwa?: boolean;
  backend?: boolean;
  busy?: boolean;
}>(), {
  pwa: false,
  backend: false,
  busy: false,
});
const emit = defineEmits<{ update: []; "update-backend": [] }>();

const pwaBusy = ref(false); // PWA 刷新中（等新 SW 接管）
const hidden = ref(false); // 用户不想现在更（×）：先收起来
const busy = computed(() => pwaBusy.value || props.busy);

// 后端更新出现时强行放出来：即使之前把 PWA 提示收起来过，也别吞掉新的更新。
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
  <div v-if="backend || !hidden" id="update-bar" role="status">
    <span class="ub-text">
      <template v-if="backend">GitHub 有新提交，可更新前后端</template>
      <template v-else>有新版本可用</template>
      <template v-if="backend && pwa">；前端也有新版本</template>
      <template v-if="busy">，正在更新…</template>
    </span>
    <VBtn v-if="backend" class="ub-go" color="primary" variant="flat" size="small" :disabled="busy" @click="updateBackend">
      {{ busy ? "更新中…" : "更新" }}
    </VBtn>
    <VBtn v-else class="ub-go" color="primary" variant="flat" size="small" :disabled="busy" @click="go">
      {{ busy ? "更新中…" : "刷新" }}
    </VBtn>
    <VBtn v-if="!backend" class="ub-x" icon variant="text" size="small" title="先不更新" aria-label="先不更新" :disabled="busy" @click="hidden = true">×</VBtn>
  </div>
</template>
