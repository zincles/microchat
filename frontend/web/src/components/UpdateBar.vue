<script setup>
// 更新条（2026-10-20 用户定）：检测到新版本时挂在顶上的一条 —— **不自动消失**，
// 由用户点「刷新」才切过去（切的动作在 utils/pwa.js，这里只管长相与按钮）。
//
// 为什么不复用 Toast：Toast 3 秒自动消 —— 对"点我一下"这种要求用户行动的通知，
// 3 秒后消失等于没通知（人没看见就错过了，而这正是这次要治的病）。
import { ref } from "vue";

const emit = defineEmits(["update"]);

const busy = ref(false); // 点了之后禁用按钮 —— 别让人重复点（切换要等新 SW 接管）
const hidden = ref(false); // 用户不想现在更（×）：先收起来，下次开机还会再提示

function go() {
  if (busy.value) return;
  busy.value = true;
  emit("update");
}
defineExpose({ show: () => { busy.value = false; hidden.value = false; } });
</script>

<template>
  <div v-if="!hidden" id="update-bar" role="status">
    <span class="ub-text">有新版本可用{{ busy ? "，正在更新…" : "" }}</span>
    <button type="button" class="ub-go" :disabled="busy" @click="go">
      {{ busy ? "更新中…" : "刷新" }}
    </button>
    <button type="button" class="ub-x" title="先不更新" aria-label="先不更新" :disabled="busy" @click="hidden = true">×</button>
  </div>
</template>
