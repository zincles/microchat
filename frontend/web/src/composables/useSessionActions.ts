// 会话级动作：请求体覆盖、Copy、导出、删除。
//
// 这些动作都跨“列表 / 当前会话 / 编辑器”三处；放一起才能保证删除与副本都走同一套
// “刷列表 + 进会话 + 清缓存”的顺序。
import type { Ref } from "vue";
import type { ExportDocument, Session, SessionView } from "../api/types.ts";
import type { SessionInfo } from "../types/app.ts";
import { downloadJSON } from "../utils/download.ts";
import { sessionTitle } from "../utils/format.ts";

interface SessionActionApi {
  patchSession(id: string, body: { request_overrides: Record<string, unknown> }): Promise<Session>;
  copySession(id: string): Promise<Session>;
  exportSession(id: string): Promise<ExportDocument>;
  deleteSession(id: string): Promise<unknown>;
  listSessions(): Promise<SessionView[]>;
}

interface OverlaySink {
  confirmAsk(text: string): Promise<boolean>;
}

interface TopbarSink {
  setOverridesSaved(value: Record<string, unknown>): void;
}

export interface SessionActionsDeps {
  api: SessionActionApi;
  toast: (text: string, kind?: string) => void;
  getSessionID: () => string | null;
  isDraft: () => boolean;
  sessionInfo: SessionInfo;
  topbarRef: Ref<TopbarSink | null>;
  overlaysRef: Ref<OverlaySink | null>;
  refreshPreview: () => void;
  refreshSessionsBar: () => Promise<void>;
  getSessionsCache: () => SessionView[];
  enterSession: (id: string) => Promise<void>;
  startDraft: () => Promise<void>;
  backToChat: () => void;
  forgetCachedWindow: (id: string) => void;
}

export function useSessionActions({
  api, toast, getSessionID, isDraft, sessionInfo, topbarRef, overlaysRef, refreshPreview,
  refreshSessionsBar, getSessionsCache, enterSession, startDraft, backToChat, forgetCachedWindow,
}: SessionActionsDeps) {
  async function saveRequestOverrides(overrides: Record<string, unknown>): Promise<void> {
    const sid = getSessionID();
    if (isDraft() || !sid) {
      toast("先建立会话，才能保存请求体覆盖", "error");
      return;
    }
    try {
      const updated = await api.patchSession(sid, { request_overrides: overrides });
      Object.assign(sessionInfo, updated);
      topbarRef.value?.setOverridesSaved(updated.request_overrides ?? {});
      refreshPreview();
      toast("请求体覆盖已保存", "ok");
    } catch (err: unknown) {
      toast(`保存请求体覆盖失败：${err instanceof Error ? err.message : String(err)}`, "error");
    }
  }

  // afterCopy：刷左栏让副本出现，然后直接进副本 —— 停在原会话里会让人以为点了没反应。
  function afterCopy(fresh: Session): void {
    if (!fresh?.id) { toast("复制完成了，但没拿到新会话 id", "error"); return; }
    toast(`已创建副本「${fresh.title || "未命名"}」`, "ok");
    void refreshSessionsBar();
    enterSession(fresh.id).catch((err: unknown) =>
      toast(`进副本失败：${err instanceof Error ? err.message : String(err)}`, "error"));
  }

  async function copySessionFromBar(id: string): Promise<void> {
    try {
      afterCopy(await api.copySession(id));
    } catch (err: unknown) {
      toast(`复制失败：${err instanceof Error ? err.message : String(err)}`, "error");
    }
  }

  async function exportSessionFromBar(id: string): Promise<void> {
    try {
      const doc = await api.exportSession(id);
      downloadJSON(doc, `${doc.title || "新会话"}.json`);
      toast(`已导出 ${doc.messages?.length ?? 0} 条`, "ok");
    } catch (err: unknown) {
      toast(`导出失败：${err instanceof Error ? err.message : String(err)}`, "error");
    }
  }

  async function askDeleteSession(id: string): Promise<void> {
    // 高延迟下别为了一个标题先等一个 RTT：左栏缓存里就有它。
    const cached = getSessionsCache().find((s) => s.id === id)?.title;
    let title = cached;
    if (title === undefined) {
      const list = await api.listSessions().catch(() => []);
      title = list.find((s) => s.id === id)?.title;
    }
    const name = sessionTitle(title);
    if (!(await overlaysRef.value?.confirmAsk(`删除会话「${name}」？不可逆。`))) return;
    try {
      await api.deleteSession(id);
    } catch (err: unknown) {
      toast(`关闭失败：${err instanceof Error ? err.message : String(err)}`, "error");
      return;
    }
    forgetCachedWindow(id);
    if (id === getSessionID()) {
      backToChat();
      await startDraft();
    } else {
      await refreshSessionsBar();
    }
  }

  return { saveRequestOverrides, copySessionFromBar, exportSessionFromBar, afterCopy, askDeleteSession };
}
