<script setup lang="ts">
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from "vue";
import { VApp, VBtn, VTabsWindow, VTabsWindowItem } from "vuetify/components";
import { createApi, mergeStreamSlice, probeHealth } from "./api/client.ts";
import { parseCommand, filterCommands, COMMAND_META } from "./utils/commands.ts";
import { uuidv7 } from "./utils/ids.ts";
import { buildCompactTree } from "./utils/tree.ts";
import {
  buildChatRows, computeSegments, windowStart, prevWindowStart, alignWindowFrom, olderCount, runsToFetch, seededMissingRuns,
  rerollTargetLive, bubbleText, editRerollTarget, latestPromptFor,
} from "./utils/chatrows.ts";
import { applyBackground } from "./utils/background.ts";
import { installViewportFit } from "./utils/viewport.ts";
import { downloadJSON } from "./utils/download.ts";
import { busySessionIds } from "./utils/tasks.ts";
import { thinkingOptions, thinkingLevelsForModel } from "./utils/thinking.ts";
import { rememberWindow, recallWindow, forgetWindow } from "./utils/windowcache.ts";
import { sharedApi, previewOn, payloadPreviewOn, applyTheme, wantsSend, apiBase, apiBaseCandidates, saveNetPrefs, lastSessionId, saveLastSession, commandMenuOn, applyTextScale, applyFonts, chromeHideOn, syncPrefs, updateAutoCheckOn } from "./utils/prefs.ts";
import {
  groupModelsByProvider,
  deletionPlanSummary,
  modelKey,
  parseModelKey,
  modelOption,
  agentOption,
  whoText,
  sessionTitle,
} from "./utils/format.ts";
import TopBar from "./components/TopBar.vue";
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
import { registerPWA, onUpdateReady, startUpdate } from "./utils/pwa.ts";
import { checkBackendUpdate, waitForRestart } from "./utils/update.ts";
import { useTypewriter } from "./composables/useTypewriter.ts";
import { useTasks } from "./composables/useTasks.ts";
import { useSidePanels } from "./composables/useSidePanels.ts";
import { useChrome } from "./composables/useChrome.ts";
import { useOverlayLayout } from "./composables/useOverlayLayout.ts";
import { useSettingsView } from "./composables/useSettingsView.ts";
import { useModelsAgents } from "./composables/useModelsAgents.ts";

const api = sharedApi();
applyTheme();
applyBackground();
// 界面缩放（2026-10-28）：同样**开机第一帧就得对** —— 晚一帧就是"字先小一下再变大"的闪。
// 它写在根节点的 `style` 上（`--app-text-scale`），theme.css 的 body 字号吃这一格。
applyTextScale();
applyFonts();
// 服务器是**偏好的权威**（2026-11-01「所有设置均存储在服务器上」）。
// 上面那几行先按**本地镜像**把主题/缩放/输入栏形态铺上 —— 那是为了让**第一帧**就是对的
// （不然会先闪一下默认配色再变）。随后这一发去问服务器，不一致就改过来（并重挂背景图）。
// 失败只 toast：连不上后端时页面照样能用，只是偏好停在镜像那一份。
syncPrefs()
  .then(() => { applyBackground(); cmdMenu.value = commandMenuOn(); })
  .catch((err) => toast(`界面偏好读取失败：${err.message}`, "error"))
  .finally(() => { startUpdatePolling(); });
// —— 新版本提示（PWA 更新条，2026-10-20 用户定）——
// 从 `autoUpdate` 改成 `prompt`：新版本**等用户点**才切，而不是悄悄自己 reload。
// 勾子必须在**开机**就挂（SW 装完是异步回调 —— 挂晚了那一次提示就丢了）。
const updateRef = ref<any>(null);
const updateReady = ref(false);
const backendUpdate = ref(false);
const backendUpdating = ref(false);
let updatePollTimer: ReturnType<typeof setInterval> | null = null;
onUpdateReady(() => { updateReady.value = true; });
registerPWA();
// 点「刷新」⇒ 发 skipWaiting，新 SW 接管后由 vite-plugin-pwa 内建回调 reload 这一页。
function applyUpdate() { startUpdate(); }

// 自动检查 GitHub：只有 remote_relation=behind 才算“有可拉取的更新”；
// 本地领先 / 分叉 / 读不到都不打扰。开关在更新面板，事件通知这里重启定时器。
async function refreshBackendUpdate(): Promise<void> {
  if (!updateAutoCheckOn()) { backendUpdate.value = false; return; }
  backendUpdate.value = await checkBackendUpdate(api);
}
function stopUpdatePolling(): void {
  if (updatePollTimer !== null) { window.clearInterval(updatePollTimer); updatePollTimer = null; }
}
function startUpdatePolling(): void {
  stopUpdatePolling();
  // SSR / 测试环境没有 window；别留一个 setInterval 把 Node 测试进程吊住。
  if (typeof window === "undefined") return;
  if (!updateAutoCheckOn()) { backendUpdate.value = false; return; }
  void refreshBackendUpdate();
  updatePollTimer = window.setInterval(() => { void refreshBackendUpdate(); }, 5 * 60 * 1000);
}
async function runBackendUpdate(): Promise<void> {
  if (backendUpdating.value) return;
  if (!(await askConfirm("会拉取远程最新代码并重启前后端，期间约 10–20 秒不可用。继续吗？"))) return;
  backendUpdating.value = true;
  try {
    const result = await api.pullUpdate();
    backendUpdate.value = false;
    await waitForRestart(result.before.started_at, api);
    location.reload();
  } catch (e) {
    backendUpdating.value = false;
    toast(`更新失败：${e instanceof Error ? e.message : String(e)}`, "error");
  }
}

// —— 组件 refs（直调实例方法，替代桥） ——
const toastRef = ref<any>(null);
const topbarRef = ref<any>(null);
const sessionbarRef = ref<any>(null);
const payloadbarRef = ref<any>(null);
const composerRef = ref<any>(null);
const overlaysRef = ref<any>(null);
// 命令菜单（输入框左侧 ☰ 与 "/" 浮层）：默认关（移动端碍事，2026-10-17 用户定）；
// 设置 → 外观里改了立刻生效（不刷新）—— 偏好住服务器（`prefs.json`），这里是它的活视图。
const cmdMenu = ref(commandMenuOn());
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
  onWorkspaceContext: refreshContextLazy,
});

const { settingsOpen, settingsOnly, openSettings, openManage, closeSettings, backToChat } = useSettingsView({ closeLeftDrawer });



const { resetChrome, onMessagesScroll } = useChrome();

const { liftOverlays, install: installOverlayLayout, uninstall: uninstallOverlayLayout } = useOverlayLayout(overlaysRef);
// askConfirm：给设置/子编辑器用的全局确认框（与左栏删除同一个口 —— "不可逆的删除先摊开"）。
function askConfirm(text) {
  return overlaysRef.value?.confirmAsk(text) ?? Promise.resolve(false);
}
// onPickAction：浮层"选中某项"的回执（动作在浮层里选好，这里执行 + 统一兜错）。
// ⚠ 别把它写回模板内联（`(a, it) => Promise.resolve(a(it))…`）：模板里的 `Promise` 会被编译成
// `_ctx.Promise`（实例代理上不存在）⇒ 点击那一刻当场 TypeError、被 Vue 吞掉，动作永远不执行
// （踩过：picker 的鼠标点击从写下那天就是死的，键盘路径绕开了它所以一直没暴露）。
function onPickAction(action, item) {
  Promise.resolve(action(item)).catch((err) => toast(`操作失败：${err.message}`, "error"));
}

