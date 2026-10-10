<script setup>
// App.vue —— 三栏骨架 + 全部接线（原 main.js + 九个桥，合一）。
// 组件直接挂模板，ref 直调实例方法，不再经 *-vue.js 桥 / mount.js。
import { ref, reactive, computed, onMounted, onUnmounted } from "vue";
import { mergeStreamSlice } from "./api/client.js";
import { parseCommand, filterCommands, COMMAND_META } from "./utils/commands.js";
import { uuidv7 } from "./utils/ids.js";
import { buildCompactTree } from "./utils/tree.js";
import { applyBackground } from "./utils/background.js";
import { sharedApi, previewOn, applyTheme, wantsSend } from "./utils/prefs.js";
import {
  formatStatusLine,
  statusOverBudget,
  groupModelsByProvider,
  deletionPlanSummary,
  modelKey,
  parseModelKey,
  modelOption,
  agentOption,
  whoText,
  sessionTitle,
} from "./utils/format.js";
import TopBar from "./components/TopBar.vue";
import SessionBar from "./components/SessionBar.vue";
import PayloadBar from "./components/PayloadBar.vue";
import MessageBubble from "./components/MessageBubble.vue";
import Composer from "./components/Composer.vue";
import Toast from "./components/Toast.vue";
import SettingsModal from "./components/SettingsModal.vue";
import SessionSettings from "./components/SessionSettings.vue";
import Overlays from "./components/Overlays.vue";

const api = sharedApi();
applyTheme();
applyBackground();

// —— 组件 refs（直调实例方法，替代桥） ——
const toastRef = ref(null);
const topbarRef = ref(null);
const sessionbarRef = ref(null);
const payloadbarRef = ref(null);
const composerRef = ref(null);
const overlaysRef = ref(null);
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
const vueStream = ref({ id: null, text: "", reasoning: "" });
const vueDisplayMode = ref("chat");
const settingsOpen = ref(false);
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
  renderHistory(await api.listMessages(sessionID));
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
// lastMessageList：最近一次渲染用的原始消息（派生物用 —— 压缩树拿它当底）。
let lastMessageList = [];
function renderHistory(messages) {
  lastMessageList = messages ?? [];
  vueMessages.value = [];
  vueStream.value = { id: null, text: "", reasoning: "" };
  for (const m of messages) {
    vueMessages.value.push({
      who: m.role === "user" ? "你" : "助手",
      role: m.role,
      content: m.content,
      reasoning: m.reasoning,
      reasoningMs: m.reasoning_ms,
      messageId: m.id,
      id: m.id,
    });
  }
  scrollBottom();
  refreshTree();
}
// refreshTree：压缩树 = 现取摘要 + 本地建树（两个 GET 就够，后端零改动）。
async function refreshTree() {
  if (!sessionID) { payloadbarRef.value?.setTree(null); return; }
  const my = epoch;
  try {
    const sums = await api.listSummaries(sessionID);
    if (my !== epoch) return;
    payloadbarRef.value?.setTree(buildCompactTree(lastMessageList, sums));
  } catch { if (my === epoch) payloadbarRef.value?.setTree(null); }
}
const lastMsgId = computed(() => [...vueMessages.value].reverse().find((m) => m.messageId)?.messageId ?? null);
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
  renderHistory(await api.listMessages(sid));
  refreshStateView(sid);
  refreshReroll();
}
// —— 重摇（尾条气泡的 ⟳ / ◀ ▶ 与 /reroll 命令共用）——
// 口径：这只是**重摇**（`reroll-message`，就地换正文、UUID 不变），不是"重发历史"
// （`/resend` 那条路归空输入点发送）。后端要求目标**必须是尾条 assistant** ⇒ 只挂最新一条。
//
// 交互（2026-10-10 用户定）：⟳ = 摇新一版并**立刻切到最新那版**（无论当前看着哪一版），
// 结果就地换正文；模式活着期间气泡下出现 `◀ n/N ▶`：◀▶ 翻版，**最右的 ▶ = 再摇一版**。
const rerollState = ref({ active: false });
// 候选流预览（就地显示在目标气泡里）：running 期间靠 `/turn/text` 攒；任何一次落地/切版都清掉。
const rerollPreview = ref({ text: "", reasoning: "" });
function clearRerollPreview() { rerollPreview.value = { text: "", reasoning: "" }; }
function rerollPreviewActive(m) {
  return !!m.messageId && rerollState.value.target_message_id === m.messageId
    && (rerollPreview.value.text !== "" || rerollPreview.value.reasoning !== "");
}
// 气泡内容的三个来源：合成流（一轮）> 候选预览（重摇）> 库内正文。
function bubbleContent(m) {
  if (m.stream) return vueStream.value.text;
  if (rerollPreviewActive(m)) return rerollPreview.value.text || m.content;
  return m.content;
}
function bubbleReasoning(m) {
  if (m.stream) return vueStream.value.reasoning ?? m.reasoning;
  if (rerollPreviewActive(m)) return rerollPreview.value.reasoning || m.reasoning;
  return m.reasoning;
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
  try {
    await api.rerollEnter(sid); // 202：进模式并立刻摇一版；已在模式里 ⇒ 再摇一版
  } catch (err) {
    toast(`重摇失败：${err.message}`, "error");
    return;
  }
  const st = await pollReroll(sid);
  if (my !== epoch) return;
  if (st?.error) { toast(`重摇失败：${st.error}`, "error"); clearRerollPreview(); return; }
  if (!st?.active) return;
  await switchReroll(sid, st.count); // 最新那版的位次就是 count（1-based）
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
  const msgs = await api.listMessages(sid).catch(() => null);
  if (my !== epoch || !msgs) return;
  renderHistory(msgs);
  clearRerollPreview();
  refreshStateView(sid);
  refreshStatusline("idle");
  await refreshReroll();
}

