// 命令面板的执行器：键来自 utils/commands.ts 的 COMMAND_META，动作在这里。
//
// App 只负责“输入框提交了什么”和“把结果接到哪个域”；命令自己的分支、picker、
// 会话前置条件都收在这一个文件里。启动时对账：表里有、这里没有 ⇒ 立刻报错，不静默。
import type { Ref } from "vue";
import { COMMAND_META } from "../utils/commands.ts";
import { deletionPlanSummary } from "../utils/format.ts";
import type { Agent, ProviderView, SessionView, Summary } from "../api/types.ts";
import type { AppRow, EditableSummary, PickerItem } from "../types/app.ts";
import type { ChatSummary } from "../utils/chatrows.ts";

interface CommandApi {
  listSessions(): Promise<SessionView[]>;
  providers(): Promise<ProviderView[]>;
  agents(): Promise<Agent[] | { agents?: Agent[] }>;
  listSummaries(id: string): Promise<Summary[]>;
  rerollSwitch(id: string, idx: number): Promise<unknown>;
  stop(id: string): Promise<unknown>;
  deleteSession(id: string): Promise<unknown>;
}

interface OverlaySink {
  showPicker(title: string, items: PickerItem[], action: (item: PickerItem) => void): void;
  hidePalette(): void;
  confirmAsk(text: string): Promise<boolean>;
}

export interface CommandsDeps {
  api: CommandApi;
  toast: (text: string, kind?: string) => void;
  overlaysRef: Ref<OverlaySink | null>;
  getSessionID: () => string | null;
  getEpoch: () => number;
  isDraft: () => boolean;
  vueMessages: Ref<AppRow[]>;
  startDraft: () => Promise<void>;
  enterSession: (id: string) => Promise<void>;
  fillModelSelect: () => unknown;
  compactNow: (blocks?: number | null) => Promise<void>;
  rerollNow: (targetId?: string | null) => Promise<void>;
  resend: () => Promise<void>;
  reloadWindow: () => Promise<boolean>;
  refreshStateView: (id?: string | null) => void;
  refreshRerolls: () => Promise<unknown>;
  openMessageEditor: (message: AppRow, kind: "content" | "reasoning") => void;
  openSummaryEdit: (sum: EditableSummary | ChatSummary | null) => void;
  deleteMessagesFlow: (sid: string, mid: string, confirmTextFor: (plan: import("../api/types.ts").DeletionPlan) => string) => Promise<boolean>;
}

