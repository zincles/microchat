// 应用更新（PWA 壳 + 后端 git）：从 App.vue 抽出来的独立生命周期。
//
// 三件事都在这里：新 SW 提示、每 5 分钟问一次 GitHub、用户点“更新”或后台自动 pull。
// App 只保留 UI 绑定；判定与定时器不散在组装根里。
import { ref, nextTick, onUnmounted } from "vue";
import { registerPWA, onUpdateReady, startUpdate } from "../utils/pwa.ts";
import { waitForRestart } from "../utils/update.ts";
import { updateAutoCheckOn, sharedApi } from "../utils/prefs.ts";

type AppApi = ReturnType<typeof sharedApi>;

export interface AppUpdateDeps {
  api: AppApi;
  toast: (text: string, kind?: string) => void;
  askConfirm: (text: string) => Promise<boolean>;
}

/** UpdateBar 的公开方法：新版本出现时把它从“用户手动收起”状态放回来。 */
export interface UpdateBarHandle {
  show(): void;
}

export function useAppUpdate({ api, toast, askConfirm }: AppUpdateDeps) {
  const updateRef = ref<UpdateBarHandle | null>(null);
  const updateReady = ref(false);
  const backendUpdate = ref(false);
  const backendUpdating = ref(false);
  let updatePollTimer: ReturnType<typeof setInterval> | null = null;

  onUpdateReady(() => {
    updateReady.value = true;
    // 如果之前把更新条点 × 收起来过，新一次更新要重新放出来。
    nextTick(() => updateRef.value?.show());
  });
  registerPWA();

  function applyUpdate(): void {
    startUpdate();
  }

  // 自动更新：每 5 分钟检查一次 GitHub；只有“本地落后”才自动 pull + 重启。
  async function refreshBackendUpdate(): Promise<void> {
    if (!updateAutoCheckOn()) { backendUpdate.value = false; return; }
    // 后台标签页不自动更新：避免用户没看着时应用重启。
    if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
    if (backendUpdating.value) return;
    let status;
    try {
      status = await api.getUpdateStatus();
    } catch {
      return;
    }
    const canUpdate = status.remote_relation === "behind" || status.remote_relation === "unknown";
    if (!canUpdate) {
      backendUpdate.value = false;
      return;
    }
    // 脏工作区 / 分叉 / 宿主二进制：自动 pull 会失败或没人重启，交给手动更新面板。
    if (status.dirty || status.remote_relation === "diverged" || !status.restart_supported) {
      backendUpdate.value = true;
      return;
    }
    await autoBackendUpdate();
  }

  async function autoBackendUpdate(): Promise<void> {
    if (backendUpdating.value) return;
    backendUpdating.value = true;
    backendUpdate.value = true;
    try {
      const result = await api.pullUpdate();
      await waitForRestart(result.before.started_at, api);
      location.reload();
    } catch (error: unknown) {
      backendUpdate.value = true;
      backendUpdating.value = false;
      const message = error instanceof Error ? error.message : String(error);
      toast(`自动更新失败：${message}`, "error");
    }
  }

  function stopUpdatePolling(): void {
    if (updatePollTimer !== null) {
      window.clearInterval(updatePollTimer);
      updatePollTimer = null;
    }
  }

  function startUpdatePolling(): void {
    stopUpdatePolling();
    // SSR / 测试环境没有 window；别留一个 setInterval 把 Node 测试进程吊住。
    if (typeof window === "undefined") return;
    if (!updateAutoCheckOn()) { backendUpdate.value = false; return; }
    void refreshBackendUpdate();
    updatePollTimer = window.setInterval(() => { void refreshBackendUpdate(); }, 5 * 60 * 1000);
  }

  async function runBackendUpdate(): Promise<void> {
    if (backendUpdating.value) return;
    if (!(await askConfirm("会拉取远程最新代码并重启前后端，期间约 10–20 秒不可用。继续吗？"))) return;
    backendUpdating.value = true;
    try {
      const result = await api.pullUpdate();
      backendUpdate.value = false;
      await waitForRestart(result.before.started_at, api);
      location.reload();
    } catch (error: unknown) {
      backendUpdating.value = false;
      const message = error instanceof Error ? error.message : String(error);
      toast(`更新失败：${message}`, "error");
    }
  }

  onUnmounted(stopUpdatePolling);

  return {
    updateRef, updateReady, backendUpdate, backendUpdating,
    applyUpdate, startUpdatePolling, stopUpdatePolling, runBackendUpdate,
  };
}
