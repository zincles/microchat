<script setup lang="ts">
// 更新面板（2026-11-09 用户定）：显示当前提交，一键 git pull --ff-only + 重启前后端。
//
// 后端只负责拉取与写重启标记；真正重启由容器的 entrypoint 看家循环完成
//（重新编译 Go、重新构建 vite、再把两个进程拉起来）。前端在这里等新进程的
// `started_at` 变了才刷新 —— 只看 /health 会被尚未被收掉的旧进程骗过去。
import { computed, onMounted, ref, shallowRef } from "vue";
import { VBtn, VSwitch, VTextField } from "vuetify/components";
import { sharedApi, saveBrandPrefs, prefsSnapshot, refreshPrefs, saveUpdateAutoCheck, updateAutoCheckOn } from "../utils/prefs.ts";
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
import RequestLoading from "./RequestLoading.vue";
import { ICONS } from "../assets/icons.ts";

const props = withDefaults(defineProps<{
  ask?: ((text: string) => Promise<boolean>) | null;
}>(), { ask: null });
const emit = defineEmits<{ "prefs-saved": [] }>();
const api = sharedApi();
const brandName = ref("MicroChat");
const brandIcon = ref<"spark" | "chat" | "bot" | "messagePlus">("spark");
const welcomeText = ref("有什么想聊的？");
const savingBrand = ref(false);
const brandError = ref("");
const brandIcons = { spark: "星芒", chat: "对话气泡", bot: "机器人", messagePlus: "新对话" } as const;

function loadBrandPrefs(): void {
  const prefs = prefsSnapshot();
  brandName.value = prefs.brand_name;
  brandIcon.value = prefs.brand_icon;
  welcomeText.value = prefs.welcome_text;
}

async function saveBrand(): Promise<void> {
  savingBrand.value = true;
  brandError.value = "";
  try {
    await saveBrandPrefs({ brand_name: brandName.value, brand_icon: brandIcon.value, welcome_text: welcomeText.value });
    loadBrandPrefs();
    emit("prefs-saved");
  } catch {
    brandError.value = "保存失败，请检查连接后重试。";
  } finally {
    savingBrand.value = false;
  }
}
const status = ref<UpdateStatus | null>(null);
const loading = shallowRef(false);
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
const pwaAction = shallowRef<"initial" | "check" | "apply" | "reload" | null>("initial");
const pwaError = ref("");

const supported = computed(() => !!status.value?.restart_supported);
const needsRebase = computed(() => status.value?.remote_relation === "diverged");
const currentBuildLabel = computed(() => buildLabel(pageBuildId, "未知（开发模式）"));
const latestBuildLabel = computed(() => latestBuild.value
  ? buildLabel(latestBuild.value.built_at || latestBuild.value.build_id, "未知")
  : "未知");
const pwaBuildMatch = computed(() => !!(latestBuild.value && pageBuildId
  && latestBuild.value.build_id === pageBuildId));
const pwaUpdateAvailable = computed(() => !!(pwaSnapshot.value?.waiting
  || (latestBuild.value && pageBuildId && latestBuild.value.build_id !== pageBuildId)));
