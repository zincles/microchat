<script setup>
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from "vue";
import { mergeStreamSlice, probeHealth } from "./api/client.js";
import { parseCommand, filterCommands, COMMAND_META } from "./utils/commands.js";
import { resetChrome as resetChromeState, stepChrome } from "./utils/chrome.js";
import { uuidv7 } from "./utils/ids.js";
import { buildCompactTree } from "./utils/tree.js";
import {
  buildChatRows, computeSegments, windowStart, prevWindowStart, alignWindowFrom, olderCount, runsToFetch,
  rerollTargetLive, bubbleText, editRerollTarget, latestPromptFor,
} from "./utils/chatrows.js";
import { applyBackground } from "./utils/background.js";
import { installViewportFit } from "./utils/viewport.js";
import { classifyAxis, pickDrag, dragProgress, resolveRelease } from "./utils/swipe.js";
import { typewriterDuration, revealCount } from "./utils/typewriter.js";
import { sharedApi, previewOn, applyTheme, wantsSend, apiBase, apiBaseCandidates, saveNetPrefs, lastSessionId, saveLastSession, commandMenuOn, applyComposerStyle, applyTextScale, chromeHideOn, syncPrefs, saveBarLeftHidden, barLeftHidden } from "./utils/prefs.js";
import {
  groupModelsByProvider,
  deletionPlanSummary,
  modelKey,
  parseModelKey,
  modelOption,
  agentOption,
  sessionTitle,
  whoText,
} from "./utils/format.js";
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
// 服务器是**偏好的权威**（2026-11-01「所有设置均存储在服务器上」）。
// 上面那几行先按**本地镜像**把主题/缩放/输入栏形态铺上 —— 那是为了让**第一帧**就是对的
// （不然会先闪一下默认配色再变）。随后这一发去问服务器，不一致就改过来（并重挂背景图）。
// 失败只 toast：连不上后端时页面照样能用，只是偏好停在镜像那一份。
syncPrefs()
  .then(() => { applyBackground(); cmdMenu.value = commandMenuOn(); })
  .catch((err) => toast(`界面偏好读取失败：${err.message}`, "error"));
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
// 设置 → 外观里改了立刻生效（不刷新）—— 偏好住服务器（`prefs.json`），这里是它的活视图。
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
// 会话加载中的骨架（2026-11-02）：进会话会先清空消息列，再等 /sessions + 消息窗口；
// 远端部署时那是两三个 RTT 的空白。给一层静态骨架，至少让人知道"在加载"而不是"坏了"。
const loadingSession = ref(true);
const vueSegments = ref([]); // 整条会话的段布局（computeSegments 的产出）：窗口/折叠/编号都认它
const vueStream = ref({ id: null, text: "", reasoning: "" });
// 流式显字（2026-11-03）：`vueStream` 是后端的完整目标；`streamDisplay` 是打字机当前露出的前缀。
// 每帧只改 streamDisplay，轮询与合并仍只认 vueStream（两个状态别互相镜像）。
const streamDisplay = ref({ text: "", reasoning: "" });
let twState = null;
let twFrame = 0;
let twLastAt = 0;
const twWaiters = [];
const nowMs = () => (typeof performance !== "undefined" ? performance.now() : Date.now());
const requestFrame = (cb) => (typeof requestAnimationFrame === "function"
  ? requestAnimationFrame(cb) : setTimeout(() => cb(nowMs()), 16));
