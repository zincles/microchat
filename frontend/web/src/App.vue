<script setup lang="ts">
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from "vue";
import { VApp, VBtn, VTabsWindow, VTabsWindowItem } from "vuetify/components";
import { createApi, probeHealth } from "./api/client.ts";
import { parseCommand, filterCommands } from "./utils/commands.ts";
import { uuidv7 } from "./utils/ids.ts";
import { buildChatRows, olderCount, editRerollTarget, latestPromptFor } from "./utils/chatrows.ts";
import { applyBackground } from "./utils/background.ts";
import { installViewportFit } from "./utils/viewport.ts";
import { thinkingOptions, thinkingLevelsForModel } from "./utils/thinking.ts";
import { forgetWindow, recallWindow } from "./utils/windowcache.ts";
import { sharedApi, previewOn, payloadPreviewOn, applyTheme, wantsSend, apiBase, apiBaseCandidates, saveNetPrefs, lastSessionId, saveLastSession, commandMenuOn, applyTextScale, applyFonts, chromeHideOn, syncPrefs, userName as macroUserName } from "./utils/prefs.ts";
import { macroMap } from "./utils/macros.ts";
import { deletionPlanSummary, whoText } from "./utils/format.ts";
import TopBar from "./components/TopBar.vue";
import AgentMenu from "./components/AgentMenu.vue";
import SessionBar from "./components/SessionBar.vue";
import MessageBubble from "./components/MessageBubble.vue";
import SummaryCard from "./components/SummaryCard.vue";
import SummaryEditor from "./components/SummaryEditor.vue";
import MessageEditor from "./components/MessageEditor.vue";
import Composer from "./components/Composer.vue";
import ContextWorkspace from "./components/ContextWorkspace.vue";
import Toast from "./components/Toast.vue";
import SettingsModal from "./components/SettingsModal.vue";
import SessionSettings from "./components/SessionSettings.vue";
import Overlays from "./components/Overlays.vue";
import UpdateBar from "./components/UpdateBar.vue";
import { useTypewriter } from "./composables/useTypewriter.ts";
import { useTasks } from "./composables/useTasks.ts";
import { useSidePanels } from "./composables/useSidePanels.ts";
import { useChrome } from "./composables/useChrome.ts";
import { useOverlayLayout } from "./composables/useOverlayLayout.ts";
import { useSettingsView } from "./composables/useSettingsView.ts";
import { useModelsAgents } from "./composables/useModelsAgents.ts";
import { useAppUpdate } from "./composables/useAppUpdate.ts";
import { useSessionWindow } from "./composables/useSessionWindow.ts";
import { useTurnStream } from "./composables/useTurnStream.ts";
import { useReroll } from "./composables/useReroll.ts";
import { usePreview } from "./composables/usePreview.ts";
import { useCommands } from "./composables/useCommands.ts";
import { useContextWorkspace } from "./composables/useContextWorkspace.ts";
import { useSessionActions } from "./composables/useSessionActions.ts";
import type {
  CreateSessionRequest, DefaultsConfig, DeletionPlan, FlowActiveNode, Message, SessionView, TurnStatus,
} from "./api/types.ts";
import type { ChatSummary } from "./utils/chatrows.ts";
import type {
  AppRow, DraftPrefs, EditableSummary, PickerItem, SessionInfo, ThinkPart,
} from "./types/app.ts";
import { errCode, errText } from "./types/app.ts";

type ConfirmText = (plan: DeletionPlan) => string;

const api = sharedApi();
applyTheme();
applyBackground();
// 界面缩放（2026-10-28）：同样**开机第一帧就得对** —— 晚一帧就是"字先小一下再变大"的闪。
// 它写在根节点的 `style` 上（`--app-text-scale`），theme.css 的 body 字号吃这一格。
applyTextScale();
applyFonts();
// —— 组件 refs（直调实例方法，替代桥） ——
const toastRef = ref<InstanceType<typeof Toast> | null>(null);
const topbarRef = ref<InstanceType<typeof TopBar> | null>(null);
const sessionbarRef = ref<InstanceType<typeof SessionBar> | null>(null);
const payloadbarRef = ref<InstanceType<typeof ContextWorkspace> | null>(null);
const composerRef = ref<InstanceType<typeof Composer> | null>(null);
const overlaysRef = ref<InstanceType<typeof Overlays> | null>(null);
// 命令菜单（输入框左侧 ☰ 与 "/" 浮层）：默认关（移动端碍事，2026-10-17 用户定）；
// 设置 → 外观里改了立刻生效（不刷新）—— 偏好住服务器（`prefs.json`），这里是它的活视图。
const cmdMenu = ref(commandMenuOn());
// 宏值变化后让消息列与顶栏面板重算（prefs 缓存不是 Vue 响应式）。
const macroRev = ref(0);
const toast = (t: string, k = "") => toastRef.value?.push(t, k);
// 任务面板轮询：busyIds 给左栏、setTasks 给顶栏环心；取消按钮回调里对齐重摇两族。
const {
  busyIds,
  refreshTasks,
  cancelTask,
  cancelQueue,
  startPolling: startTasksPolling,
  stopPolling: stopTasksPolling,
} = useTasks({
  api,
  toast,
  topbarRef,
  onCanceled: () => { refreshReroll(); refreshRerollSummary(); },
});

const {
  leftHidden,
  workspace,
  selectWorkspace,
  toggleLeft,
  closeLeftDrawer,
  install: installSidePanels,
  uninstall: uninstallSidePanels,
} = useSidePanels({
  isOverlayOpen: () => !!(overlaysRef.value?.isPaletteOpen() || overlaysRef.value?.isPickerOpen()),
  onWorkspaceContext: () => refreshContextLazy(),
});

const { settingsOpen, settingsOnly, openSettings, openManage, closeSettings, backToChat } = useSettingsView({ closeLeftDrawer });



const { resetChrome, onMessagesScroll } = useChrome();

const { liftOverlays, install: installOverlayLayout, uninstall: uninstallOverlayLayout } = useOverlayLayout(overlaysRef);
// askConfirm：给设置/子编辑器用的全局确认框（与左栏删除同一个口 —— "不可逆的删除先摊开"）。
function askConfirm(text: string): Promise<boolean> {
  return overlaysRef.value?.confirmAsk(text) ?? Promise.resolve(false);
}
// onPickAction：浮层"选中某项"的回执（动作在浮层里选好，这里执行 + 统一兜错）。
// ⚠ 别把它写回模板内联（`(a, it) => Promise.resolve(a(it))…`）：模板里的 `Promise` 会被编译成
// `_ctx.Promise`（实例代理上不存在）⇒ 点击那一刻当场 TypeError、被 Vue 吞掉，动作永远不执行
// （踩过：picker 的鼠标点击从写下那天就是死的，键盘路径绕开了它所以一直没暴露）。
function onPickAction(action: (item: PickerItem) => unknown, item: PickerItem): void {
  Promise.resolve(action(item)).catch((err) => toast(`操作失败：${errText(err)}`, "error"));
}
// 更新生命周期（PWA 壳 + 后端 git）搬进 composable；App 只留模板绑定。
const {
  updateRef, updateReady, backendUpdate, backendUpdating,
  applyUpdate, startUpdatePolling, runBackendUpdate,
} = useAppUpdate({ api, toast, askConfirm });

// 服务器是**偏好的权威**：先用本地镜像铺第一帧，再问服务器；失败只 toast。
syncPrefs()
  .then(() => { applyBackground(); cmdMenu.value = commandMenuOn(); macroRev.value += 1; })
  .catch((err) => toast(`界面偏好读取失败：${errText(err)}`, "error"))
  .finally(() => { startUpdatePolling(); });

// —— 会话状态 ——
let sessionID: string | null = null;
// 会话列表缓存（2026-11-05，高延迟优化）：从前每点一条会话都要先 `GET /sessions`
// 才轮到消息窗口 —— 远端 1s+ 时那一整个 RTT 什么也没画，而列表本来就在屏幕上。
// 缓存只当"先用着的快照"：进会话后按需在后台补一次（见 enterSession），不拿它当真相。
const sessionsCache = ref<SessionView[]>([]);
// 左栏的加载态（MD3）：首次拿列表画骨架；后台补列表时顶部一条 linear progress。
// 从前空列表与"还没加载"在界面上分不出来 —— boot 的那两秒会写"还没有会话"，那是假话。
const sessionsLoading = ref(true);
const sessionsRefreshing = ref(false);
let sessionsFetchedAt = 0;
const SESSIONS_FRESH_MS = 10_000;
// 回看缓存（2026-11-05 用户定）：每条会话上一屏的窗口快照（LRU、最多 8 条）。
// 切回去先把这屏画出来（0 RTT），再后台对账 —— 见 enterSession 与 windowcache.js。
// localRev：本地的"消息列已经变了"计数（乐观发送 / 编辑 / 删除…）。
// 后台对账在落屏前比一次：期间人动过 ⇒ 这一发作废，别拿旧快照覆盖刚画上去的字。
let localRev = 0;
// pendingSends：正在等 202 的发送数。乐观气泡已经上屏、但服务端还没落库 ——
// 这时候落到屏上的窗口快照可能**还没有**那句，必须把它判成"旧"。
let pendingSends = 0;
// 会话世代：进会话/回草稿就 +1。所有会话域的异步续写都在 await 后核对本世代，对不上就丢 ——
// 没有这道闸，快速切会话时迟到的 A 续写会覆掉已切到的 B 的抬头与消息列（实测复现过：
// 顶栏 A、高亮 B、气泡 A —— 界面被撕成三份）。
let epoch = 0;
const sessionInfo = reactive<SessionInfo>({});
// 草稿态（DeepSeek 网页版行为，2026-10-09 定）：＋/开机/`/new` 只开一张**客户端草稿**，库里没有行；
// 首条发送才 POST /sessions（带上草稿里选好的 provider/model/agent）。
// ⚠ HACK（刻意，全项目唯一的口子）：草稿**铸一枚 UUIDv7** 当将来那会话的 id —— 预演
// （POST /outgoing 临时会话）拿它进请求头，建会话时把同一个值交给 POST /sessions
// ⇒ 预演的那一发给真发的那一发逐字节一致。别扩散（消息/摘要的 id 仍是服务端铸）。
// 要会话的命令（compact/cut/reroll…）在草稿里明确拒绝（needsSession 标在 commands.js 的表上）。
let draft = false;
let draftPrefs: DraftPrefs = { provider: "", model: "", agent: "", thinking_level: "", native_tools_disabled: [] };
let draftId: string | null = null;