// compactNow：受理一次压缩（命令面板与右栏压缩面板共用一条路）；`blocks` 空 = 用配置默认。
async function compactNow(blocks) {
  const sid = sessionID;
  if (!sid) { toast("先发一句话：草稿里还没有会话可压"); return; }
  try {
    const accepted = await api.compact(sid, blocks);
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
    renderHistory(await api.listMessages(sid));
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
    topbarRef.value?.setAgents(list, sessionInfo.agent_id);
    // 没 agent 时别猜 list[0]：后端认的是 default_agent。
    if (!sessionInfo.agent_id && list.length) applyAgentValue(data.default_agent || list[0].id);
  }).catch((err) => { if (my === epoch) toast(`Agent 列表失败：${err.message}`, "error"); });
}
async function applyAgentValue(id) {
  if (!id) return;
  if (draft) {
    draftPrefs.agent = id;
    Object.assign(sessionInfo, { agent_id: id });
    paintSessionHeader();
    refreshDisplayMode();
    refreshStatusline("idle");
    refreshPreview();
    return;
  }
  const my = epoch;
  const updated = await api.patchSession(sessionID, { agent_id: id });
  if (my !== epoch) return;
  Object.assign(sessionInfo, updated);
  paintSessionHeader();
  refreshDisplayMode();
  refreshStatusline("idle");
  refreshPreview();
}
async function refreshDisplayMode() {
  const my = epoch;
  try {
    const data = await api.agents();
    if (my !== epoch) return;
    const a = (data.agents ?? []).find((x) => x.id === sessionInfo.agent_id);
    vueDisplayMode.value = a?.display_mode === "roleplay" ? "roleplay" : "chat";
  } catch { /* 拿不到就 chat 兜底 */ }
}
function applySideToggle(barID, hidden) {
  document.getElementById(barID)?.classList.toggle("collapsed", hidden);
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
    closeSettings(); // 被删的是当前会话 ⇒ 接下来要去草稿：设置视图让位
    await startDraft(); // 删掉当前会话 ⇒ 回到草稿（不再预建空会话 —— 那正是被拿掉的旧行为）
  } else refreshSessionsBar();
}
async function startDraft() {
  ++epoch;
  draft = true;
  sessionID = null;
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
  refreshDisplayMode();
  fillModelSelect();
  fillAgentSelect();
  payloadbarRef.value?.setWire(null);
  payloadbarRef.value?.setTables({});
  refreshStatusline("idle");
  refreshSessionsBar();
  refreshReroll();
  refreshTree();
  refreshPreview(); // 输入框里若还留着字（＋ 时不清输入框），预演立刻跟上
}
function newSession() {
  closeLeftDrawer(); // 窄屏：抽屉让位（人已经去新对话了）
  closeSettings(); // 侧栏动作 = 把人带回对话区（设置视图让位）
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
  const [sessions, messages] = await Promise.all([api.listSessions(), api.listMessages(id)]);
  if (my !== epoch) return; // 迟到的上一发：整段丢掉（切会话竞态的唯一守卫）
  Object.assign(sessionInfo, sessions.find((s) => s.id === id) ?? {});
  paintSessionHeader();
  refreshDisplayMode();
  fillModelSelect();
  fillAgentSelect();
  renderSessions(sessions);
  renderHistory(messages);
  refreshStatusline("idle");
  refreshStateView(id);
  refreshReroll();
  if (previewOn() && messages.length) {
    api.outgoingPreview(id, "").then((w) => { if (my === epoch) payloadbarRef.value?.setWire(w); })
      .catch((err) => { if (my === epoch) toast(`预演失败：${err.message}`, "error"); });
  } else {
    payloadbarRef.value?.setWire(null); // 别把上一条会话的载荷挂在空会话上
  }
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
  const health = await api.health();
  connText = `已连接 v${health.version ?? "?"}`;
  api.getChat().then((c) => { compactDefault.value = c?.compact_blocks ?? null; }).catch(() => {});
  // 不再"删空会话 + 预建真会话"：web 走草稿态，库里不产生空行（旧编排的两步一起拿掉）。
  await startDraft();
}

