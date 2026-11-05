// 后台任务面板（`GET /tasks`）的轮询与取消。
//
// 2026-11-01 用户定：「任务菜单里，我希望有办法取消任务」。这里只管三件事：
//   ① 2s 拉一次 `/tasks`（进程内事实，零成本）；
//   ② 把同一拍喂给顶栏环心与左栏行内环（**同一个快照**，别各自 filter）；
//   ③ 取消原子任务 / 队列，并让重摇两族当场对齐。
//
// `canceled:false` 不是错误（刚好跑完 / 压根不能停）—— 补刷一次面板，那一行自己会消失。
import { ref } from "vue";
import type { Ref } from "vue";
import type { Queue, TaskBoard } from "../api/types.ts";
import { busySessionIds } from "../utils/tasks.ts";

const TASKS_POLL_MS = 2000;

interface TasksApi {
  tasks(): Promise<TaskBoard>;
  cancelTask(id: string): Promise<{ canceled: boolean }>;
  cancelQueue(id: string): Promise<Queue>;
}
interface TopbarTasks {
  setTasks(board: TaskBoard | null): void;
}
interface UseTasksOptions {
  api: TasksApi;
  toast: (text: string, kind?: string) => void;
  topbarRef: Ref<TopbarTasks | null>;
  onCanceled?: () => void;
}

export function useTasks({ api, toast, topbarRef, onCanceled }: UseTasksOptions) {
  const busyIds = ref<string[]>([]);
  let timer: ReturnType<typeof setInterval> | null = null;

  async function refreshTasks(): Promise<void> {
    try {
      const board = await api.tasks();
      topbarRef.value?.setTasks(board);
      // 左栏的行内环：哪条会话此刻有任务在跑（与顶栏环心同一个快照）。
      busyIds.value = busySessionIds(board);
    } catch {
      // 读不到 ⇒ 面板说"读不到"，左栏**不能**继续挂着上一拍的环（那会是一条永远转的假象）。
      topbarRef.value?.setTasks(null);
      busyIds.value = [];
    }
  }

  // 按下之后**不立刻**从面板上抹掉那一行：真正停不停由那一发自己决定（ctx 取消是**请求**）。
  function cancelTask(id: string): void {
    api.cancelTask(id)
      .then((res) => {
        if (!res?.canceled) toast("这一发已经结束了");
        refreshTasks();
        // 按停的可能是一趟重摇 ⇒ 两族的界面状态也当场对齐（别等轮询或者下一次进会话）。
        onCanceled?.();
      })
      .catch((err: unknown) => toast(`按停失败：${err instanceof Error ? err.message : String(err)}`, "error"));
  }

  // 取消一个**队列任务**：在飞的那一发会被一起掐断；补刷一次面板，别等 2 秒节拍。
  function cancelQueue(id: string): void {
    api.cancelQueue(id)
      .then(() => refreshTasks())
      .catch((err: unknown) => toast(`取消失败：${err instanceof Error ? err.message : String(err)}`, "error"));
  }

  function startPolling(): void {
    refreshTasks();
    timer = setInterval(refreshTasks, TASKS_POLL_MS);
  }
  function stopPolling(): void {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  return { busyIds, refreshTasks, cancelTask, cancelQueue, startPolling, stopPolling };
}