// 出站预演只有一处：载荷、草稿第一句、压缩载荷。App 只把结果交给右栏。
const { previewPayload, refreshPreview, previewCompactPayload } = usePreview({
  api,
  toast,
  getEpoch: () => epoch,
  getSessionID: () => sessionID,
  isDraft: () => draft,
  getDraftId: () => draftId,
  draftPrefs,
  composerRef,
  payloadbarRef,
  previewOn,
});

const {
  vueAgents,
  vueModels,
  statusAgent,
  paintSessionHeader,
  fillModelSelect,
  applyModelValue,
  fillAgentSelect,
  agentLabelOf,
  applyAgentValue,
  refreshAgents,
} = useModelsAgents({
  api,
  toast,
  topbarRef,
  getEpoch: () => epoch,
  getSessionID: () => sessionID,
  isDraft: () => draft,
  sessionInfo,
  draftPrefs,
  onRefreshStatusline: refreshStatusline,
  onRefreshPreview: refreshPreview,
});
// 思考档位：Session 上的显式值（空 = 跟随模型默认）。能选哪些档由模型清单里的
// `thinking_levels` 决定（后端 providers.ThinkingLevels 的唯一权威）—— 前端不另抄模型表。
const thinkingLevels = computed(() => {
  if (sessionInfo.provider === "dummy") return [];
  return thinkingLevelsForModel(vueModels.value, sessionInfo.provider, sessionInfo.model);
});
const thinkingChoices = computed(() => {
  const levels = thinkingLevels.value;
  return levels.length ? thinkingOptions(levels) : [];
});
// 当前模型可用的 provider-native 工具数（来自 GET /models 的 model_define_rules 派生）。
// 草稿与真会话都读 sessionInfo；模型清单未到时显示「工具：无」，不猜。
const currentModelRow = computed(() => {
  const provider = String(sessionInfo.provider ?? "");
  const model = String(sessionInfo.model ?? "");
  if (!provider || !model) return null;
  return vueModels.value.find((m) => m.provider === provider && m.upstream_id === model) ?? null;
});
// 规则给的是**可用**工具；会话里的 native_tools_disabled 是用户手动关掉的。
const currentAllTools = computed<string[]>(() => [
  ...(currentModelRow.value?.server_tools ?? []),
  ...(currentModelRow.value?.local_tools ?? []),
].filter(Boolean));
async function toggleNativeTool(id: string, enabled: boolean): Promise<void> {
  if (!id) return;
  const next = (sessionInfo.native_tools_disabled ?? []).filter((x: string) => x !== id);
  if (!enabled) next.push(id);
  if (draft) {
    draftPrefs.native_tools_disabled = [...next];
    sessionInfo.native_tools_disabled = [...next];
    refreshPreview();
    return;
  }
  if (!sessionID) return;
  const prev = [...(sessionInfo.native_tools_disabled ?? [])];
  sessionInfo.native_tools_disabled = [...next];
  try {
    const updated = await api.patchSession(sessionID, { native_tools_disabled: next });
    Object.assign(sessionInfo, updated);
    refreshPreview();
  } catch (err) {
    sessionInfo.native_tools_disabled = prev;
    toast(`工具选择保存失败：${err instanceof Error ? errText(err) : String(err)}`, "error");
  }
}
const currentThinking = computed(() => {
  // 草稿与真会话都把当前档位放在 sessionInfo 上（setThinking 两边一起写），
  // 别在这里读普通对象 draftPrefs —— 它不是响应式的，菜单标签不会更新。
  const raw = sessionInfo.thinking_level;
  return typeof raw === "string" ? raw : "";
});
async function setThinking(level: string): Promise<void> {
  const next = typeof level === "string" ? level : "";
  if (draft) {
    draftPrefs.thinking_level = next;
    sessionInfo.thinking_level = next;
    refreshPreview();
    return;
  }
  if (!sessionID) return;
  const prev = sessionInfo.thinking_level ?? "";
  if (next === prev) return;
  sessionInfo.thinking_level = next; // 先上屏（1s+ RTT：别让人以为没点动）
  try {
    const updated = await api.patchSession(sessionID, { thinking_level: next });
    Object.assign(sessionInfo, updated);
    refreshPreview();
  } catch (err) {
    sessionInfo.thinking_level = prev;
    toast(`思考等级保存失败：${errText(err)}`, "error");
  }
}
// 换模型后旧档位可能不在新模型的能力集里 ⇒ 清回"跟随默认"（后端也只会接受能力集里的值）。
watch(thinkingLevels, (levels) => {
  const cur = currentThinking.value;
  if (!cur || !levels.length) return;
  if (!levels.includes(cur)) setThinking("");
});
// 消息窗口的取数/分页/摘要展开/预取都在 composable；App 只关心“当前会话”这一层。
const {
  vueMessages, vueSegments, loadingSession, windowLimit, windowFrom, summaryMsgs, openSummaries, windowCache,
  toRow, loadWindow, reloadWindow, reloadWindowKeepingScroll, loadMore, prefetchSession,
  onSummaryToggle,
} = useSessionWindow({
  api,
  toast,
  getSessionID: () => sessionID,
  getEpoch: () => epoch,
  getLocalRev: () => localRev,
  isDraft: () => draft,
  canReconcile: () => pendingSends === 0,
  scrollBottom,
  onWindowLoaded: () => { if (treeVisible.value) refreshTree(); },
});

// 会话级动作（覆盖 / Copy / 导出 / 删除）统一在这里，避免三处各写一遍列表刷新顺序。
const {
  saveRequestOverrides, copySessionFromBar, exportSessionFromBar, askDeleteSession,
} = useSessionActions({
  api,
  toast,
  getSessionID: () => sessionID,
  isDraft: () => draft,
  sessionInfo,
  topbarRef,
  overlaysRef,
  refreshPreview,
  refreshSessionsBar,
  getSessionsCache: () => sessionsCache.value,
  enterSession,
  startDraft,
  backToChat,
  forgetCachedWindow: (id) => forgetWindow(windowCache, id),
});
const vueStream = ref<{ id: string | null; text: string; reasoning: string }>({ id: null, text: "", reasoning: "" });
// 流式显字（2026-11-03）：`vueStream` 是后端的完整目标；`streamDisplay` 是打字机当前露出的前缀。
// 每帧只改 streamDisplay，轮询与合并仍只认 vueStream（两个状态别互相镜像）。
const { streamDisplay, setStreamTarget, waitTypewriter, resetTypewriter } = useTypewriter();
// 设置视图的"单独入口"："" = 完整设置（四个 tab）；"session" / "provider" = 只显示那一页，
// 从左侧栏底部的「会话 / 渠道」进来。两者共用同一个视图与同一份数据加载。
// **设置 = root 专属**（用户定，2026-10-10）：`is_admin` 才给进设置；其他用户的入口按钮也不给。
const compactDefault = ref<number | null>(null); // chat.compact_blocks（压缩面板输入框的占位提示）
// windowLimit：消息列首屏/加载更多一页多少段（chat.history_window；默认 5）。
function applyChatConfig(c: { compact_blocks?: number; history_window?: number } | null): void {
  compactDefault.value = c?.compact_blocks ?? null;
  const n = Number(c?.history_window);
  windowLimit.value = Number.isFinite(n) && n >= 1 ? Math.floor(n) : 5;
}
const sessionSettingsId = ref<string | null>(null);
const agentFocus = ref("");

function openAgentFlow(id: string): void {
  if (!id) return;
  agentFocus.value = id;
  openManage("agent");
}

// 离开设置时清掉聚焦，避免下次从普通入口进「智能体」又自动打开同一个编辑器。
watch(settingsOpen, (open) => { if (!open) agentFocus.value = ""; });

// —— 消息列 ——
// tmpKey：只给"还没进库"的气泡（乐观用户消息 / 流式占位）当 v-for key —— 不能共用 'stream'（重键）。
let tmpSeq = 0;
function tmpKey() { return `tmp-${++tmpSeq}`; }
function scrollBottom() {
  requestAnimationFrame(() => {
    const el = document.getElementById("messages-vue");
    if (el) el.scrollTop = el.scrollHeight;
  });
}
function addMessage(who: string, text: string, cls: string, think: ThinkPart = undefined, messageId?: string | null): AppRow {
  const row: AppRow = {
    id: tmpKey(),
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: typeof think === "string" ? undefined : think?.ms,
    messageId: messageId ?? undefined,
  };
  vueMessages.value.push(row);
  scrollBottom();
  return row;
}
// 乐观上屏的那句用户消息（2026-11-05，高延迟优化）：先画，等 202 回来再换成服务端那条
// （换的是**同一条消息**：服务端回的 `user` 带真 id/idx，编号与后续编辑/删除都认它）。
// 失败就撤掉，绝不留在屏幕上假装发过 —— "看到的"和"真发出去的"分家比慢更糟。
function settleOptimisticUser(row: AppRow, user?: Message | null): void {
  if (!user) return;
  const at = vueMessages.value.indexOf(row);
  if (at >= 0) vueMessages.value.splice(at, 1, toRow(user));
}
function dropOptimistic(row: AppRow): void {
  const at = vueMessages.value.indexOf(row);
  if (at >= 0) vueMessages.value.splice(at, 1);
}

