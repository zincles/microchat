<script setup>
// 连接门板：**进前端第一眼** —— 探得通后端就直接进聊天；探不通（含新设备第一次打开）
// 先在这儿输地址。点"检查连通性"：过 ⇒ 存偏好并刷新页面进聊天（组件的 api 客户端是装载时
// 建的，"改完刷新生效"本来就是老规矩，这里沿用）。
import { ref } from "vue";
import { probeHealth } from "../api/client.js";
import { apiBase, apiToken, saveNetPrefs } from "../utils/prefs.js";

const props = defineProps({
  initialError: { type: String, default: "" }, // 启动自检失败的原因（摆出来，别让人猜）
});
const base = ref(apiBase());
const token = ref(apiToken());
const status = ref(props.initialError ? `上次连接失败：${props.initialError}` : "");
const busy = ref(false);

async function check() {
  busy.value = true;
  status.value = "检查中…";
  try {
    const health = await probeHealth(base.value, token.value);
    saveNetPrefs(base.value, token.value);
    status.value = `已连接 v${health.version ?? "?"} —— 正在进入…`;
    location.reload();
  } catch (e) {
    if (e?.name === "AbortError") status.value = "连不上：超时（地址/网络/后端在不在？）";
    else if (e?.status === 401) status.value = "后端要口令：把 Token 填上再试";
    else if (e?.status) status.value = `后端回 HTTP ${e.status}（地址后缀是 /api/v1 吗？）`;
    else status.value = `连不上：${e?.message ?? e}`;
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div id="connect-gate">
    <div class="gate-box">
      <h2>连接到 microchat 后端</h2>
      <label class="field"><span>后端地址</span>
        <input v-model="base" placeholder="http://192.168.0.110:8787/api/v1" @keydown.enter="check" />
      </label>
      <label class="field"><span>Token（后端没设口令就留空）</span>
        <input v-model="token" type="password" placeholder="可空" @keydown.enter="check" />
      </label>
      <div class="row">
        <button type="button" class="primary" :disabled="busy" @click="check">检查连通性</button>
        <span class="status">{{ status }}</span>
      </div>
      <p class="hint">已按你打开这个页面的主机预填（后端默认在它的 8787）；只填到端口也行（自动补 /api/v1）。</p>
    </div>
  </div>
</template>
