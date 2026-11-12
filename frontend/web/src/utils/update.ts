// 更新相关的共用动作（2026-11-09）。
//
// - waitForRestart：pull 之后等新 Go 进程（用 started_at 判定，别被旧进程骗）。
//   自动检查/自动 pull 的判定在 App（需要看 dirty / 分叉 / 容器能力）。
//
// UpdatePane（手动拉取）与 App（顶部更新条）共用，避免两边各写一套轮询。
import { probeHealth } from "../api/client.ts";
import { apiBase, sharedApi } from "./prefs.ts";
import type { UpdateStatus } from "../api/types.ts";

export interface UpdateApi {
  getUpdateStatus(): Promise<UpdateStatus>;
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

// waitForRestart：entrypoint 每 2 秒看一次标记；先留一拍，再等后端 started_at 变化。
// 单纯等 /health 会被还没被收掉的旧进程骗过去。
export async function waitForRestart(baseline: string, api: UpdateApi & { getUpdateStatus(): Promise<UpdateStatus> } = sharedApi()): Promise<void> {
  await sleep(2400);
  const deadline = Date.now() + 60000;
  while (Date.now() < deadline) {
    try {
      const next = await api.getUpdateStatus();
      if (next.started_at && next.started_at !== baseline) {
        // API 已经穿过前端代理 ⇒ 新的 Vite preview 也起来了；再给静态资源半拍。
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
