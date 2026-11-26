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

const sleep = (ms: number): Promise<void> => {
	const { promise, resolve } = Promise.withResolvers<void>();
	setTimeout(resolve, ms);
	return promise;
};

// waitForRestart：entrypoint 每 2 秒看一次标记；先留一拍，再等后端 started_at 变化。
// 单纯等 /health 会被还没被收掉的旧进程骗过去；新进程或代理暂时不可达时继续重试。
export async function waitForRestart(baseline: string, api: UpdateApi = sharedApi()): Promise<void> {
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    let next: UpdateStatus | null = null;
    try {
      next = await api.getUpdateStatus();
    } catch {
      // 重启窗口里后端暂时连不上是正常的，继续等。
    }

    if (next?.started_at && next.started_at !== baseline) {
      try {
        // 新后端已通过代理；再给前端 preview 一拍，并重试短暂不可用的代理。
        await sleep(700);
        await probeHealth(apiBase(), { timeout: 2000 });
        return;
      } catch {
        // 后端先恢复、前端代理稍后恢复时继续等，而不是提示用户再次拉取。
      }
    }
    await sleep(Math.min(1000, Math.max(0, deadline - Date.now())));
  }
  throw new Error("等待 120 秒后仍未确认新进程和前端代理恢复；entrypoint/容器可能没有完成重建。请检查容器日志（docker/podman logs microchat）");
}
