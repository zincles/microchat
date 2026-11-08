<script setup lang="ts">
// 更新面板（2026-11-09 用户定）：显示当前提交，一键 git pull --ff-only + 重启前后端。
//
// 后端只负责拉取与写重启标记；真正重启由容器的 entrypoint 看家循环完成
//（重新编译 Go、重新构建 vite、再把两个进程拉起来）。前端在这里等新进程的
// `started_at` 变了才刷新 —— 只看 /health 会被尚未被收掉的旧进程骗过去。
import { computed, onMounted, ref } from "vue";
import { VBtn, VDivider } from "vuetify/components";
import { probeHealth } from "../api/client.ts";
import { apiBase, sharedApi } from "../utils/prefs.ts";
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

const supported = computed(() => !!status.value?.restart_supported);
const buttonText = computed(() => {
  if (phase.value === "pulling") return "正在拉取…";
  if (phase.value === "restarting") return "正在重启…";
  return "拉取并重启";
});

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
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

async function waitForRestart(baseline: string): Promise<void> {
  // entrypoint 每 2 秒看一次标记；先留出一拍，别拿还没被收掉的旧进程当“新版本”。
  await sleep(2400);
  const deadline = Date.now() + 60000;
  while (Date.now() < deadline) {
    try {
      const next = await api.getUpdateStatus();
      status.value = next;
      if (next.started_at && next.started_at !== baseline) {
        // API 已经能穿过前端代理 ⇒ 新的 Vite preview 也起来了；再给静态资源半拍。
        await sleep(700);
        await probeHealth(apiBase(), { timeout: 2000 });
        return;
      }
    } catch {
      // 重启窗口里连不上是正常的，继续等。
    }
    await sleep(1000);
  }
  throw new Error("重启后 60 秒内没有连上新进程；看容器日志（docker/podman logs microchat）");
}

async function pullAndRestart(): Promise<void> {
  if (busy.value) return;
  if (!supported.value) {
    error.value = "当前不是容器部署：可以拉取，但没有 entrypoint 能自动重启。请手动重启服务。";
    return;
  }
  if (props.ask && !(await props.ask("会拉取远程最新代码并重启前后端，期间约 10–20 秒不可用。继续吗？"))) return;
  busy.value = true;
  error.value = "";
  message.value = "";
  log.value = "";
  phase.value = "pulling";
  try {
    const result = await api.pullUpdate();
    status.value = result.after;
    log.value = result.output || "";
    message.value = result.changed
      ? `已拉取到 ${result.after.short || result.after.commit.slice(0, 7)}，正在重启前后端…`
      : "已经是最新提交，正在重启前后端…";
    phase.value = "restarting";
    await waitForRestart(result.before.started_at);
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

onMounted(() => { load().catch(() => {}); });
</script>

<template>
  <section id="panel-update" class="panel">
    <section class="settings-card">
      <h3>版本更新</h3>
      <p class="hint">拉取仓库最新提交（只走 <code>git pull --ff-only</code>），成功后由容器 entrypoint 重新编译并重启前后端。</p>

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
                本地与远端已分叉，先处理再拉取
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
            @click="pullAndRestart">
            {{ buttonText }}
          </VBtn>
          <VBtn variant="text" size="small" type="button" :disabled="busy" @click="load">刷新状态</VBtn>
          <span class="status">{{ message }}</span>
        </div>
        <p v-if="error" class="hint update-error">{{ error }}</p>
        <pre v-if="log" class="update-log">{{ log }}</pre>
      </template>
      <p v-else-if="error" class="hint update-error">{{ error }}</p>
    </section>
  </section>
</template>
