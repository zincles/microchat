// 出站预演（右栏载荷）：会话里问 /sessions/{id}/outgoing，草稿问 /outgoing；
// 压缩载荷问 /compact/payload；会话世代与请求序号丢弃迟到的响应。
import { computed, shallowRef, type Ref } from "vue";
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
  setCompactWire(payload: PayloadPreview | null): void;
}

export interface PreviewDeps {
  api: PreviewApi;
  toast: (text: string, kind?: string) => void;
  getEpoch: () => number;
  getSessionID: () => string | null;
  isDraft: () => boolean;
  getDraftId: () => string | null;
  draftPrefs: DraftPrefs;
  getText: () => string;
  payloadbarRef: Ref<PayloadSink | null>;
  previewOn: () => boolean;
}

export function usePreview({
  api, toast, getEpoch, getSessionID, isDraft, getDraftId, draftPrefs, getText, payloadbarRef, previewOn,
}: PreviewDeps) {
  let previewSeq = 0;
  const input = shallowRef<{ text: string; wire: LastPayload | null; loading: boolean; error: string } | null>(null);
  const inputPreview = computed(() => input.value?.text === getText() ? input.value : null);
  const compactPreviewLoading = shallowRef(false);

  function resetPreviews(): void {
    previewSeq++;
    input.value = null;
    compactPreviewLoading.value = false;
  }

  async function previewPayload(value: string): Promise<void> {
    const my = getEpoch();
    const sid = getSessionID();
    const draft = isDraft();
    if (!draft && !sid) return;
    const draftId = getDraftId();
    if (draft && !draftId) return;
    const seq = ++previewSeq;
    const current = () => seq === previewSeq && my === getEpoch() && draft === isDraft() && value === getText();
    input.value = { text: value, wire: null, loading: true, error: "" };
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
      if (current()) input.value = { text: value, wire: out, loading: false, error: "" };
    } catch (err: unknown) {
      if (!current()) return;
      const code = typeof err === "object" && err !== null && "code" in err
        ? (err as { code?: unknown }).code : null;
      if (!draft && code === "invalid") return; // 空会话问历史（400 invalid）⇒ 保持空话
      const message = err instanceof Error ? err.message : String(err);
      input.value = { text: value, wire: null, loading: false, error: `预演失败：${message}` };
      toast(`预演失败：${message}`, "error");
    } finally {
      if (seq === previewSeq && my === getEpoch() && input.value?.loading) input.value = null;
    }
  }

  function refreshPreview(): void {
    if (!previewOn()) return;
    const value = getText();
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
    if (compactPreviewLoading.value) return;
    const my = getEpoch();
    compactPreviewLoading.value = true;
    try {
      const result = await api.compactPayload(sid, blocks ?? undefined);
      if (my === getEpoch()) payloadbarRef.value?.setCompactWire(result);
    } catch (err: unknown) {
      if (my === getEpoch()) toast(`压缩载荷预演失败：${err instanceof Error ? err.message : String(err)}`, "error");
    } finally {
      if (my === getEpoch()) compactPreviewLoading.value = false;
    }
  }

  return { inputPreview, compactPreviewLoading, resetPreviews, previewPayload, refreshPreview, previewCompactPayload };
}
