// App 层的边界类型：消息列展示行、编辑目标、草稿偏好与错误读取。
// 这些类型不属于线上 JSON（api/types.ts），也不属于纯计算（utils/chatrows.ts）——
// 它们是组装根与 composable 之间的契约，集中在这里，避免 App 里再散一堆内联形状。
import type { RerollState, Session, Summary } from "../api/types.ts";
import type { ChatMessage } from "../utils/chatrows.ts";

/** 消息列的一行：接口 Message + 纯展示字段。 */
export type AppRow = ChatMessage & {
  phase?: "sending" | "waiting" | "";
  step?: number;
  durationMs?: number | null;
};

export type ThinkPart = string | { text?: string; ms?: number } | undefined;
export type PickerItem = { label: string; value?: unknown; [key: string]: unknown };
export type SessionInfo = Partial<Session>;
export type DraftPrefs = {
  provider: string;
  model: string;
  agent: string;
  thinking_level: string;
  native_tools_disabled: string[];
};
export type EditableSummary = Pick<Summary, "id" | "text" | "begin_idx" | "end_idx">;
export type RerollView = Omit<RerollState, "active"> & { active: boolean };

export function emptyReroll(): RerollView {
  return { active: false, count: 0, current_idx: 0, running: false, elapsed_ms: 0, items: [] };
}

export function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function errCode(err: unknown): string | null {
  if (typeof err !== "object" || err === null || !("code" in err)) return null;
  const code = (err as { code?: unknown }).code;
  return typeof code === "string" ? code : null;
}