export function useCommands({
  api, toast, overlaysRef, getSessionID, getEpoch, isDraft, vueMessages, startDraft, enterSession,
  fillModelSelect, compactNow, rerollNow, resend, reloadWindow, refreshStateView, refreshRerolls,
  openMessageEditor, openSummaryEdit, deleteMessagesFlow,
}: CommandsDeps) {
  const runners: Record<string, (args: string[]) => Promise<unknown>> = {
    new: async () => {
      if (isDraft()) { toast("已经在一张新对话里了"); return; }
      await startDraft();
    },
    resume: async () => {
      const list = await api.listSessions();
      overlaysRef.value?.showPicker(
        "选会话（resume）",
        list.map((s) => ({ label: `${s.title || "(无标题)"} [${s.messages}条]`, value: s.id })),
        (it: PickerItem) => enterSession(String(it.value)),
      );
    },
    model: async () => { fillModelSelect(); },
    providers: async () => {
      const rows = await api.providers();
      overlaysRef.value?.showPicker("providers", rows.map((r) => ({ label: r.name ?? r.id, value: r })), () => {});
    },
    agents: async () => {
      const data = await api.agents();
      const rows = Array.isArray(data) ? data : (data.agents ?? []);
      overlaysRef.value?.showPicker("agents", rows.map((r) => ({ label: r.name ?? r.id, value: r })), () => {});
    },
    compact: async (args) => {
      const n = args[0] ? Number(args[0]) : 0;
      await compactNow(Number.isFinite(n) && n > 0 ? n : undefined);
    },
    reroll: async () => { await rerollNow(); },
    switch: async (args) => {
      const sid = getSessionID();
      if (!sid) return;
      const my = getEpoch();
      await api.rerollSwitch(sid, Number(args[0] ?? 0));
      if (my !== getEpoch()) return;
      await reloadWindow();
    },
    stop: async () => { const sid = getSessionID(); if (sid) await api.stop(sid); },
    resend: async () => { await resend(); },
    delete: async () => {
      const sid = getSessionID();
      if (!sid) return;
      if (!(await overlaysRef.value?.confirmAsk(`删除整个会话 ${sid}？`))) return;
      await api.deleteSession(sid);
      await startDraft();
    },
    cut: async () => {
      const sid = getSessionID();
      if (!sid) return;
      const messages = vueMessages.value;
      overlaysRef.value?.showPicker(
        "cut：选一条（删它及之后）",
        messages.filter((m) => (m.idx ?? 0) > 0).map((m) => ({
          label: `#${m.idx} ${m.role} ${(m.content ?? "").slice(0, 40)}`,
          value: m,
        })),
        async (it: PickerItem) => {
          const target = it.value as AppRow;
          const ok = await deleteMessagesFlow(sid, String(target.id),
            (plan) => `${deletionPlanSummary(plan)}，确认删 #${target.idx} 及之后？`);
          if (!ok || sid !== getSessionID()) return;
          await reloadWindow();
          refreshStateView(sid);
          refreshRerolls();
        },
      );
    },
    edit: async (args) => {
      const messages = vueMessages.value;
      const wantThink = (args[0] ?? "").toLowerCase().startsWith("思") || (args[1] ?? "").toLowerCase().startsWith("思");
      const n = Number(wantThink && isNaN(Number(args[0])) ? args[1] : args[0]);
      const one = (m: AppRow): void => openMessageEditor(m, wantThink ? "reasoning" : "content");
      if (Number.isInteger(n) && n > 0) {
        const m = messages.find((x) => x.idx === n);
        if (!m) { toast(`没有第 ${n} 条`, "error"); return; }
        await one(m);
        return;
      }
      overlaysRef.value?.showPicker(
        `edit：选一条（改${wantThink ? "思考" : "正文"}）`,
        messages.filter((m) => (m.idx ?? 0) > 0).map((m) => ({
          label: `#${m.idx} ${m.role} ${(wantThink ? (m.reasoning ?? "") : (m.content ?? "")).slice(0, 40)}`,
          value: m,
        })),
        async (it: PickerItem) => { await one(it.value as AppRow); },
      );
    },
    editsum: async () => {
      const sid = getSessionID();
      if (!sid) return;
      const items = await api.listSummaries(sid);
      if (!items.length) { toast("这条会话还没有摘要"); return; }
      overlaysRef.value?.showPicker(
        "editsum：选一条摘要（改它的正文）",
        items.map((s) => ({
          label: `${(s.text ?? "").slice(0, 40)}${s.dirty ? "（已过期）" : ""}`,
          value: s,
        })),
        async (it: PickerItem) => { openSummaryEdit(it.value as EditableSummary); },
      );
    },
  };

  // 表里有、执行器没有 ⇒ 启动即报（命令表是面板与派发的唯一来源，漏了不许静默）。
  for (const key of Object.keys(COMMAND_META)) {
    if (!runners[key]) console.error(`命令 /${key} 有表无执行器：COMMAND_META 与 runners 对不上`);
  }

  async function runCommand(name: string, args: string[]): Promise<void> {
    overlaysRef.value?.hidePalette();
    const meta = COMMAND_META[name];
    if (!meta) { toast(`未知命令 /${name}`); return; }
    if (isDraft() && meta.needsSession) {
      toast(`这是新对话：先发一句话再用 /${name}`, "error");
      return;
    }
    await runners[name](args ?? []);
  }

  return { runCommand };
}