// 一轮落地后的共同收尾：流已经结束，这里只做“界面与后端对齐”。
async function onTurnFinished(sid: string, st: TurnStatus): Promise<void> {
  if (sessionID !== sid) return;
  const failed = st.phase === "error";
  await waitTypewriter();
  if (sessionID !== sid) return;
  await reloadWindowKeepingScroll();
  refreshMacros();
  resetTypewriter();
  if (sessionID !== sid) return;
  if (failed) {
    const text = `失败：${st.error || "未知"}`;
    vueMessages.value.push({ who: "助手", role: "assistant", content: text, id: tmpKey(), noIdx: true });
    scrollBottom();
    toast(text, "error");
  }
  refreshStatusline(st.phase, st.elapsed_ms);
  refreshSessionsBar();
  refreshStateView(sid);
  refreshRerolls();
  if (previewOn()) {
    api.outgoingPreview(sid, "").then((w) => payloadbarRef.value?.setWire(w))
      .catch((err) => toast(`预演失败：${errText(err)}`, "error"));
  }
}
// 一轮生成的流（发送/SSE/轮询/step）在 composable；落库后的刷新仍在本文件。
const {
  startSendingBubble, send, resend, pollTurn, hasLivePoll,
} = useTurnStream({
  api,
  getSessionID: () => sessionID,
  getEpoch: () => epoch,
  vueMessages,
  vueStream,
  addMessage,
  settleOptimisticUser,
  dropOptimistic,
  tmpKey,
  composerRef,
  resetTypewriter,
  setStreamTarget,
  refreshStatusline,
  refreshTasks,
  bumpLocalRev: () => { localRev++; },
  beginPending: () => { pendingSends++; },
  endPending: () => { pendingSends--; },
  onTurnFinished,
});
// FlowAgent 的「重新生成」：不是候选列表，而是用当前历史重跑图并原地换尾条。
// 用户消息可能已被上一轮 output.user 改写；这里是历史事实，不能也不用“还原”。
async function regenerateNow(): Promise<void> {
  const sid = sessionID;
  if (!sid || !currentAgentIsFlow.value) return;
  const row = [...vueMessages.value].reverse().find((m) => m.role === "assistant" && m.messageId);
  if (!row) { toast("这条会话还没有可重新生成的回复", "error"); return; }
  row.stream = true;
  row.phase = "waiting";
  row.content = "";
  row.reasoning = "";
  row.reasoningMs = null;
  row.tool = null;
  row.sources = [];
  localRev++;
  try {
    const accepted = await api.regenerate(sid);
    refreshTasks();
    await pollTurn(sid, accepted.turn.message_id, row);
  } catch (err) {
    row.stream = false;
    row.phase = "";
    localRev++;
    toast(`重新生成失败：${errText(err)}`, "error");
    await reloadWindow();
  }
}

