<script setup lang="ts">
// 更新面板（2026-11-09 用户定）：显示当前提交，一键 git pull --ff-only + 重启前后端。
//
// 后端只负责拉取与写重启标记；真正重启由容器的 entrypoint 看家循环完成
//（重新编译 Go、重新构建 vite、再把两个进程拉起来）。前端在这里等新进程的
// `started_at` 变了才刷新 —— 只看 /health 会被尚未被收掉的旧进程骗过去。
import { computed, onMounted, ref } from "vue";
import { VBtn, VDivider, VSwitch } from "vuetify/components";
import { sharedApi, saveUpdateAutoCheck, updateAutoCheckOn } from "../utils/prefs.ts";
import { waitForRestart } from "../utils/update.ts";
import {
  applyPwaUpdate,
  checkPwaUpdate,
  fetchLatestBuild,
  forceReloadPwa,
  readPwaSnapshot,
} from "../utils/pwa.ts";
import type { PwaBuild, PwaSnapshot } from "../utils/pwa.ts";
import type { UpdateStatus } from "../api/types.ts";

const props = withDefaults(defineProps<{
  ask?: ((text: string) => Promise<boolean>) | null;
}>(), { ask: null });

const api = sharedApi();
const status = ref<UpdateStatus | null>(null);
const loading = ref(true);
const busy = ref(false);
const phase = ref<"idle" | "pulling" | "restarting">("idle");
const message = ref("");
const error = ref("");
const log = ref("");
// 自动检查更新：App 每 5 分钟拉一次 /update；这个开关只负责写偏好并通知 App。
const autoCheck = ref(updateAutoCheckOn());

// PWA 前端壳：当前页面构建身份（构建时注入） vs 服务器 build.json。
const pageBuildId = typeof __BUILD_ID__ !== "undefined" ? __BUILD_ID__ : "";
const pwaSnapshot = ref<PwaSnapshot | null>(null);
const latestBuild = ref<PwaBuild | null>(null);
const pwaBusy = ref(false);
const pwaError = ref("");

const supported = computed(() => !!status.value?.restart_supported);
const needsRebase = computed(() => status.value?.remote_relation === "diverged");
const currentBuildLabel = computed(() => buildLabel(pageBuildId, "未知（开发模式）"));
const latestBuildLabel = computed(() => latestBuild.value
  ? buildLabel(latestBuild.value.built_at || latestBuild.value.build_id, "未知")
  : "读取中…");
const pwaBuildMatch = computed(() => !!(latestBuild.value && pageBuildId
  && latestBuild.value.build_id === pageBuildId));
const pwaUpdateAvailable = computed(() => !!(pwaSnapshot.value?.waiting
  || (latestBuild.value && pageBuildId && latestBuild.value.build_id !== pageBuildId)));
const pwaOk = computed(() => pwaBuildMatch.value && !pwaSnapshot.value?.waiting);
const pwaSwLabel = computed(() => {
  const s = pwaSnapshot.value;
  if (!s) return "读取中…";
  if (s.error) return "读取失败";
  if (!s.supported) return "不支持";
  if (!s.secure) return "非安全上下文";
  if (s.waiting) return "新版本等待切换";
  if (s.installing) return "正在安装新版本";
  if (s.controlled) return "已接管";
  if (s.registered) return "已注册（当前页未受控）";
  return "未注册";
});
const pwaStatus = computed(() => {
  const s = pwaSnapshot.value;
  if (!s) return "正在读取 PWA 状态…";
  if (s.error) return "读取 PWA 状态失败。";
  if (!s.supported) return "此浏览器不支持 Service Worker；只能核对构建版本。";
  if (!s.secure) return "非安全上下文：PWA 不会注册（需要 HTTPS 或 localhost）。";
  if (pwaUpdateAvailable.value) {
    return s.waiting ? "发现新版本，可以立即切换。" : "服务器前端已有新版本，点「更新并刷新」。";
  }
  if (pwaBuildMatch.value) return "当前页面与服务器构建一致。";
  return "无法确认服务器前端版本（可能不是构建产物）。";
});
const buttonText = computed(() => {
  if (phase.value === "pulling") return needsRebase.value ? "正在变基…" : "正在拉取…";
  if (phase.value === "restarting") return "正在重启…";
  return needsRebase.value ? "变基并重启" : "拉取并重启";
});

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function buildLabel(value: string, fallback: string): string {
  if (!value) return fallback;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

function setAutoCheck(value: boolean | null): void {
  autoCheck.value = !!value;
  saveUpdateAutoCheck(autoCheck.value);
  window.dispatchEvent(new Event("mc-update-auto-check"));
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

async function load(): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    status.value = await api.getUpdateStatus();
  } catch (e) {
    error.value = `读取当前版本失败：${errText(e)}`;
  } finally {
    loading.value = false;
  }
}

