// 出站预演（右栏载荷）：会话里问 /sessions/{id}/outgoing，草稿问 /outgoing；
// 压缩载荷问 /compact/payload。三条都只算不写，且用同一个 seq 丢弃迟到的旧响应。
import type { Ref } from "vue";
import type { LastPayload, PayloadPreview } from "../api/types.ts";
import type { DraftPrefs } from "../types/app.ts";

interface PreviewApi {
  draftOutgoing(body: {
    id: string;
    provider: string;
    model: string;
    agent_id: string;
    thinking_level?: string;
    content: string;
  }): Promise<LastPayload>;
  outgoingPreview(id: string, content?: string): Promise<LastPayload>;
  compactPayload(id: string, blocks?: number): Promise<PayloadPreview>;
}

interface PayloadSink {
  setWire(payload: LastPayload | null): void;
  setCompactWire(payload: PayloadPreview | null): void;
}

interface ComposerSink {
  text: string;
}

export interface PreviewDeps {
  api: PreviewApi;
  toast: (text: string, kind?: string) => void;
  getEpoch: () => number;
  getSessionID: () => string | null;
  isDraft: () => boolean;
  getDraftId: () => string | null;
  draftPrefs: DraftPrefs;
  composerRef: Ref<ComposerSink | null>;
  payloadbarRef: Ref<PayloadSink | null>;
  previewOn: () => boolean;
}

export function usePreview({
  api, toast, getEpoch, getSessionID, isDraft, getDraftId, draftPrefs, composerRef, payloadbarRef, previewOn,
}: PreviewDeps) {
  let previewSeq = 0;

  async function previewPayload(value: string): Promise<void> {
    const my = getEpoch();
    const sid = getSessionID();
    const draft = isDraft();
    if (!draft && !sid) return;
    const draftId = getDraftId();
    if (draft && !draftId) return;
    const seq = ++previewSeq;
    try {
      const out = draft
        ? await api.draftOutgoing({
          id: draftId as string,
          provider: draftPrefs.provider,
          model: draftPrefs.model,
          agent_id: draftPrefs.agent,
          thinking_level: draftPrefs.thinking_level || undefined,
          content: value,
        })
        : await api.outgoingPreview(sid as string, value);
      if (seq !== previewSeq || my !== getEpoch()) return;
      payloadbarRef.value?.setWire(out);
    } catch (err: unknown) {
      if (seq !== previewSeq || my !== getEpoch()) return;
      const code = typeof err === "object" && err !== null && "code" in err
        ? (err as { code?: unknown }).code : null;
      if (!draft && code === "invalid") return; // 空会话问历史（400 invalid）⇒ 保持空话
      const message = err instanceof Error ? err.message : String(err);
      toast(`预演失败：${message}`, "error");
    }
  }

  function refreshPreview(): void {
    if (!previewOn()) return;
    const value = composerRef.value?.text ?? "";
    if (!value.trim() || value.startsWith("/")) return;
    if (isDraft() ? !getDraftId() : !getSessionID()) return;
    void previewPayload(value);
  }

  async function previewCompactPayload(blocks?: number | null): Promise<void> {
    const sid = getSessionID();
    if (isDraft() || !sid) {
      toast("这是新对话：先发一句话再预览压缩", "error");
      return;
    }
    try {
      payloadbarRef.value?.setCompactWire(await api.compactPayload(sid, blocks ?? undefined));
    } catch (err: unknown) {
      toast(`压缩载荷预演失败：${err instanceof Error ? err.message : String(err)}`, "error");
    }
  }

  return { previewPayload, refreshPreview, previewCompactPayload };
}