const cancelFrame = (id) => {
  if (typeof cancelAnimationFrame === "function") cancelAnimationFrame(id);
  else clearTimeout(id);
};
// 消息窗口（分页 + 折叠；口径与全套算法在 utils/chatrows.js）：
// 只挂最后 N 段；更老的按"加载更多"一页一页往回；被摘要盖住的原文**展开才拉**（这里当缓存）。
const WINDOW_SEGMENTS = 20; // 首屏段数；"加载更多"一页也这么多
const windowFrom = ref(1); // 窗口起点（段对齐；1 = 已到最老）
const summaryMsgs = reactive(new Map()); // 摘要 id → {state:"loading"|"ready", rows}（只有展开过的才有）
const openSummaries = new Set(); // 哪些卡正展开着（reload 后补拉新鲜的）
const vueAgents = ref([]); // 生效 agent 列表（草稿首段的合成气泡看它；显示形态本身已无开关）
const settingsOpen = ref(false);
// 设置视图的"单独入口"："" = 完整设置（四个 tab）；"session" / "provider" = 只显示那一页，
// 从左侧栏底部的「会话 / 渠道」进来。两者共用同一个视图与同一份数据加载。
const settingsOnly = ref("");
// **设置 = root 专属**（用户定，2026-10-10）：`is_admin` 才给进设置；其他用户的入口按钮也不给。
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
// foreground / 落库后的重拉：**人没在底部就别把人拽回底部**（2026-11-03 用户报：
// 出去截图再回来，滚动条自己跳回最新一条）。贴底才继续贴底；否则恢复原来的 scrollTop。
const SCROLL_STICK_PX = 48;
function nearBottom(el) {
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight <= SCROLL_STICK_PX;
}
async function reloadWindowKeepingScroll() {
  const el = document.getElementById("messages-vue");
  const stick = nearBottom(el);
  const top = el?.scrollTop ?? 0;
  const ok = await reloadWindow({ scroll: false });
  const next = document.getElementById("messages-vue");
  if (next) {
    if (stick) next.scrollTop = next.scrollHeight;
    else next.scrollTop = Math.min(top, Math.max(0, next.scrollHeight - next.clientHeight));
  }
  return ok;
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
let payloadTab = "payload"; // PayloadBar 的默认页；它切页时 onPayloadTab 会更新
function onPayloadTab(t) {
  payloadTab = t;
  treeVisible = t === "tree";
  if (workspace.value !== "context") return; // 人不在工作区：等他切过去再由 refreshContextLazy 拉
  if (treeVisible) refreshTree();
  else if (t === "state") refreshStateView(sessionID);
  else refreshHistoryPreview();
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
// 历史预演（载荷 tab 的"这一发会发什么"）：**只在真的看载荷时才问**。
// 从前 enterSession 无条件 `POST /outgoing` —— 服务端要列消息、现演世界状态、拼整段出站，
// 换会话/刷新页面时这是最重的一发，而人可能根本没打开工作区。现在改成按需。
let historyPreviewSeq = 0;
async function refreshHistoryPreview() {
  if (!previewOn() || draft || !sessionID) { payloadbarRef.value?.setWire(null); return; }
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
  else refreshHistoryPreview();
}
const lastMsgId = computed(() => [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId ?? null);
// 最新 AI 回复对应的用户输入（A-B-C-D-E-F ⇒ E）——“编辑最新 Prompt”的入口目标。
const latestPromptId = computed(() => latestPromptFor(vueMessages.value));
// 渲染行：气泡（带编号）+ 被顶层摘要折起来的卡（口径见 utils/chatrows.js）。
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
const messageEditTarget = ref(null); // 编辑中的消息（MessageEditor 模态）
// 候选流预览（就地显示在目标气泡里）：running 期间靠 `/turn/text` 攒；任何一次落地/切版都清掉。
const rerollPreview = ref({ text: "", reasoning: "" });
function clearRerollPreview() { rerollPreview.value = { text: "", reasoning: "" }; }
// rerollStarting：**刚点下 ⟳ 的那一刻**（状态还没回来）就记下目标 —— 让那条气泡立刻切成空白 +
// 「正在输出…」（2026-10-17 用户定：旧正文一直挂到第一帧才换掉很难看）。状态回来后交给 rerollState。
const rerollStarting = ref(null);
// 判据都在 utils/chatrows.js（纯函数、契约测试钉着）：这条是不是"正在摇的那条"、该显示什么。
// —— 流式打字机（2026-11-03 用户定：「不是每隔 0.5s 全冒出来，而是在这 0.5s 里平滑打出来」）——
// 轮询每次到一大块（后端 buffer 增量），这里按 typewriterDuration 把它摊到几帧里逐字露出；
// 下一拍来了就把剩余字符重新计时，永远追着最新目标走。
function settleTwWaiters() {
  const list = twWaiters.splice(0);
  for (const resolve of list) resolve();
}
function resetTypewriter() {
  if (twFrame) cancelFrame(twFrame);
  twFrame = 0;
  twState = null;
  twLastAt = 0;
  streamDisplay.value = { text: "", reasoning: "" };
  settleTwWaiters();
}
function flushTypewriter() {
  if (twFrame) cancelFrame(twFrame);
  twFrame = 0;
  const state = twState;
  twState = null;
  if (state) streamDisplay.value = { text: state.targetText, reasoning: state.targetReasoning };
  settleTwWaiters();
}
function twTick(ts) {
  const s = twState;
  if (!s) { twFrame = 0; return; }
  const at = typeof ts === "number" ? ts : nowMs();
  const p = s.duration > 0 ? Math.min(1, (at - s.start) / s.duration) : 1;
  // 30fps 就够了：每帧都重排 markdown 会把 CPU 吃满，而肉眼也看不出 30 与 60 的区别。
  if (p < 1 && at - (s.lastPaint ?? s.start) < 32) {
    twFrame = requestFrame(twTick);
    return;
  }
  s.lastPaint = at;
  const textEnd = revealCount(s.fromText, s.toText, p * s.duration, s.duration);
  const thinkEnd = revealCount(s.fromReason, s.toReason, p * s.duration, s.duration);
  streamDisplay.value = {
    text: s.targetText.slice(0, textEnd),
    reasoning: s.targetReasoning.slice(0, thinkEnd),
  };
  if (p < 1) {
    twFrame = requestFrame(twTick);
  } else {
    twFrame = 0;
    twState = null;
    settleTwWaiters();
  }
}
function setStreamTarget(text, reasoning) {
  const targetText = text ?? "";
  const targetReasoning = reasoning ?? "";
  const shown = streamDisplay.value;
  let fromText = shown.text.length;
  let fromReason = shown.reasoning.length;
  // 缓冲被重置 / 换了目标（不是前缀）⇒ 从空白重新开始；否则接着当前前缀往后露。
  if (!targetText.startsWith(shown.text) || !targetReasoning.startsWith(shown.reasoning)) {
    fromText = 0;
    fromReason = 0;
    twLastAt = 0;
    streamDisplay.value = { text: "", reasoning: "" };
  }
  const toText = targetText.length;
  const toReason = targetReasoning.length;
  if (toText <= fromText && toReason <= fromReason) return;
  const start = nowMs();
  // 这一拍的时长 = **上一个数据包到这一拍的实测间隔**；第一拍没有上一拍 ⇒ 默认 300ms。
  const gap = twLastAt ? start - twLastAt : 0;
  twLastAt = start;
  twState = {
    targetText, targetReasoning, fromText, fromReason, toText, toReason,
    start, lastPaint: start, duration: typewriterDuration(gap),
  };
  if (!twFrame) twFrame = requestFrame(twTick);
}
// waitTypewriter：一轮收尾时先把最后一块放完再 reloadWindow（顶栏/消息别在最后一跳时换掉）。
// rAF 在后台标签页可能被冻住 ⇒ 超时兜底 flush，不无限等。
function waitTypewriter(maxMs = 0) {
  if (!twState) return Promise.resolve();
  const limit = maxMs > 0 ? maxMs : Math.min(2000, twState.duration + 100);
  return new Promise((resolve) => {
    let done = false;
    const finish = () => { if (done) return; done = true; clearTimeout(timer); resolve(); };
    const timer = setTimeout(() => { flushTypewriter(); finish(); }, limit);
    twWaiters.push(finish);
  });
}
const isLive = (m) => rerollTargetLive(m, { startingId: rerollStarting.value, state: rerollState.value });
function bubbleContent(m) {
  return bubbleText(m, { field: "content", live: isLive(m), streamText: streamDisplay.value.text, previewText: rerollPreview.value.text });
}
function bubbleReasoning(m) {
  // 思考多一层回落：库里没思考、合成流也没有 ⇒ 照旧用 m.reasoning（与从前一致）。
  return bubbleText(m, { field: "reasoning", live: isLive(m), streamText: streamDisplay.value.reasoning, previewText: rerollPreview.value.reasoning });
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
async function onMessageEdited(result = {}) {
  const sid = sessionID;
  const target = messageEditTarget.value;
  messageEditTarget.value = null;
  await reloadWindow();
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

// undoSummary：**撤销一次压缩**（2026-11-01 用户定）。
//
// 只允许撤合法的（顶层 + 每层最近那次）—— 判定在**后端**（`compact.PlanUndo`），
// 界面那颗按钮亮不亮读的也是同一份判断（`summary.undoable` / 树的 `node.undoable`）。
// 所以正常情况下不会点出 409；真撞上了（比如树是几秒前的快照）就把后端那句话**原样**给人看，
// 别换成"撤销失败" —— 那句"它所在的第 1 层，最近一次压缩不是它"才是他要知道的。
async function undoSummary(summaryID) {
  const sid = sessionID;
  if (!sid || !summaryID) return;
  const my = epoch;
  try {
    await api.undoSummary(sid, summaryID);
  } catch (err) {
    if (my === epoch) toast(`撤销失败：${err.message}`, "error");
    return;
  }
  if (my !== epoch) return;
  toast("已撤销这次压缩", "ok");
  // 装配会**变长**（那一段回到原文）⇒ 消息列、状态、环上的占用都要重算一遍。
  await reloadWindow();
  refreshStateView(sid);
  refreshStatusline("idle");
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
    refreshStateView(sessionID);
    refreshRerolls();
    // 重摇那趟如果还活着 ⇒ 把它的轮询也接回来（不 await：它可能还要摇一阵）。
    resumeRerollIfLive().catch(() => {});
    if (st.phase === "error" && st.error) toast(`上一轮失败了：${st.error}`, "error");
    if (previewOn() && vueMessages.value.length) {
      api.outgoingPreview(sessionID, "")
        .then((w) => { if (my === epoch) payloadbarRef.value?.setWire(w); })
        .catch(() => {});
    }
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
// Agent 列表在"进会话"这一拍会被两条路同时要（顶栏选人 + 草稿首段/状态行）：
// 从前各发一次 GET /agents（实测同一毫秒两发、每发 42KB）。这里让同一拍共享一发。
let agentsInflight = null;
function fetchAgentsOnce() {
  if (!agentsInflight) {
    agentsInflight = api.agents().finally(() => { agentsInflight = null; });
  }
  return agentsInflight;
}
function fillAgentSelect() {
  const my = epoch;
  fetchAgentsOnce().then((data) => {
    if (my !== epoch) return;
    const list = (data.agents ?? []).map(agentOption);
    // 顶栏的圆头像 = Agent 入口（2026-10-14）；状态行小字里那份是"显示"。
    topbarRef.value?.setAgents(list, sessionInfo.agent_id);
    statusAgent.value = list.find((a) => a.id === sessionInfo.agent_id)?.label ?? sessionInfo.agent_id ?? "";
    // 这一发只更新"助手"那格；上下文环由 enterSession 的 refreshStatusline 统一问一次
    //（从前这里也带 withContext，实测同一拍发两次 GET /context）。
    refreshStatusline("idle", undefined, { withContext: false });
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
    const data = await fetchAgentsOnce();
    if (my !== epoch) return;
    vueAgents.value = data.agents ?? [];
  } catch { /* 拿不到就空着：草稿不画首段、状态行回落到 agent id */ }
}
// leftHidden：左栏收没收的**响应式镜像**（真实状态仍是 DOM 类 + 服务器偏好；
// 2026-10-14 用户定：栏开着时顶栏不渲染那一侧的开关 —— 按钮跟着它控制的东西走）。
// （右栏 2026-10-29 搬进上下文工作区 ⇒ 没有"右侧栏收没收"这回事了。）
const leftHidden = ref(false);

// —— 工作区（2026-10-29 用户定）——
// 「每个对话分为俩部分：对话界面 和 上下文工作区 界面」，顶栏右上角（绿环左边）那颗两段开关切。
//
// **不存进 localStorage**：它是"这一下要看哪块"，不是外观偏好。存下来的话，下次打开
// 直接落在空白的工作区里、以为会话是空的 —— 那种"我的消息没了"的惊吓比省一次点击贵得多。
// 换会话也不重置：人正在对照两份上下文时，切会话不该把他弹出工作区。
const workspace = ref("chat");
function selectWorkspace(next) {
  // 只认这两个值：调用方（按钮）本来只发这两个，但这儿再挡一道 ——
  // 将来加了第三块，漏改某处会得到一个**空白区**（v-show 谁都不匹配），那是最难查的一类。
  workspace.value = next === "context" ? "context" : "chat";
  // 面板是 `v-show` 常驻的 ⇒ 里面选中的 tab 会留着（这是对的：回头再看还是那一页），
  // 但"看着压缩 tab"这件事 App 只在 **切 tab 那一下** 知道（`onPayloadTab`）。
  // 于是从聊天切回工作区时，树/状态/载荷都不会自己重拉 —— 按当前 tab 补一发。
  if (workspace.value === "context") refreshContextLazy();
}
function applySideToggle(barID, hidden) {
  document.getElementById(barID)?.classList.toggle("collapsed", hidden);
  if (barID === "sessions-bar") leftHidden.value = hidden;
}

// —— 会话 ——
// —— 侧栏/抽屉（2026-10-10 移动端）—— 同一对按钮两种身份：
// 宽屏 = 布局里的栏（collapsed = 收起）；窄屏 = 覆盖式抽屉（collapsed = 关着）。
// 窄屏一律默认关、不读不写偏好 —— 抽屉的开合是瞬时的事，那是桌面偏好的账。
// 响应式断点：与 main.css 的 @media **同源**（700 = 左栏改抽屉），
// CSS 里没法读 JS 常量，改一处要改两处。
//
// 2026-10-29：右栏搬进上下文工作区（用户：「把右侧栏那个上下文给挪过来」）⇒ **只剩左抽屉**。
// `BP.rightBar` / `mqlRight` / `narrowRight` / `mc_bar_right_hidden` 一起删了 ——
// 留着它们就是留着一条永远走不到的分支（拖拽那边同理，见 `rightDrawer: false` 的注释）。
const BP = { leftDrawer: 700 };
const mqlLeft = window.matchMedia?.(`(max-width: ${BP.leftDrawer}px)`) ?? null;
function narrowLeft() { return !!mqlLeft?.matches; }
function applySideStates() {
  applySideToggle("sessions-bar", narrowLeft() ? true : barLeftHidden());
}
function toggleLeft() {
  const bar = document.getElementById("sessions-bar");
  const hidden = !bar?.classList.contains("collapsed");
  if (!narrowLeft()) saveBarLeftHidden(hidden);
  applySideToggle("sessions-bar", hidden);
}
// 窄屏里"人去了别处"就得把左抽屉收回去（选会话 / 开新对话）。
function closeLeftDrawer() { if (narrowLeft()) applySideToggle("sessions-bar", true); }

// —— 滑动面板（触屏**跟手拖拽**；2026-10-10 用户定/细化）——
// 拖到哪儿面板停哪儿（**半开也维持**，不用等松手）；松手按"过半 + 速度"吸附开/关。
// 方向：左滑出右栏、右滑出左栏；已有一扇开着 ⇒ 先关后开（只认朝它关回去的方向）。
// 最终仍落在同一个 collapsed 类上（手势 = [◧]/[◨] 的替身，不另造开合状态）。
// 判定全在 utils/swipe.js（纯函数）；这里只碰 DOM：跟手期间写内联 transform（transition 先掐掉），
// 松手把内联清掉、按结果落类 —— 由 CSS 自己的 200ms 过渡完成吸附动画。
// 只剩左抽屉（右栏 2026-10-29 搬进上下文工作区）⇒ 不再按 `bar` 分派：
// 两个 helper 一起收掉，直接写死左栏那两枚 id。
// 为什么不留着通用版：它们指向的 DOM（`#payload-bar` / `#scrim-right`）已经不存在了，
// 留着就是两条永远拿到 null 的分支 —— 而 null 是静默的（拖拽照样跑，只是什么都不动）。
// `pickDrag` 本身仍是通用纯函数（有它自己的测试），我们只是永远不给它"右边能开"的输入。
let panelDrag = null;

function swipeBlocked(target) {
  if (overlaysRef.value?.isPaletteOpen() || overlaysRef.value?.isPickerOpen()) return true;
  if (document.querySelector("#settings-view") || document.querySelector(".modal")) return true;
  // 抽屉自己身上 ⇒ 一律接管（pre 也不例外）："把抽屉拖走"在这儿就该能用 ——
  // 右栏的载荷 tab 整块就是个 <pre>，早先被 pre 例外吞掉，真机上表现为"在面板上滑没反应"。
  if (target?.closest?.("#sessions-bar")) return false;
  return !!target?.closest?.("textarea, input, select, pre, .palette, .picker, .modal, #settings-view");
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
    const pick = pickDrag({
      dx,
      leftDrawer: narrowLeft(),
      // 右栏已经没了（2026-10-29 搬进上下文工作区）⇒ 永远"没有右抽屉、也没开着"。
      // 写死这两格而不是删参数：`pickDrag` 是通用纯函数（有自己的测试），
      // 这两格是**它的输入**，不是它的 identity —— 将来真要再加一根右栏，改回来就是。
      rightDrawer: false,
      leftOpen: !!left && !left.classList.contains("collapsed"),
      rightOpen: false,
    });
    if (!pick) { panelDrag = null; return; }
    d.axis = axis; // ⚠ 必须记住：不然每次 touchmove 都重进这里，第二枚就被自己摘掉的 collapsed 骗死
    d.pick = pick;
    d.p = pick.base; // ⚠ 起点开度（开=0 / 关=1）：不初始化，第一枚 move 的速度会算出天文数字、
    //   被"快甩"规则误判成弹回（实测咬过一次）。⚠ **别动 d.lastT**：它在 touchstart 起表，
    //   重置成此刻的话第一枚 move 的 dt≈0、速度样本直接作废（快甩失灵，也实测咬过）。
    d.el = pick.bar === "left" ? left : right;
    d.scrim = document.getElementById("scrim-left");
    d.w = d.el.getBoundingClientRect().width || 300;
    d.el.style.transition = "none"; // 跟手期间不许有过渡
    if (d.scrim) d.scrim.style.transition = "none";
    applySideToggle("sessions-bar", false); // 拖起来先亮出来，位置全由内联管
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
  applySideToggle("sessions-bar", !open);
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
  // `id: ""` 是必须的：`Object.assign` **不清旧键**，从一条真会话切到草稿时
  // `sessionInfo.id` 会留着上一条的 id —— 于是"这是草稿吗"就答错了（曾经就没人读它，
  // 所以一直没露；上下文工作区要读它才露出来）。
  Object.assign(sessionInfo, {
    id: "", title: "", provider: draftPrefs.provider, model: draftPrefs.model,
    agent_id: draftPrefs.agent, system_prompt: "",
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
  sessionbarRef.value?.setSessions(sessions, sessionID);
}
async function refreshSessionsBar() {
  try {
    renderSessions(await api.listSessions());
  } catch (err) { toast(`刷会话栏失败：${err.message}`, "error"); }
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
  // 开机恢复那条路已经拉过一次 `/sessions`（boot），别在同一毫秒再要一遍 ——
  // 远端部署时那是一整个 RTT + 一份完整会话列表。
  const sessions = preloaded ?? await api.listSessions();
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
    }
  }).catch(() => {});
  } finally {
    if (my === epoch) loadingSession.value = false;
  }
}
async function boot() {
  // 自检按候选顺序探（没存过地址时：先"跟页面同主机的 8787"，再"同源 /api/v1"兜底反代部署）。
  //
  // 2026-11-01：探的口从 `/auth/status` 换成 `/health`（鉴权砍了，那个口也没了）。
  // 判据照旧两道（都是踩过的坑）：地址先 `normalizeApiBase`（裸地址会被 fetch 当相对路径、
  // 被 vite 的 200 HTML 骗过）；回体必须是 JSON 且 `status:"ok"`。
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
  // 压缩面板的默认块数读的是实例级 chat 配置（现在没有"设置专属"这回事了：本机单人）。
  api.getChat().then((c) => { compactDefault.value = c?.compact_blocks ?? null; }).catch(() => {});
  // 回到"上次在看的那条会话"（2026-10-11 用户定）：刷新 / 被安卓丢弃后重载 ⇒ 不再是一张空草稿。
  // 记的 id 取不到（删了 / 换人登录 / 别的设备）⇒ 回落草稿 —— 取不到就是取不到，不猜。
  const last = lastSessionId();
  if (last) {
    const sessions = await api.listSessions().catch(() => []);
    if (sessions.some((s) => s.id === last)) {
      await enterSession(last, sessions);
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
// liveTurnPoll：**本地此刻有几条轮询循环真的在跑**（0 或 1）。
//
// 为什么需要这个数，而不是"看有没有 stream 气泡"：气泡是**一次性的**，循环死掉（网络错、
// 后台被冻住之后异常退出）而气泡还留在列表里 —— 那个气泡就是僵住的假象。判"要不要接回来"
// 必须看循环本身在不在。
let liveTurnPoll = 0;

async function pollTurn(sid, turnID) {
  const my = epoch;
  liveTurnPoll++;
  const m = { who: "助手", role: "assistant", _stream: true, stream: true, content: "", reasoning: "", id: tmpKey() };
  vueMessages.value.push(m);
  vueStream.value = { id: turnID, text: "", reasoning: "" };
  resetTypewriter();
  composerRef.value?.setRunning(true);
  // 受理这一刻用户那句已经进库了 ⇒ 占用本来就变了：问一次（这一发整轮只问这一次，
  // 轮询里靠 withContext:false 把它省掉；见 refreshStatusline）。
  refreshStatusline("pending");
  let cur = { from: 0, thinkFrom: 0 };
  let misses = 0; // 连续几拍没拿到（切后台/断网那几下会这样）
  try {
    for (;;) {
      await new Promise((r) => setTimeout(r, 300));
      if (my !== epoch) return; // 会话世代变了（切走/回草稿）：这一轮不再往界面上写（库里照旧落）
      let st;
      let slice;
      try {
        st = await api.status(sid);
        slice = await api.turnText(sid, cur.from, cur.thinkFrom);
        misses = 0;
      } catch (err) {
        // **一拍失败不判死刑**（2026-11-01）：切到后台、手机换网、App 被冻住再醒 ——
        // 那几拍请求本来就会失败。从前这里直接抛出去 ⇒ 轮询循环当场死掉、而气泡还留在
        // 列表里 ⇒ 界面就一直"在等待"（用户报的正是这个）。连着十拍都不行才算真不行。
        if (++misses > 10) throw err;
        continue;
      }
      if (my !== epoch) return;
      // 游标回退（缓冲被新一轮重置）⇒ 清零重建（mergeStreamSlice 的语义，别在别处再写一份）。
      const merged = mergeStreamSlice(
        { text: vueStream.value.text ?? "", reasoning: vueStream.value.reasoning ?? "" }, cur, slice);
      vueStream.value.text = merged.text;
      vueStream.value.reasoning = merged.reasoning;
      setStreamTarget(merged.text, merged.reasoning);
      m.reasoning = merged.reasoning;
      cur = merged.cur;
      // 轮询期间只推相位（本地写，不发请求）；左栏与占用等这一轮落地再刷 ——
      // 从前这里是每 300ms 一次 GET /sessions + GET /context（一条 30 秒的回复 ≈ 200 个请求，
      // 且 /context 每次都要全量装配：列消息 + 现演状态 + 拼出站）。见 refreshStatusline 的 withContext。
      refreshStatusline(st.phase, st.elapsed_ms, { withContext: false });
      if (st.phase === "idle" || st.phase === "error") {
        if (my !== epoch) return;
        const failed = st.phase === "error";
        await waitTypewriter();
        if (my !== epoch) return;
        await reloadWindowKeepingScroll();
        resetTypewriter();
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
    liveTurnPoll--;
    composerRef.value?.setRunning(false);
  }
}
let connText = "未连接";
// statusAgent：当前助手的显示名（顶栏去壳后，它住在状态行小字里；数据在进会话时现取）。
const statusAgent = ref("");
// 后台任务行（/tasks 是进程内事实；2s 一拉 —— 端点零成本，也别等太久看不见压缩/标题这些短命任务）。
const TASKS_POLL_MS = 2000;
// cancelTask：按停**一个**原子任务（2026-11-01 用户定：「任务菜单里，我希望有办法取消任务」）。
//
// `canceled:false` 不是错误：面板轮询里那些 id 随时可能"刚好跑完"。这种时候什么都不说
// （补刷一次面板，那一行自己就消失了）—— 弹一条"取消失败"只会让人以为自己点慢了。
//
// ⚠ 按下之后**不立刻**从面板上抹掉那一行：真正停不停由那一发自己决定（ctx 取消是**请求**）。
// 我们只是补刷一次，让它按真实状态变（多半下一秒就翻成"取消"）。
function cancelTask(id) {
  api.cancelTask(id)
    .then((res) => {
      if (!res?.canceled) toast("这一发已经结束了");
      refreshTasks();
      // 按停的可能是一趟重摇 ⇒ 两族的界面状态也当场对齐（别等轮询或者下一次进会话）。
      // 轮询那两条路自己会写回，但"面板按停"不一定有轮询在跑（例如换了标签页回来才点的）。
      refreshReroll();
      refreshRerollSummary();
    })
    .catch((err) => toast(`按停失败：${err.message}`, "error"));
}
// cancelQueue：取消一个**队列任务**（2026-11-01）—— 在飞的那一发会被一起掐断。
// 取消完立刻补刷一次面板，别等那 2 秒的节拍（人按了按钮就该马上看见变化）。
function cancelQueue(id) {
  api.cancelQueue(id)
    .then(() => refreshTasks())
    .catch((err) => toast(`取消失败：${err.message}`, "error"));
}
// afterCopy：**创建副本**之后（2026-11-01）—— 刷左栏让它出现，然后直接进副本。
//
// 为什么刷了左栏还要再进一次：副本是"接着在这儿往下写"的东西，
// 停在原会话里会让人以为点了没反应（副本在左栏、视线还在弹窗上）。
function afterCopy(fresh) {
  if (!fresh?.id) { toast("复制完成了，但没拿到新会话 id", "error"); return; }
  toast(`已创建副本「${fresh.title || "未命名"}」`, "ok");
  refreshSessionsBar();
  enterSession(fresh.id).catch((err) => toast(`进副本失败：${err.message}`, "error"));
}
let tasksTimer = null;
async function refreshTasks() {
  try {
    const board = await api.tasks();
    topbarRef.value?.setTasks(board);
  } catch { topbarRef.value?.setTasks(null); }
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
function refreshStatusline(phase, elapsedMs, { withContext = true } = {}) {
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
  // 量不到就别写（2026-10-29）：切到上下文工作区时输入行是 `display:none`，量出来是 0 ——
  // 那不是"输入行 0 高"，是"这一下没在量"。照写会让 `--composer-h` 掉到 0，
  // 切回聊天时消息列底部的留白先塌一下、等 ResizeObserver 回调再补回来（一帧的跳动，
  // 眼睛看得见、代码看不出来）。
  if (h <= 0) return;
  overlaysRef.value?.lift(Math.max(0, h - 60));
  // 输入行现在是**悬浮层**（绝对定位）⇒ 消息列底部留白得知道它多高（textarea 会长高，写死会错位）。
  document.getElementById("chat-col")?.style.setProperty("--composer-h", `${Math.round(h)}px`);
}
// 输入框高度是异步变的（autosize 在 nextTick 里长高）⇒ 量一次不够，盯住它本身。
let composerRO = null;
// 顶栏高度（2026-11-02）：窄屏模型名允许换行后栏会变高，消息列顶部留白必须跟着量，
// 否则第一行消息会被顶栏盖住（和 composerRO 同一个道理）。
let topbarRO = null;
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
  // 2026-11-01：账号砍了 ⇒ 没有"只有管理员能进"这回事（本机单人使用）。
  settingsOnly.value = "";
  settingsOpen.value = true;
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
// openManage：左栏底部的「会话 / 渠道」—— 同一个视图、只显示那一页（不经过设置 tab）。
function openManage(pane) {
  settingsOnly.value = pane === "provider" ? "provider" : "session";
  settingsOpen.value = true;
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
// 外观那一区改完偏好 ⇒ 聊天壳上活着的开关立刻跟上（命令菜单是唯一现在就长短不一的那个）。
function onPrefsSaved() {
  cmdMenu.value = commandMenuOn();
  if (!chromeHideOn()) resetChrome(document.querySelector("#messages-vue")); // 关掉这功能 ⇒ 栏立刻回来
  if (!cmdMenu.value) overlaysRef.value?.hidePalette();
}
function closeSettings() {
  settingsOpen.value = false;
  settingsOnly.value = "";
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
// backToChat：**侧栏动作要把人带回对话区**（点会话 / 点＋新对话 / 当前会话被删）—— 唯一出口。
//
// 为什么要有它（2026-10-20 用户报："设置里点左侧会话会退出设置进对话，用户视图点了没反应"）：
// 2026-11-01 之前这里是"设置与用户视图两块的让位"（那个 bug：`closeUserView()` 定义了却
// 从没被调用，于是侧栏动作在用户视图里全失灵）。用户视图随账号一起删了 ⇒ 只剩设置要让位，
// 但**这道出口留着**：往后再加"与对话平级的视图"时，别再成对地手写两个 close。
//
// ⚠ 只给**侧栏动作**用它：命令面板（`/resume` `/new` `/delete` 那些）与启动恢复路径**不需要** ——
// 命令面板挂在聊天壳里（`#chat-col` 有 `v-show`），那两个视图开着时根本触发不到。
function backToChat() {
  closeSettings();
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
  // 跨断点（桌面 ⇄ 抽屉）时把状态重算一遍：变成抽屉就一律先关掉，不然会"啪"地盖上来。
  // 只剩左栏这一个断点（右栏 2026-10-29 搬进上下文工作区）。
  mqlLeft?.addEventListener?.("change", applySideStates);
  // 回到前台就对齐（2026-11-01 用户定）。三条都要：
  //   visibilitychange —— 切标签页/切 App（手机最常见的那条路）；
  //   pageshow         —— 从 bfcache 里恢复（安卓丢页面再回来常走这条，那时 visibilitychange 可能不响）；
  //   focus            —— 同窗口切焦点（桌面 alt+tab 到别的窗口）。
  document.addEventListener("visibilitychange", resyncOnForeground);
  window.addEventListener("focus", resyncOnForeground);
  window.addEventListener("pageshow", resyncOnForeground);
  if (typeof ResizeObserver !== "undefined" && !composerRO) {
    composerRO = new ResizeObserver(() => liftOverlays());
    const form = document.querySelector("#composer");
    if (form) composerRO.observe(form);
  }
  if (typeof ResizeObserver !== "undefined" && !topbarRO) {
    const bar = document.querySelector("#chat-col > header");
    if (bar) {
      const sync = () => {
        const h = bar.getBoundingClientRect().height;
        // 量不到（切工作区等）就别写 0 —— 那会让消息列顶到栏里去。
        if (h > 0) document.getElementById("chat-col")?.style.setProperty("--topbar-h", `${Math.round(h)}px`);
      };
      topbarRO = new ResizeObserver(sync);
      topbarRO.observe(bar);
      sync();
    }
  }
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
  document.removeEventListener("touchstart", onTouchStart);
  document.removeEventListener("touchmove", onTouchMove);
  document.removeEventListener("touchend", onTouchEnd);
  document.removeEventListener("touchcancel", onTouchCancel);
  if (tasksTimer) clearInterval(tasksTimer);
  tasksTimer = null;
  composerRO?.disconnect();
  composerRO = null;
  topbarRO?.disconnect();
  topbarRO = null;
  mqlLeft?.removeEventListener?.("change", applySideStates);
  document.removeEventListener("visibilitychange", resyncOnForeground);
  window.removeEventListener("focus", resyncOnForeground);
  window.removeEventListener("pageshow", resyncOnForeground);
});
</script>

<template>
  <div id="app">
    <div id="columns">
      <SessionBar
        ref="sessionbarRef"
        @toggle-left="toggleLeft"
        @select="(id) => { closeLeftDrawer(); backToChat(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
        @settings="(id) => { sessionSettingsId = id; }"
        @close="askDeleteSession"
        @new="newSession"
        @open-settings="openSettings"
        @open-sessions="() => openManage('session')"
        @open-providers="() => openManage('provider')"
      />
      <div id="scrim-left" @click="toggleLeft"></div>
      <div id="content">
      <section id="chat-col" v-show="!settingsOpen">
    <TopBar
      ref="topbarRef"
      :left-hidden="leftHidden"
      :workspace="workspace"
      @select-workspace="selectWorkspace"
      @toggle-left="toggleLeft"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${err.message}`, 'error'))"
      @select-agent="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, 'error'))"
      @cancel-queue="cancelQueue"
      @cancel-task="cancelTask"
    />
        <main id="messages-vue" v-show="workspace === 'chat'">
          <!-- 滚动容器（#messages-vue）现在**占满整个对话区**（滚动条因此贴最右缘，2026-10-14 用户定）；
               阅读列约束（760 居中、两侧 56px、上下留白）落在这一层 —— 气泡别拉成整屏长句。 -->
          <div class="messages-inner">
          <!-- 会话加载骨架（2026-11-02）：先撑住消息列，别让人对着纯黑猜"是不是坏了"。 -->
          <div v-if="loadingSession" class="chat-skeleton" aria-label="正在加载会话" role="status">
            <span class="sk-line sk-a"></span>
            <span class="sk-line sk-b"></span>
            <span class="sk-line sk-c"></span>
          </div>
          <!-- 空草稿首屏（2026-11-02）：从前点「开启新对话」得到的是一整页纯黑 ——
               没有欢迎语、没有当前助手/模型，第一眼像坏了。这里补一句人话 + 当前配置；
               真正的第一条消息仍然由输入框发起（不伪造历史）。 -->
          <div v-if="draft && !chatRows.length" class="draft-welcome">
            <h2>有什么想聊的？</h2>
            <p class="dw-sub">{{ draftAgentName }}<span class="dw-sep">·</span>{{ draftModelName }}</p>
            <p class="dw-hint">直接在下面输入第一条消息开始；也可以用 <kbd>/</kbd> 开头的命令。</p>
          </div>
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
              :message-id="row.msg.messageId"
              :prompt-id="row.msg.messageId === lastMsgId ? latestPromptId : null"
              :is-last="!!row.msg.messageId && row.msg.messageId === lastMsgId"
              :streaming="!!row.msg.stream || isLive(row.msg)"
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
      v-show="workspace === 'chat'"
      ref="composerRef"
      :menu-on="cmdMenu"
      @input-text="onComposerInput"
      @keydown="onComposerKeydown"
      @submit="onComposerSubmit"
      @stop="stopCurrent"
      @menu="() => overlaysRef?.showPalette(filterCommands(''))"
    />
    <!-- 上下文工作区（2026-10-29 用户定）：与消息列 + 输入行互斥 —— 同一块地方换个视角看。
         顶栏不在切换范围内（它自己带着那颗开关，切掉就找不回来了）。 -->
    <ContextWorkspace
      v-show="workspace === 'context'"
      ref="payloadbarRef"
      :has-session="!!sessionInfo.id"
      :default-blocks="compactDefault"
      @compact="(n) => compactNow(n ?? undefined)"
      @compact-payload="previewCompactPayload"
      @tab="onPayloadTab"
      @undo="undoSummary"
    />
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
        @prefs-saved="onPrefsSaved"
        @goto-session="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
      />
      </div>
    </div>
    <Toast ref="toastRef" />
    <!-- 更新条：只在检测到新版本时出现（挂根层 —— 设置视图开着也该看得见）。 -->
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
      @copy="(fresh) => { sessionSettingsId = null; afterCopy(fresh); }"
    />
  </div>
</template>
