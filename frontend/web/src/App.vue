<script setup>
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from "vue";
import { mergeStreamSlice, probeAuthStatus } from "./api/client.js";
import { parseCommand, filterCommands, COMMAND_META } from "./utils/commands.js";
import { resetChrome as resetChromeState, stepChrome } from "./utils/chrome.js";
import { uuidv7 } from "./utils/ids.js";
import { buildCompactTree } from "./utils/tree.js";
import {
  buildChatRows, computeSegments, windowStart, prevWindowStart, alignWindowFrom, olderCount, runsToFetch,
  rerollTargetLive, bubbleText,
} from "./utils/chatrows.js";
import { applyBackground } from "./utils/background.js";
import { installViewportFit } from "./utils/viewport.js";
import { classifyAxis, pickDrag, dragProgress, resolveRelease } from "./utils/swipe.js";
import { sharedApi, previewOn, applyTheme, wantsSend, apiBase, apiToken, apiBaseCandidates, saveNetPrefs, lastSessionId, saveLastSession, commandMenuOn, applyComposerStyle, applyTextScale, chromeHideOn } from "./utils/prefs.js";
import {
  groupModelsByProvider,
  deletionPlanSummary,
  modelKey,
  parseModelKey,
  modelOption,
  agentOption,
  sessionTitle,
  taskLineText,
} from "./utils/format.js";
import TopBar from "./components/TopBar.vue";
import SessionBar from "./components/SessionBar.vue";
import PayloadBar from "./components/PayloadBar.vue";
import MessageBubble from "./components/MessageBubble.vue";
import SummaryCard from "./components/SummaryCard.vue";
import SummaryEditor from "./components/SummaryEditor.vue";
import ConnectGate from "./components/ConnectGate.vue";
import Composer from "./components/Composer.vue";
import Toast from "./components/Toast.vue";
import SettingsModal from "./components/SettingsModal.vue";
import UserView from "./components/UserView.vue";
import SessionSettings from "./components/SessionSettings.vue";
import Overlays from "./components/Overlays.vue";
import UpdateBar from "./components/UpdateBar.vue";
import { registerPWA, onUpdateReady, startUpdate } from "./utils/pwa.js";

const api = sharedApi();
applyTheme();
applyBackground();
// 输入栏形态（主题项，2026-10-20）：挂根节点的 `data-composer` —— CSS 认它，组件不用传 prop。
// 放在这里与 applyTheme 并列：**开机第一帧就得对**，否则会先闪一下浮空卡再变实心。
applyComposerStyle();
// 界面缩放（2026-10-28）：同样**开机第一帧就得对** —— 晚一帧就是"字先小一下再变大"的闪。
// 它写在根节点的 `style` 上（`--app-text-scale`），theme.css 的 body 字号吃这一格。
applyTextScale();
// —— 新版本提示（PWA 更新条，2026-10-20 用户定）——
// 从 `autoUpdate` 改成 `prompt`：新版本**等用户点**才切，而不是悄悄自己 reload。
// 勾子必须在**开机**就挂（SW 装完是异步回调 —— 挂晚了那一次提示就丢了）。
const updateRef = ref(null);
const updateReady = ref(false);
onUpdateReady(() => { updateReady.value = true; });
registerPWA();
// 点「刷新」⇒ 发 skipWaiting，新 SW 接管后由 vite-plugin-pwa 内建回调 reload 这一页。
function applyUpdate() { startUpdate(); }

// —— 组件 refs（直调实例方法，替代桥） ——
const toastRef = ref(null);
const topbarRef = ref(null);
const sessionbarRef = ref(null);
const payloadbarRef = ref(null);
const composerRef = ref(null);
const overlaysRef = ref(null);
// 命令菜单（输入框左侧 ☰ 与 "/" 浮层）：默认关（移动端碍事，2026-10-17 用户定）；
// 用户视图里改了立刻生效（不刷新）—— 偏好住 localStorage，这里是它的活视图。
const cmdMenu = ref(commandMenuOn());
const toast = (t, k) => toastRef.value?.push(t, k);
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
const showGate = ref(false); // 启动探不通后端 ⇒ 先给"连接"门板（见 boot）
const gateError = ref("");
let sessionID = null;
// 会话世代：进会话/回草稿就 +1。所有会话域的异步续写都在 await 后核对本世代，对不上就丢 ——
// 没有这道闸，快速切会话时迟到的 A 续写会覆掉已切到的 B 的抬头与消息列（实测复现过：
// 顶栏 A、高亮 B、气泡 A —— 界面被撕成三份）。
let epoch = 0;
const sessionInfo = reactive({});
// 草稿态（DeepSeek 网页版行为，2026-10-09 定）：＋/开机/`/new` 只开一张**客户端草稿**，库里没有行；
// 首条发送才 POST /sessions（带上草稿里选好的 provider/model/agent）。
// ⚠ HACK（刻意，全项目唯一的口子）：草稿**铸一枚 UUIDv7** 当将来那会话的 id —— 预演
// （POST /outgoing 临时会话）拿它进请求头，建会话时把同一个值交给 POST /sessions
// ⇒ 预演的那一发给真发的那一发逐字节一致。别扩散（消息/摘要的 id 仍是服务端铸）。
// 要会话的命令（compact/cut/reroll…）在草稿里明确拒绝（needsSession 标在 commands.js 的表上）。
let draft = false;
let draftPrefs = { provider: "", model: "", agent: "" };
let draftId = null;
const vueMessages = ref([]);
const vueSegments = ref([]); // 整条会话的段布局（computeSegments 的产出）：窗口/折叠/编号都认它
const vueStream = ref({ id: null, text: "", reasoning: "" });
// 消息窗口（分页 + 折叠；口径与全套算法在 utils/chatrows.js）：
// 只挂最后 N 段；更老的按"加载更多"一页一页往回；被摘要盖住的原文**展开才拉**（这里当缓存）。
const WINDOW_SEGMENTS = 20; // 首屏段数；"加载更多"一页也这么多
const windowFrom = ref(1); // 窗口起点（段对齐；1 = 已到最老）
const summaryMsgs = reactive(new Map()); // 摘要 id → {state:"loading"|"ready", rows}（只有展开过的才有）
const openSummaries = new Set(); // 哪些卡正展开着（reload 后补拉新鲜的）
const vueAgents = ref([]); // 生效 agent 列表（草稿首段的合成气泡看它；显示形态本身已无开关）
const settingsOpen = ref(false);
const userOpen = ref(false); // 用户视图（与设置平级：一个管登录者，一个管实例）
const currentUser = ref(null); // 当前登录者（boot 时问 /auth/me；用户视图也自己问一遍）
// **设置 = root 专属**（用户定，2026-10-10）：`is_admin` 才给进设置；其他用户的入口按钮也不给。
const isAdmin = computed(() => !!currentUser.value?.is_admin);
const compactDefault = ref(null); // chat.compact_blocks（压缩面板输入框的占位提示）
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
function addMessage(who, text, cls, think, messageId) {
  vueMessages.value.push({
    id: tmpKey(),
    who,
    role: cls === "user" ? "user" : "assistant",
    content: text,
    reasoning: typeof think === "string" ? think : think?.text,
    reasoningMs: think?.ms,
    messageId,
  });
  scrollBottom();
}
async function editMessageSave(messageId, kind, value) {
  await api.editMessage(sessionID, messageId, kind === "reasoning" ? { reasoning: value } : { content: value });
  await reloadWindow();
  refreshStateView();
}
async function editMessageFlow(messageId, kind) {
  const messages = await api.listMessages(sessionID);
  const target = messages.find((m) => m.id === messageId);
  if (!target) { toast("那条消息已经不在了（多半被删了）：重进会话看看", "error"); return; }
  const old = kind === "reasoning" ? (target.reasoning ?? "") : target.content;
  const next = window.prompt(kind === "reasoning" ? "改思考（空=清掉）" : "改正文", old);
  if (next === null) return;
  await editMessageSave(messageId, kind, next);
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
    messageId: m.id,
    id: m.id,
    // usage：气泡脚注用它（token + 成本）。上游没报就是 null ⇒ 脚注整行不渲染。
    // 归一化在后端（providers.NormalizeUsage），这里**只读**那几格。
    usage: m.usage ?? null,
  };
}

