<script setup>
// 连接门板：**进前端第一眼** —— 探得通后端就直接进聊天；探不通 / 还没登录就先在这儿办。
// 多用户（2026-10-10）：探测 `/auth/status` 有两个答案 —— 还没初始化 ⇒ 给 root（uid 0，建库种子）设口令；
// 已有用户 ⇒ 登录。过 ⇒ 存凭据（JWT 进 mc_token）+ 刷新页面进聊天（组件的 api 客户端是装载时
// 建的，"改完刷新生效"本来就是老规矩，这里沿用）。
// 地址输入框（2026-10-14 用户定）：**只有连不上时才出现** —— 单容器部署里浏览器永远同源
// （/api 由 vite 代理），探通了就说明地址是对的，登录界面不该再让人填一遍后端地址。
import { ref, computed, onMounted } from "vue";
import { probeAuthStatus, authLogin, authSetup } from "../api/client.js";
import { apiBase, saveNetPrefs } from "../utils/prefs.js";

const props = defineProps({
  initialError: { type: String, default: "" }, // 启动自检失败的原因（摆出来，别让人猜）
});
const base = ref(apiBase());
const user = ref("");
const password = ref("");
const password2 = ref(""); // setup 模式的确认口令（口误一次就锁死，得防）
const status = ref(props.initialError ? `上次连接失败：${props.initialError}` : "");
const busy = ref(false);
const mode = ref(""); // "" 还没探测（或又断了）· "setup" 建管理员 · "login" 登录
const needAddress = computed(() => !mode.value); // 探通了就收起地址栏

onMounted(async () => {
  try {
    const st = await probeAuthStatus(base.value);
    mode.value = st.needs_setup ? "setup" : "login";
    status.value = st.needs_setup
      ? `后端 v${st.version} 还没初始化：设置 root 口令（uid 0，第一个用户）`
      : `已发现后端 v${st.version}：请登录`;
  } catch (e) {
    status.value = `连不上：${e?.message ?? e}`;
  }
});

async function connect() {
  busy.value = true;
  try {
    const st = await probeAuthStatus(base.value);
    mode.value = st.needs_setup ? "setup" : "login";
    if (st.needs_setup) {
      if (!password.value) { status.value = "想个口令（≥6 位），再点一次"; return; }
      if (password.value !== password2.value) { status.value = "两次口令不一致"; return; }
      const out = await authSetup(base.value, password.value);
      saveNetPrefs(base.value, out.token);
      status.value = `已初始化 root（${out.user.name}）—— 正在进入…`;
      location.reload();
      return;
    }
    if (!user.value.trim() || !password.value) { status.value = "填用户名与口令，再点一次"; return; }
    const out = await authLogin(base.value, user.value.trim(), password.value);
    saveNetPrefs(base.value, out.token);
    status.value = `已连接 v${st.version}（${out.user.name}${out.user.is_admin ? " · 管理员" : ""}）—— 正在进入…`;
    location.reload();
  } catch (e) {
    if (e?.status === 401) status.value = "用户名或口令不对";
    else if (e?.status === 403) status.value = "root 已经初始化过了 —— 请改用登录";
    else if (e?.status === 400) status.value = e.message;
    else {
      status.value = `连不上：${e?.message ?? e}`;
      mode.value = ""; // 又断了 ⇒ 把地址栏放回来（可能是地址填错/后端停/换了网络）
    }
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div id="connect-gate">
    <div class="gate-box">
      <h2>{{ mode === "setup" ? "初始化 root" : mode === "login" ? "登录 microchat" : "连接到 microchat 后端" }}</h2>
      <label class="field" v-if="needAddress"><span>后端地址</span>
        <input v-model="base" placeholder="http://192.168.0.110:8787/api/v1" @keydown.enter="connect" />
      </label>
      <label class="field" v-if="mode !== 'setup'"><span>用户名/uid</span>
        <input v-model="user" placeholder="root 或 0" @keydown.enter="connect" />
      </label>
      <label class="field"><span>{{ mode === "setup" ? "root 口令（≥6 位）" : "口令" }}</span>
        <input v-model="password" type="password" placeholder="≥6 位" @keydown.enter="connect" />
      </label>
      <label class="field" v-if="mode === 'setup'"><span>再输一次</span>
        <input v-model="password2" type="password" placeholder="确认口令" @keydown.enter="connect" />
      </label>
      <div class="row">
        <button type="button" class="primary" :disabled="busy" @click="connect">
          {{ mode === "setup" ? "初始化并进入" : "登录" }}
        </button>
        <span class="status">{{ status }}</span>
      </div>
      <p class="hint" v-if="needAddress">已按你打开这个页面的主机预填（后端默认在它的 8787）；只填到端口也行（自动补 /api/v1）。</p>
    </div>
  </div>
</template>