async function loadPwa(forceCheck = false): Promise<void> {
  if (pwaBusy.value) return;
  pwaBusy.value = true;
  pwaError.value = "";
  try {
    latestBuild.value = await fetchLatestBuild().catch((e) => {
      pwaError.value = `读取服务器前端版本失败：${errText(e)}`;
      return null;
    });
    pwaSnapshot.value = forceCheck ? await checkPwaUpdate() : await readPwaSnapshot();
  } catch (e) {
    pwaError.value = `PWA 状态读取失败：${errText(e)}`;
  } finally {
    pwaBusy.value = false;
  }
}

async function applyPwa(): Promise<void> {
  if (pwaBusy.value) return;
  pwaBusy.value = true;
  pwaError.value = "";
  try {
    await applyPwaUpdate();
  } catch (e) {
    pwaError.value = `PWA 更新失败：${errText(e)}`;
  } finally {
    pwaBusy.value = false;
  }
}

async function forceReload(): Promise<void> {
  if (pwaBusy.value) return;
  if (props.ask && !(await props.ask("会注销 Service Worker 并清空前端缓存，然后重新加载页面。继续吗？"))) return;
  pwaBusy.value = true;
  pwaError.value = "";
  try {
    await forceReloadPwa();
  } catch (e) {
    pwaError.value = `强制重新加载失败：${errText(e)}`;
  } finally {
    pwaBusy.value = false;
  }
}

async function pullAndRestart(rebase = false): Promise<void> {
  if (busy.value) return;
  if (!supported.value) {
    error.value = "当前不是容器部署：可以拉取，但没有 entrypoint 能自动重启。请手动重启服务。";
    return;
  }
  const confirm = rebase
    ? "会把本地尚未推送的提交变基到远端最新提交上（冲突会自动中止并恢复），然后重启前后端，期间约 10–20 秒不可用。继续吗？"
    : "会拉取远程最新代码并重启前后端，期间约 10–20 秒不可用。继续吗？";
  if (props.ask && !(await props.ask(confirm))) return;
  busy.value = true;
  error.value = "";
  message.value = "";
  log.value = "";
  phase.value = "pulling";
  try {
    const result = rebase ? await api.rebaseUpdate() : await api.pullUpdate();
    status.value = result.after;
    log.value = result.output || "";
    message.value = result.changed
      ? `已${rebase ? "变基" : "拉取"}到 ${result.after.short || result.after.commit.slice(0, 7)}，正在重启前后端…`
      : "已经是最新提交，正在重启前后端…";
    phase.value = "restarting";
    await waitForRestart(result.before.started_at, api);
    message.value = "前后端已重启，正在刷新…";
    await sleep(600);
    location.reload();
  } catch (e) {
    phase.value = "idle";
    error.value = `更新失败：${errText(e)}`;
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  load().catch(() => {});
  loadPwa().catch(() => {});
});
</script>