// loadWindow：把窗口 [windowFrom..尾] 的消息取回来 —— **只按消息段拉**（runsToFetch）：
// 被摘要盖住的 idx 一个字节都不取（想看原文 ⇒ 摘要卡展开时另拉）。
// reset ⇒ 重新按"最后 N 段"定窗口；否则保持起点（先对回段边界：压缩/删除会改结构）。
async function loadWindow({ reset = false, scroll = true } = {}) {
  const sid = sessionID;
  const my = epoch;
  if (!sid) return false;
  const [sums, tail] = await Promise.all([api.listSummaries(sid), api.listMessagesLast(sid, 1)]);
  if (my !== epoch) return false;
  // 总数 = 最后一条的 idx（拿"尾一行"问，比会话列表里的计数新鲜；空会话 = 0）。
  const total = tail.length ? tail[tail.length - 1].idx : 0;
  const segments = computeSegments(total, sums);
  windowFrom.value = reset
    ? windowStart(segments, WINDOW_SEGMENTS)
    : alignWindowFrom(segments, windowFrom.value, WINDOW_SEGMENTS);
  const runs = runsToFetch(segments, windowFrom.value, Infinity);
  const got = [];
  for (const [a, b] of runs) {
    const part = b === Infinity ? await api.listMessagesTail(sid, a) : await api.listMessagesRange(sid, a, b);
    got.push(...part);
  }
  if (my !== epoch) return false;
  vueSegments.value = segments;
  vueMessages.value = got.sort((x, y) => x.idx - y.idx).map(toRow);
  // 摘要缓存：卡没了的清掉；还开着的补拉新鲜的一份（结构或正文可能刚变过）。
  const alive = new Set(segments.filter((seg) => seg.kind === "summary").map((seg) => seg.id));
  for (const key of [...summaryMsgs.keys()]) if (!alive.has(key)) summaryMsgs.delete(key);
  for (const id of openSummaries) if (alive.has(id)) refreshSummaryMsgs(id);
  if (scroll) scrollBottom();
  if (treeVisible) refreshTree(); // 树看着就让它的全量账本跟着换
  return true;
}

// reloadWindow：把当前窗口按服务端最新状态重拉（一轮结束 / 编辑 / 删除 / 压缩 / 切版之后）。
async function reloadWindow({ scroll = true } = {}) {
  return loadWindow({ reset: false, scroll });
}

