import type { ChatRow, ChatRowMessage } from "./chatrows.ts";
import { nativeToolLabel, toolRunHint } from "./format.ts";
import { splitState } from "./markdown.ts";

export interface AnswerGroup {
  kind: "answer";
  key: string;
  rows: ChatRowMessage[];
  partial: boolean;
}
export type TranscriptItem = ChatRow | AnswerGroup;
export interface BubblePresentation {
  answerView?: "full" | "prose" | "prose-footer";
  copyAnswer?: boolean;
  progressShown?: boolean;
  candidate?: boolean;
}

function run(row: ChatRow): string | undefined {
  if (row.kind === "msg" && row.msg.role === "assistant" && typeof row.msg.run_id === "string" && row.msg.run_id.trim()) return row.msg.run_id;
}

function hasGap(left: ChatRowMessage, right: ChatRowMessage): boolean {
  if (left.displayIdx !== null && right.displayIdx !== null && right.displayIdx !== left.displayIdx + 1) return true;
  const a = left.msg.step, b = right.msg.step;
  return typeof a === "number" && typeof b === "number" && b !== a + 1;
}

function needsProcess(row: ChatRowMessage): boolean {
  const msg = row.msg;
  if (msg.stream) return !!msg.tool || msg.kind === "tool_run" || Number(msg.step) > 1;
  // 单条带正文的托管工具回复已经有自己的来源时间线，不再叠一层过程。
  if (msg.kind !== "tool_run" && !msg.tools?.length && (!msg.status || msg.status === "done")) return false;
  return !splitState(msg.content).cleaned;
}

/** 只在当前窗口里合并相邻实证；摘要的内部原文始终留在摘要组件。 */
export function transcriptItems(rows: ChatRow[], windowFrom: number, rerollingId?: string | null): TranscriptItem[] {
  const out: TranscriptItem[] = [];
  for (let i = 0; i < rows.length;) {
    const first = rows[i];
    const id = run(first);
    if (!id || first.kind !== "msg") { out.push(first); i++; continue; }
    const steps = [first];
    let j = i + 1;
    while (j < rows.length) {
      const next = rows[j];
      if (next.kind !== "msg" || run(next) !== id || hasGap(steps[steps.length - 1], next)) break;
      steps.push(next);
      j++;
    }
    if (steps.length > 1 || ((!rerollingId || first.msg.messageId !== rerollingId) && needsProcess(first))) {
      const before = rows[i - 1];
      const partial = (!before && (windowFrom > 1 || (first.displayIdx ?? 1) > 1)) || before?.kind === "summary"
        || (before?.kind === "msg" && hasGap(before, first)) || steps.some((row, index) => Number(row.msg.step) > index + 1);
      out.push({ kind: "answer", key: first.key, rows: steps, partial });
    } else out.push(first);
    i = j;
  }
  return out;
}

export function issueLabel(status?: string): string {
  switch (status) {
    case "failed": case "error": return "失败";
    case "interrupted": return "已中断";
    case "cancelled": case "canceled": return "已取消";
    default: return "";
  }
}

export function processLabel(group: AnswerGroup, rerollingId?: string | null): string {
  let calls = 0, completed = 0;
  const issues = new Set<string>();
  const last = group.rows[group.rows.length - 1].msg;
  const candidate = !!rerollingId && last.messageId === rerollingId;
  for (const { msg } of group.rows) {
    if (rerollingId && msg.messageId === rerollingId) continue;
    const status = issueLabel(msg.status);
    if (status) issues.add(`回答${status}`);
    for (const tool of msg.tools ?? []) {
      calls++;
      if (tool.status === "done" || tool.status === "completed") completed++;
      const issue = issueLabel(tool.status);
      if (issue) issues.add(`工具调用${issue}`);
    }
  }
  const currentIssue = !candidate && last.stream ? issueLabel(last.tool?.state) : "";
  if (currentIssue) issues.add(`${nativeToolLabel(last.tool?.kind)}${currentIssue}`);
  const labels: string[] = [];
  if (calls) {
    const prefix = group.partial ? "当前可见 " : completed === calls && !issues.size ? "已完成 " : "";
    labels.push(`${prefix}${calls} 次工具调用`);
  } else if (!last.stream && !candidate && last.kind === "tool_run") labels.push("工具调用");
  if (candidate) labels.push("正在重新生成回答…");
  else if (last.stream && !issueLabel(last.status)) {
    if (last.tool && !currentIssue) {
      labels.push(last.tool.state === "done" ? `${nativeToolLabel(last.tool.kind)}完成，正在整理结果…` : toolRunHint(last.tool));
    } else labels.push("正在整理回答…");
  }
  if (!candidate && !last.stream && !last.content?.trim() && !issues.size && last.status === "done") labels.push("末步无正文");
  labels.push(...issues);
  return labels.join(" · ") || "回答过程";
}

export interface AnswerStep {
  row: ChatRowMessage;
  presentation: BubblePresentation;
}

/** 每条原文只提供一个槽位。展开时在原位置补回该步的身份、工具、思考和操作。 */
export function answerSteps(group: AnswerGroup, expanded: boolean, liveHasBody: boolean, rerollingId?: string | null): AnswerStep[] {
  const rows = group.rows;
  const last = rows[rows.length - 1];
  // 有些失败只产生库外的错误行；最近一条实际落库的工具消息仍是可操作的末条。
  let footer = last;
  if (last.msg.noIdx && !last.msg.messageId) {
    for (let i = rows.length - 2; i >= 0; i--) {
      if (rows[i].msg.messageId) { footer = rows[i]; break; }
    }
  }
  const out: AnswerStep[] = [];
  for (const row of rows) {
    const msg = row.msg;
    const final = row === last;
    const candidate = !!rerollingId && rerollingId === msg.messageId;
    const hasBody = msg.stream || candidate ? liveHasBody : !!msg.content?.trim();
    if (!expanded && !hasBody && row !== footer && !final) continue;
    // 末步有正文和按位置记录的工具时保留原生时间线；没有正文的末步只常显操作/用量。
    const finalDetails = final && hasBody && !candidate && (msg.kind !== "tool_run" || msg.tools?.some(tool => typeof tool.text_offset === "number"));
    const view = expanded || finalDetails ? "full" : row === footer ? "prose-footer" : "prose";
    out.push({ row, presentation: { answerView: view, copyAnswer: final, progressShown: true, candidate } });
  }
  return out;
}