// 打开消息编辑器（新窗口，2026-11-02 用户定）：不再在气泡里就地换 textarea。
// message 可以是消息列里的行，也可以是 /edit 从 vueMessages 里挑的原对象。
function openMessageEditor(message: AppRow, kind: "content" | "reasoning"): void {
  const mid = message?.messageId ?? message?.id;
  if (!mid) { toast("这条消息还没有 id，暂时不能编辑", "error"); return; }
  // 重摇只认尾条 assistant：编辑它自己、或编辑它前面那条 user，才能「保存并重摇」。
  const rerollTargetId = editRerollTarget(vueMessages.value, mid);
  messageEditTarget.value = {
    sessionId: sessionID ?? "",
    message: { ...message, id: mid },
    kind,
    rerollable: !!rerollTargetId,
    rerollTargetId,
  };
}
const {
  treeVisible, onPayloadTab, onPayloadPreview, onAdvancedDone,
  refreshTree, refreshMacros, refreshContextLazy,
} = useContextWorkspace({
  api,
  toast,
  getSessionID: () => sessionID,
  getEpoch: () => epoch,
  isDraft: () => draft,
  workspace,
  isFlow: () => currentAgentIsFlow.value,
  payloadbarRef,
  setMacros: (report) => topbarRef.value?.setMacros(report),
  refreshStateView,
  reloadWindow: () => reloadWindow(),
  payloadPreviewOn,
});
const lastMsgId = computed(() => [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId ?? null);
// 最新 AI 回复对应的用户输入（A-B-C-D-E-F ⇒ E）——“编辑最新 Prompt”的入口目标。
const latestPromptId = computed(() => latestPromptFor(vueMessages.value));
// 重摇两族（消息 / 摘要）在 composable；模板只读它给出的状态与纯函数。
const {
  rerollState, summaryReroll,
  isLive, bubbleContent, bubbleReasoning, rowDuration,
  refreshReroll, refreshRerollSummary, refreshRerolls,
  summaryRefresh, summaryVersion, rerollNow, rerollGo,
  resumeRerollIfLive,
} = useReroll({
  api,
  toast,
  getSessionID: () => sessionID,
  getEpoch: () => epoch,
  isFlow: () => currentAgentIsFlow.value,
  getLastMsgId: () => lastMsgId.value,
  vueSegments,
  streamDisplay,
  setStreamTarget,
  resetTypewriter,
  waitTypewriter,
  refreshStatusline,
  refreshTasks,
  reloadWindow: () => reloadWindow(),
  refreshStateView,
});
// 渲染行：气泡（带编号）+ 被顶层摘要折起来的卡（口径见 utils/chatrows.ts）。
// 当前 agent 的"首段对话"（`first_message`，2026-10-11 用户定）—— **computed 只看响应式的两格**
// （vueAgents + sessionInfo.agent_id）。⚠ 别把它写成"先看普通变量 draft 再早退"：早退分支不碰任何
// 响应式依赖 ⇒ computed 没依赖、缓存住空串**永不重算**（踩过）。
const agentGreeting = computed(() => {
  const a = vueAgents.value.find((x) => x.id === sessionInfo.agent_id);
  return (a?.first_message ?? "").trim();
});
// FlowAgent 暂不共用普通 Agent 的上下文面板；新的节点面板另行设计。
const currentAgent = computed(() => vueAgents.value.find((x) => x.id === sessionInfo.agent_id) ?? null);
const currentAgentIsFlow = computed(() => currentAgent.value?.kind === "flow");
const flowCalls = ref(0);
const flowActiveNodes = ref<FlowActiveNode[]>([]);
let flowCallsTimer: ReturnType<typeof setInterval> | null = null;
async function refreshFlowCalls(): Promise<void> {
  if (draft || !sessionID || !currentAgentIsFlow.value) { flowCalls.value = 0; flowActiveNodes.value = []; return; }
  const sid = sessionID;
  try {
    const trace = await api.flowRun(sid);
    if (sid === sessionID) {
      flowCalls.value = Number(trace?.active_llm_calls ?? (trace?.active_node_id ? 1 : 0));
      flowActiveNodes.value = trace?.active_nodes ?? [];
    }
  } catch {
    if (sid === sessionID) { flowCalls.value = 0; flowActiveNodes.value = []; }
  }
}
// 空草稿首屏的两格（2026-11-02）：当前助手名 + 会话模型。
// 模型显示上游 id（顶栏那份是 prettify 后的名字）—— 草稿还没有模型列表可查，
// 而这一行是"我马上要跟谁说话"，宁可给原始 id 也不留空。
const draftAgentName = computed(() => agentLabelOf(sessionInfo.agent_id) || "默认助手");
const macroValues = computed(() => {
  macroRev.value; // 依赖：prefs/agent 变化时重算
  return macroMap(macroUserName(), agentLabelOf(sessionInfo.agent_id), sessionInfo.model);
});
const draftModelName = computed(() => whoText(sessionInfo.provider, sessionInfo.model, "未选模型"));
const chatRows = computed(() => {
  // 草稿里把首段**合成气泡先画出来**（发送后它就是库里的 #1，编号顺延天然给它 1；
  // 换 agent 就换首段）。draft 是普通变量 ⇒ 判断放在这儿（chatRows 每次都重算）。
  const greet = draft ? agentGreeting.value : "";
  return buildChatRows({
    segments: vueSegments.value,
    windowFrom: windowFrom.value,
    messages: greet
      ? [{ id: "draft-first", role: "assistant", who: "助手", content: greet }, ...vueMessages.value]
      : vueMessages.value,
    summaryMsgs,
  });
});
const olderSegments = computed(() => olderCount(vueSegments.value, windowFrom.value));
function onBubbleEdit(mid: string | null | undefined, kind: "content" | "reasoning"): void {
  if (!mid) return;
  const m = vueMessages.value.find((x) => x.messageId === mid || x.id === mid);
  if (!m) { toast("那条消息已经不在了（多半被删了）：重进会话看看", "error"); return; }
  openMessageEditor(m, kind);
}
function onBubbleEditPrompt(mid: string | null | undefined): void { onBubbleEdit(mid, "content"); }
// deleteMessagesFlow：消息级删除的唯一出口（气泡"删"与 /cut 共用）——预览 → 确认 → 执行；
// 执行时 409（conflict：末尾变了，别人删过/改过）⇒ **重新预览再确认**（最多三轮），
// 不是干弹一个错（AGENTS 的口径：核对字段不符 ⇒ 409 ⇒ 重新预览）。
async function deleteMessagesFlow(sid: string, mid: string, confirmTextFor: ConfirmText): Promise<boolean> {
  for (let i = 0; i < 3; i++) {
    const plan = await api.deletionPreview(sid, mid).catch((err) => {
      toast(`删除预览失败：${errText(err)}`, "error");
      return null;
    });
    if (!plan || sid !== sessionID) return false;
    if (!(await overlaysRef.value?.confirmAsk(confirmTextFor(plan)))) return false;
    try {
      await api.deleteMessages(sid, mid, plan.last_deleted_message_id);
      localRev++; // 删完消息列已经变了（等这一发重新拉窗口）
    } catch (err) {
      if (errCode(err) === "conflict" && i < 2) { toast("末尾变了：重新预览一眼再删", "error"); continue; }
      toast(`删除失败：${errText(err)}`, "error");
      return false;
    }
    return true;
  }
  return false;
}
async function onBubbleDelete(mid: string | null | undefined, isLast: boolean): Promise<void> {
  if (!mid) return;
  const sid = sessionID;
  if (!sid) return;
  const my = epoch;
  const ok = await deleteMessagesFlow(sid, mid, (plan) => {
    const suffix = isLast
      ? "确认删除这条？"
      : `这不是最后一条：删它会连同之后 ${plan.deleted_message_ids.length - 1} 条一起删，确认？`;
    return `${deletionPlanSummary(plan)}，${suffix}`;
  });
  if (!ok || my !== epoch) return;
  await reloadWindow();
  refreshStateView(sid);
  refreshRerolls();
}
// —— 重摇（尾条气泡的 ⟳ / ◀ ▶ 与 /reroll 命令共用）——
// 口径：这只是**重摇**（`reroll-message`，就地换正文、UUID 不变），不是"重发历史"
// （`/resend` 那条路归空输入点发送）。后端要求目标**必须是尾条 assistant** ⇒ 只挂最新一条。
//
// 交互（2026-10-10 用户定）：⟳ = 摇新一版并**立刻切到最新那版**（无论当前看着哪一版），
// 结果就地换正文；模式活着期间气泡下出现 `◀ n/N ▶`：◀▶ 翻版，**最右的 ▶ = 再摇一版**。
const summaryEditTarget = ref<EditableSummary | null>(null); // 编辑中的摘要（SummaryEditor 模态）
const messageEditTarget = ref<{
  sessionId: string;
  message: AppRow & { id: string };
  kind: "content" | "reasoning";
  rerollable: boolean;
  rerollTargetId: string | null;
} | null>(null); // 编辑中的消息（MessageEditor 模态）

function openSummaryEdit(sum: EditableSummary | ChatSummary | null): void {
  if (sum?.id) summaryEditTarget.value = sum as EditableSummary;
}
async function onSummaryEdited() {
  summaryEditTarget.value = null;
  await reloadWindow();
  if (treeVisible.value) refreshTree();
  toast("摘要已改", "ok");
}
async function onMessageEdited(result: { message?: Message; reroll?: boolean } = {}): Promise<void> {
  const sid = sessionID;
  const target = messageEditTarget.value;
  messageEditTarget.value = null;
  // 先就地把这一行换成 PATCH 回来的正文（高延迟下别让旧字多挂一个来回），
  // 再重拉一次窗口对账（摘要的 dirty / 区间可能跟着变了）。滚动位置保住。
  localRev++; // 编辑也是本地先改：在飞的对账作废
  const updated = result?.message;
  if (updated) {
    const row = vueMessages.value.find((r) => (r.messageId ?? r.id) === updated.id);
    if (row) {
      row.content = updated.content;
      row.reasoning = updated.reasoning ?? "";
      row.reasoningMs = updated.reasoning_ms ?? null;
    }
  }
  await reloadWindowKeepingScroll();
  refreshStateView(sid);
  if (result.reroll && target?.rerollTargetId) {
    // 先保存、再生成：FlowAgent 没有“重摇”，用当前历史重新生成；
    // 普通 Agent 仍走候选列表（编辑尾条 user 时目标是它后面那条 assistant）。
    if (currentAgentIsFlow.value) await regenerateNow();
    else await rerollNow(target.rerollTargetId);
    return;
  }
  toast("消息已保存", "ok");
}
// compactNow：受理一次压缩（命令面板与右栏压缩面板共用一条路）；`blocks` 空 = 用配置默认。
async function compactNow(blocks?: number | null): Promise<void> {
  const sid = sessionID;
  if (!sid) { toast("先发一句话：草稿里还没有会话可压"); return; }
  try {
    const accepted = await api.compact(sid, blocks ?? undefined);
    localRev++; // 压缩会改摘要/段结构：在飞的后台对账别把旧结构盖回去
    refreshTasks();
    toast(`压缩已受理${blocks ? `（${blocks} 块）` : ""}`, "ok");
    pollCompact(sid, accepted?.at_ms).catch(() => {});
  } catch (err) {
    toast(`压缩失败：${errText(err)}`, "error");
  }
}

// undoSummary：**撤销一次压缩**（2026-11-01 加，2026-11-05 支持级联）。
//
// 顶层摘要都可撤；撤更早的一条会连同**同层更晚的压缩**一起删。先问后端要 preview，
// 超过一条就摊开警告（哪些区间会一起没），绝不静默删一串。
async function undoSummary(summaryID: string): Promise<void> {
  const sid = sessionID;
  if (!sid || !summaryID) return;
  const my = epoch;
  let preview;
  try {
    preview = await api.undoPreview(sid, summaryID);
  } catch (err) {
    if (my === epoch) toast(`撤销失败：${errText(err)}`, "error");
    return;
  }
  const items = preview.items ?? [];
  if (preview.count > 1) {
    const ranges = items.map((item) => `第 ${item.from_idx}–${item.to_idx} 条`).join("、");
    const ok = await overlaysRef.value?.confirmAsk(
      `撤销这次压缩会同时撤销同层更晚的 ${preview.count - 1} 次压缩：${ranges}。` +
      `这些摘要会被删除，对应内容回到原文，不可逆。继续？`,
    );
    if (!ok) return;
  }
  if (my !== epoch) return;
  try {
    await api.undoSummary(sid, summaryID);
  } catch (err) {
    if (my === epoch) toast(`撤销失败：${errText(err)}`, "error");
    return;
  }
  if (my !== epoch) return;
  toast(preview.count > 1 ? `已撤销 ${preview.count} 次压缩` : "已撤销这次压缩", "ok");
  // 装配会**变长**（那些段回到原文）⇒ 消息列、状态、环上的占用都要重算一遍。
  await reloadWindow();
  refreshStateView(sid);
  refreshStatusline("idle");
  if (treeVisible.value) refreshTree();
}
// pollCompact：盯一次压缩到收尾（拿受理时的 `at_ms` 对上这一发 —— 状态位是**持久**的，
// 不认准 at_ms 会被上一次的旧结局骗），完了刷新消息列（装配变短）与压缩树。
async function pollCompact(sid: string, atMS: number | undefined): Promise<void> {
  const my = epoch;
  for (let i = 0; i < 300; i++) {
    await new Promise((r) => setTimeout(r, 700));
    if (my !== epoch) return;
    const st = await api.status(sid).catch(() => null);
    if (my !== epoch) return;
    const c = st?.compact;
    if (!c || c.at_ms !== atMS || c.state === "running") continue;
    if (c.state === "error") {
      toast(`压缩失败：${c.error || "未知"}`, "error");
      return;
    }
    // 按停（2026-11-01）：**不是失败**，也不是"完成" —— 库里什么都没变，重提交一次就行。
    if (c.state === "canceled") {
      toast("压缩已取消（一条都没写，重提交还是压同一段）");
      return;
    }
    const span = c.from_idx ? `第 ${c.from_idx}–${c.to_idx} 条` : "";
    toast(`压缩完成${span ? `：${span}` : ""}${c.merged ? "（合并）" : ""}`, "ok");
    await reloadWindow();
    refreshStateView(sid);
    return;
  }
}

// ── 回到前台：把界面跟后端对齐（2026-11-01 用户定）──────────────────────
//
// 用户原话：「假设，我发送了消息，llm要一分钟才能返回结果。此时我切换到其他网页。
// 然后2min后回来。此时，我必须打开别的对话再回到当前会话才能刷新结果。这尤其常见于安卓浏览器里。」
//
// 为什么本地的轮询靠不住：**后台标签页的计时器会被节流、甚至整个冻住**（安卓更狠，内存紧时
// 直接丢页面）。所以"我发了一句话然后切走"这件事，回来时本地那个循环要么卡在很久以前那一拍、
// 要么已经因为一次网络错退出。**唯一可信的是后端**：回来就问一句"现在什么相位"。
//
// 三条分寸（都踩过或想清楚了才这么写）：
//   ① **还在跑就不许重拉消息列** —— 那会把正在流的那个气泡换成库里的，等于把活人从屏幕上抹掉。
//      这种情况只把轮询**接回来**（并且先摘掉僵住的 stream 气泡，免得接出两个）。
//   ② **已经落地就一次性对齐**：消息列、状态视图、占用、重摇、任务、预演。用户回来该看到的是
//      "答案就在那儿"，不是"还得再点一下"。
//   ③ **合并连发事件**：切回来时 `visibilitychange` / `focus` / `pageshow` 常常一串来，
//      每条都跑一遍就是三倍请求。用一个很短的时间窗吃掉（resync 本身是幂等的，重复跑不会错，
//      只是浪费）。
let lastResyncAt = 0;
let resyncing = false;

// dropStreamBubbles：摘掉本地那些"正在流"的合成气泡（循环已经不在了，它们就是僵住的假象）。
function dropStreamBubbles() {
  if (!vueMessages.value.some((m) => m.stream)) return;
  vueMessages.value = vueMessages.value.filter((m) => !m.stream);
}

async function resyncOnForeground() {
  if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
  const now = Date.now();
  if (resyncing || now - lastResyncAt < 300) return;
  resyncing = true;
  lastResyncAt = now;
  const my = epoch;
  try {
    // 左栏与任务面板与"哪条会话"无关，先刷（草稿态也有意义）。
    refreshSessionsBar();
    refreshTasks();
    if (!sessionID) return;
    const st = await api.status(sessionID).catch(() => null);
    if (my !== epoch || !st) return;

    if (st.phase === "pending" || st.phase === "streaming") {
      // ① 还在跑：只把轮询接回来，**不动消息列**。
      refreshStatusline(st.phase, st.elapsed_ms);
      if (!hasLivePoll()) {
        dropStreamBubbles();
        pollTurn(sessionID, st.message_id).catch(() => {});
      }
      // 重摇是另一条活（同一把闸，不可能与一轮同时在跑），但它的轮询也可能一起断了。
      return;
    }

    // ② 已经落地（idle / error）：整体对齐。
    //
    // ⚠ 先摘僵住的 stream 气泡再 reload —— 不摘的话 `reloadWindow` 里那句"没在流才重建"
    // 的判断可能把旧的合成气泡留在列表里（它就是那个"永远在等待"的来源）。
    dropStreamBubbles();
    await reloadWindowKeepingScroll();
    if (my !== epoch) return;
    refreshStatusline(st.phase, st.elapsed_ms);
    // 上下文三 tab 只在**真的在看工作区**时才刷；聊天界面不白拉 /state 与 /outgoing。
    refreshContextLazy();
    refreshRerolls();
    // 重摇那趟如果还活着 ⇒ 把它的轮询也接回来（不 await：它可能还要摇一阵）。
    resumeRerollIfLive().catch(() => {});
    if (st.phase === "error" && st.error) toast(`上一轮失败了：${st.error}`, "error");
  } finally {
    resyncing = false;
  }
}

async function startDraft() {
  const my = ++epoch;
  draft = true;
  sessionID = null;
  saveLastSession(""); // 人在草稿 ⇒ 开机也该回草稿（别把人拽回上次那条会话）
  draftId = uuidv7(); // 这张草稿将来那会话的 id（预演与建会话都用它）
  // ⚠ **不要 `draftPrefs = {...}`**：composable 在创建时捕获了同一个对象引用，
  // 重新赋值会让 applyModelValue/applyAgentValue 改到旧对象，sendFromDraft 却读新对象
  // ⇒ 草稿里选的模型/Agent 到建会话时退回 defaults（2026-11-08 用户报 dummy/dummy）。
  Object.assign(draftPrefs, { provider: "", model: "", agent: "", thinking_level: "", native_tools_disabled: [] });
  // `id: ""` 是必须的：`Object.assign` **不清旧键**，从一条真会话切到草稿时
  // `sessionInfo.id` 会留着上一条的 id —— 于是"这是草稿吗"就答错了（曾经就没人读它，
  // 所以一直没露；上下文工作区要读它才露出来）。
  Object.assign(sessionInfo, {
    id: "", title: "", provider: "", model: "", agent_id: "", thinking_level: "", system_prompt: "", native_tools_disabled: [], request_overrides: {},
  });
  paintSessionHeader();
  const d = await api.getDefaults().catch((): DefaultsConfig => ({ provider: "", model: "", agent: "" }));
  if (my !== epoch || !draft) return; // 期间切去别的会话/又开了新草稿：这一发作废
  // 用户在默认值回来之前已经选过模型/Agent ⇒ 不覆盖那些格；只补没选过的。
  if (!draftPrefs.provider && !draftPrefs.model) {
    draftPrefs.provider = d.provider ?? "";
    draftPrefs.model = d.model ?? "";
  }
  if (!draftPrefs.agent) draftPrefs.agent = d.agent ?? "";
  Object.assign(sessionInfo, {
    provider: draftPrefs.provider, model: draftPrefs.model,
    agent_id: draftPrefs.agent, thinking_level: draftPrefs.thinking_level,
    native_tools_disabled: draftPrefs.native_tools_disabled,
  });
  vueMessages.value = [];
  loadingSession.value = false;
  vueStream.value = { id: null, text: "", reasoning: "" };
  resetTypewriter();
  paintSessionHeader();
  refreshAgents();
  fillModelSelect();
  fillAgentSelect();
  payloadbarRef.value?.setWire(null);
  payloadbarRef.value?.setTables({});
  payloadbarRef.value?.setCompactWire(null);
  refreshStatusline("idle");
  void refreshFlowCalls();
  refreshSessionsBar();
  refreshRerolls();
  // 窗口/树/懒加载缓存一起清（草稿没有会话可问）。
  vueSegments.value = [];
  windowFrom.value = 1;
  summaryMsgs.clear();
  openSummaries.clear();
  payloadbarRef.value?.setTree(null);
  // 人可能正站在上下文工作区里换会话（左栏点一下就换）⇒ 当前 tab 要跟着换上新的，
  // 否则那块地方会一直显示上一条会话的账本（静默的错，看不出来）。
  refreshContextLazy();
  refreshPreview(); // 输入框里若还留着字（＋ 时不清输入框），预演立刻跟上
}
function newSession() {
  closeLeftDrawer(); // 窄屏：抽屉让位（人已经去新对话了）
  backToChat(); // 侧栏动作 = 把人带回对话区（**两个视图**都让位 —— 别再只 closeSettings）
  startDraft().catch((err) => toast(`新建失败：${errText(err)}`, "error"));
}
function renderSessions(sessions: SessionView[]): void {
  sessionsCache.value = sessions ?? [];
  sessionsLoading.value = false;
  sessionbarRef.value?.setSessions(sessions, sessionID);
}
async function refreshSessionsBar() {
  sessionsRefreshing.value = true;
  try {
    const sessions = await api.listSessions();
    sessionsFetchedAt = Date.now();
    renderSessions(sessions);
  } catch (err) { toast(`刷会话栏失败：${errText(err)}`, "error"); }
  finally {
    sessionsRefreshing.value = false;
    sessionsLoading.value = false;
  }
}
async function enterSession(id: string, preloaded: SessionView[] | null = null): Promise<void> {
  const my = ++epoch;
  loadingSession.value = true;
  try {
  draft = false;
  sessionID = id;
  saveLastSession(id); // 记住"在看哪条"——刷新/重载回来就进它
  // 窗口重新起：先清掉上一条会话的一切（消息/段/展开态/懒加载缓存），再按"最后 N 段"拉。
  vueMessages.value = [];
  vueSegments.value = [];
  windowFrom.value = 1;
  resetTypewriter();
  summaryMsgs.clear();
  openSummaries.clear();
  // 上一会话的上下文占用不许挂在新会话上（高延迟下它会挂满一个 RTT 才被新数覆盖 ——
  // 那正好是"看起来像新会话的账、其实是旧的"最难受的一秒）。
  topbarRef.value?.setContext(null);
  topbarRef.value?.setMacros(null);
  // 开机恢复那条路已经拉过一次 `/sessions`（boot），别在同一毫秒再要一遍 ——
  // 远端部署时那是一整个 RTT + 一份完整会话列表。
  // 同理，点左栏切会话时列表**就在屏幕上**（sessionsCache）⇒ 直接用，别先问一遍再画：
  // 高延迟下那等于把"进会话"硬生生推迟一个来回。缓存里没有（冷启动/别处新建）才真去拉。
  let sessions = preloaded ?? sessionsCache.value;
  if (!sessions.some((s) => s.id === id)) {
    sessions = await api.listSessions();
    if (my !== epoch) return; // 迟到的上一发：整段丢掉（切会话竞态的唯一守卫）
    sessionsFetchedAt = Date.now();
  }
  Object.assign(sessionInfo, { thinking_level: "" }, sessions.find((s) => s.id === id) ?? {});
  paintSessionHeader();
  refreshAgents();
  fillModelSelect();
  fillAgentSelect();
  renderSessions(sessions);
  // 手里这份列表可能是几分钟前的（别的设备改了标题、新建了会话）—— 后台补一次，
  // 不挡这一屏（有 10 秒保鲜期，切来切去不会每点一下发一个请求）。
  if (Date.now() - sessionsFetchedAt > SESSIONS_FRESH_MS) refreshSessionsBar();
  // 回看缓存（2026-11-05 用户定）：这条会话上一屏还留着 ⇒ 先原样画出来，**不挂骨架**，
  // 等 status 说空闲再在后台对一次账。1s+ 延迟下，切回来是"几乎是瞬间"而不是先白一秒。
  const cached = recallWindow(windowCache, id);
  if (cached) {
    vueSegments.value = cached.segments;
    windowFrom.value = cached.windowFrom;
    vueMessages.value = cached.rows;
    loadingSession.value = false; // 一屏已经在了，骨架没有意义
    nextTick(scrollBottom);
  } else {
    const ok = await loadWindow({ reset: true });
    if (my !== epoch || !ok) return;
  }
  refreshStatusline("idle");
  refreshRerolls();
  // 载荷/状态/树都只在"人正看着工作区"时才拉（2026-11-02）：换会话时从前无条件
  // `POST /outgoing` + `GET /state`，而聊天界面根本用不到它们 —— 远端部署时那是两次
  // 重装配（列消息 + 现演状态 + 拼整段出站）+ 两个 RTT。
  payloadbarRef.value?.setWire(null);
  // 压缩那发的载荷同理：换会话就清掉（那是**某一次**压缩的账，不属于"当前"）
  payloadbarRef.value?.setCompactWire(null);
  refreshContextLazy();
  // 这一轮还在跑？（刷新页面、切走又回来）—— status 是权威：把合成的流气泡与轮询接回来。
  // reply 落库前消息列里本来就没有它，不接的话界面装 idle、发送才知道 409。
  api.status(id).then((st) => {
    if (my !== epoch) return;
    if (st?.phase === "pending" || st?.phase === "streaming") {
      if (!vueMessages.value.some((m) => m.stream)) pollTurn(id, st.message_id).catch(() => {});
      // 轮询收尾会自己重拉窗口 ⇒ 别再叠一次后台对账跟它抢着写消息列。
      return;
    }
    // 缓存那一屏是旧的：空闲时后台对一次（等待期间人动过消息列 ⇒ 这一发作废）。
    if (cached) reloadWindowKeepingScroll({ onlyIfUnchanged: true }).catch(() => {});
  }).catch(() => {});
  } finally {
    if (my === epoch) loadingSession.value = false;
  }
}
async function boot() {
  // 自检按候选顺序探（没存过地址时先同源 `/api/v1` —— 单容器与 dev 都靠 vite 代理；
  // 兜底才是同主机的 8787，给"前端被别的静态服务器端出来"的场合）。
  //
  // 2026-11-01：探的口从 `/auth/status` 换成 `/health`（鉴权砍了，那个口也没了）。
  // 判据照旧两道（都是踩过的坑）：地址先 `normalizeApiBase`（裸地址会被 fetch 当相对路径、
  // 被 vite 的 200 HTML 骗过）；回体必须是 JSON 且 `status:"ok"`。
  //
  // 2026-11-05：探活与**开屏数据并行**（探活要一个 RTT，chat/sessions 本来也要一个；
  // 串着做等于白等一秒）。warm 用**第一个候选**发 —— 不是 apiBase()：单容器里 apiBase()
  // 默认的 :8787 对外不可达，真正的基址是同源代理。候选没赢就丢掉这份 warm。
  const last = lastSessionId();
  const firstBase = apiBaseCandidates()[0];
  const warmApi = createApi({ base: firstBase });
  const warm = Promise.all([
    warmApi.getChat().catch(() => null),
    last ? warmApi.listSessions().catch(() => []) : Promise.resolve([]),
  ]);
  let health = null;
  let lastErr: unknown = null;
  let winner = "";
  for (const cand of apiBaseCandidates()) {
    try {
      health = await probeHealth(cand);
      winner = cand;
      break;
    } catch (err) {
      lastErr = err;
    }
  }
  if (!health) {
    // 连不上后端：不开一副破界面，就明说"连不上"，并把试过的地址列出来 ——
    // 从前这里弹的是一张要填地址/口令的**门板**，账号砍了之后那种门板没有意义了
    //（没有要填的东西），于是改成一句能照着查的提示。
    toast(`连不上后端（${errText(lastErr)}）。检查它起没起、地址对不对。`, "error");
    connText = "未连接";
    loadingSession.value = false;
    sessionsLoading.value = false;
    return;
  }
  if (winner && winner !== apiBase()) {
    // 非首选候选胜出（给了同源等）：存下来 + 刷新 —— sharedApi() 等一切读的都是 apiBase()，
    // 不存的话这一页会指着错的地址干活。一次性闪一下，之后就走存的。
    saveNetPrefs(winner);
    location.reload();
    return;
  }
  connText = `已连接 · v${health.version ?? "?"}`;
  // 回到"上次在看的那条会话"（2026-10-11 用户定）：刷新 / 被安卓丢弃后重载 ⇒ 不再是一张空草稿。
  // 首选候选赢 ⇒ 直接吃与探活并行拉回来的那份；它没赢（例如同源代理不存在、回落到 8787）就现拉。
  let chatCfg, sessions;
  if (winner === firstBase) {
    [chatCfg, sessions] = await warm;
  } else {
    [chatCfg, sessions] = await Promise.all([
      api.getChat().catch(() => null),
      last ? api.listSessions().catch(() => []) : Promise.resolve([]),
    ]);
  }
  // 这趟就是"刚拉过"的会话列表 ⇒ 记下时间，别让 enterSession 立刻又在后台补一次。
  if (last) sessionsFetchedAt = Date.now();
  applyChatConfig(chatCfg);
  if (last && sessions.some((s) => s.id === last)) {
    await enterSession(last, sessions);
    return;
  }
  await startDraft();
}

let connText = "未连接";
// refreshStatusline：连接 / 助手 / 相位 / 上下文 —— 全都喂给右上角那枚环（2026-10-28 用户定）。
//
// 这四样从前是一行小字（"已连接 v0.1.0｜上下文 344/1.04M｜默认助手｜idle"），紧贴着对话；
// 现在共用一处落点：环里是占比，点开的浮板里是明细（含连接与相位）。
// ⚠ 别再造第二条"显示状态行"的路 —— 同一个数字住两处，只会让两处都可能过期。
// withContext：这一拍要不要**真去问** `/context`（它是 O(n) 的全量装配：列消息 + 现演状态 + 拼出站）。
// 生成 / 重摇 / 摘要刷新这些轮询每秒要问好几次，而占用在一轮落地前根本不变 ⇒ 轮询里传 false，
// 只在翻 idle 那一拍（以及进会话/换模型这些"事情真的变了"的时刻）按默认 true 问一次。
// 少掉的是请求与装配，不是显示：环在轮询期间保持上一拍的数（那正是此刻真实的占用）。
function refreshStatusline(phase: string = "idle", elapsedMs: number | null = null, { withContext = true }: { withContext?: boolean } = {}): void {
  // 连接 + 助手 + 相位：草稿态也要报（"没连上"跟有没有会话无关）。
  topbarRef.value?.setStatus({ conn: connText, agent: statusAgent.value, phase, elapsedMs });
  if (draft || !sessionID) {
    // 草稿：没有会话可问占用 ⇒ 环清空（没有会话就没有"占了多少"这回事）。
    topbarRef.value?.setContext(null);
    return;
  }
  if (!withContext) return;
  api.context(sessionID).catch(() => null).then((ctx) => {
    topbarRef.value?.setContext(ctx);
  refreshMacros();
  }).catch((err) => toast(`上下文占用读取失败：${errText(err)}`, "error"));
}
function refreshStateView(id: string | null = null): void {
  const sid = id ?? sessionID;
  if (draft || !sid) return; // 草稿：没有会话可问状态（右栏保持空话）
  const my = epoch;
  api.sessionState(sid).then((v) => { if (my === epoch) payloadbarRef.value?.setTables(v?.tables); }).catch(() => {});
}
// 命令执行器：键表在 utils/commands.ts，动作与前置条件收在 composable。
const { runCommand } = useCommands({
  api,
  toast,
  overlaysRef,
  getSessionID: () => sessionID,
  getEpoch: () => epoch,
  isDraft: () => draft,
  vueMessages,
  startDraft,
  enterSession,
  fillModelSelect,
  compactNow,
  rerollNow: () => currentAgentIsFlow.value ? regenerateNow() : rerollNow(),
  resend,
  reloadWindow,
  refreshStateView,
  refreshRerolls,
  openMessageEditor,
  openSummaryEdit,
  deleteMessagesFlow,
});

// —— 预演（右载荷栏）——
// 两条路同一支笔：会话里走 `POST /sessions/{id}/outgoing`；草稿走 `POST /outgoing`（临时会话，
// 带上草稿铸的 id —— 头上那个会话键就是将来真会话的）。previewSeq 丢弃迟到的那一版。
let previewTimer = 0;
function onComposerInput(v: string): void {
  liftOverlays();
  if (cmdMenu.value && v.startsWith("/")) {
    const cmd = parseCommand(v);
    overlaysRef.value?.showPalette(filterCommands(cmd ? cmd.name : ""));
  } else {
    overlaysRef.value?.hidePalette();
  }
  clearTimeout(previewTimer);
  previewTimer = setTimeout(() => {
    if (!previewOn()) return;
    if (v.startsWith("/") || (!draft && !sessionID)) {
      refreshStatusline("idle");
      return;
    }
    if (draft && !v.trim()) {
      payloadbarRef.value?.setWire(null); // 草稿清空 ⇒ 右栏回到占位文案
      return;
    }
    previewPayload(v);
  }, 300);
}
function onComposerKeydown(e: KeyboardEvent, value: string): void {
  const pickerOpen = overlaysRef.value?.isPickerOpen();
  const palOpen = overlaysRef.value?.isPaletteOpen();
  if (e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)) {
    if (palOpen || pickerOpen) {
      e.preventDefault();
      if (pickerOpen) overlaysRef.value?.movePicker(1);
      else overlaysRef.value?.movePalette(1);
    }
  } else if (e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey)) {
    if (palOpen || pickerOpen) {
      e.preventDefault();
      if (pickerOpen) overlaysRef.value?.movePicker(-1);
      else overlaysRef.value?.movePalette(-1);
    }
  } else if (e.key === "Enter" && (palOpen || pickerOpen) && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
    // 只有裸回车归浮层（修饰回车一律换行，与发送键口径一致 —— 不查修饰键的话
    // Shift+回车会把高亮命令直接执行掉）。
    if (pickerOpen && overlaysRef.value?.pickerAction()) {
      e.preventDefault();
      const it = overlaysRef.value?.pickerSelected();
      const pa = overlaysRef.value?.pickerAction();
      overlaysRef.value?.hidePicker();
      if (it && pa) pa(it);
    } else if (palOpen && value.startsWith("/") && overlaysRef.value?.palSelected()) {
      e.preventDefault();
      const cmd = parseCommand(value);
      const entry = overlaysRef.value.palSelected();
      composerRef.value?.clear();
      liftOverlays();
      runCommand(entry?.name ?? "", cmd ? cmd.args : []).catch((err) =>
        toast(`命令失败：${errText(err)}`, "error"),
      );
    }
  } else if (e.key === "Escape") {
    overlaysRef.value?.hidePalette();
    overlaysRef.value?.hidePicker();
  } else if (e.key === "Enter") {
    // 三档判定与 Composer 的提示同源（utils/prefs.ts）。
    if (wantsSend(e)) {
      e.preventDefault();
      composerRef.value?.submit();
    }
  }
}
// sendFromDraft：草稿的第一句 —— 建会话（带草稿里选的 model/agent）→ 受理 → 轮询。
// 只有"受理"失败才回滚（删掉刚建的空会话、退回草稿）；轮询掉线不回滚（消息已入库）。
async function sendFromDraft(text: string): Promise<void> {
  const body: CreateSessionRequest = {};
  if (draftId) body.id = draftId; // 预演头上那个 id —— 真会话必须落同一个
  if (draftPrefs.provider) body.provider = draftPrefs.provider;
  if (draftPrefs.model) body.model = draftPrefs.model;
  if (draftPrefs.agent) body.agent_id = draftPrefs.agent;
  if (draftPrefs.thinking_level) body.thinking_level = draftPrefs.thinking_level;
  if (Array.isArray(draftPrefs.native_tools_disabled) && draftPrefs.native_tools_disabled.length) {
    body.native_tools_disabled = draftPrefs.native_tools_disabled;
  }
  // 草稿这条路是**两个** back-to-back 请求（建会话 → 受理）⇒ 远端 1s+ 时足足两秒没反应。
  // 先画再说：建会话失败就撤掉并退回草稿（连输入框里的字一起还回去）。
  const optimistic = addMessage("你", text, "user");
  const live = startSendingBubble();
  localRev++;
  let s;
  try {
    s = await api.createSession(body);
  } catch (err) {
    dropOptimistic(optimistic);
    dropOptimistic(live);
    composerRef.value?.setText(text);
    throw err;
  }
  draft = false;
  sessionID = s.id;
  saveLastSession(s.id); // 草稿转真会话：从现在起"在看的就是它"
  Object.assign(sessionInfo, s);
  paintSessionHeader();
  refreshSessionsBar();
  let accepted;
  pendingSends++;
  try {
    accepted = await api.sendMessage(s.id, text);
    refreshTasks();
  } catch (err) {
    await api.deleteSession(s.id).catch(() => {});
    await startDraft();
    dropOptimistic(live);
    composerRef.value?.setText(text);
    throw err;
  } finally {
    pendingSends--;
  }
  settleOptimisticUser(optimistic, accepted?.user);
  live.phase = "waiting";
  localRev++;
  await pollTurn(s.id, accepted.turn.message_id, live);
}
function stopCurrent() {
  if (!sessionID) return; // 草稿没有会话可停
  api.stop(sessionID).catch((err) => toast(`停止失败：${errText(err)}`, "error"));
}
function onComposerSubmit(text: string): void {
  liftOverlays();
  overlaysRef.value?.hidePalette();
  if (!text) {
    if (draft) { toast("新对话还没有可续写的历史：先写一句"); return; }
    resend().catch((err) => toast(`重发失败：${errText(err)}`, "error"));
    return;
  }
  const cmd = parseCommand(text);
  if (cmd) {
    runCommand(cmd.name, cmd.args).catch((err) => toast(`命令失败：${errText(err)}`, "error"));
    return;
  }
  if (draft) {
    sendFromDraft(text).catch((err) => toast(`发送失败：${errText(err)}`, "error"));
    return;
  }
  send(text).catch((err) => toast(`发送失败：${errText(err)}`, "error"));
}