// loadMore：往回一页（WINDOW_SEGMENTS 段）。插入新行前记高度，插完把滚动位置补回去（锚住视野）。
async function loadMore() {
  const sid = sessionID;
  const my = epoch;
  const older = prevWindowStart(vueSegments.value, windowFrom.value, WINDOW_SEGMENTS);
  if (!sid || older >= windowFrom.value) return;
  const runs = runsToFetch(vueSegments.value, older, windowFrom.value - 1);
  const got = [];
  for (const [a, b] of runs) got.push(...await api.listMessagesRange(sid, a, b));
  if (my !== epoch) return;
  const el = document.getElementById("messages-vue");
  const beforeH = el ? el.scrollHeight : 0;
  const beforeTop = el ? el.scrollTop : 0;
  vueMessages.value = [...got.sort((x, y) => x.idx - y.idx).map(toRow), ...vueMessages.value];
  windowFrom.value = older;
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
// 树要的是整条账本（原文占比、嵌套），而消息列走的是窗口（见 utils/chatrows.js）；
// "树 tab 开着"就是用户显式说"给我看全量"。
let treeVisible = false;
function onPayloadTab(t) {
  treeVisible = t === "tree";
  if (treeVisible) refreshTree();
}
async function refreshTree() {
  if (!sessionID || !treeVisible) return;
  const my = epoch;
  try {
    const [msgs, sums] = await Promise.all([api.listMessages(sessionID), api.listSummaries(sessionID)]);
    if (my !== epoch) return;
    payloadbarRef.value?.setTree(buildCompactTree(msgs, sums));
  } catch {
    if (my === epoch) payloadbarRef.value?.setTree(null);
  }
}
const lastMsgId = computed(() => [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId ?? null);
// 渲染行：气泡（带编号）+ 被顶层摘要折起来的卡（口径见 utils/chatrows.js）。
// 当前 agent 的"首段对话"（`first_message`，2026-10-11 用户定）—— **computed 只看响应式的两格**
// （vueAgents + sessionInfo.agent_id）。⚠ 别把它写成"先看普通变量 draft 再早退"：早退分支不碰任何
// 响应式依赖 ⇒ computed 没依赖、缓存住空串**永不重算**（踩过）。
const agentGreeting = computed(() => {
  const a = vueAgents.value.find((x) => x.id === sessionInfo.agent_id);
  return (a?.first_message ?? "").trim();
});
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
function onBubbleSave(mid, kind, value) {
  editMessageSave(mid, kind, value).catch((err) => toast(`改消息失败：${err.message}`, "error"));
}
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
const rerollState = ref({ active: false });
const summaryReroll = ref({ active: false }); // 摘要重摇（另一族；与消息族共用同一把闸 —— 进错族 409）
const summaryEditTarget = ref(null); // 编辑中的摘要（SummaryEditor 模态）
// 候选流预览（就地显示在目标气泡里）：running 期间靠 `/turn/text` 攒；任何一次落地/切版都清掉。
const rerollPreview = ref({ text: "", reasoning: "" });
function clearRerollPreview() { rerollPreview.value = { text: "", reasoning: "" }; }
// rerollStarting：**刚点下 ⟳ 的那一刻**（状态还没回来）就记下目标 —— 让那条气泡立刻切成空白 +
// 「正在输出…」（2026-10-17 用户定：旧正文一直挂到第一帧才换掉很难看）。状态回来后交给 rerollState。
const rerollStarting = ref(null);
// 判据都在 utils/chatrows.js（纯函数、契约测试钉着）：这条是不是"正在摇的那条"、该显示什么。
const isLive = (m) => rerollTargetLive(m, { startingId: rerollStarting.value, state: rerollState.value });
function bubbleContent(m) {
  return bubbleText(m, { field: "content", live: isLive(m), streamText: vueStream.value.text, previewText: rerollPreview.value.text });
}
function bubbleReasoning(m) {
  // 思考多一层回落：库里没思考、合成流也没有 ⇒ 照旧用 m.reasoning（与从前一致）。
  return bubbleText(m, { field: "reasoning", live: isLive(m), streamText: vueStream.value.reasoning ?? m.reasoning, previewText: rerollPreview.value.reasoning });
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
    if (st.active && st.target_kind === "summary") summaryReroll.value = st;
    refreshStatusline(st.running ? "摘要刷新中" : "idle");
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
    if (st.active && st.target_kind === "message") rerollState.value = st;
    if (st.running) {
      // 候选流：与一轮的游标读同一套（读不消费；token 对不上后端自然给空）。
      const slice = await api.turnText(sid, cur.from, cur.thinkFrom).catch(() => null);
      if (slice && my === epoch) {
        const merged = mergeStreamSlice(
          { text: rerollPreview.value.text, reasoning: rerollPreview.value.reasoning }, cur, slice);
        rerollPreview.value = { text: merged.text, reasoning: merged.reasoning };
        cur = merged.cur;
      }
    }
    refreshStatusline(st.running ? "重摇中" : "idle");
    if (!st.running) return st;
  }
  return st;
}
// rerollNow：⟳ / `/reroll` —— 摇一版（没进模式就先进），摇完**切到最新那版**。
async function rerollNow() {
  const sid = sessionID;
  if (!sid) return;
  const my = epoch;
  clearRerollPreview();
  // 立刻把那条气泡切空白（别等第一帧）：目标 = 尾条 assistant（后端也只认它，不是它 ⇒ 400）。
  rerollStarting.value = lastMsgId.value;
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
    if (!st?.active) return;
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
    refreshTasks();
    toast(`压缩已受理${blocks ? `（${blocks} 块）` : ""}`, "ok");
    pollCompact(sid, accepted?.at_ms).catch(() => {});
  } catch (err) {
    toast(`压缩失败：${err.message}`, "error");
  }
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
    const span = c.from_idx ? `第 ${c.from_idx}–${c.to_idx} 条` : "";
    toast(`压缩完成${span ? `：${span}` : ""}${c.merged ? "（合并）" : ""}`, "ok");
    await reloadWindow();
    refreshStateView(sid);
    return;
  }
}

// —— 顶栏 ——
function paintSessionHeader() {
  topbarRef.value?.setSession(sessionInfo, draft);
  document.title = sessionInfo.title ? `${sessionInfo.title} - MicroChat` : "MicroChat";
}
async function fillModelSelect() {
  const my = epoch;
  api.models().then((models) => {
    if (my !== epoch) return;
    const groups = groupModelsByProvider(models).map((g) => ({
      provider: g.provider,
      items: g.items.map(modelOption),
    }));
    const cur = sessionInfo.provider && sessionInfo.model ? modelKey(sessionInfo.provider, sessionInfo.model) : "";
    topbarRef.value?.setModels(groups, cur);
  }).catch((err) => { if (my === epoch) toast(`模型列表失败：${err.message}`, "error"); });
}
async function applyModelValue(value) {
  const mp = parseModelKey(value);
  if (!mp) return;
  const { provider, model } = mp;
  if (draft) {
    // 草稿：改动只活在客户端，建会话时随 POST /sessions 一起带上。
    draftPrefs.provider = provider;
    draftPrefs.model = model;
    Object.assign(sessionInfo, { provider, model });
    paintSessionHeader();
    refreshStatusline("idle");
    refreshPreview(); // 面板不许停在旧渠道那一发上
    return;
  }
  const my = epoch;
  const updated = await api.patchSession(sessionID, { provider, model });
  if (my !== epoch) return;
  Object.assign(sessionInfo, updated);
  paintSessionHeader();
  refreshStatusline("idle");
  refreshPreview();
}
function fillAgentSelect() {
  const my = epoch;
  api.agents().then((data) => {
    if (my !== epoch) return;
    const list = (data.agents ?? []).map(agentOption);
    // 顶栏的圆头像 = Agent 入口（2026-10-14）；状态行小字里那份是"显示"。
    topbarRef.value?.setAgents(list, sessionInfo.agent_id);
    statusAgent.value = list.find((a) => a.id === sessionInfo.agent_id)?.label ?? sessionInfo.agent_id ?? "";
    refreshStatusline();
    // 没 agent 时别猜 list[0]：后端认的是 default_agent。
    if (!sessionInfo.agent_id && list.length) applyAgentValue(data.default_agent || list[0].id);
  }).catch((err) => { if (my === epoch) toast(`Agent 列表失败：${err.message}`, "error"); });
}
// 状态行那格"当前助手"的标签（顶栏那份交给圆头像了；这格只管显示）。
// 换人要当场重算 —— 它原来只在"进会话"时算过一次，换完留着旧名字（2026-10-14 实测咬到）。
function agentLabelOf(id) {
  return vueAgents.value.find((a) => a.id === id)?.name || id || "";
}
async function applyAgentValue(id) {
  if (!id) return;
  if (draft) {
    draftPrefs.agent = id;
    Object.assign(sessionInfo, { agent_id: id });
    paintSessionHeader();
    refreshAgents();
    statusAgent.value = agentLabelOf(id);
    refreshStatusline("idle");
    refreshPreview();
    return;
  }
  const my = epoch;
  const updated = await api.patchSession(sessionID, { agent_id: id });
  if (my !== epoch) return;
  Object.assign(sessionInfo, updated);
  paintSessionHeader();
  refreshAgents();
  statusAgent.value = agentLabelOf(id);
  refreshStatusline("idle");
  refreshPreview();
}
// refreshAgents：拉生效 agent 列表（**只为草稿的首段对话**与状态行的助手名 —— 从前它顺带算
// "显示模式"，2026-10-27 那个开关没了 ⇒ 更名收窄，别再往这儿加显示相关的东西）。
async function refreshAgents() {
  const my = epoch;
  try {
    const data = await api.agents();
    if (my !== epoch) return;
    vueAgents.value = data.agents ?? [];
  } catch { /* 拿不到就空着：草稿不画首段、状态行回落到 agent id */ }
}
// leftHidden / rightHidden：两侧栏收没收的**响应式镜像**（真实状态仍是 DOM 类 + localStorage 偏好；
// 2026-10-14 用户定：栏开着时顶栏不渲染那一侧的开关 —— 按钮跟着它控制的东西走）。
const leftHidden = ref(false);
const rightHidden = ref(false);
function applySideToggle(barID, hidden) {
  document.getElementById(barID)?.classList.toggle("collapsed", hidden);
  if (barID === "sessions-bar") leftHidden.value = hidden;
  if (barID === "payload-bar") rightHidden.value = hidden;
}