const pwaOk = computed(() => pwaBuildMatch.value && !pwaSnapshot.value?.waiting);
const pwaSwLabel = computed(() => {
  const s = pwaSnapshot.value;
  if (!s) return "读取失败";
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
  if (!s) return "读取 PWA 状态失败。";
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
  if (loading.value || busy.value) return;
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
  if (forceCheck && pwaAction.value) return;
  pwaAction.value = forceCheck ? "check" : "initial";
  pwaError.value = "";
  try {
    try {
      latestBuild.value = await fetchLatestBuild();
    } catch (e) {
      pwaError.value = `读取服务器前端版本失败：${errText(e)}`;
    }
    pwaSnapshot.value = forceCheck ? await checkPwaUpdate() : await readPwaSnapshot();
  } catch (e) {
    pwaError.value = `PWA 状态读取失败：${errText(e)}`;
  } finally {
    pwaAction.value = null;
  }
}

async function applyPwa(): Promise<void> {
  if (pwaAction.value) return;
  pwaAction.value = "apply";
  pwaError.value = "";
  try {
    await applyPwaUpdate();
  } catch (e) {
    pwaError.value = `PWA 更新失败：${errText(e)}`;
  } finally {
    pwaAction.value = null;
  }
}

async function forceReload(): Promise<void> {
  if (pwaAction.value) return;
  if (props.ask && !(await props.ask("会注销 Service Worker 并清空前端缓存，然后重新加载页面。继续吗？"))) return;
  if (pwaAction.value) return;
  pwaAction.value = "reload";
  pwaError.value = "";
  try {
    await forceReloadPwa();
  } catch (e) {
    pwaError.value = `强制重新加载失败：${errText(e)}`;
  } finally {
    pwaAction.value = null;
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
  refreshPrefs().then(loadBrandPrefs).catch(() => loadBrandPrefs());
});
</script>

<template>
  <section id="panel-update" class="panel">
    <RequestLoading
      v-if="(loading && !status) || pwaAction === 'initial'"
      :label="loading && !status
        ? (pwaAction === 'initial' ? '读取当前版本与 PWA 状态…' : '读取当前版本…')
        : '读取前端版本与 PWA 状态…'"
    />
    <div class="update-grid">
      <section class="settings-group">
        <h3>版本</h3>
        <div class="settings-group-body">
          <div id="setting-auto-update" class="settings-item" tabindex="-1">
            <label class="field">
              <span>自动更新（每 5 分钟）</span>
              <VSwitch
                :model-value="autoCheck"
                color="primary"
                density="compact"
                hide-details
                aria-describedby="auto-update-hint"
                @update:model-value="setAutoCheck"
              />
            </label>
            <p id="auto-update-hint" class="hint">开启后每 5 分钟检查 GitHub；只有远端明确落后、工作区干净且容器支持重启时才自动拉取并重启前后端。有未提交修改、分叉、远端状态不明或非容器部署时只提示手动处理。</p>
          </div>

          <template v-if="status">
            <div class="update-version-grid">
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
                  <template v-else>本地与远端提交不同，可拉取并重启</template>
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
                :loading="phase !== 'idle'" :disabled="loading || phase !== 'idle' || !supported"
                @click="pullAndRestart(needsRebase)">
                {{ buttonText }}
              </VBtn>
              <VBtn variant="text" size="small" type="button" :loading="loading" :disabled="busy || loading" @click="load">刷新状态</VBtn>
              <span class="status">{{ message }}</span>
            </div>
            <p v-if="error" class="hint update-error">{{ error }}</p>
            <pre v-if="log" class="update-log">{{ log }}</pre>
          </template>
          <p v-else-if="error" class="hint update-error">{{ error }}</p>
        </div>
      </section>

      <section id="setting-pwa" tabindex="-1" class="settings-group" aria-labelledby="pwa-title">
        <h3 id="pwa-title">前端缓存（PWA）</h3>
        <div class="settings-group-body">
          <p class="hint">核对当前页面与服务器上的前端构建；如果安装的 PWA 还显示旧界面，用这里直接检查并切换。</p>
          <div class="update-row"><span>当前页面</span><code :title="pageBuildId">{{ currentBuildLabel }}</code></div>
          <template v-if="pwaAction !== 'initial'">
            <div class="update-row"><span>服务器最新</span><code :title="latestBuild?.build_id">{{ latestBuildLabel }}</code></div>
            <div class="update-row"><span>Service Worker</span><span>{{ pwaSwLabel }}</span></div>
            <p class="update-compare"
              :class="pwaUpdateAvailable ? 'update-warn' : (pwaOk ? 'update-ok' : '')">{{ pwaStatus }}</p>
            <div class="row">
              <VBtn color="primary" variant="flat" size="small" type="button"
                :loading="pwaAction === 'check'" :disabled="!!pwaAction" @click="loadPwa(true)">
                检查 PWA 更新
              </VBtn>
              <VBtn v-if="pwaUpdateAvailable" color="primary" variant="tonal" size="small" type="button"
                :loading="pwaAction === 'apply'" :disabled="!!pwaAction" @click="applyPwa">
                更新并刷新
              </VBtn>
              <VBtn variant="text" size="small" type="button" :loading="pwaAction === 'reload'" :disabled="!!pwaAction" @click="forceReload">
                强制重新加载
              </VBtn>
            </div>
            <p v-if="pwaError" class="hint update-warn" role="alert">{{ pwaError }}</p>
          </template>
        </div>
      </section>
    </div>

    <section class="settings-group project-about">
      <h3>关于 MicroChat</h3>
      <div class="settings-group-body">
        <p>
          MicroChat 是一个轻量、可自托管的 SillyTavern 替代品，面向角色扮演与长对话：
          Go 后端负责上下文、压缩与状态，Vue 前端负责阅读与编辑体验。基础功能不依赖外部账号，数据落在自己的 <code>data/</code> 目录。
        </p>
        <label id="setting-brand-name" class="field" tabindex="-1"><span>品牌名称</span><VTextField v-model="brandName" density="compact" variant="outlined" hide-details maxlength="80" /></label>
        <fieldset id="setting-brand-icon" class="brand-icon-field" tabindex="-1">
          <legend>品牌图标</legend>
          <VBtn v-for="(label, icon) in brandIcons" :key="icon" class="brand-icon-option" type="button" icon variant="outlined"
            :aria-label="label" :title="label" :aria-pressed="brandIcon === icon" @click="brandIcon = icon">
            <span class="brand-icon-glyph" aria-hidden="true" v-html="ICONS[icon]"></span>
          </VBtn>
        </fieldset>
        <label id="setting-welcome-text" class="field" tabindex="-1"><span>空白草稿欢迎语</span><VTextField v-model="welcomeText" density="compact" variant="outlined" hide-details maxlength="240" /></label>
        <div class="row"><VBtn color="primary" variant="flat" size="small" :loading="savingBrand" :disabled="savingBrand" @click="saveBrand">保存品牌与欢迎语</VBtn>
          <span v-if="brandError" class="hint update-error" role="alert">{{ brandError }}</span></div>
        <p class="project-authors">作者：Zincles、DeepSeekV4.1Flash</p>
        <p class="project-link">
          <a href="https://github.com/zincles/microchat" target="_blank" rel="noopener noreferrer">github.com/zincles/microchat</a>
        </p>
      </div>
    </section>
  </section>
</template>
<style scoped>
.brand-icon-field { display: flex; flex-wrap: wrap; align-items: center; gap: calc(var(--chat-space) / 2);
  min-width: 0; margin: var(--chat-space) 0; padding: calc(var(--chat-space) / 2) 0; border: 0; }
.brand-icon-field legend { color: var(--md-on-surface-variant); font-size: var(--md-type-body-medium); }
#app .brand-icon-option {
  width: var(--chat-control); height: var(--chat-control); padding: 1px;
  border: 1px solid var(--md-outline-variant); border-radius: var(--md-shape-sm);
  color: var(--md-on-surface-variant);
}
#app .brand-icon-option[aria-pressed="true"] { color: var(--md-primary); border-color: var(--md-primary); background: var(--md-primary-container); }
#app .brand-icon-option:focus-visible { outline: 2px solid var(--md-primary); outline-offset: 2px; }
.brand-icon-option :deep(.v-btn__content), .brand-icon-glyph, .brand-icon-glyph :deep(svg) { width: 100%; height: 100%; }
.brand-icon-glyph, .brand-icon-glyph :deep(svg) { display: block; }
</style>