// —— 滚动隐藏上下栏（触屏；2026-10-17 用户定，默认开）——
// 味道照浏览器：**进度跟位移成正比**（不是"过阈值啪一下"）。方向是用户定的口径：
//   往下看（`scrollTop` ↑，内容前进）⇒ **顶栏**按位移逐步收起，底栏同时放回来；
//   往回看（`scrollTop` ↓）⇒ **底栏**逐步收起，顶栏放回来。
// 一次只藏一边（藏起来的那边由反向滚动放回）；到顶/见底立刻全放回（浏览器也这样）。
// 载体只有两个 CSS 变量（px），CSS 只做 translateY ⇒ 这里永远不碰布局。
// —— 设置视图（与对话同级，2026-10-10） ——
// 窄屏：进/出设置都把左抽屉归位（手机上别让它压着视图）；宽屏：不动侧栏（并排看没问题）。
async function onChatSaved() {
  const before = windowLimit.value;
  applyChatConfig(await api.getChat().catch(() => null));
  if (sessionID && before !== windowLimit.value) {
    await loadWindow({ reset: true, scroll: false });
  }
}
function onPrefsSaved() {
  macroRev.value += 1;
  refreshMacros();
  cmdMenu.value = commandMenuOn();
  if (!chromeHideOn()) resetChrome(document.querySelector("#messages-vue")); // 关掉这功能 ⇒ 栏立刻回来
  if (!cmdMenu.value) overlaysRef.value?.hidePalette();
}
// —— 拖拽调宽 ——
let uninstallViewportFit: (() => void) | null = null;
onMounted(() => {
  installSidePanels();
  startTasksPolling();
  flowCallsTimer = window.setInterval(() => { void refreshFlowCalls(); }, 700);
  void refreshFlowCalls();
  // 键盘遮挡：整盒跟着视觉视口拟合（见 utils/viewport.ts）；键盘一开，如果人正在输入，
  // 顺手把消息列贴回底部（矮了一截后最新一条不该被挤出视野）。
  uninstallViewportFit = installViewportFit();
  const vv = window.visualViewport;
  const onVvResize = () => { if (document.activeElement?.id === "input") scrollBottom(); };
  vv?.addEventListener("resize", onVvResize);
  // 滚动隐藏上下栏（触屏）：passive 监听，只写两个 CSS 变量。
  document.querySelector("#messages-vue")?.addEventListener("scroll", onMessagesScroll, { passive: true });
  // 滑动面板：跟手拖拽。touchmove 必须非 passive（接管后要 preventDefault 压住页面滚）。

  // 跨断点（桌面 ⇄ 抽屉）时把状态重算一遍：变成抽屉就一律先关掉，不然会"啪"地盖上来。
  // 只剩左栏这一个断点（右栏 2026-10-29 搬进上下文工作区）。

  // 回到前台就对齐（2026-11-01 用户定）。三条都要：
  //   visibilitychange —— 切标签页/切 App（手机最常见的那条路）；
  //   pageshow         —— 从 bfcache 里恢复（安卓丢页面再回来常走这条，那时 visibilitychange 可能不响）；
  //   focus            —— 同窗口切焦点（桌面 alt+tab 到别的窗口）。
  document.addEventListener("visibilitychange", resyncOnForeground);
  window.addEventListener("focus", resyncOnForeground);
  window.addEventListener("pageshow", resyncOnForeground);
  window.addEventListener("mc-update-auto-check", startUpdatePolling);
  installOverlayLayout();
  boot().catch((err) => {
    connText = "未连接";
    loadingSession.value = false;
    refreshStatusline("idle");
    toast(`启动失败：${errText(err)}`, "error");
  });
});
onUnmounted(() => {
  uninstallViewportFit?.();
  uninstallViewportFit = null;

  stopTasksPolling();
  if (flowCallsTimer !== null) { clearInterval(flowCallsTimer); flowCallsTimer = null; }
  uninstallOverlayLayout();
  uninstallSidePanels();
  document.removeEventListener("visibilitychange", resyncOnForeground);
  window.removeEventListener("focus", resyncOnForeground);
  window.removeEventListener("pageshow", resyncOnForeground);
  window.removeEventListener("mc-update-auto-check", startUpdatePolling);
});
</script>