// —— 会话状态 ——
let sessionID: any = null;
// 会话列表缓存（2026-11-05，高延迟优化）：从前每点一条会话都要先 `GET /sessions`
// 才轮到消息窗口 —— 远端 1s+ 时那一整个 RTT 什么也没画，而列表本来就在屏幕上。
// 缓存只当"先用着的快照"：进会话后按需在后台补一次（见 enterSession），不拿它当真相。
const sessionsCache = ref([]);
// 左栏的加载态（MD3）：首次拿列表画骨架；后台补列表时顶部一条 linear progress。
// 从前空列表与"还没加载"在界面上分不出来 —— boot 的那两秒会写"还没有会话"，那是假话。
const sessionsLoading = ref(true);
const sessionsRefreshing = ref(false);
let sessionsFetchedAt = 0;
const SESSIONS_FRESH_MS = 10_000;
// 回看缓存（2026-11-05 用户定）：每条会话上一屏的窗口快照（LRU、最多 8 条）。
// 切回去先把这屏画出来（0 RTT），再后台对账 —— 见 enterSession 与 windowcache.js。
const windowCache = new Map();
// localRev：本地的"消息列已经变了"计数（乐观发送 / 编辑 / 删除…）。
// 后台对账在落屏前比一次：期间人动过 ⇒ 这一发作废，别拿旧快照覆盖刚画上去的字。
let localRev = 0;
// pendingSends：正在等 202 的发送数。乐观气泡已经上屏、但服务端还没落库 ——
// 这时候落到屏上的窗口快照可能**还没有**那句，必须把它判成"旧"。
let pendingSends = 0;
// 悬停/触摸预取（2026-11-05）：会话行被"意图"指到就把它的窗口先拉进缓存，
// 点下去就是 0 RTT。并发上限 3 —— 手指扫过整列时别把每一条都排上队。
const PREFETCH_MAX_ACTIVE = 3;
const PREFETCH_FRESH_MS = 30_000;
const prefetching = new Set();
const prefetchedAt = new Map();
let prefetchActive = 0;
// 会话世代：进会话/回草稿就 +1。所有会话域的异步续写都在 await 后核对本世代，对不上就丢 ——
// 没有这道闸，快速切会话时迟到的 A 续写会覆掉已切到的 B 的抬头与消息列（实测复现过：
// 顶栏 A、高亮 B、气泡 A —— 界面被撕成三份）。
let epoch = 0;
const sessionInfo = reactive<any>({});
// 草稿态（DeepSeek 网页版行为，2026-10-09 定）：＋/开机/`/new` 只开一张**客户端草稿**，库里没有行；
// 首条发送才 POST /sessions（带上草稿里选好的 provider/model/agent）。
// ⚠ HACK（刻意，全项目唯一的口子）：草稿**铸一枚 UUIDv7** 当将来那会话的 id —— 预演
// （POST /outgoing 临时会话）拿它进请求头，建会话时把同一个值交给 POST /sessions
// ⇒ 预演的那一发给真发的那一发逐字节一致。别扩散（消息/摘要的 id 仍是服务端铸）。
// 要会话的命令（compact/cut/reroll…）在草稿里明确拒绝（needsSession 标在 commands.js 的表上）。
let draft = false;
let draftPrefs: any = { provider: "", model: "", agent: "", thinking_level: "" };
let draftId: any = null;

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
const currentNativeTools = computed<string[]>(() => {
  const provider = String(sessionInfo.provider ?? "");
  const model = String(sessionInfo.model ?? "");
  if (!provider || !model) return [];
  const row = vueModels.value.find((m) => m.provider === provider && m.upstream_id === model);
  return (row?.native_tools ?? []).filter(Boolean);
});
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
    toast(`思考等级保存失败：${err.message}`, "error");
  }
}
// 换模型后旧档位可能不在新模型的能力集里 ⇒ 清回"跟随默认"（后端也只会接受能力集里的值）。
watch(thinkingLevels, (levels) => {
  const cur = currentThinking.value;
  if (!cur || !levels.length) return;
  if (!levels.includes(cur)) setThinking("");
});
const vueMessages = ref<any[]>([]);
// 会话加载中的骨架（2026-11-02）：进会话会先清空消息列，再等 /sessions + 消息窗口；
// 远端部署时那是两三个 RTT 的空白。给一层静态骨架，至少让人知道"在加载"而不是"坏了"。
const loadingSession = ref(true);
const vueSegments = ref<any[]>([]); // 整条会话的段布局（computeSegments 的产出）：窗口/折叠/编号都认它
const vueStream = ref({ id: null, text: "", reasoning: "" });
// 流式显字（2026-11-03）：`vueStream` 是后端的完整目标；`streamDisplay` 是打字机当前露出的前缀。
// 每帧只改 streamDisplay，轮询与合并仍只认 vueStream（两个状态别互相镜像）。
const { streamDisplay, setStreamTarget, waitTypewriter, resetTypewriter, flushTypewriter } = useTypewriter();
// 消息窗口（分页 + 折叠；口径与全套算法在 utils/chatrows.ts）：
// 只挂最后 N 段（默认 5，来自 chat.history_window）；更老的按"加载更多"一页一页往回；
// 被摘要盖住的原文**展开才拉**（这里当缓存）。
const windowLimit = ref(5);
const windowFrom = ref(1); // 窗口起点（段对齐；1 = 已到最老）
const summaryMsgs = reactive<any>(new Map()); // 摘要 id → {state:"loading"|"ready", rows}（只有展开过的才有）
const openSummaries = new Set<string>(); // 哪些卡正展开着（reload 后补拉新鲜的）
// 设置视图的"单独入口"："" = 完整设置（四个 tab）；"session" / "provider" = 只显示那一页，
// 从左侧栏底部的「会话 / 渠道」进来。两者共用同一个视图与同一份数据加载。
// **设置 = root 专属**（用户定，2026-10-10）：`is_admin` 才给进设置；其他用户的入口按钮也不给。
const compactDefault = ref(null); // chat.compact_blocks（压缩面板输入框的占位提示）
// windowLimit：消息列首屏/加载更多一页多少段（chat.history_window；默认 5）。
function applyChatConfig(c) {
  compactDefault.value = c?.compact_blocks ?? null;
  const n = Number(c?.history_window);
  windowLimit.value = Number.isFinite(n) && n >= 1 ? Math.floor(n) : 5;
}
const sessionSettingsId = ref(null);

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
function addMessage(who, text, cls, think = undefined, messageId = undefined) {
  const row: any = {
    id: tmpKey(),
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: think?.ms,
    messageId,
  };
  vueMessages.value.push(row);
  scrollBottom();
  return row;
}
// startSendingBubble：2026-11-09 用户要的两段状态。
// 在 POST 受理之前就画助手的空气泡并标 “sending”（正在向后端发送请求）；
// 202 回来 / pollTurn 接手时改成 “waiting”（等待 Provider 响应）。
function startSendingBubble() {
  const row = addMessage("助手", "", "assistant");
  row.stream = true;
  row.phase = "sending";
  return row;
}
// 乐观上屏的那句用户消息（2026-11-05，高延迟优化）：先画，等 202 回来再换成服务端那条
// （换的是**同一条消息**：服务端回的 `user` 带真 id/idx，编号与后续编辑/删除都认它）。
// 失败就撤掉，绝不留在屏幕上假装发过 —— "看到的"和"真发出去的"分家比慢更糟。
function settleOptimisticUser(row, user) {
  if (!user) return;
  const at = vueMessages.value.indexOf(row);
  if (at >= 0) vueMessages.value.splice(at, 1, toRow(user));
}
function dropOptimistic(row) {
  const at = vueMessages.value.indexOf(row);
  if (at >= 0) vueMessages.value.splice(at, 1);
}
// 打开消息编辑器（新窗口，2026-11-02 用户定）：不再在气泡里就地换 textarea。
// message 可以是消息列里的行，也可以是 /edit 从 vueMessages 里挑的原对象。
function openMessageEditor(message, kind) {
  const mid = message?.messageId ?? message?.id;
  if (!mid) { toast("这条消息还没有 id，暂时不能编辑", "error"); return; }
  // 重摇只认尾条 assistant：编辑它自己、或编辑它前面那条 user，才能「保存并重摇」。
  const rerollTargetId = editRerollTarget(vueMessages.value, mid);
  messageEditTarget.value = {
    sessionId: sessionID,
    message: { ...message, id: mid },
    kind,
    rerollable: !!rerollTargetId,
    rerollTargetId,
  };
}
// toRow：库里的消息 → 消息列的一行（展示形状）。
function toRow(m) {
  return {
    who: m.role === "user" ? "你" : "助手",
    idx: m.idx, // 服务端派生的序号（编号就认它）
    role: m.role,
    content: m.content,
    reasoning: m.reasoning,
    reasoningMs: m.reasoning_ms,
    durationMs: m.duration_ms ?? null, // 脚注的速度要用它（与 reasoning_ms 配对）
    messageId: m.id,
    id: m.id,
    // usage：气泡脚注用它（token + 成本）。上游没报就是 null ⇒ 脚注整行不渲染。
    // 归一化在后端（providers.NormalizeUsage），这里**只读**那几格。
    usage: m.usage ?? null,
  };
}

// batchRanges：runsToFetch 的 Infinity 尾段 → 批量接口的 to=0。
function batchRanges(runs) {
  return runs.map(([a, b]) => [a, b === Infinity ? 0 : b]);
}

// fetchWindow：拉一屏窗口（摘要 + 尾部种子 + 必要的老段）并算好行/段/起点 —— **不碰界面**。
//
// loadWindow（进会话 / 对账）与"悬停预取"共用它：预取只把结果塞进 windowCache，
// 绝不许改当前消息列。`from = null` ⇒ 按"最后 N 段"起；否则保持起点（压缩/删除后对回段边界）。
// 尾部那一屏做单飞：悬停后马上点下去，两边拿的是同一个 Promise，不会发两遍。
const tailInflight = new Map();
function fetchWindow(id, { from = null, limit = windowLimit.value } = {}) {
  if (from !== null) return fetchWindowNow(id, { from, limit });
  const key = id + ":" + limit;
  const hit = tailInflight.get(key);
  if (hit) return hit;
  const p = fetchWindowNow(id, { from, limit }).finally(() => tailInflight.delete(key));
  tailInflight.set(key, p);
  return p;
}
async function fetchWindowNow(id, { from, limit }) {
  const [sums, recent] = await Promise.all([
    api.listSummaries(id),
    // 总数与"窗口可能正好要的那几条"一起拿：`?last=N` 的末条 idx 就是总数。
    // 没有摘要横跨窗口时，最后 N 条正好就是窗口要的全部 ⇒ 第二拍是空的。
    api.listMessagesLast(id, limit),
  ]);
  const total = recent.length ? recent[recent.length - 1].idx : 0;
  const segments = computeSegments(total, sums as any);
  const windowFrom = from === null ? windowStart(segments, limit) : alignWindowFrom(segments, from, limit);
  const runs = runsToFetch(segments, windowFrom, Infinity);
  // 只有"窗口比尾部种子更老"才补一发（典型：摘要把窗口起点推到很前面）。
  const missing = seededMissingRuns(runs, recent as any);
  const rest = missing.length ? await api.messageRanges(id, batchRanges(missing)) : [];
  return {
    // 种子可能多带了几条（被摘要盖住 / 落在窗口外）—— buildChatRows 本来就只画段里的。
    rows: [...recent, ...rest].sort((x, y) => x.idx - y.idx).map(toRow),
    segments,
    windowFrom,
  };
}