// —— 会话 ——
// —— 侧栏/抽屉（2026-10-10 移动端）—— 同一对按钮两种身份：
// 宽屏 = 布局里的栏（collapsed = 收起）；窄屏 = 覆盖式抽屉（collapsed = 关着）。
// 窄屏一律默认关、不读不写 localStorage —— 抽屉的开合是瞬时的事，那是桌面偏好的账。
// 响应式断点：与 main.css 的三处 @media **同源**（700 = 左栏改抽屉 / 1100 = 右栏改抽屉），
// CSS 里没法读 JS 常量，改一处要改两处。
const BP = { leftDrawer: 700, rightBar: 1100 };
const mqlLeft = window.matchMedia?.(`(max-width: ${BP.leftDrawer}px)`) ?? null;
const mqlRight = window.matchMedia?.(`(max-width: ${BP.rightBar}px)`) ?? null;
function narrowLeft() { return !!mqlLeft?.matches; }
function narrowRight() { return !!mqlRight?.matches; }
function applySideStates() {
  applySideToggle("sessions-bar", narrowLeft() ? true : localStorage.getItem("mc_bar_left_hidden") === "1");
  applySideToggle("payload-bar", narrowRight() ? true : localStorage.getItem("mc_bar_right_hidden") === "1");
}
function toggleLeft() {
  const bar = document.getElementById("sessions-bar");
  const hidden = !bar?.classList.contains("collapsed");
  if (!narrowLeft()) localStorage.setItem("mc_bar_left_hidden", hidden ? "1" : "0");
  applySideToggle("sessions-bar", hidden);
}
function toggleRight() {
  const bar = document.getElementById("payload-bar");
  const hidden = !bar?.classList.contains("collapsed");
  if (!narrowRight()) localStorage.setItem("mc_bar_right_hidden", hidden ? "1" : "0");
  applySideToggle("payload-bar", hidden);
}
// 窄屏里"人去了别处"就得把左抽屉收回去（选会话 / 开新对话）。
function closeLeftDrawer() { if (narrowLeft()) applySideToggle("sessions-bar", true); }

// —— 滑动面板（触屏**跟手拖拽**；2026-10-10 用户定/细化）——
// 拖到哪儿面板停哪儿（**半开也维持**，不用等松手）；松手按"过半 + 速度"吸附开/关。
// 方向：左滑出右栏、右滑出左栏；已有一扇开着 ⇒ 先关后开（只认朝它关回去的方向）。
// 最终仍落在同一个 collapsed 类上（手势 = [◧]/[◨] 的替身，不另造开合状态）。
// 判定全在 utils/swipe.js（纯函数）；这里只碰 DOM：跟手期间写内联 transform（transition 先掐掉），
// 松手把内联清掉、按结果落类 —— 由 CSS 自己的 200ms 过渡完成吸附动画。
const barSide = (bar) => (bar === "left" ? "sessions-bar" : "payload-bar");
const scrimSide = (bar) => (bar === "left" ? "scrim-left" : "scrim-right");
let panelDrag = null;