<template>
  <VApp>
    <!-- 更新条：**悬浮**在输入栏上方（fixed + bottom: --composer-h + 0.5em），不占布局。
         从前试过流内横幅：它会把整个应用往下推、独自占掉一大段；用户 2026-11-06 要求改回悬浮。 -->
    <UpdateBar
      v-if="updateReady || backendUpdate || backendUpdating"
      ref="updateRef"
      :pwa="updateReady"
      :backend="backendUpdate"
      :busy="backendUpdating"
      @update="applyUpdate"
      @update-backend="runBackendUpdate"
    />
    <div id="columns">
      <SessionBar
        ref="sessionbarRef"
        :collapsed="leftHidden"
        :busy-ids="busyIds"
        :loading="sessionsLoading"
        :refreshing="sessionsRefreshing"
        @prefetch="prefetchSession"
        @toggle-left="toggleLeft"
        @select="(id) => { closeLeftDrawer(); backToChat(); enterSession(id).catch((err) => toast(`进会话失败：${errText(err)}`, 'error')); }"
        @settings="(id) => { sessionSettingsId = id; }"
        @copy="copySessionFromBar"
        @export="exportSessionFromBar"
        @close="askDeleteSession"
        @new="newSession"
        @new-image="() => toast('生图功能尚未接入', 'warn')"
        @open-settings="openSettings"
        @open-sessions="() => openManage('session')"
        @open-providers="() => openManage('provider')"
        @open-agent="() => openManage('agent')"
        @open-update="() => openManage('update')"
      />
      <div id="scrim-left" @click="toggleLeft"></div>
      <div id="content">
      <section id="chat-col" v-show="!settingsOpen">
    <TopBar
      ref="topbarRef"
      :left-hidden="leftHidden"
      :workspace="workspace"
      :loading="loadingSession"
      :flow="currentAgentIsFlow"
      :flow-calls="flowCalls"
      :flow-active-nodes="flowActiveNodes"
      @select-workspace="selectWorkspace"
      @save-overrides="saveRequestOverrides"
      @toggle-left="toggleLeft"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${errText(err)}`, 'error'))"
      @cancel-queue="cancelQueue"
      @cancel-task="cancelTask"
      @refresh-macros="refreshMacros"
    />
        <!-- 聊天 / 上下文：VTabsWindow 真正负责左右滑动；顶栏的 VTabs 只管 tab 条。 -->
        <VTabsWindow v-model="workspace" class="workspace-window">
        <VTabsWindowItem value="chat">
        <div class="workspace-pane">
        <main id="messages-vue">
          <!-- 滚动容器（#messages-vue）现在**占满整个对话区**（滚动条因此贴最右缘，2026-10-14 用户定）；
               阅读列约束（760 居中、两侧 56px、上下留白）落在这一层 —— 气泡别拉成整屏长句。 -->
          <div class="messages-inner">
          <!-- 会话加载骨架（2026-11-02）：先撑住消息列，别让人对着纯黑猜"是不是坏了"。 -->
          <div v-if="loadingSession" class="chat-skeleton" aria-label="正在加载会话" role="status">
            <span class="sk-line sk-a"></span>
            <span class="sk-line sk-b"></span>
            <span class="sk-line sk-c"></span>
          </div>
          <!-- 空草稿首屏（2026-11-02）：新会话不是一页纯黑 —— 欢迎语 + 当前助手/模型。
               左栏的品牌位已删；这里只回答"我马上要跟谁说话"。 -->
          <div v-if="draft && !chatRows.length" class="draft-welcome">
            <h2>有什么想聊的？</h2>
            <p class="dw-sub">{{ draftAgentName }}<span class="dw-sep">·</span>{{ draftModelName }}</p>
            <p class="dw-hint">直接在下面输入第一条消息开始；也可以用 <kbd>/</kbd> 开头的命令。</p>
          </div>
          <VBtn
            v-if="olderSegments > 0"
            id="load-more"
            variant="outlined"
            size="small"
            @click="loadMore().catch((err) => toast(`加载更多失败：${errText(err)}`, 'error'))"
          >↑ 加载更多历史（还有 {{ olderSegments }} 段）</VBtn>
          <template v-for="row in chatRows" :key="row.key">
            <SummaryCard
              v-if="row.kind === 'summary'"
              :summary="row.summary"
              :from-idx="row.fromIdx"
              :to-idx="row.toIdx"
              :rows="row.rows"
              :loading="row.loading"
              :reroll="summaryReroll.active && summaryReroll.target_summary_id === row.summary.id ? summaryReroll : null"
              :macros="macroValues"
              @toggle-raw="(open) => onSummaryToggle(row.summary.id, open)"
              @refresh="summaryRefresh(row.summary.id)"
              @edit="openSummaryEdit(row.summary)"
              @version="(d) => summaryVersion(d)"
              @undo="undoSummary(row.summary.id)"
            />
            <MessageBubble
              v-else
              :who="row.msg.who"
              :idx="row.displayIdx"
              :role="row.msg.role"
              :content="bubbleContent(row.msg)"
              :reasoning="bubbleReasoning(row.msg)"
              :reasoning-ms="row.msg.reasoningMs"
              :duration-ms="rowDuration(row.msg)"
              :message-id="row.msg.messageId"
              :prompt-id="row.msg.messageId === lastMsgId ? latestPromptId : null"
              :is-last="!!row.msg.messageId && row.msg.messageId === lastMsgId"
              :streaming="!!row.msg.stream || isLive(row.msg)"
              :phase="String(row.msg.phase || '')"
              :kind="row.msg.kind || 'chat'"
              :status="row.msg.status || 'done'"
              :tool="row.msg.tool"
              :sources="row.msg.sources"
              :tools="row.msg.tools"
              :usage="row.msg.usage"
              :macros="macroValues"
              :reroll="currentAgentIsFlow ? null : (row.msg.messageId && rerollState.active && rerollState.target_message_id === row.msg.messageId ? rerollState : null)"
              :flow="currentAgentIsFlow"
              @edit="onBubbleEdit"
              @edit-prompt="onBubbleEditPrompt"
              @delete="onBubbleDelete"
              @reroll="rerollNow"
              @reroll-go="rerollGo"
              @regenerate="regenerateNow"
            />
          </template>
          </div>
        </main>
    <Composer
      ref="composerRef"
      :menu-on="cmdMenu"
      :thinking="currentThinking"
      :thinking-options="thinkingChoices"
      :native-tools="currentAllTools"
      :native-tools-disabled="sessionInfo.native_tools_disabled ?? []"
      :flow="currentAgentIsFlow"
      @select-thinking="setThinking"
      @toggle-native-tool="toggleNativeTool"
      @input-text="onComposerInput"
      @keydown="onComposerKeydown"
      @submit="onComposerSubmit"
      @stop="stopCurrent"
      @menu="() => overlaysRef?.showPalette(filterCommands(''))"
    >
      <template #toolbar>
        <AgentMenu
          :agents="vueAgents"
          :model-value="sessionInfo.agent_id || ''"
          @select="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${errText(err)}`, 'error'))"
        />
      </template>
    </Composer>
        </div>
        </VTabsWindowItem>
    <!-- 上下文工作区（2026-10-29 用户定）：与消息列 + 输入行互斥 —— 同一块地方换个视角看。
         顶栏不在切换范围内（它自己带着那颗开关，切掉就找不回来了）。 -->
    <VTabsWindowItem value="context">
    <div class="workspace-pane">
    <ContextWorkspace
      ref="payloadbarRef"
      :has-session="!!sessionInfo.id"
      :session-id="sessionInfo.id"
      :default-blocks="compactDefault"
      :flow="currentAgentIsFlow"
      :agent="currentAgent"
      @flow-saved="refreshAgents"
      @edit-flow-agent="openAgentFlow"
      @compact="(n) => compactNow(n ?? undefined)"
      @compact-payload="previewCompactPayload"
      @tab="onPayloadTab"
      @undo="undoSummary"
      @preview="onPayloadPreview"
      @advanced-done="onAdvancedDone"
    />
    </div>
    </VTabsWindowItem>
        </VTabsWindow>
      </section>
      <SettingsModal
        v-if="settingsOpen"
        :only="settingsOnly"
        :focus-agent-id="agentFocus"
        :ask="askConfirm"
        :esc-blocked="!!sessionSettingsId"
        @close="closeSettings"
        @toggle-left="toggleLeft"
        @sessions-changed="refreshSessionsBar"
        @models-changed="fillModelSelect"
        @chat-saved="onChatSaved"
        @prefs-saved="onPrefsSaved"
        @goto-session="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${errText(err)}`, 'error')); }"
      />
      </div>
    </div>
    <Toast ref="toastRef" />
    <!-- 更新条：只在检测到新版本时出现（挂根层 —— 设置视图开着也该看得见）。 -->
    <!-- Overlays 挂根层（不在聊天视图里）：设置视图开着时"确认"也要能弹（fixed 定位照旧）。 -->
    <Overlays ref="overlaysRef" @run-command="(n) => runCommand(n, []).catch((err) => toast(`命令失败：${errText(err)}`, 'error'))" @pick="onPickAction" />
    <SummaryEditor
      v-if="summaryEditTarget"
      :session-id="sessionID ?? ''"
      :summary="summaryEditTarget"
      @close="summaryEditTarget = null"
      @saved="onSummaryEdited"
    />
    <MessageEditor
      v-if="messageEditTarget"
      :session-id="messageEditTarget.sessionId"
      :message="messageEditTarget.message"
      :kind="messageEditTarget.kind"
      :rerollable="messageEditTarget.rerollable"
      :flow="currentAgentIsFlow"
      @close="messageEditTarget = null"
      @saved="onMessageEdited"
    />
    <SessionSettings
      v-if="sessionSettingsId"
      :session-id="sessionSettingsId"
      @close="sessionSettingsId = null"
      @renamed="() => { refreshSessionsBar(); const sid = sessionID; if (sid && sessionSettingsId === sid) enterSession(sid).catch(() => {}); sessionSettingsId = null; }"
    />
  </VApp>
</template>