// loadWindow：把窗口 [windowFrom..尾] 的消息取回来 —— **只按消息段拉**（runsToFetch）：
// 被摘要盖住的 idx 一个字节都不取（想看原文 ⇒ 摘要卡展开时另拉）。
// reset ⇒ 重新按"最后 N 段"定窗口；否则保持起点（先对回段边界：压缩/删除会改结构）。
async function loadWindow({ reset = false, scroll = true, onlyIfUnchanged = false } = {}) {
  const sid = sessionID;
  const my = epoch;
  const rev = onlyIfUnchanged ? localRev : -1;
  if (!sid) return false;
  const snap = await fetchWindow(sid, {
    from: reset ? null : windowFrom.value,
    limit: windowLimit.value,
  });
  if (my !== epoch) return false;
  // 后台对账专用闸：等待期间人已经动了消息列（发送/编辑/删除…）⇒ 这一发是旧快照，丢掉。
  // 不丢的话，切回缓存会话后马上打字发送，1 秒后那份"还没含新消息"的窗口会把乐观气泡冲掉。
  if (onlyIfUnchanged && (rev !== localRev || pendingSends > 0)) return false;
  const { segments } = snap;
  vueSegments.value = segments;
  windowFrom.value = snap.windowFrom;
  vueMessages.value = snap.rows;
  // 摘要缓存：卡没了的清掉；还开着的补拉新鲜的一份（结构或正文可能刚变过）。
  const alive = new Set(segments.filter((seg) => seg.kind === "summary").map((seg) => seg.id));
  for (const key of [...summaryMsgs.keys()]) if (!alive.has(key)) summaryMsgs.delete(key);
  for (const id of openSummaries) if (alive.has(id)) refreshSummaryMsgs(id);
  // 记下这一屏：切走再切回时先画它（0 RTT），后台再对账。
  rememberWindow(windowCache, sid, { rows: vueMessages.value, segments, windowFrom: windowFrom.value });
  if (scroll) scrollBottom();
  if (treeVisible) refreshTree(); // 树看着就让它的全量账本跟着换
  return true;
}

// reloadWindow：把当前窗口按服务端最新状态重拉（一轮结束 / 编辑 / 删除 / 压缩 / 切版之后）。
async function reloadWindow({ scroll = true, onlyIfUnchanged = false } = {}) {
  return loadWindow({ reset: false, scroll, onlyIfUnchanged });
}
// foreground / 落库后的重拉：**人没在底部就别把人拽回底部**（2026-11-03 用户报：
// 出去截图再回来，滚动条自己跳回最新一条）。贴底才继续贴底；否则恢复原来的 scrollTop。
const SCROLL_STICK_PX = 48;
function nearBottom(el) {
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight <= SCROLL_STICK_PX;
}
async function reloadWindowKeepingScroll({ onlyIfUnchanged = false } = {}) {
  const el = document.getElementById("messages-vue");
  const stick = nearBottom(el);
  const top = el?.scrollTop ?? 0;
  const ok = await reloadWindow({ scroll: false, onlyIfUnchanged });
  const next = document.getElementById("messages-vue");
  if (next) {
    if (stick) next.scrollTop = next.scrollHeight;
    else next.scrollTop = Math.min(top, Math.max(0, next.scrollHeight - next.clientHeight));
  }
  return ok;
}

// prefetchSession：把一条会话的窗口悄悄取进缓存（不动界面、不抢当前会话的轮询）。
//
// 触发点在 SessionBar：桌面 140ms 悬停意图 / 键盘 focus / 触屏 touchstart。
// 已有缓存或正在取的同一条直接跳过；取失败就算了（点下去还有正常路径）。
function prefetchSession(id) {
  if (!id || id === sessionID || draft) return;
  if (windowCache.has(id) || prefetching.has(id)) return;
  if (Date.now() - (prefetchedAt.get(id) ?? 0) < PREFETCH_FRESH_MS) return;
  if (prefetchActive >= PREFETCH_MAX_ACTIVE) return;
  prefetchActive++;
  prefetching.add(id);
  fetchWindow(id).then((snap) => {
    // 期间人可能已经进了这条会话（loadWindow 会写自己那一份）—— 只在还没有缓存时补。
    if (!windowCache.has(id)) rememberWindow(windowCache, id, snap);
    prefetchedAt.set(id, Date.now());
  }).catch(() => {}).finally(() => {
    prefetchActive--;
    prefetching.delete(id);
  });
}

// loadMore：往回一页（windowLimit 段）。插入新行前记高度，插完把滚动位置补回去（锚住视野）。
async function loadMore() {
  const sid = sessionID;
  const my = epoch;
  const older = prevWindowStart(vueSegments.value, windowFrom.value, windowLimit.value);
  if (!sid || older >= windowFrom.value) return;
  const runs = runsToFetch(vueSegments.value, older, windowFrom.value - 1);
  const got = runs.length ? await api.messageRanges(sid, batchRanges(runs)) : [];
  if (my !== epoch) return;
  const el = document.getElementById("messages-vue");
  const beforeH = el ? el.scrollHeight : 0;
  const beforeTop = el ? el.scrollTop : 0;
  vueMessages.value = [...got.sort((x, y) => x.idx - y.idx).map(toRow), ...vueMessages.value];
  windowFrom.value = older;
  rememberWindow(windowCache, sid, {
    rows: vueMessages.value, segments: vueSegments.value, windowFrom: windowFrom.value,
  });
  await nextTick();
  if (el) el.scrollTop = beforeTop + (el.scrollHeight - beforeH);
}

// 摘要卡展开：**按下才拉**未压缩文本（每次按下都拉新的一版 —— 展开里编辑过也不吃旧缓存）。
async function refreshSummaryMsgs(id) {
  const seg = vueSegments.value.find((x) => x.kind === "summary" && x.id === id);
  if (!seg) { summaryMsgs.delete(id); return; }
  const my = epoch;
  summaryMsgs.set(id, { state: "loading" });
  try {
    const msgs = await api.listMessagesRange(sessionID, seg.b, seg.e);
    if (my !== epoch) return;
    summaryMsgs.set(id, {
      state: "ready",
      rows: msgs.sort((x, y) => x.idx - y.idx).map((m) => ({ msg: toRow(m), displayIdx: m.idx })),
    });
  } catch (err) {
    if (my !== epoch) return;
    summaryMsgs.delete(id);
    toast(`未压缩文本拉取失败：${err.message}`, "error");
  }
}
function onSummaryToggle(id, open) {
  if (!open) { openSummaries.delete(id); return; }
  openSummaries.add(id);
  refreshSummaryMsgs(id);
}

// 压缩树（右栏"压缩"tab）：**看着才拉全量**消息 + 摘要建树 ——
// 树要的是整条账本（原文占比、嵌套），而消息列走的是窗口（见 utils/chatrows.ts）；
// "树 tab 开着"就是用户显式说"给我看全量"。
let treeVisible = false;
let payloadTab = "payload"; // PayloadBar 的默认页；它切页时 onPayloadTab 会更新
function onPayloadTab(t) {
  payloadTab = t;
  treeVisible = t === "tree";
  if (workspace.value !== "context") return; // 人不在工作区：等他切过去再由 refreshContextLazy 拉
  if (treeVisible) refreshTree();
  else if (t === "state") refreshStateView(sessionID);
  else if (t === "payload") refreshHistoryPreview();
}
// 载荷面板的「预览载荷」开关：关 ⇒ 清掉旧载荷（不占内存/不显示旧请求）；开 ⇒ 当前 tab 拉一发。
function onPayloadPreview(on) {
  if (!on) payloadbarRef.value?.setWire(null);
  if (workspace.value === "context" && payloadTab === "payload") refreshHistoryPreview();
}
// 高级压缩完成后：消息列、状态视图、压缩树都可能变（一批新摘要 + summary_id 回填）。
function onAdvancedDone() {
  if (!sessionID) return;
  reloadWindow();
  refreshStateView(sessionID);
  if (treeVisible) refreshTree();
}