function swipeBlocked(target) {
  if (overlaysRef.value?.isPaletteOpen() || overlaysRef.value?.isPickerOpen()) return true;
  if (document.querySelector("#settings-view, #user-view") || document.querySelector(".modal")) return true;
  // 抽屉自己身上 ⇒ 一律接管（pre 也不例外）："把抽屉拖走"在这儿就该能用 ——
  // 右栏的载荷 tab 整块就是个 <pre>，早先被 pre 例外吞掉，真机上表现为"在面板上滑没反应"。
  if (target?.closest?.("#sessions-bar, #payload-bar")) return false;
  return !!target?.closest?.("textarea, input, select, pre, .palette, .picker, .modal, #settings-view, #user-view");
}
function paintDrag(d, p) {
  const off = d.pick.bar === "left" ? -102 : 102; // 与 CSS 的 collapsed 位移一致
  d.el.style.transform = `translateX(${off * (1 - p)}%)`;
  if (d.scrim) d.scrim.style.opacity = String(p);
}
function onTouchStart(e) {
  panelDrag = null;
  if (e.touches.length !== 1 || swipeBlocked(e.target)) return;
  const t = e.touches[0];
  panelDrag = {
    x0: t.clientX, y0: t.clientY, axis: null, pick: null,
    el: null, scrim: null, w: 0, p: 0, lastT: Date.now(), v: 0,
  };
}
function onTouchMove(e) {
  const d = panelDrag;
  if (!d || e.touches.length !== 1) return;
  const t = e.touches[0];
  const dx = t.clientX - d.x0;
  const dy = t.clientY - d.y0;
  if (!d.axis) {
    const axis = classifyAxis(dx, dy);
    if (!axis) return;
    if (axis === "v") { panelDrag = null; return; } // 归浏览器滚列表
    const left = document.getElementById("sessions-bar");
    const right = document.getElementById("payload-bar");
    const pick = pickDrag({
      dx,
      leftDrawer: narrowLeft(),
      rightDrawer: narrowRight(),
      leftOpen: !!left && !left.classList.contains("collapsed"),
      rightOpen: !!right && !right.classList.contains("collapsed"),
    });
    if (!pick) { panelDrag = null; return; }
    d.axis = axis; // ⚠ 必须记住：不然每次 touchmove 都重进这里，第二枚就被自己摘掉的 collapsed 骗死
    d.pick = pick;
    d.p = pick.base; // ⚠ 起点开度（开=0 / 关=1）：不初始化，第一枚 move 的速度会算出天文数字、
    //   被"快甩"规则误判成弹回（实测咬过一次）。⚠ **别动 d.lastT**：它在 touchstart 起表，
    //   重置成此刻的话第一枚 move 的 dt≈0、速度样本直接作废（快甩失灵，也实测咬过）。
    d.el = pick.bar === "left" ? left : right;
    d.scrim = document.getElementById(scrimSide(pick.bar));
    d.w = d.el.getBoundingClientRect().width || 300;
    d.el.style.transition = "none"; // 跟手期间不许有过渡
    if (d.scrim) d.scrim.style.transition = "none";
    applySideToggle(barSide(pick.bar), false); // 拖起来先亮出来，位置全由内联管
  }
  e.preventDefault(); // 这手势归我们了：别让页面跟着滚
  const p = dragProgress(d.pick.base, dx, d.w, d.pick.bar);
  const now = Date.now();
  const dt = now - d.lastT;
  if (dt > 0) d.v = (p - d.p) / dt; // 开度速度（朝开为正）
  d.lastT = now;
  d.p = p;
  paintDrag(d, p);
}
function settleDrag(useVelocity) {
  const d = panelDrag;
  panelDrag = null;
  if (!d || !d.axis || !d.pick) return;
  // 速度样本要有**保鲜期**：甩完停住手再松（>120ms 没有新 move）就按位置吸附 ——
  // 不然照着历史峰值走，拖到一半停手也会被当快甩弹开（实测咬过一次）。
  const fresh = Date.now() - d.lastT <= 120;
  const open = resolveRelease(d.p, useVelocity && fresh ? d.v : 0) === "open";
  d.el.style.transition = ""; // 先还 CSS 过渡，再清内联 —— 吸附动画由它完成
  d.el.style.transform = "";
  if (d.scrim) { d.scrim.style.transition = ""; d.scrim.style.opacity = ""; }
  applySideToggle(barSide(d.pick.bar), !open);
}
function onTouchEnd() { settleDrag(true); }
function onTouchCancel() { settleDrag(false); }
async function askDeleteSession(id) {
  const list = await api.listSessions().catch(() => []);
  const name = sessionTitle(list.find((s) => s.id === id)?.title);
  if (!(await overlaysRef.value.confirmAsk(`删除会话「${name}」？不可逆。`))) return;
  try {
    await api.deleteSession(id);
  } catch (err) {
    toast(`关闭失败：${err.message}`, "error");
    return; // 删失败就别切会话（catch 回调里的 return 不是这个函数的 return）
  }
  if (id === sessionID) {
    backToChat(); // 被删的是当前会话 ⇒ 接下来要去草稿：**两个视图都让位**（哪边开着都回对话区）
    await startDraft(); // 删掉当前会话 ⇒ 回到草稿（不再预建空会话 —— 那正是被拿掉的旧行为）
  } else refreshSessionsBar();
}
async function startDraft() {
  ++epoch;
  draft = true;
  sessionID = null;
  saveLastSession(""); // 人在草稿 ⇒ 开机也该回草稿（别把人拽回上次那条会话）
  draftId = uuidv7(); // 这张草稿将来那会话的 id（预演与建会话都用它）
  const d = await api.getDefaults().catch(() => ({}));
  draftPrefs = { provider: d.provider ?? "", model: d.model ?? "", agent: d.agent ?? "" };
  Object.assign(sessionInfo, {
    title: "", provider: draftPrefs.provider, model: draftPrefs.model,
    agent_id: draftPrefs.agent, system_prompt: "",
  });
  vueMessages.value = [];
  vueStream.value = { id: null, text: "", reasoning: "" };
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
  refreshPreview(); // 输入框里若还留着字（＋ 时不清输入框），预演立刻跟上
}
function newSession() {
  closeLeftDrawer(); // 窄屏：抽屉让位（人已经去新对话了）
  backToChat(); // 侧栏动作 = 把人带回对话区（**两个视图**都让位 —— 别再只 closeSettings）
  startDraft().catch((err) => toast(`新建失败：${err.message}`, "error"));
}
function renderSessions(sessions) {
  sessionbarRef.value?.setSessions(sessions, sessionID);
}
async function refreshSessionsBar() {
  try {
    renderSessions(await api.listSessions());
  } catch (err) { toast(`刷会话栏失败：${err.message}`, "error"); }
}
async function enterSession(id) {
  const my = ++epoch;
  draft = false;
  sessionID = id;
  saveLastSession(id); // 记住"在看哪条"——刷新/重载回来就进它
  // 窗口重新起：先清掉上一条会话的一切（消息/段/展开态/懒加载缓存），再按"最后 N 段"拉。
  vueMessages.value = [];
  vueSegments.value = [];
  windowFrom.value = 1;
  summaryMsgs.clear();
  openSummaries.clear();
  const sessions = await api.listSessions();
  if (my !== epoch) return; // 迟到的上一发：整段丢掉（切会话竞态的唯一守卫）
  Object.assign(sessionInfo, sessions.find((s) => s.id === id) ?? {});
  paintSessionHeader();
  refreshAgents();
  fillModelSelect();
  fillAgentSelect();
  renderSessions(sessions);
  const ok = await loadWindow({ reset: true });
  if (my !== epoch || !ok) return;
  refreshStatusline("idle");
  refreshStateView(id);
  refreshRerolls();
  if (previewOn() && vueMessages.value.length) {
    api.outgoingPreview(id, "").then((w) => { if (my === epoch) payloadbarRef.value?.setWire(w); })
      .catch((err) => { if (my === epoch) toast(`预演失败：${err.message}`, "error"); });
  } else {
    payloadbarRef.value?.setWire(null); // 别把上一条会话的载荷挂在空会话上
  }
  // 压缩那发的载荷同理：换会话就清掉（那是**某一次**压缩的账，不属于"当前"）
  payloadbarRef.value?.setCompactWire(null);
  // 这一轮还在跑？（刷新页面、切走又回来）—— status 是权威：把合成的流气泡与轮询接回来。
  // reply 落库前消息列里本来就没有它，不接的话界面装 idle、发送才知道 409。
  api.status(id).then((st) => {
    if (my !== epoch) return;
    if (st?.phase === "pending" || st?.phase === "streaming") {
      if (!vueMessages.value.some((m) => m.stream)) pollTurn(id, st.message_id).catch(() => {});
    }
  }).catch(() => {});
}
async function boot() {
  // 自检按候选顺序探（没存过地址时：先"跟页面同主机的 8787"，再"同源 /api/v1"兜底反代部署）。
  // 探测走开放的 `/auth/status`（没有凭据也能问"后端活着吗、注册开了没"）。
  let st = null;
  let lastErr = null;
  let winner = "";
  for (const cand of apiBaseCandidates()) {
    try {
      st = await probeAuthStatus(cand);
      winner = cand;
      break;
    } catch (err) {
      lastErr = err;
    }
  }
  if (!st) {
    // 连不上后端：不开一副破界面，先给"连接"面板（新设备第一次打开走的也是这儿）。
    gateError.value = String(lastErr?.message ?? lastErr);
    showGate.value = true;
    return;
  }
  if (winner && winner !== apiBase()) {
    // 非首选候选胜出（给了同源等）：存下来 + 刷新 —— sharedApi() 等一切读的都是 apiBase()，
    // 不存的话这一页会指着错的地址干活。一次性闪一下，之后就走存的。
    saveNetPrefs(winner, apiToken());
    location.reload();
    return;
  }
  // 鉴权（2026-10-10；uid 0 = root 恒在）：root 没口令 ⇒ 门板初始化；没凭据/凭据失效 ⇒ 门板登录。
  if (st.needs_setup) {
    gateError.value = "root 还没初始化：先在门板里设置 root 口令";
    showGate.value = true;
    return;
  }
  if (!apiToken()) {
    gateError.value = "请登录";
    showGate.value = true;
    return;
  }
  try {
    currentUser.value = await api.me();
  } catch {
    gateError.value = "登录已失效：重新登录（改过口令/被踢/换了 key 都会这样）";
    showGate.value = true;
    return;
  }
  connText = `已连接 v${st.version ?? "?"}`;
  // 压缩面板的默认块数读的是实例级 chat 配置（设置专属）：只有 admin 去问；普通用户留占位。
  if (isAdmin.value) {
    api.getChat().then((c) => { compactDefault.value = c?.compact_blocks ?? null; }).catch(() => {});
  } else {
    compactDefault.value = null;
  }
  // 回到"上次在看的那条会话"（2026-10-11 用户定）：刷新 / 被安卓丢弃后重载 ⇒ 不再是一张空草稿。
  // 记的 id 取不到（删了 / 换人登录 / 别的设备）⇒ 回落草稿 —— 取不到就是取不到，不猜。
  const last = lastSessionId();
  if (last) {
    const sessions = await api.listSessions().catch(() => []);
    if (sessions.some((s) => s.id === last)) {
      await enterSession(last);
      return;
    }
  }
  await startDraft();
}