<template>
  <section id="panel-update" class="panel">
    <section class="settings-card">
      <h3>版本更新</h3>
      <p class="hint">拉取仓库最新提交（只走 <code>git pull --ff-only</code>），成功后由容器 entrypoint 重新编译并重启前后端。</p>

      <div class="update-auto">
        <span>自动检查更新</span>
        <VSwitch
          :model-value="autoCheck"
          color="primary"
          density="compact"
          hide-details
          @update:model-value="setAutoCheck"
        />
      </div>
      <p class="hint">开启后每 5 分钟检查一次 GitHub；本地落后时顶部会出现「GitHub 有新提交」更新条。</p>
      <VDivider class="update-divider" />

      <section class="update-block pwa-block">
        <h4 class="update-section-title">PWA / 已安装应用</h4>
        <p class="hint">核对当前页面与服务器上的前端构建；如果安装的 PWA 还显示旧界面，用这里直接检查并切换。</p>
        <div class="update-row"><span>当前页面</span><code :title="pageBuildId">{{ currentBuildLabel }}</code></div>
        <div class="update-row"><span>服务器最新</span><code :title="latestBuild?.build_id">{{ latestBuildLabel }}</code></div>
        <div class="update-row"><span>Service Worker</span><span>{{ pwaSwLabel }}</span></div>
        <p class="update-compare"
          :class="pwaUpdateAvailable ? 'update-warn' : (pwaOk ? 'update-ok' : '')">{{ pwaStatus }}</p>
        <div class="row">
          <VBtn color="primary" variant="flat" size="small" type="button"
            :loading="pwaBusy" :disabled="pwaBusy" @click="loadPwa(true)">
            检查 PWA 更新
          </VBtn>
          <VBtn v-if="pwaUpdateAvailable" color="primary" variant="tonal" size="small" type="button"
            :disabled="pwaBusy" @click="applyPwa">
            更新并刷新
          </VBtn>
          <VBtn variant="text" size="small" type="button" :disabled="pwaBusy" @click="forceReload">
            强制重新加载
          </VBtn>
        </div>
        <p v-if="pwaError" class="hint update-warn">{{ pwaError }}</p>
      </section>

      <VDivider class="update-divider" />

      <div v-if="loading" class="hint">读取当前版本…</div>
      <template v-else-if="status">
        <div class="update-version">
          <section class="update-block">
            <h4 class="update-section-title">当前本地版本</h4>
            <div class="update-row"><span>提交</span><code>{{ status.short || status.commit }}</code></div>
            <div class="update-row"><span>分支</span><code>{{ status.branch || "（detached）" }}</code></div>
            <div v-if="status.subject" class="update-subject">{{ status.subject }}</div>
            <div class="update-hash" :title="status.commit">{{ status.commit }}</div>
            <p v-if="status.dirty" class="hint update-warn">
              工作区有 {{ status.changed_files }} 个未提交改动；<code>git pull --ff-only</code> 可能因此失败，失败时不会重启。
            </p>
          </section>

          <VDivider class="update-divider" />

          <section class="update-block">
            <h4 class="update-section-title">GitHub 最新版本</h4>
            <div class="update-row"><span>提交</span><code>{{ status.remote_short || "—" }}</code></div>
            <div v-if="status.remote_subject" class="update-subject">{{ status.remote_subject }}</div>
            <div v-if="status.remote_commit" class="update-hash" :title="status.remote_commit">{{ status.remote_commit }}</div>
            <p v-if="status.remote_commit" class="update-compare"
              :class="status.remote_commit === status.commit ? 'update-ok' : 'update-warn'"
              :title="status.remote_commit">
              <template v-if="status.remote_commit === status.commit">✓ 已是最新提交</template>
              <template v-else-if="status.remote_relation === 'ahead'">
                本地领先远端 {{ status.ahead || 0 }} 个提交（尚未推送）
              </template>
              <template v-else-if="status.remote_relation === 'behind'">
                远端有新提交 {{ status.behind || 0 }} 个，可拉取并重启
              </template>
              <template v-else-if="status.remote_relation === 'diverged'">
                本地与远端已分叉，可「变基并重启」
              </template>
              <template v-else>远端有新提交，可拉取并重启</template>
            </p>
            <p v-else-if="status.remote_error" class="hint update-warn">
              无法读取 GitHub 最新提交：{{ status.remote_error }}
            </p>
          </section>
        </div>
        <p v-if="!supported" class="hint update-warn">
          当前不是容器部署（没有 <code>MICROCHAT_RESTART_FILE</code>）：拉取后不会自动重启。
        </p>


        <div class="row">
          <VBtn color="primary" variant="flat" size="small" type="button"
            :loading="phase !== 'idle'" :disabled="phase !== 'idle' || !supported"
            @click="pullAndRestart(needsRebase)">
            {{ buttonText }}
          </VBtn>
          <VBtn variant="text" size="small" type="button" :disabled="busy" @click="load">刷新状态</VBtn>
          <span class="status">{{ message }}</span>
        </div>
        <p v-if="error" class="hint update-error">{{ error }}</p>
        <pre v-if="log" class="update-log">{{ log }}</pre>
      </template>
      <p v-else-if="error" class="hint update-error">{{ error }}</p>

      <section class="project-about">
        <h3>关于 MicroChat</h3>
        <p>
          MicroChat 是一个轻量、可自托管的 SillyTavern 替代品，面向角色扮演与长对话：
          Go 后端负责上下文、压缩与状态，Vue 前端负责阅读与编辑体验。基础功能不依赖外部账号，数据落在自己的 <code>data/</code> 目录。
        </p>
        <p class="project-authors">作者：Zincles、DeepSeekV4.1Flash</p>
        <p class="project-link">
          <a href="https://github.com/zincles/microchat" target="_blank" rel="noopener noreferrer">github.com/zincles/microchat</a>
        </p>
      </section>
    </section>
  </section>
</template>