async function refreshTree() {
  if (!sessionID || !treeVisible) return;
  const my = epoch;
  try {
    const [msgs, sums] = await Promise.all([api.listMessages(sessionID), api.listSummaries(sessionID)]);
    if (my !== epoch) return;
    payloadbarRef.value?.setTree(buildCompactTree(msgs as any, sums as any));
  } catch {
    if (my === epoch) payloadbarRef.value?.setTree(null);
  }
}
// 历史预演（载荷 tab 的"这一发会发什么"）：**只在真的看载荷时才问**。
// 从前 enterSession 无条件 `POST /outgoing` —— 服务端要列消息、现演世界状态、拼整段出站，
// 换会话/刷新页面时这是最重的一发，而人可能根本没打开工作区。现在改成按需。
let historyPreviewSeq = 0;
async function refreshHistoryPreview() {
  // 载荷 tab 是用户**显式点开**的上下文；预览开关是载荷自己的（2026-11-04 加），
  // 与输入框的 previewOn 分开。草稿 / 无会话 / 关闭预览都清空、不发请求。
  if (draft || !sessionID || !payloadPreviewOn()) { payloadbarRef.value?.setWire(null); return; }
  const sid = sessionID;
  const my = epoch;
  const seq = ++historyPreviewSeq;
  try {
    const w = await api.outgoingPreview(sid, "");
    if (my === epoch && seq === historyPreviewSeq) payloadbarRef.value?.setWire(w);
  } catch (err) {
    if (my === epoch && seq === historyPreviewSeq) toast(`预演失败：${err.message}`, "error");
  }
}
// 工作区按当前 tab 懒加载：切进上下文 / 换会话时只拉"此刻真要看的那一页"。
function refreshContextLazy() {
  if (workspace.value !== "context") return;
  if (payloadTab === "tree") refreshTree();
  else if (payloadTab === "state") refreshStateView(sessionID);
  else if (payloadTab === "payload") refreshHistoryPreview();
}
const lastMsgId = computed(() => [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId ?? null);
// 最新 AI 回复对应的用户输入（A-B-C-D-E-F ⇒ E）——“编辑最新 Prompt”的入口目标。
const latestPromptId = computed(() => latestPromptFor(vueMessages.value));
// 渲染行：气泡（带编号）+ 被顶层摘要折起来的卡（口径见 utils/chatrows.ts）。
// 当前 agent 的"首段对话"（`first_message`，2026-10-11 用户定）—— **computed 只看响应式的两格**
// （vueAgents + sessionInfo.agent_id）。⚠ 别把它写成"先看普通变量 draft 再早退"：早退分支不碰任何
// 响应式依赖 ⇒ computed 没依赖、缓存住空串**永不重算**（踩过）。
const agentGreeting = computed(() => {
  const a = vueAgents.value.find((x) => x.id === sessionInfo.agent_id);
  return (a?.first_message ?? "").trim();
});
// 空草稿首屏的两格（2026-11-02）：当前助手名 + 会话模型。
// 模型显示上游 id（顶栏那份是 prettify 后的名字）—— 草稿还没有模型列表可查，
// 而这一行是"我马上要跟谁说话"，宁可给原始 id 也不留空。
const draftAgentName = computed(() => agentLabelOf(sessionInfo.agent_id) || "默认助手");
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
function onBubbleEdit(mid, kind) {
  const m = vueMessages.value.find((x) => x.messageId === mid || x.id === mid);
  if (!m) { toast("那条消息已经不在了（多半被删了）：重进会话看看", "error"); return; }
  openMessageEditor(m, kind);
}
function onBubbleEditPrompt(mid) { onBubbleEdit(mid, "content"); }
// deleteMessagesFlow：消息级删除的唯一出口（气泡"删"与 /cut 共用）——预览 → 确认 → 执行；
// 执行时 409（conflict：末尾变了，别人删过/改过）⇒ **重新预览再确认**（最多三轮），
// 不是干弹一个错（AGENTS 的口径：核对字段不符 ⇒ 409 ⇒ 重新预览）。
async function deleteMessagesFlow(sid, mid, confirmTextFor) {
  for (let i = 0; i < 3; i++) {
    const plan = await api.deletionPreview(sid, mid).catch((err) => {
      toast(`删除预览失败：${err.message}`, "error");
      return null;
    });
    if (!plan || sid !== sessionID) return false;
    if (!(await overlaysRef.value.confirmAsk(confirmTextFor(plan)))) return false;
    try {
      await api.deleteMessages(sid, mid, plan.last_deleted_message_id);
      localRev++; // 删完消息列已经变了（等这一发重新拉窗口）
    } catch (err) {
      if (err.code === "conflict" && i < 2) { toast("末尾变了：重新预览一眼再删", "error"); continue; }
      toast(`删除失败：${err.message}`, "error");
      return false;
    }
    return true;
  }
  return false;
}
async function onBubbleDelete(mid, isLast) {
  if (!mid) return;
  const sid = sessionID;
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
const rerollState = ref<any>({ active: false });
const summaryReroll = ref<any>({ active: false }); // 摘要重摇（另一族；与消息族共用同一把闸 —— 进错族 409）
const summaryEditTarget = ref<any>(null); // 编辑中的摘要（SummaryEditor 模态）
const messageEditTarget = ref<any>(null); // 编辑中的消息（MessageEditor 模态）
// 候选流预览（就地显示在目标气泡里）：running 期间靠 `/turn/text` 攒；任何一次落地/切版都清掉。
const rerollPreview = ref({ text: "", reasoning: "" });
function clearRerollPreview() { rerollPreview.value = { text: "", reasoning: "" }; }
// rerollStarting：**刚点下 ⟳ 的那一刻**（状态还没回来）就记下目标 —— 让那条气泡立刻切成空白 +
// 「正在输出…」（2026-10-17 用户定：旧正文一直挂到第一帧才换掉很难看）。状态回来后交给 rerollState。
const rerollStarting = ref(null);
// 判据都在 utils/chatrows.ts（纯函数、契约测试钉着）：这条是不是"正在摇的那条"、该显示什么。
const isLive = (m) => rerollTargetLive(m, { startingId: rerollStarting.value, state: rerollState.value });
function bubbleContent(m) {
  return bubbleText(m, { field: "content", live: isLive(m), streamText: streamDisplay.value.text, previewText: streamDisplay.value.text });
}
function bubbleReasoning(m) {
  // 思考多一层回落：库里没思考、合成流也没有 ⇒ 照旧用 m.reasoning（与从前一致）。
  return bubbleText(m, { field: "reasoning", live: isLive(m), streamText: streamDisplay.value.reasoning, previewText: streamDisplay.value.reasoning });
}
// rowDuration：消息行的 `duration_ms` 收窄成气泡脚注要的 number|null。
// ChatMessage 的索引签名是 unknown，而 `Number(undefined/null)` 不是"0 秒"——那会让速度公式
// 除出无穷大；所以只放行真的大于 0 的有限数。
function rowDuration(m) {
  const n = Number(m?.durationMs);
  return Number.isFinite(n) && n > 0 ? n : null;
}

// refreshReroll：问一次重摇状态（进会话 / 一轮结束 / 删改之后都要对齐 —— 候选只在内存里，
// 发新消息、删目标、切会话都会让它自然消失）。
async function refreshReroll() {
  if (!sessionID) { rerollState.value = { active: false }; clearRerollPreview(); return; }
  const my = epoch;
  try {
    const st = await api.rerollState(sessionID);
    if (my !== epoch) return;
    const active = st?.active && st.target_kind === "message";
    rerollState.value = active ? st : { active: false };
    if (!active) clearRerollPreview();
  } catch {
    if (my !== epoch) return;
    rerollState.value = { active: false };
    clearRerollPreview();
  }
}
// 摘要族的状态读取（两族各自一个 GET；形状同）。
async function refreshRerollSummary() {
  if (!sessionID) { summaryReroll.value = { active: false }; return; }
  const my = epoch;
  try {
    const st = await api.rerollSummaryState(sessionID);
    if (my !== epoch) return;
    summaryReroll.value = st?.active && st.target_kind === "summary" ? st : { active: false };
  } catch {
    if (my === epoch) summaryReroll.value = { active: false };
  }
}
// 两族一起对齐（切会话/一轮结束/删改之后都要）。
function refreshRerolls() {
  return Promise.all([refreshReroll(), refreshRerollSummary()]).catch(() => {});
}
// 摘要刷新（卡上 ⟳）：进摘要重摇模式并摇一版 —— **当前那版先占第 1 位**（不丢），完了切到最新那版。
// 生成走 compact 的"只生成、不落库"半程（非流式）⇒ 没有流式预览，卡片头标"刷新中…"。
async function summaryRefresh(sumId) {
  const sid = sessionID;
  const seg = vueSegments.value.find((x) => x.kind === "summary" && x.id === sumId);
  if (!sid || !seg) return;
  if (summaryReroll.value.active && summaryReroll.value.running) { toast("摘要还在刷新"); return; }
  // 与 `rerollNow` 同一个口径：只有"真的多出一版"才切（被按停 ⇒ 不切）。
  const versionsBefore = summaryReroll.value?.count ?? 0;
  try {
    await api.rerollSummaryEnter(sid, seg.b); // idx = 段起点（同树任意一条都上溯到同一个根）
    refreshTasks();
  } catch (err) {
    toast(`刷新失败：${err.message}`, "error");
    return;
  }
  const my = epoch;
  const st = await pollRerollSummary(sid);
  if (my !== epoch) return;
  if (st?.error) { toast(`刷新失败：${st.error}`, "error"); return; }
  if (!st?.active) return;
  if (st.count <= versionsBefore) { refreshRerollSummary(); return; } // 被按停：不切（同上一条）
  await summarySwitch(st.count); // 最新那版的位次就是 count（1-based，与气泡同一套）
}
async function pollRerollSummary(sid) {
  const my = epoch;
  let st = null;
  for (let i = 0; i < 450; i++) {
    await new Promise((r) => setTimeout(r, 400));
    if (my !== epoch) return null;
    st = await api.rerollSummaryState(sid).catch(() => null);
    if (!st || my !== epoch) return null;
    // 与消息族同一个坑，同一条改法（见 `pollReroll` 上面那条注释）。
    summaryReroll.value = st.active && st.target_kind === "summary" ? st : { active: false };
    refreshStatusline(st.running ? "摘要刷新中" : "idle", undefined, { withContext: !st.running });
    if (!st.running) return st;
  }
  return st;
}
async function summarySwitch(idx) {
  const my = epoch;
  try {
    await api.rerollSummarySwitch(sessionID, idx);
  } catch (err) {
    toast(`切版失败：${err.message}`, "error");
    return;
  }
  if (my !== epoch) return;
  await reloadWindow();
  refreshStateView();
  await refreshRerollSummary();
}
// ◀ ▶ 翻版；最右的 ▶ 再按 = 再摇一版（与气泡同一套语义）。
async function summaryVersion(delta) {
  const st = summaryReroll.value;
  if (!st?.active || st.running) return;
  const to = st.current_idx + delta;
  if (delta > 0 && to > st.count) { await summaryRefresh(st.target_summary_id); return; }
  if (delta < 0 && to < 1) { toast("已经是第一版了"); return; }
  await summarySwitch(to);
}
function openSummaryEdit(sum) {
  if (sum?.id) summaryEditTarget.value = sum;
}
async function onSummaryEdited() {
  summaryEditTarget.value = null;
  await reloadWindow();
  if (treeVisible) refreshTree();
  toast("摘要已改", "ok");
}
async function onMessageEdited(result: any = {}) {
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
    // 先保存、再摇：保存已经落库，随后走正常重摇路径（旧正文立刻清空）。
    // 编辑尾条 user 时，重摇的是它后面那条 assistant，不是这条 user 本身。
    await rerollNow(target.rerollTargetId);
    return;
  }
  toast("消息已保存", "ok");
}
// pollReroll：等这一摇落地（顺带把状态行挂上"重摇中"，并把候选流攒进预览）。
async function pollReroll(sid) {
  const my = epoch;
  let st = null;
  let cur = { from: 0, thinkFrom: 0 };
  for (let i = 0; i < 450; i++) {
    await new Promise((r) => setTimeout(r, 400));
    if (my !== epoch) return null;
    st = await api.rerollState(sid).catch(() => null);
    if (!st || my !== epoch) return null;
    // ⚠ **无条件写回**，不是"活着才写"（2026-11-01 修的 bug：从任务面板按停之后，
    // 对话界面一直"在等待"）。原来写的是 `if (st.active && ...) rerollState.value = st`
    // ⇒ 状态变成"没了"时**一个字都不写**，`rerollState` 就永远停在旧的 `running:true` 上
    // ⇒ `rerollTargetLive` 一直为真 ⇒ 气泡一直是空白 +"正在输出"，直到下一次刷新才恢复。
    // 这与 `refreshReroll` 同一条口径：**不活跃就显式写回 inactive**。
    rerollState.value = st.active && st.target_kind === "message" ? st : { active: false };
    if (st.running) {
      // 候选流：与一轮的游标读同一套（读不消费；token 对不上后端自然给空）。
      const slice = await api.turnText(sid, cur.from, cur.thinkFrom).catch(() => null);
      if (slice && my === epoch) {
        const merged = mergeStreamSlice(
          { text: rerollPreview.value.text, reasoning: rerollPreview.value.reasoning }, cur, slice);
        rerollPreview.value = { text: merged.text, reasoning: merged.reasoning };
        setStreamTarget(merged.text, merged.reasoning);
        cur = merged.cur;
      }
    }
    refreshStatusline(st.running ? "重摇中" : "idle", undefined, { withContext: !st.running });
    if (!st.running) return st;
  }
  return st;
}
// rerollNow：⟳ / `/reroll` —— 摇一版（没进模式就先进），摇完**切到最新那版**。
async function rerollNow(targetId = null) {
  const sid = sessionID;
  if (!sid) return;
  const my = epoch;
  clearRerollPreview();
  resetTypewriter();
  // 立刻把**被点的那条**切空白（别等第一帧）：优先用气泡报上来的 id，
  // 命令 / 版本导航没有 id 时回落到尾条 assistant（后端也只认它）。
  const target = targetId || lastMsgId.value;
  if (!target) return;
  rerollStarting.value = target;
  // 摇之前有几版 —— 摇完只有"真的多出来一版"才切过去。
  //
  // 为什么不能直接 `switchReroll(st.count)`（2026-11-01 修的第二个洞）：从任务面板**按停**之后
  // 候选列表还在（之前摇过的版本还在），`st.count` 没变，于是那句会把正文切到最后**已经存在**的那版上
  // —— 用户按的是"取消"，界面却换了一版内容。那就是"取消"干了实事，正是要避免的。
  const versionsBefore = rerollState.value?.count ?? 0;
  try {
    try {
      await api.rerollEnter(sid); // 202：进模式并立刻摇一版；已在模式里 ⇒ 再摇一版
      refreshTasks();
    } catch (err) {
      toast(`重摇失败：${err.message}`, "error");
      return;
    }
    const st = await pollReroll(sid);
    if (my !== epoch) return;
    if (st?.error) { toast(`重摇失败：${st.error}`, "error"); clearRerollPreview(); return; }
    if (!st?.active) return; // 连模式都退了（取消 / 只剩原文一版）
    if (st.count <= versionsBefore) {
      // 没多出新的一版 ⇒ 这一趟是被按停的（或者没摇成）：**什么都不切**。
      refreshReroll();
      return;
    }
    await waitTypewriter(); // 候选最后一块先放完，再切到库里那版（不然会跳一下）
    await switchReroll(sid, st.count); // 最新那版的位次就是 count（1-based）
  } finally {
    // 走到哪儿都别留下"永远在摇"的假象（出错、被停、切会话、epoch 变了都算）。
    rerollStarting.value = null;
  }
}
// rerollGo：◀ ▶ —— 翻版；最右再按 = 再摇一版（与 TUI/TG 的 `‹ 2/3 ›` 同一套语义）。
async function rerollGo(delta) {
  const sid = sessionID;
  const st = rerollState.value;
  if (!sid || !st?.active || st.running) return;
  const to = st.current_idx + delta;
  if (delta > 0 && to > st.count) { await rerollNow(); return; }
  if (delta < 0 && to < 1) { toast("已经是第一版了"); return; }
  await switchReroll(sid, to);
}
// switchReroll：就地换正文（UUID 不变），然后对齐列表与状态。
async function switchReroll(sid, idx) {
  const my = epoch;
  try {
    await api.rerollSwitch(sid, idx);
  } catch (err) {
    toast(`切版失败：${err.message}`, "error");
    return;
  }
  if (my !== epoch) return;
  await reloadWindow();
  // 两件事必须**同一拍**做（中间不能有 await）：预览一清、库内正文也已经是新那版 ⇒ 气泡无缝换过去；
  // `rerollStarting` 若拖到 rerollNow 的 finally 才清，中间会闪一帧空白（预览空了却还判"在摇"）。
  clearRerollPreview();
  rerollStarting.value = null;
  refreshStateView(sid);
  refreshStatusline("idle");
  await refreshRerolls();
}

// compactNow：受理一次压缩（命令面板与右栏压缩面板共用一条路）；`blocks` 空 = 用配置默认。
async function compactNow(blocks) {
  const sid = sessionID;
  if (!sid) { toast("先发一句话：草稿里还没有会话可压"); return; }
  try {
    const accepted = await api.compact(sid, blocks);
    localRev++; // 压缩会改摘要/段结构：在飞的后台对账别把旧结构盖回去
    refreshTasks();
    toast(`压缩已受理${blocks ? `（${blocks} 块）` : ""}`, "ok");
    pollCompact(sid, accepted?.at_ms).catch(() => {});
  } catch (err) {
    toast(`压缩失败：${err.message}`, "error");
  }
}

// undoSummary：**撤销一次压缩**（2026-11-01 加，2026-11-05 支持级联）。
//
// 顶层摘要都可撤；撤更早的一条会连同**同层更晚的压缩**一起删。先问后端要 preview，
// 超过一条就摊开警告（哪些区间会一起没），绝不静默删一串。
async function undoSummary(summaryID) {
  const sid = sessionID;
  if (!sid || !summaryID) return;
  const my = epoch;
  let preview;
  try {
    preview = await api.undoPreview(sid, summaryID);
  } catch (err) {
    if (my === epoch) toast(`撤销失败：${err.message}`, "error");
    return;
  }
  const items = preview.items ?? [];
  if (preview.count > 1) {
    const ranges = items.map((item) => `第 ${item.from_idx}–${item.to_idx} 条`).join("、");
    const ok = await overlaysRef.value.confirmAsk(
      `撤销这次压缩会同时撤销同层更晚的 ${preview.count - 1} 次压缩：${ranges}。` +
      `这些摘要会被删除，对应内容回到原文，不可逆。继续？`,
    );
    if (!ok) return;
  }
  if (my !== epoch) return;
  try {
    await api.undoSummary(sid, summaryID);
  } catch (err) {
    if (my === epoch) toast(`撤销失败：${err.message}`, "error");
    return;
  }
  if (my !== epoch) return;
  toast(preview.count > 1 ? `已撤销 ${preview.count} 次压缩` : "已撤销这次压缩", "ok");
  // 装配会**变长**（那些段回到原文）⇒ 消息列、状态、环上的占用都要重算一遍。
  await reloadWindow();
  refreshStateView(sid);
  refreshStatusline("idle");
  if (treeVisible) refreshTree();
}
// pollCompact：盯一次压缩到收尾（拿受理时的 `at_ms` 对上这一发 —— 状态位是**持久**的，
// 不认准 at_ms 会被上一次的旧结局骗），完了刷新消息列（装配变短）与压缩树。
async function pollCompact(sid, atMS) {
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
      if (liveTurnPoll === 0) {
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

// resumeReroll：重摇那两条轮询也可能在后台断掉 —— 回来后接着等它（口径与 `rerollNow` 一致：
// 只有**真的多出一版**才切，取消不换版）。
async function resumeRerollIfLive() {
  if (!sessionID) return;
  const messageLive = rerollState.value?.active && rerollState.value?.running;
  const summaryLive = summaryReroll.value?.active && summaryReroll.value?.running;
  if (!messageLive && !summaryLive) return;
  const my = epoch;
  if (messageLive) {
    const before = rerollState.value?.count ?? 0;
    const st = await pollReroll(sessionID).catch(() => null);
    if (my !== epoch || !st) return;
    if (st.active && st.count > before) await switchReroll(sessionID, st.count);
    else refreshReroll();
    return;
  }
  const before = summaryReroll.value?.count ?? 0;
  const st = await pollRerollSummary(sessionID).catch(() => null);
  if (my !== epoch || !st) return;
  if (st.active && st.count > before) await summarySwitch(st.count);
  else refreshRerollSummary();
}

async function askDeleteSession(id) {
  // 高延迟下别为了一个标题先等一个 RTT：左栏缓存里就有它。
  let title = sessionsCache.value.find((s) => s.id === id)?.title;
  if (title === undefined) {
    const list = await api.listSessions().catch(() => []);
    title = list.find((s) => s.id === id)?.title;
  }
  const name = sessionTitle(title);
  if (!(await overlaysRef.value.confirmAsk(`删除会话「${name}」？不可逆。`))) return;
  try {
    await api.deleteSession(id);
  } catch (err) {
    toast(`关闭失败：${err.message}`, "error");
    return; // 删失败就别切会话（catch 回调里的 return 不是这个函数的 return）
  }
  forgetWindow(windowCache, id);
  if (id === sessionID) {
    backToChat(); // 被删的是当前会话 ⇒ 接下来要去草稿：**两个视图都让位**（哪边开着都回对话区）
    await startDraft(); // 删掉当前会话 ⇒ 回到草稿（不再预建空会话 —— 那正是被拿掉的旧行为）
  } else refreshSessionsBar();
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
  Object.assign(draftPrefs, { provider: "", model: "", agent: "", thinking_level: "" });
  // `id: ""` 是必须的：`Object.assign` **不清旧键**，从一条真会话切到草稿时
  // `sessionInfo.id` 会留着上一条的 id —— 于是"这是草稿吗"就答错了（曾经就没人读它，
  // 所以一直没露；上下文工作区要读它才露出来）。
  Object.assign(sessionInfo, {
    id: "", title: "", provider: "", model: "", agent_id: "", thinking_level: "", system_prompt: "",
  });
  paintSessionHeader();
  const d = await api.getDefaults().catch(() => ({} as any));
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
  payloadbarRef.value?.setCompactWire(null); // 上一发的压缩载荷不许挂在别的会话上
  refreshStatusline("idle");
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
  startDraft().catch((err) => toast(`新建失败：${err.message}`, "error"));
}
function renderSessions(sessions) {
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
  } catch (err) { toast(`刷会话栏失败：${err.message}`, "error"); }
  finally {
    sessionsRefreshing.value = false;
    sessionsLoading.value = false;
  }
}
async function enterSession(id, preloaded = null) {
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
  let lastErr = null;
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
    toast(`连不上后端（${String(lastErr?.message ?? lastErr)}）。检查它起没起、地址对不对。`, "error");
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

// —— 发送 / 轮询 ——
async function send(content) {
  const sid = sessionID;
  // 乐观上屏：远端 1s+ 时，等 202 再画那句会让人以为"按了没反应"。
  const optimistic = addMessage("你", content, "user");
  const live = startSendingBubble();
  localRev++; // 消息列已经变了：在飞的后台对账不许拿旧快照覆盖它
  let accepted;
  pendingSends++;
  try {
    accepted = await api.sendMessage(sid, content);
  } catch (err) {
    dropOptimistic(optimistic);
    dropOptimistic(live);
    composerRef.value?.setText(content); // 发失败别把那句话吞掉
    throw err;
  } finally {
    pendingSends--;
  }
  settleOptimisticUser(optimistic, accepted?.user);
  live.phase = "waiting";
  localRev++; // 服务端那条已经落库：在飞的对账（可能早于落库取的快照）到此为止
  refreshTasks();
  await pollTurn(sid, accepted.turn.message_id, live);
}
async function resend() {
  const sid = sessionID;
  const live = startSendingBubble();
  let accepted;
  try {
    accepted = await api.resend(sid);
  } catch (err) {
    dropOptimistic(live);
    throw err;
  }
  live.phase = "waiting";
  refreshTasks();
  await pollTurn(sid, accepted.turn.message_id, live);
}
// liveTurnPoll：**本地此刻有几条轮询循环真的在跑**（0 或 1）。
//
// 为什么需要这个数，而不是"看有没有 stream 气泡"：气泡是**一次性的**，循环死掉（网络错、
// 后台被冻住之后异常退出）而气泡还留在列表里 —— 那个气泡就是僵住的假象。判"要不要接回来"
// 必须看循环本身在不在。
let liveTurnPoll = 0;
// 同一个会话只允许一个轮询循环（2026-11-03）：前台对齐/重进会话/发送三路都可能喊 pollTurn，
// 两个循环各带各的游标写同一个 streamDisplay ⇒ 字符会重复/跳序。这里是一把**前端单飞闸**。
const activeTurnPolls = new Set();

// applyTurnStream：把一次 status+slice 并进显示与打字机；返回新游标。
function applyTurnStream(st, slice, cur, row) {
  const merged = mergeStreamSlice(
    { text: vueStream.value.text ?? "", reasoning: vueStream.value.reasoning ?? "" }, cur, slice);
  vueStream.value.text = merged.text;
  vueStream.value.reasoning = merged.reasoning;
  setStreamTarget(merged.text, merged.reasoning);
  row.reasoning = merged.reasoning;
  refreshStatusline(st.phase, st.elapsed_ms, { withContext: false });
  return merged.cur;
}

// finishTurnStream：一轮落地后的共同收尾（SSE / 轮询回退都走这一处）。
async function finishTurnStream(sid, st) {
  if (sessionID !== sid) return;
  const failed = st.phase === "error";
  await waitTypewriter();
  if (sessionID !== sid) return;
  await reloadWindowKeepingScroll();
  resetTypewriter();
  if (sessionID !== sid) return;
  if (failed) {
    const errText = `失败：${st.error || "未知"}`;
    vueMessages.value.push({ who: "助手", role: "assistant", content: errText, id: tmpKey(), noIdx: true });
    scrollBottom();
    toast(errText, "error");
  }
  refreshStatusline(st.phase, st.elapsed_ms);
  refreshSessionsBar();
  refreshStateView(sid);
  refreshRerolls();
  if (previewOn()) {
    api.outgoingPreview(sid, "").then((w) => payloadbarRef.value?.setWire(w))
      .catch((err) => toast(`预演失败：${err.message}`, "error"));
  }
}

// pollTurnEvents：优先走 SSE（远程/CF 少每拍一个 RTT）。返回最终 status；null = 已换代/旧轮次，别回退。
// 连接建了但 8s 一个事件都没有 ⇒ 抛错，让 pollTurn 回退到 HTTP 轮询（CF 缓冲/旧代理的兜底）。
async function pollTurnEvents(sid, turnID, my, row) {
  let cur = { from: 0, thinkFrom: 0 };
  let es = null;
  try {
    return await new Promise((resolve, reject) => {
      let got = false;
      const watchdog = setTimeout(() => {
        if (got) return;
        es?.close();
        reject(new Error("SSE 连接超时"));
      }, 8000);
      es = new EventSource(api.turnEventsUrl(sid, 0, 0));
      es.onmessage = (event) => {
        got = true;
        clearTimeout(watchdog);
        if (my !== epoch) { es.close(); resolve(null); return; }
        let poll;
        try { poll = JSON.parse(event.data); } catch { return; }
        const st = poll.status;
        const slice = poll.slice;
        if (!st || !slice) return;
        // 迟到的旧连接拿到的是新一轮：立刻收手，别把新的字写进旧的显示前缀。
        if (turnID && st.message_id && st.message_id !== turnID && st.phase !== "idle") {
          es.close(); resolve(null); return;
        }
        cur = applyTurnStream(st, slice, cur, row);
        if (st.phase === "idle" || st.phase === "error") {
          es.close(); resolve(st);
        }
      };
      es.onerror = () => {
        // EventSource 自己会带 Last-Event-ID 重连；这里不关它。
        // 如果从未收到过事件，watchdog 会兜底切 HTTP。
      };
    });
  } finally {
    es?.close();
  }
}

// pollTurnHTTP：SSE 不可用时的回退（也是原来那套 status+增量轮询）。
async function pollTurnHTTP(sid, turnID, my, row) {
  let cur = { from: 0, thinkFrom: 0 };
  let misses = 0;
  let delay = 200;
  for (;;) {
    await new Promise((r) => setTimeout(r, delay));
    if (my !== epoch) return;
    let poll;
    let st;
    let slice;
    const requestAt = performance.now();
    try {
      poll = await api.turnPoll(sid, cur.from, cur.thinkFrom);
      st = poll.status;
      slice = poll.slice;
      if (turnID && st.message_id && st.message_id !== turnID && st.phase !== "idle") return;
      misses = 0;
    } catch (err) {
      if (++misses > 10) throw err;
      delay = 200;
      continue;
    }
    delay = Math.max(80, 300 - (performance.now() - requestAt));
    if (my !== epoch) return;
    cur = applyTurnStream(st, slice, cur, row);
    if (st.phase === "idle" || st.phase === "error") {
      await finishTurnStream(sid, st);
      return;
    }
  }
}

async function pollTurn(sid, turnID, existing = null) {
  if (activeTurnPolls.has(sid)) return;
  activeTurnPolls.add(sid);
  const my = epoch;
  liveTurnPoll++;
  const m = existing && vueMessages.value.includes(existing)
    ? existing
    : { who: "助手", role: "assistant", _stream: true, stream: true, content: "", reasoning: "", id: tmpKey() };
  if (!m.stream) m.stream = true;
  m.phase = "waiting";
  if (!vueMessages.value.includes(m)) vueMessages.value.push(m);
  vueStream.value = { id: turnID, text: "", reasoning: "" };
  resetTypewriter();
  composerRef.value?.setRunning(true);
  refreshStatusline("pending");
  try {
    let finalStatus = null;
    if (typeof EventSource === "function") {
      try {
        finalStatus = await pollTurnEvents(sid, turnID, my, m);
      } catch {
        finalStatus = undefined; // 明确：SSE 没起来，走下面的 HTTP 回退
      }
      if (finalStatus === null) return; // 世代/旧轮次：已收手，不回退
    }
    if (finalStatus) {
      await finishTurnStream(sid, finalStatus);
    } else {
      await pollTurnHTTP(sid, turnID, my, m);
    }
  } finally {
    activeTurnPolls.delete(sid);
    liveTurnPoll--;
    composerRef.value?.setRunning(false);
  }
}
let connText = "未连接";
// 左栏「选项」浮板的两件动作（2026-11-04 从会话设置面板挪过来）：
// 复制成功后仍走 afterCopy（进副本）；导出仍用同一个 API + downloadJSON。
async function copySessionFromBar(id) {
  try {
    const fresh = await api.copySession(id);
    afterCopy(fresh);
  } catch (err) {
    toast(`复制失败：${err.message}`, "error");
  }
}
async function exportSessionFromBar(id) {
  try {
    const doc = await api.exportSession(id);
    downloadJSON(doc, `${doc.title || "新会话"}.json`);
    toast(`已导出 ${doc.messages?.length ?? 0} 条`, "ok");
  } catch (err) {
    toast(`导出失败：${err.message}`, "error");
  }
}

// afterCopy：**创建副本**之后（2026-11-01）—— 刷左栏让它出现，然后直接进副本。
//
// 为什么刷了左栏还要再进一次：副本是"接着在这儿往下写"的东西，
// 停在原会话里会让人以为点了没反应（副本在左栏、视线还在浮板上）。
function afterCopy(fresh) {
  if (!fresh?.id) { toast("复制完成了，但没拿到新会话 id", "error"); return; }
  toast(`已创建副本「${fresh.title || "未命名"}」`, "ok");
  refreshSessionsBar();
  enterSession(fresh.id).catch((err) => toast(`进副本失败：${err.message}`, "error"));
}
// refreshStatusline：连接 / 助手 / 相位 / 上下文 —— 全都喂给右上角那枚环（2026-10-28 用户定）。
//
// 这四样从前是一行小字（"已连接 v0.1.0｜上下文 344/1.04M｜默认助手｜idle"），紧贴着对话；
// 现在共用一处落点：环里是占比，点开的浮板里是明细（含连接与相位）。
// ⚠ 别再造第二条"显示状态行"的路 —— 同一个数字住两处，只会让两处都可能过期。
// withContext：这一拍要不要**真去问** `/context`（它是 O(n) 的全量装配：列消息 + 现演状态 + 拼出站）。
// 生成 / 重摇 / 摘要刷新这些轮询每秒要问好几次，而占用在一轮落地前根本不变 ⇒ 轮询里传 false，
// 只在翻 idle 那一拍（以及进会话/换模型这些"事情真的变了"的时刻）按默认 true 问一次。
// 少掉的是请求与装配，不是显示：环在轮询期间保持上一拍的数（那正是此刻真实的占用）。
function refreshStatusline(phase, elapsedMs = null, { withContext = true } = {}) {
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
  }).catch((err) => toast(`上下文占用读取失败：${err.message}`, "error"));
}
function refreshStateView(id = null) {
  if (draft || !(id ?? sessionID)) return; // 草稿：没有会话可问状态（右栏保持空话）
  const my = epoch;
  api.sessionState(id ?? sessionID).then((v) => { if (my === epoch) payloadbarRef.value?.setTables(v?.tables); }).catch(() => {});
}

// —— 命令 ——
// 命令执行器：键与 commands.js 的 COMMAND_META **同一份命令表**（说明在表里、动作在这里；
// 启动时对账，两边对不上立刻 console.error，不静默）。“要真会话”由表上的 needsSession 决定。
const COMMAND_RUNNERS = {
  new: async () => {
    if (draft) { toast("已经在一张新对话里了"); return; }
    await startDraft();
  },
  resume: async () => {
    const list = await api.listSessions();
    overlaysRef.value?.showPicker(
      "选会话（resume）",
      list.map((s) => ({ label: `${s.title || "(无标题)"} [${s.messages}条]`, value: s.id })),
      (it) => enterSession(it.value),
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
    const my = epoch;
    await api.rerollSwitch(sessionID, Number(args[0] ?? 0));
    if (my !== epoch) return;
    await reloadWindow();
  },
  stop: async () => { await api.stop(sessionID); },
  resend: async () => { await resend(); },
  delete: async () => {
    if (!(await overlaysRef.value.confirmAsk(`删除整个会话 ${sessionID}？`))) return;
    await api.deleteSession(sessionID);
    await startDraft();
  },
  cut: async () => {
    const sid = sessionID;
    const messages = vueMessages.value; // 只列表里有的（想看更老的先"加载更多"）
    overlaysRef.value?.showPicker(
      "cut：选一条（删它及之后）",
      messages.filter((m) => m.idx > 0).map((m) => ({
        label: `#${m.idx} ${m.role} ${m.content.slice(0, 40)}`,
        value: m,
      })),
      async (it) => {
        const ok = await deleteMessagesFlow(sid, it.value.id,
          (plan) => `${deletionPlanSummary(plan)}，确认删 #${it.value.idx} 及之后？`);
        if (!ok || sid !== sessionID) return;
        await reloadWindow();
        refreshStateView(sid);
        refreshRerolls();
      },
    );
  },
  edit: async (args) => {
    const messages = vueMessages.value; // 只列表里有的（想看更老的先"加载更多"）
    const wantThink = (args[0] ?? "").toLowerCase().startsWith("思") || (args[1] ?? "").toLowerCase().startsWith("思");
    const n = Number(wantThink && isNaN(Number(args[0])) ? args[1] : args[0]);
    const one = (m) => openMessageEditor(m, wantThink ? "reasoning" : "content");
    if (Number.isInteger(n) && n > 0) {
      const m = messages.find((x) => x.idx === n);
      if (!m) { toast(`没有第 ${n} 条`, "error"); return; }
      await one(m);
      return;
    }
    overlaysRef.value?.showPicker(
      `edit：选一条（改${wantThink ? "思考" : "正文"}）`,
      messages.filter((m) => m.idx > 0).map((m) => ({
        label: `#${m.idx} ${m.role} ${(wantThink ? (m.reasoning ?? "") : m.content).slice(0, 40)}`,
        value: m,
      })),
      async (it) => { await one(it.value); },
    );
  },
  editsum: async () => {
    const items = await api.listSummaries(sessionID);
    if (!items.length) { toast("这条会话还没有摘要"); return; }
    overlaysRef.value?.showPicker(
      "editsum：选一条摘要（改它的正文）",
      items.map((s) => ({
        label: `${(s.text ?? "").slice(0, 40)}${s.dirty ? "（已过期）" : ""}`,
        value: s,
      })),
      async (it) => { openSummaryEdit(it.value); },
    );
  },
};
// 表里有、执行器没有 ⇒ 启动即报（命令表是面板与派发的唯一来源，漏了不许静默）。
for (const k of Object.keys(COMMAND_META)) {
  if (!COMMAND_RUNNERS[k]) console.error(`命令 /${k} 有表无执行器：COMMAND_META 与 COMMAND_RUNNERS 对不上`);
}
async function runCommand(name, args) {
  overlaysRef.value?.hidePalette();
  const meta = COMMAND_META[name];
  if (!meta) { toast(`未知命令 /${name}`); return; }
  if (draft && meta.needsSession) {
    toast(`这是新对话：先发一句话再用 /${name}`, "error");
    return;
  }
  await COMMAND_RUNNERS[name](args ?? []);
}

// —— 预演（右载荷栏）——
// 两条路同一支笔：会话里走 `POST /sessions/{id}/outgoing`；草稿走 `POST /outgoing`（临时会话，
// 带上草稿铸的 id —— 头上那个会话键就是将来真会话的）。previewSeq 丢弃迟到的那一版。
let previewTimer = 0;
let previewSeq = 0;
async function previewPayload(v) {
  const seq = ++previewSeq;
  const my = epoch;
  try {
    const out = draft
      ? await api.draftOutgoing({
        id: draftId, provider: draftPrefs.provider, model: draftPrefs.model,
        agent_id: draftPrefs.agent, thinking_level: draftPrefs.thinking_level || undefined, content: v,
      })
      : await api.outgoingPreview(sessionID, v);
    if (seq !== previewSeq || my !== epoch) return; // 有更新的一发/换了会话：旧的丢掉
    payloadbarRef.value?.setWire(out);
  } catch (err) {
    if (seq !== previewSeq || my !== epoch) return;
    if (!draft && err?.code === "invalid") return; // 空会话问历史（400 invalid）⇒ 保持空话
    toast(`预演失败：${err.message}`, "error");
  }
}
// refreshPreview：拿输入框里现有的字**立刻**重跑（改模型/Agent、回草稿之后用）——空字/命令不跑。
function refreshPreview() {
  if (!previewOn()) return;
  const v = composerRef.value?.text ?? "";
  if (!v.trim() || v.startsWith("/")) return;
  if (draft ? !draftId : !sessionID) return;
  previewPayload(v);
}
// previewCompactPayload：压缩那发**真会发出去什么**（右栏「看载荷」按钮）。
//
// 只算不发：后端与真压走同一段拼装（同一个 `wireFor`，合同测试逐字节钉着），
// 所以这里拿到的就是那一发请求本身 —— 与载荷 tab 同一套渲染器能画。
// 草稿态没有会话 ⇒ 没得压（后端那条口是会话域的），直接说清。
async function previewCompactPayload(n) {
  if (draft || !sessionID) {
    toast("这是新对话：先发一句话再预览压缩", "error");
    return;
  }
  try {
    payloadbarRef.value?.setCompactWire(await api.compactPayload(sessionID, n ?? null));
  } catch (err) {
    // 400 多半是"这会儿没得压"（凑不满块 / 能力关着）—— 原话照转，别自己编一句
    toast(`压缩载荷预演失败：${err.message}`, "error");
  }
}
function onComposerInput(v) {
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
function onComposerKeydown(e, value) {
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
        toast(`命令失败：${err.message}`, "error"),
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
async function sendFromDraft(text) {
  const body: any = {};
  if (draftId) body.id = draftId; // 预演头上那个 id —— 真会话必须落同一个
  if (draftPrefs.provider) body.provider = draftPrefs.provider;
  if (draftPrefs.model) body.model = draftPrefs.model;
  if (draftPrefs.agent) body.agent_id = draftPrefs.agent;
  if (draftPrefs.thinking_level) body.thinking_level = draftPrefs.thinking_level;
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
  api.stop(sessionID).catch((err) => toast(`停止失败：${err.message}`, "error"));
}
function onComposerSubmit(text) {
  liftOverlays();
  overlaysRef.value?.hidePalette();
  if (!text) {
    if (draft) { toast("新对话还没有可续写的历史：先写一句"); return; }
    resend().catch((err) => toast(`重发失败：${err.message}`, "error"));
    return;
  }
  const cmd = parseCommand(text);
  if (cmd) {
    runCommand(cmd.name, cmd.args).catch((err) => toast(`命令失败：${err.message}`, "error"));
    return;
  }
  if (draft) {
    sendFromDraft(text).catch((err) => toast(`发送失败：${err.message}`, "error"));
    return;
  }
  send(text).catch((err) => toast(`发送失败：${err.message}`, "error"));
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
  cmdMenu.value = commandMenuOn();
  if (!chromeHideOn()) resetChrome(document.querySelector("#messages-vue")); // 关掉这功能 ⇒ 栏立刻回来
  if (!cmdMenu.value) overlaysRef.value?.hidePalette();
}
// —— 拖拽调宽 ——
let uninstallViewportFit: (() => void) | null = null;
onMounted(() => {
  installSidePanels();
  startTasksPolling();
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
    toast(`启动失败：${err.message}`, "error");
  });
});
onUnmounted(() => {
  uninstallViewportFit?.();
  uninstallViewportFit = null;

  stopTasksPolling();
  stopUpdatePolling();
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
        @select="(id) => { closeLeftDrawer(); backToChat(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
        @settings="(id) => { sessionSettingsId = id; }"
        @copy="copySessionFromBar"
        @export="exportSessionFromBar"
        @close="askDeleteSession"
        @new="newSession"
        @new-image="() => toast('生图功能尚未接入', 'warn')"
        @open-settings="openSettings"
        @open-sessions="() => openManage('session')"
        @open-providers="() => openManage('provider')"
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
      @select-workspace="selectWorkspace"
      @toggle-left="toggleLeft"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${err.message}`, 'error'))"
      @select-agent="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, 'error'))"
      @cancel-queue="cancelQueue"
      @cancel-task="cancelTask"
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
            @click="loadMore().catch((err) => toast(`加载更多失败：${err.message}`, 'error'))"
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
              :usage="row.msg.usage"
              :reroll="row.msg.messageId && rerollState.active && rerollState.target_message_id === row.msg.messageId ? rerollState : null"
              @edit="onBubbleEdit"
              @edit-prompt="onBubbleEditPrompt"
              @delete="onBubbleDelete"
              @reroll="rerollNow"
              @reroll-go="rerollGo"
            />
          </template>
          </div>
        </main>
    <Composer
      ref="composerRef"
      :menu-on="cmdMenu"
      :thinking="currentThinking"
      :thinking-options="thinkingChoices"
      :native-tools="currentNativeTools"
      @select-thinking="setThinking"
      @input-text="onComposerInput"
      @keydown="onComposerKeydown"
      @submit="onComposerSubmit"
      @stop="stopCurrent"
      @menu="() => overlaysRef?.showPalette(filterCommands(''))"
    />
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
        :ask="askConfirm"
        :esc-blocked="!!sessionSettingsId"
        @close="closeSettings"
        @toggle-left="toggleLeft"
        @sessions-changed="refreshSessionsBar"
        @models-changed="fillModelSelect"
        @chat-saved="onChatSaved"
        @prefs-saved="onPrefsSaved"
        @goto-session="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
      />
      </div>
    </div>
    <Toast ref="toastRef" />
    <!-- 更新条：只在检测到新版本时出现（挂根层 —— 设置视图开着也该看得见）。 -->
    <!-- Overlays 挂根层（不在聊天视图里）：设置视图开着时"确认"也要能弹（fixed 定位照旧）。 -->
    <Overlays ref="overlaysRef" @run-command="(n) => runCommand(n, []).catch((err) => toast(`命令失败：${err.message}`, 'error'))" @pick="onPickAction" />
    <SummaryEditor
      v-if="summaryEditTarget"
      :session-id="sessionID"
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
      @close="messageEditTarget = null"
      @saved="onMessageEdited"
    />
    <SessionSettings
      v-if="sessionSettingsId"
      :session-id="sessionSettingsId"
      @close="sessionSettingsId = null"
      @renamed="() => { refreshSessionsBar(); if (sessionSettingsId === sessionID) enterSession(sessionID).catch(() => {}); sessionSettingsId = null; }"
    />
  </VApp>
</template>