// —— 发送 / 轮询 ——
async function send(content) {
  const sid = sessionID;
  const accepted = await api.sendMessage(sid, content);
  refreshTasks();
  addMessage("你", content, "user");
  await pollTurn(sid, accepted.turn.message_id);
}
async function resend() {
  const sid = sessionID;
  const accepted = await api.resend(sid);
  refreshTasks();
  await pollTurn(sid, accepted.turn.message_id);
}
async function pollTurn(sid, turnID) {
  const my = epoch;
  const m = { who: "助手", role: "assistant", _stream: true, stream: true, content: "", reasoning: "", id: tmpKey() };
  vueMessages.value.push(m);
  vueStream.value = { id: turnID, text: "", reasoning: "" };
  composerRef.value?.setRunning(true);
  let cur = { from: 0, thinkFrom: 0 };
  try {
    for (;;) {
      await new Promise((r) => setTimeout(r, 300));
      if (my !== epoch) return; // 会话世代变了（切走/回草稿）：这一轮不再往界面上写（库里照旧落）
      const st = await api.status(sid);
      const slice = await api.turnText(sid, cur.from, cur.thinkFrom);
      if (my !== epoch) return;
      // 游标回退（缓冲被新一轮重置）⇒ 清零重建（mergeStreamSlice 的语义，别在别处再写一份）。
      const merged = mergeStreamSlice(
        { text: vueStream.value.text ?? "", reasoning: vueStream.value.reasoning ?? "" }, cur, slice);
      vueStream.value.text = merged.text;
      vueStream.value.reasoning = merged.reasoning;
      m.reasoning = merged.reasoning;
      cur = merged.cur;
      refreshStatusline(st.phase, st.elapsed_ms);
      refreshSessionsBar();
      if (st.phase === "idle" || st.phase === "error") {
        if (my !== epoch) return;
        const failed = st.phase === "error";
        await reloadWindow();
        if (my !== epoch) return;
        if (failed) {
          // 失败合成一条气泡摆在文末（reloadWindow 会清 vueStream，所以必须摆在它后面）+ toast 兜底。
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
        break;
      }
    }
  } finally {
    composerRef.value?.setRunning(false);
  }
}
let connText = "未连接";
// statusAgent：当前助手的显示名（顶栏去壳后，它住在状态行小字里；数据在进会话时现取）。
const statusAgent = ref("");
// 后台任务行（/tasks 是进程内事实；2s 一拉 —— 端点零成本，也别等太久看不见压缩/标题这些短命任务）。
const TASKS_POLL_MS = 2000;
let tasksTimer = null;
async function refreshTasks() {
  try {
    const board = await api.tasks();
    composerRef.value?.setTaskLine(taskLineText(board));
  } catch { composerRef.value?.setTaskLine(""); }
}
// refreshStatusline：连接 / 助手 / 相位 / 上下文 —— 全都喂给右上角那枚环（2026-10-28 用户定）。
//
// 这四样从前是一行小字（"已连接 v0.1.0｜上下文 344/1.04M｜默认助手｜idle"），紧贴着对话；
// 现在共用一处落点：环里是占比，点开的浮板里是明细（含连接与相位）。
// ⚠ 别再造第二条"显示状态行"的路 —— 同一个数字住两处，只会让两处都可能过期。
function refreshStatusline(phase, elapsedMs) {
  // 连接 + 助手 + 相位：草稿态也要报（"没连上"跟有没有会话无关）。
  topbarRef.value?.setStatus({ conn: connText, agent: statusAgent.value, phase, elapsedMs });
  if (draft || !sessionID) {
    // 草稿：没有会话可问占用 ⇒ 环清空（没有会话就没有"占了多少"这回事）。
    topbarRef.value?.setContext(null);
    return;
  }
  api.context(sessionID).catch(() => null).then((ctx) => {
    topbarRef.value?.setContext(ctx);
  }).catch((err) => toast(`上下文占用读取失败：${err.message}`, "error"));
}
function refreshStateView(id) {
  if (draft || !(id ?? sessionID)) return; // 草稿：没有会话可问状态（右栏保持空话）
  const my = epoch;
  api.sessionState(id ?? sessionID).then((v) => { if (my === epoch) payloadbarRef.value?.setTables(v?.tables); }).catch(() => {});
}

// —— 命令 ——
function liftOverlays() {
  // 输入框自身就是 #composer（组件根）——"#composer-vue" 那层包装早没了，别再查它。
  const form = document.querySelector("#composer");
  if (!form) return;
  const h = form.getBoundingClientRect().height;
  overlaysRef.value?.lift(Math.max(0, h - 60));
  // 输入行现在是**悬浮层**（绝对定位）⇒ 消息列底部留白得知道它多高（textarea 会长高，写死会错位）。
  document.getElementById("chat-col")?.style.setProperty("--composer-h", `${Math.round(h)}px`);
}
// 输入框高度是异步变的（autosize 在 nextTick 里长高）⇒ 量一次不够，盯住它本身。
let composerRO = null;
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
    const rows = data.agents ?? data;
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
    const one = async (m) => editMessageFlow(m.id, wantThink ? "reasoning" : "content");
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
        agent_id: draftPrefs.agent, content: v,
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
    // 三档判定与 Composer 的提示同源（utils/prefs.js）。
    if (wantsSend(e)) {
      e.preventDefault();
      composerRef.value?.submit();
    }
  }
}
// sendFromDraft：草稿的第一句 —— 建会话（带草稿里选的 model/agent）→ 受理 → 轮询。
// 只有"受理"失败才回滚（删掉刚建的空会话、退回草稿）；轮询掉线不回滚（消息已入库）。
async function sendFromDraft(text) {
  const body = {};
  if (draftId) body.id = draftId; // 预演头上那个 id —— 真会话必须落同一个
  if (draftPrefs.provider) body.provider = draftPrefs.provider;
  if (draftPrefs.model) body.model = draftPrefs.model;
  if (draftPrefs.agent) body.agent_id = draftPrefs.agent;
  const s = await api.createSession(body);
  draft = false;
  sessionID = s.id;
  saveLastSession(s.id); // 草稿转真会话：从现在起"在看的就是它"
  Object.assign(sessionInfo, s);
  paintSessionHeader();
  refreshSessionsBar();
  let accepted;
  try {
    accepted = await api.sendMessage(s.id, text);
    refreshTasks();
  } catch (err) {
    await api.deleteSession(s.id).catch(() => {});
    await startDraft();
    throw err;
  }
  addMessage("你", text, "user");
  await pollTurn(s.id, accepted.turn.message_id);
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
const CHROME_JUMP = 200; // 一次跳这么多 ⇒ 不是手指，是切会话/贴底 ⇒ 重置，别把栏收走
let chrome = resetChromeState();
const coarsePointer = window.matchMedia?.("(pointer: coarse)") ?? null;
function applyChrome() {
  const root = document.documentElement.style;
  root.setProperty("--chrome-top", `${Math.round(chrome.top)}px`);
  root.setProperty("--chrome-bottom", `${Math.round(chrome.bottom)}px`);
}
function resetChrome(el) {
  chrome = resetChromeState(el?.scrollTop ?? 0);
  applyChrome();
}
// 判据在 utils/chrome.js（纯函数、契约测试钉着）；这里只喂数字 + 写两个 CSS 变量。
function onMessagesScroll() {
  const el = document.querySelector("#messages-vue");
  if (!el) return;
  if (!chromeHideOn() || !coarsePointer?.matches) {
    if (chrome.top || chrome.bottom) resetChrome(el);
    else chrome.last = el.scrollTop;
    return;
  }
  const y = el.scrollTop;
  const dy = y - chrome.last;
  if (Math.abs(dy) > CHROME_JUMP) { resetChrome(el); return; }
  chrome = stepChrome(chrome, {
    dy,
    scrollTop: y,
    headerH: document.querySelector("#chat-col > header")?.offsetHeight ?? 40,
    composerH: document.querySelector("#composer")?.offsetHeight ?? 64,
  });
  applyChrome();
}

// —— 设置视图（与对话同级，2026-10-10） ——
// 窄屏：进/出设置都把左抽屉归位（手机上别让它压着视图）；宽屏：不动侧栏（并排看没问题）。
function openSettings() {
  if (!isAdmin.value) { toast("只有 root（管理员）能进设置"); return; }
  settingsOpen.value = true;
  userOpen.value = false; // 与用户视图互斥（平级的两块，一屏只开一个）
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
function openUserView() {
  userOpen.value = true;
  settingsOpen.value = false;
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
function closeUserView() { userOpen.value = false; }
// 用户视图里存了个人偏好 ⇒ 聊天壳上活着的开关立刻跟上（命令菜单是唯一现在就长短不一的那个）。
function onUserPrefsSaved() {
  cmdMenu.value = commandMenuOn();
  if (!chromeHideOn()) resetChrome(document.querySelector("#messages-vue")); // 关掉这功能 ⇒ 栏立刻回来
  if (!cmdMenu.value) overlaysRef.value?.hidePalette();
}
function closeSettings() {
  settingsOpen.value = false;
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
// backToChat：**侧栏动作要把人带回对话区**（点会话 / 点＋新对话 / 当前会话被删）—— 唯一出口。
//
// 为什么要有它（2026-10-20 用户报："设置里点左侧会话会退出设置进对话，用户视图点了没反应"）：
// 设置与用户视图是**平级的两块**（一屏只能开一个），所以"让位"必须**同时**管两边。
// 从前只有 `closeSettings()`（只关设置），而 `closeUserView()` 定义了却**从没被调用过** ⇒
// 于是侧栏那三个动作在用户视图里**全都**失灵（点会话、`/new`、删当前会话）。
// 收成一个出口，往后加侧栏动作只需调它 —— 别再成对地手写两个 close（漏一个就是这条 bug 复发）。
//
// ⚠ 只给**侧栏动作**用它：命令面板（`/resume` `/new` `/delete` 那些）与启动恢复路径**不需要** ——
// 命令面板挂在聊天壳里（`#chat-col` 有 `v-show`），那两个视图开着时根本触发不到。
function backToChat() {
  closeSettings();
  closeUserView();
}
// —— 拖拽调宽 ——
let uninstallViewportFit = null;
onMounted(() => {
  applySideStates();
  refreshTasks();
  tasksTimer = setInterval(refreshTasks, TASKS_POLL_MS);
  // 键盘遮挡：整盒跟着视觉视口拟合（见 utils/viewport.js）；键盘一开，如果人正在输入，
  // 顺手把消息列贴回底部（矮了一截后最新一条不该被挤出视野）。
  uninstallViewportFit = installViewportFit();
  const vv = window.visualViewport;
  const onVvResize = () => { if (document.activeElement?.id === "input") scrollBottom(); };
  vv?.addEventListener("resize", onVvResize);
  // 滚动隐藏上下栏（触屏）：passive 监听，只写两个 CSS 变量。
  document.querySelector("#messages-vue")?.addEventListener("scroll", onMessagesScroll, { passive: true });
  // 滑动面板：跟手拖拽。touchmove 必须非 passive（接管后要 preventDefault 压住页面滚）。
  document.addEventListener("touchstart", onTouchStart, { passive: true });
  document.addEventListener("touchmove", onTouchMove, { passive: false });
  document.addEventListener("touchend", onTouchEnd, { passive: true });
  document.addEventListener("touchcancel", onTouchCancel, { passive: true });
  // 跨断点（桌面 ⇄ 抽屉）时把两侧状态重算一遍：变成抽屉就一律先关掉，不然会"啪"地盖上来。
  mqlLeft?.addEventListener?.("change", applySideStates);
  mqlRight?.addEventListener?.("change", applySideStates);
  if (typeof ResizeObserver !== "undefined" && !composerRO) {
    composerRO = new ResizeObserver(() => liftOverlays());
    const form = document.querySelector("#composer");
    if (form) composerRO.observe(form);
  }
  boot().catch((err) => {
    connText = "未连接";
    refreshStatusline("idle");
    toast(`启动失败：${err.message}`, "error");
  });
});
onUnmounted(() => {
  uninstallViewportFit?.();
  uninstallViewportFit = null;
  document.removeEventListener("touchstart", onTouchStart);
  document.removeEventListener("touchmove", onTouchMove);
  document.removeEventListener("touchend", onTouchEnd);
  document.removeEventListener("touchcancel", onTouchCancel);
  if (tasksTimer) clearInterval(tasksTimer);
  tasksTimer = null;
  composerRO?.disconnect();
  composerRO = null;
  mqlLeft?.removeEventListener?.("change", applySideStates);
  mqlRight?.removeEventListener?.("change", applySideStates);
});
</script>

<template>
  <div id="app">
    <ConnectGate v-if="showGate" :initial-error="gateError" />
    <div id="columns">
      <SessionBar
        ref="sessionbarRef"
        @toggle-left="toggleLeft"
        :can-settings="isAdmin"
        @select="(id) => { closeLeftDrawer(); backToChat(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
        @settings="(id) => { sessionSettingsId = id; }"
        @user="openUserView"
        @close="askDeleteSession"
        @new="newSession"
        @open-settings="openSettings"
      />
      <div id="scrim-left" @click="toggleLeft"></div>
      <div id="content">
      <section id="chat-col" v-show="!settingsOpen && !userOpen">
    <TopBar
      ref="topbarRef"
      :left-hidden="leftHidden"
      :right-hidden="rightHidden"
      @toggle-left="toggleLeft"
      @toggle-right="toggleRight"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${err.message}`, 'error'))"
      @select-agent="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, 'error'))"
    />
        <main id="messages-vue">
          <!-- 滚动容器（#messages-vue）现在**占满整个对话区**（滚动条因此贴最右缘，2026-10-14 用户定）；
               阅读列约束（760 居中、两侧 56px、上下留白）落在这一层 —— 气泡别拉成整屏长句。 -->
          <div class="messages-inner">
          <button
            v-if="olderSegments > 0"
            id="load-more"
            type="button"
            @click="loadMore().catch((err) => toast(`加载更多失败：${err.message}`, 'error'))"
          >↑ 加载更多历史（还有 {{ olderSegments }} 段）</button>
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
            />
            <MessageBubble
              v-else
              :who="row.msg.who"
              :idx="row.displayIdx"
              :role="row.msg.role"
              :content="bubbleContent(row.msg)"
              :reasoning="bubbleReasoning(row.msg)"
              :reasoning-ms="row.msg.reasoningMs"
              :message-id="row.msg.messageId"
              :is-last="!!row.msg.messageId && row.msg.messageId === lastMsgId"
              :streaming="!!row.msg.stream || isLive(row.msg)"
              :usage="row.msg.usage"
              :reroll="row.msg.messageId && rerollState.active && rerollState.target_message_id === row.msg.messageId ? rerollState : null"
              @save="onBubbleSave"
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
      @input-text="onComposerInput"
      @keydown="onComposerKeydown"
      @submit="onComposerSubmit"
      @stop="stopCurrent"
      @menu="() => overlaysRef?.showPalette(filterCommands(''))"
    />
      </section>
      <PayloadBar
        v-show="!settingsOpen && !userOpen"
        ref="payloadbarRef"
        :default-blocks="compactDefault"
        @toggle-right="toggleRight"
        @compact="(n) => compactNow(n ?? undefined)"
        @compact-payload="previewCompactPayload"
        @tab="onPayloadTab"
      />
      <UserView
        v-if="userOpen"
        :esc-blocked="!!sessionSettingsId"
        @close="closeUserView"
        @toggle-left="toggleLeft"
        @prefs-saved="onUserPrefsSaved"
      />
      <SettingsModal
        v-if="settingsOpen"
        :ask="askConfirm"
        :esc-blocked="!!sessionSettingsId"
        @close="closeSettings"
        @toggle-left="toggleLeft"
        @sessions-changed="refreshSessionsBar"
        @models-changed="fillModelSelect"
        @goto-session="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
      />
      <div id="scrim-right" @click="toggleRight"></div>
      </div>
    </div>
    <Toast ref="toastRef" />
    <!-- 更新条：只在检测到新版本时出现（挂根层 —— 设置/用户视图开着也该看得见）。 -->
    <UpdateBar v-if="updateReady" ref="updateRef" @update="applyUpdate" />
    <!-- Overlays 挂根层（不在聊天视图里）：设置视图开着时"确认"也要能弹（fixed 定位照旧）。 -->
    <Overlays ref="overlaysRef" @run-command="(n) => runCommand(n, []).catch((err) => toast(`命令失败：${err.message}`, 'error'))" @pick="onPickAction" />
    <SummaryEditor
      v-if="summaryEditTarget"
      :session-id="sessionID"
      :summary="summaryEditTarget"
      @close="summaryEditTarget = null"
      @saved="onSummaryEdited"
    />
    <SessionSettings
      v-if="sessionSettingsId"
      :session-id="sessionSettingsId"
      @close="sessionSettingsId = null"
      @renamed="() => { refreshSessionsBar(); if (sessionSettingsId === sessionID) enterSession(sessionID).catch(() => {}); sessionSettingsId = null; }"
    />
  </div>
</template>
