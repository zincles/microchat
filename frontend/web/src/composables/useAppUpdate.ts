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
  const backendUpdateMessage = ref("");
  let autoCheckGeneration = 0;
  let statusCheckInFlight = false;
  let refreshAfterCurrentCheck = false;

  function setBackendUpdateNotice(message: string): void {
    backendUpdate.value = true;
    backendUpdateMessage.value = message;
  }

  function clearBackendUpdateNotice(): void {
    backendUpdate.value = false;
    backendUpdateMessage.value = "";
  }

  onUpdateReady(() => {
    updateReady.value = true;
    // 如果之前把更新条点 × 收起来过，新一次更新要重新放出来。
    nextTick(() => updateRef.value?.show());
  });
  registerPWA();

  function applyUpdate(): void {
    startUpdate();
  }

  // 自动更新：每 5 分钟检查一次 GitHub；远端状态不明时只提示手动处理。
  async function refreshBackendUpdate(): Promise<void> {
    if (!updateAutoCheckOn()) { clearBackendUpdateNotice(); return; }
    // 后台标签页不自动检查或更新：避免用户没看着时应用重启。
    if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
    if (backendUpdating.value) return;
    if (statusCheckInFlight) {
      refreshAfterCurrentCheck = true;
      return;
    }

    statusCheckInFlight = true;
    const generation = autoCheckGeneration;
    try {
      const status = await api.getUpdateStatus();
      if (generation !== autoCheckGeneration || !updateAutoCheckOn()) return;

      if (status.remote_relation === "same" || status.remote_relation === "ahead") {
        clearBackendUpdateNotice();
        return;
      }
      if (status.remote_relation === "diverged") {
        setBackendUpdateNotice("本地与远端已分叉，请在版本面板手动处理");
        return;
      }
      if (status.remote_relation !== "behind" || !status.remote_commit?.trim() || status.remote_error?.trim()) {
        setBackendUpdateNotice("无法确认远端更新状态；请在版本面板手动检查");
        return;
      }

      if (status.dirty) {
        setBackendUpdateNotice("GitHub 有新提交；工作区有未提交修改，请手动处理");
        return;
      }
      if (!status.restart_supported) {
        setBackendUpdateNotice("GitHub 有新提交；当前部署不支持自动重启，请手动处理");
        return;
      }

      setBackendUpdateNotice("GitHub 有新提交，可更新前后端");
      if (typeof document === "undefined" || document.visibilityState !== "visible") return;
      await autoBackendUpdate(generation);
    } catch {
      if (generation === autoCheckGeneration && updateAutoCheckOn()) {
        setBackendUpdateNotice("无法检查 GitHub 更新；请在版本面板手动检查");
      }
    } finally {
      statusCheckInFlight = false;
      if (refreshAfterCurrentCheck) {
        refreshAfterCurrentCheck = false;
        if (updateAutoCheckOn() && !backendUpdating.value) void refreshBackendUpdate();
      }
    }
  }

  async function autoBackendUpdate(generation: number): Promise<void> {
    if (backendUpdating.value || generation !== autoCheckGeneration || !updateAutoCheckOn()) return;
    backendUpdating.value = true;
    try {
      const result = await api.pullUpdate();
      await waitForRestart(result.before.started_at, api);
      location.reload();
    } catch (error: unknown) {
      backendUpdating.value = false;
      if (updateAutoCheckOn()) {
        setBackendUpdateNotice("GitHub 有新提交；自动更新未完成，请手动处理");
      }
      const message = error instanceof Error ? error.message : String(error);
      toast(`自动更新失败：${message}`, "error");
    }
  }

  function stopUpdatePolling(): void {
    autoCheckGeneration += 1;
    refreshAfterCurrentCheck = false;
    if (updatePollTimer !== null) {
      window.clearInterval(updatePollTimer);
      updatePollTimer = null;
    }
  }

  function startUpdatePolling(): void {
    stopUpdatePolling();
    // SSR / 测试环境没有 window；别留一个 setInterval 把 Node 测试进程吊住。
    if (typeof window === "undefined") return;
    if (!updateAutoCheckOn()) { clearBackendUpdateNotice(); return; }
    void refreshBackendUpdate();
    updatePollTimer = window.setInterval(() => { void refreshBackendUpdate(); }, 5 * 60 * 1000);
  }

  function onAutoCheckChanged(): void {
    if (!updateAutoCheckOn()) {
      stopUpdatePolling();
      clearBackendUpdateNotice();
      return;
    }
    startUpdatePolling();
  }

  async function runBackendUpdate(): Promise<void> {
    if (backendUpdating.value) return;
    if (!(await askConfirm("会拉取远程最新代码并重启前后端，期间约 10–20 秒不可用。继续吗？"))) return;
    if (backendUpdating.value) return;
    backendUpdating.value = true;
    try {
      const result = await api.pullUpdate();
      clearBackendUpdateNotice();
      await waitForRestart(result.before.started_at, api);
      location.reload();
    } catch (error: unknown) {
      backendUpdating.value = false;
      const message = error instanceof Error ? error.message : String(error);
      toast(`更新失败：${message}`, "error");
    }
  }

  if (typeof window !== "undefined") {
    window.addEventListener("mc-update-auto-check", onAutoCheckChanged);
  }
  onUnmounted(() => {
    if (typeof window !== "undefined") {
      window.removeEventListener("mc-update-auto-check", onAutoCheckChanged);
    }
    stopUpdatePolling();
  });

  return {
    updateRef, updateReady, backendUpdate, backendUpdateMessage, backendUpdating,
    applyUpdate, startUpdatePolling, stopUpdatePolling, runBackendUpdate,
  };
}