// —— 发送 / 轮询 ——
async function send(content) {
  const sid = sessionID;
  const accepted = await api.sendMessage(sid, content);
  addMessage("你", content, "user");
  await pollTurn(sid, accepted.turn.message_id);
}
async function resend() {
  const sid = sessionID;
  const accepted = await api.resend(sid);
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
        renderHistory(await api.listMessages(sid));
        if (failed) {
          // 失败合成一条气泡摆在文末（renderHistory 会清 vueStream，所以必须摆在它后面）+ toast 兜底。
          const errText = `失败：${st.error || "未知"}`;
          vueMessages.value.push({ who: "助手", role: "assistant", content: errText, id: tmpKey() });
          scrollBottom();
          toast(errText, "error");
        }
        refreshStatusline(st.phase, st.elapsed_ms);
        refreshSessionsBar();
        refreshStateView(sid);
        refreshReroll();
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
function refreshStatusline(phase, elapsedMs) {
  if (draft || !sessionID) {
    // 草稿：没有会话可问占用 —— 状态行只报"新对话 + 将来会用的模型"。
    const who = whoText(sessionInfo.provider, sessionInfo.model, "未选模型");
    composerRef.value?.setStatus(`${connText}｜新对话｜${who}｜${phase ?? "idle"}`, false);
    return;
  }
  api.context(sessionID).catch(() => null).then((ctx) => {
    composerRef.value?.setStatus(`${connText}｜` + formatStatusLine({
      ctx, phase, elapsedMs, provider: sessionInfo.provider, model: sessionInfo.model,
    }), statusOverBudget(ctx));
  }).catch((err) => toast(`状态行失败：${err.message}`, "error"));
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
  if (form) overlaysRef.value?.lift(Math.max(0, form.getBoundingClientRect().height - 60));
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
    const sid = sessionID;
    const my = epoch;
    await api.rerollSwitch(sid, Number(args[0] ?? 0));
    const msgs = await api.listMessages(sid);
    if (my !== epoch) return;
    renderHistory(msgs);
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
    const messages = await api.listMessages(sid);
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
        renderHistory(await api.listMessages(sid));
        refreshStateView(sid);
        refreshReroll();
      },
    );
  },
  edit: async (args) => {
    const messages = await api.listMessages(sessionID);
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
      async (it) => {
        const next = window.prompt("改摘要正文", it.value.text ?? "");
        if (next === null || !next.trim()) return;
        await api.editSummary(sessionID, it.value.id, next);
        toast("摘要已改", "ok");
      },
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
function onComposerInput(v) {
  liftOverlays();
  if (v.startsWith("/")) {
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
  Object.assign(sessionInfo, s);
  paintSessionHeader();
  refreshSessionsBar();
  let accepted;
  try {
    accepted = await api.sendMessage(s.id, text);
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

// —— 设置视图（与对话同级，2026-10-10） ——
// 窄屏：进/出设置都把左抽屉归位（手机上别让它压着视图）；宽屏：不动侧栏（并排看没问题）。
function openSettings() {
  settingsOpen.value = true;
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
function closeSettings() {
  settingsOpen.value = false;
  if (narrowLeft()) applySideToggle("sessions-bar", true);
}
// —— 拖拽调宽 ——
onMounted(() => {
  applySideStates();
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
  composerRO?.disconnect();
  composerRO = null;
  mqlLeft?.removeEventListener?.("change", applySideStates);
  mqlRight?.removeEventListener?.("change", applySideStates);
});
</script>

<template>
  <div id="app">
    <div id="columns">
      <SessionBar
        ref="sessionbarRef"
        @select="(id) => { closeLeftDrawer(); closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
        @settings="(id) => { sessionSettingsId = id; }"
        @close="askDeleteSession"
        @new="newSession"
        @open-settings="openSettings"
      />
      <div id="scrim-left" @click="toggleLeft"></div>
      <div id="content">
      <section id="chat-col" v-show="!settingsOpen">
    <TopBar
      ref="topbarRef"
      @toggle-left="toggleLeft"
      @toggle-right="toggleRight"
      @select-model="(v) => applyModelValue(v).catch((err) => toast(`换模型失败：${err.message}`, 'error'))"
      @select-agent="(id) => applyAgentValue(id).catch((err) => toast(`换 Agent 失败：${err.message}`, 'error'))"
    />
        <main id="messages-vue">
          <MessageBubble
            v-for="m in vueMessages"
            :key="m.id ?? 'stream'"
            :who="m.who"
            :role="m.role"
            :content="bubbleContent(m)"
            :reasoning="bubbleReasoning(m)"
            :reasoning-ms="m.reasoningMs"
            :message-id="m.messageId"
            :is-last="!!m.messageId && m.messageId === lastMsgId"
            :display-mode="vueDisplayMode"
            :streaming="!!m.stream"
            :reroll="m.messageId && rerollState.active && rerollState.target_message_id === m.messageId ? rerollState : null"
            @save="onBubbleSave"
            @delete="onBubbleDelete"
            @reroll="rerollNow"
            @reroll-go="rerollGo"
          />
        </main>
    <Composer
      ref="composerRef"
      @input-text="onComposerInput"
      @keydown="onComposerKeydown"
      @submit="onComposerSubmit"
      @stop="stopCurrent"
      @menu="() => overlaysRef?.showPalette(filterCommands(''))"
    />
      </section>
      <PayloadBar v-show="!settingsOpen" ref="payloadbarRef" :default-blocks="compactDefault" @compact="(n) => compactNow(n ?? undefined)" />
      <SettingsModal
        v-if="settingsOpen"
        :ask="askConfirm"
        :esc-blocked="!!sessionSettingsId"
        @close="closeSettings"
        @toggle-left="toggleLeft"
        @sessions-changed="refreshSessionsBar"
        @goto-session="(id) => { closeSettings(); enterSession(id).catch((err) => toast(`进会话失败：${err.message}`, 'error')); }"
      />
      <div id="scrim-right" @click="toggleRight"></div>
      </div>
    </div>
    <Toast ref="toastRef" />
    <!-- Overlays 挂根层（不在聊天视图里）：设置视图开着时"确认"也要能弹（fixed 定位照旧）。 -->
    <Overlays ref="overlaysRef" @run-command="(n) => runCommand(n, []).catch((err) => toast(`命令失败：${err.message}`, 'error'))" @pick="onPickAction" />
    <SessionSettings
      v-if="sessionSettingsId"
      :session-id="sessionSettingsId"
      @close="sessionSettingsId = null"
      @renamed="() => { refreshSessionsBar(); if (sessionSettingsId === sessionID) enterSession(sessionID).catch(() => {}); sessionSettingsId = null; }"
    />
  </div>
</template>
