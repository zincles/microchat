<script setup>
// 设置**视图**（2026-10-10 起与对话同级，不再是弹窗 —— OpenWebUI 那种）：占满左栏右边的整块区域，
// 自带小头（[◧] 左栏开关 · 设置 · [×] 返回对话）。子编辑器（Agent/Provider）改为对应 tab 内的二级页（列表 ⇄ 详情就地切换）。
// 「界面」一族的个人偏好（主题/背景图/发送键/预演）已搬到**用户视图**（与设置平级的另一半）。
// 关走 close（Esc/×）—— 视图不卸载就丢状态的口径照旧：v-if 挂载，进视图拉一次。
import { ref, onMounted, onUnmounted, watch, nextTick } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import AgentEditor from "./AgentEditor.vue";
import SegmentEditor from "./SegmentEditor.vue";
import assistantPreset from "../../presets/assistant.json";
import rpPreset from "../../presets/rp-agent.json";
import ProviderEditor from "./ProviderEditor.vue";
import { ICONS } from "./icons.js";
import { sharedApi, apiBase, apiToken, saveNetPrefs } from "../utils/prefs.js";
import { SETTINGS_TABS } from "../utils/format.js";
import { downloadJSON } from "../utils/download.js";
import { buildCartridge, cartridgeAgent } from "../utils/cartridge.js";

const emit = defineEmits(["close", "toggle-left", "sessions-changed", "goto-session"]);
// ask：全局确认框（App 的 Overlays.confirmAsk）—— 设置里的删除（会话/渠道/Agent）也必须先摊开再点头。
// escBlocked：外面临时弹窗（会话设置）开着时，Esc 归它 —— 一次按键不许关两层。
const props = defineProps({
  ask: { type: Function, default: null },
  escBlocked: { type: Boolean, default: false },
});

const api = sharedApi();
const leftSvg = ICONS.panelLeft;

const tab = ref("chat");
// tab-thumb 跟随：量选中按钮相对 nav 的 left/width（切页 + 进视图 + 缩放都重算）。
const navEl = ref(null);
const thumbLeft = ref(0);
const thumbWidth = ref(0);
function moveThumb() {
  nextTick(() => {
    const nav = navEl.value;
    if (!nav) return;
    const btn = nav.querySelector(`#tab-${tab.value}`);
    if (!btn) return;
    thumbLeft.value = btn.offsetLeft;
    thumbWidth.value = btn.offsetWidth;
  });
}
watch(tab, moveThumb);
const chat = ref({ title_chars: "", model_context_tokens: "", compact_blocks: "", compact_trigger_tokens: "", replay_reasoning: false, auto_compact: true });
const chatStatus = ref("");
const defaults = ref({ provider: "", model: "", agent: "" });
const defStatus = ref("");
const cli = ref({ api: apiBase(), token: "" });
const netStatus = ref("");
const agents = ref([]);
const segmentsLib = ref([]); // 提示词段库（随 GET /agents 一起给）
const curDefault = ref("");
const editAgent = ref(null); // null | 'new' | agent（二级编辑器）
const editSeg = ref(null); // null | 'new' | segment（「提示词」tab 内的二级页）
// 谁引用了这个段（「提示词」tab 列表与编辑器共用）。
const segRefs = (id) => agents.value.filter((a) => (a.segments ?? []).includes(id)).map((a) => a.name || a.id);
const providers = ref([]);
const editProvider = ref(null); // null | 'new' | provider（二级编辑器）
const routesStatus = ref("");

async function loadChat() {
  try {
    const c = await api.getChat();
    chat.value = {
      title_chars: c.title_chars ?? "",
      model_context_tokens: c.model_context_tokens ?? "",
      compact_blocks: c.compact_blocks ?? "",
      compact_trigger_tokens: c.compact_trigger_tokens ?? "",
      replay_reasoning: !!c.replay_reasoning,
      auto_compact: !!c.auto_compact, // 缺字段 = 关（与后端同一口径；这套未经验证，默认不开）
    };
  } catch {}
}
async function saveChat() {
  const num = (v) => (String(v ?? "").trim() === "" ? undefined : Number(v));
  const body = {};
  const t = num(chat.value.title_chars);
  const m = num(chat.value.model_context_tokens);
  const b = num(chat.value.compact_blocks);
  if (t !== undefined) body.title_chars = t;
  if (m !== undefined) body.model_context_tokens = m;
  if (b !== undefined) body.compact_blocks = b;
  const cur = await api.getChat().catch(() => ({}));
  const fullBody = { ...cur, ...body, replay_reasoning: !!chat.value.replay_reasoning, auto_compact: !!chat.value.auto_compact };
  if (String(chat.value.compact_trigger_tokens ?? "").trim() === "") delete fullBody.compact_trigger_tokens;
  else fullBody.compact_trigger_tokens = Number(chat.value.compact_trigger_tokens);
  try {
    await api.putChat(fullBody);
    chatStatus.value = "已保存";
  } catch (e) { chatStatus.value = `保存失败：${e.message}`; }
}

async function loadDefaults() {
  try {
    const d = await api.getDefaults();
    defaults.value = { provider: d.provider ?? "", model: d.model ?? "", agent: d.agent ?? "" };
  } catch {}
}
async function saveDefaults() {
  try {
    await api.putDefaults({ ...defaults.value });
    defStatus.value = "已保存";
  } catch (e) { defStatus.value = `保存失败：${e.message}`; }
}

// 会话行导出（2026-10-11）：与 ☰ 里那颗同一支笔（GET /sessions/{id}/export → 下载）。
async function exportSessionRow(s) {
  try {
    const doc = await api.exportSession(s.id);
    downloadJSON(doc, `${s.title || "新会话"}.json`);
    sessionMgrStatus.value = `已导出 ${doc.messages.length} 条：${s.title || "新会话"}`;
  } catch (e) { sessionMgrStatus.value = `导出失败：${e.message}`; }
}

// Agent 卡带导入（2026-10-11）：卡带 = agent + 它引用到的用户段（id+原文）。
// 合并规则（用户定）：系统里已有同 id 的段（用户段或内置）⇒ **丢弃卡带那份**；没有 ⇒ 建（打「导入」标记）。
const cartridgeInput = ref(null);
const cartridgeStatus = ref("");
async function importCartridge(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  cartridgeStatus.value = "导入中…";
  try {
    const card = cartridgeAgent(JSON.parse(await file.text()));
    const known = new Set(segmentsLib.value.map((s) => s.id));
    let created = 0;
    let skipped = 0;
    for (const seg of card.segments) {
      if (known.has(seg.id)) { skipped++; continue; }
      await api.createSegment({ id: seg.id, text: seg.text, imported: true });
      known.add(seg.id);
      created++;
    }
    const made = await api.createAgent(card.agent);
    cartridgeStatus.value = `已导入「${made.name}」（段：新建 ${created} / 复用 ${skipped}）`;
    await loadAgents();
  } catch (e) { cartridgeStatus.value = `导入失败：${e.message}`; }
  event.target.value = "";
}

// JSON 对话导入（2026-10-11）：与 ST 导入同口径 —— 文件框 → POST /sessions/import-json → 刷列表。
const jsonStatus = ref("");
async function importJSONFile(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  jsonStatus.value = "导入中…";
  try {
    const doc = JSON.parse(await file.text());
    const out = await api.importJson(doc);
    const bits = [`已导入 ${out.messages} 条：${out.session?.title || "新会话"}`];
    // v2 起文件带着会话的三格绑定 ⇒ 回执里能看出"搬过来之后绑的是谁"。
    const bound = [out.session?.provider, out.session?.model].filter(Boolean).join("/");
    if (bound) bits.push(`渠道/模型 ${bound}`);
    // agent 本机没有 ⇒ 照绑了 id（不静默换人），但人设会回落内置默认 —— 必须说出来 + 给出下一步。
    if (out.agent_missing) {
      bits.push(`⚠ Agent「${doc?.agent_name || doc?.agent_id || "?"}」本机没有：已按 id 绑上，人设会回落内置默认；要人设请从「智能体」导它的卡带`);
    }
    jsonStatus.value = bits.join(" · ");
    await loadSessions();
    emit("sessions-changed");
  } catch (e) { jsonStatus.value = `导入失败：${e.message}`; }
  event.target.value = "";
}

// 已有对话管理器：ST 导入（文件框 → POST /sessions/import-st，导完刷会话 tab + 左栏）。
const importTitle = ref("");
const importStatus = ref("");
const stInput = ref(null);   // ST 的隐藏文件框（按钮负责长相，见下）
const jsonInput = ref(null); // 对话 JSON 的隐藏文件框
async function importSTFile(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  importStatus.value = "导入中…";
  try {
    const text = await file.text();
    const out = await api.importSt(text, importTitle.value.trim() || undefined);
    importStatus.value = `已导入 ${out.messages} 条（跳过 ${out.skipped} 行）`;
    await loadSessions();
    emit("sessions-changed");
  } catch (e) { importStatus.value = `导入失败：${e.message}`; }
  event.target.value = "";
}
// 会话 tab：列表 + 新建/进入/删除（编辑走左栏 ☰ 二级）。
const sessions = ref([]);
const sessionMgrStatus = ref("");

function loadClient() {
  // 只剩"连接"这一摊：外观/输入那类**登录者自己的偏好**已搬到用户视图（与设置平级）。
  cli.value = { api: apiBase(), token: apiToken() };
}
async function loadAgents() {
  try {
    const data = await api.agents();
    curDefault.value = data.default_agent ?? "";
    agents.value = data.agents ?? [];
    segmentsLib.value = data.segments ?? [];
  } catch {}
}

// 预设模板（随 git 走：presets/*.json）：长提示词不硬编码，前端不存第二份真相。
const presetPick = ref("");
function newFromTemplate(kind) {
  if (kind === "blank") { editAgent.value = "new"; return; }
}
function presetNew() {
  const kind = presetPick.value;
  presetPick.value = "";
  if (!kind) return;
  // 名字清空等你起；其余（提示词/prepend/能力）照模板带。
  const src = kind === "rp" ? rpPreset : assistantPreset;
  editAgent.value = { template: { ...src, name: "" } };
}

async function loadProviders() {
  try {
    providers.value = (await api.providers()) ?? [];
  } catch (e) { routesStatus.value = `渠道列表失败：${e.message}`; }
}

async function askDeleteProvider(id) {
  if (props.ask && !(await props.ask(`删除渠道「${id}」？密钥随记录一起没，不可逆。`))) return;
  try {
    await api.deleteProvider(id);
    if (editProvider.value && editProvider.value !== "new" && editProvider.value.id === id) editProvider.value = null;
    loadProviders().catch(() => {});
  } catch (e) { routesStatus.value = `删除失败：${e.message}`; }
}

// 会话 tab 动作：进会话关设置面板（App 切；删除也要先确认 —— 与左栏同一个口）。
async function loadSessions() {
  try {
    sessions.value = await api.listSessions();
  } catch (e) { sessionMgrStatus.value = `拉列表失败：${e.message}`; }
}
function gotoSession(id) {
  emit("goto-session", id);
}
async function askDropSession(id, title) {
  if (props.ask && !(await props.ask(`删除会话「${title || "新会话"}」？消息与摘要一并删，不可逆。`))) return;
  try {
    await api.deleteSession(id);
    await loadSessions();
    emit("sessions-changed");
  } catch (e) { sessionMgrStatus.value = `删除失败：${e.message}`; }
}

onMounted(() => {
  loadClient();
  loadChat().catch(() => {});
  loadDefaults().catch(() => {});
  loadAgents().catch(() => {});
  loadProviders().catch(() => {});
  loadSessions().catch(() => {});
  document.addEventListener("keydown", onKey);
  moveThumb();
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e) {
  if (e.key !== "Escape") return;
  // 二级页（Agent/渠道详情）或外面临时弹窗开着 ⇒ Esc 归它们；一次按键只关一层（踩过：双关）。
  if (editAgent.value || editSeg.value || editProvider.value || props.escBlocked) return;
  emit("close");
}
</script>

<template>
  <section id="settings-view">
    <div class="settings-bar">
      <button
        class="icon-btn"
        type="button"
        title="显示/隐藏会话栏"
        aria-label="显示/隐藏会话栏"
        @click="$emit('toggle-left')"
        v-html="leftSvg"
      ></button>
      <nav ref="navEl" class="settings-nav">
        <span class="tab-thumb" :style="{ left: thumbLeft + 'px', width: thumbWidth + 'px' }"></span>
        <button
          v-for="t in SETTINGS_TABS"
          :key="t.id"
          :id="`tab-${t.id}`"
          type="button"
          class="tab"
          :class="{ sel: tab === t.id }"
          @click="tab = t.id"
        >
          {{ t.label }}
        </button>
      </nav>
      <button id="settings-close" class="icon-btn" type="button" title="退出设置（Esc）" aria-label="退出设置" @click="$emit('close')">
        ×
      </button>
    </div>
    <div class="settings-body">
      <div class="settings-box">
        <div class="settings-cols">
        <section v-show="tab === 'chat'" id="panel-chat" class="panel">
          <h3>生成与压缩</h3>
          <label class="field"><span>标题字数</span><input v-model="chat.title_chars" type="number" min="1" /></label>
          <label class="field"><span>模型上下文 tokens</span><input v-model="chat.model_context_tokens" type="number" min="1" /></label>
          <!-- 为什么状态行的分母常常比这里小：它是**输入预算** = 上下文 − 给回复留的输出额度。
               移动端没有 hover，所以这句话必须写在这儿（不能只藏在状态行的 title 里）。 -->
          <p class="hint">状态行的分母是<b>输入预算</b> = 这里填的上下文 − 模型的输出预留（上游报的 <code>max_output</code>，没报按 4096）⇒ 填 1000000 时它显示 995904 ✓。</p>
          <label class="field"><span>压缩块数（自动压缩一次压几个）</span><input v-model="chat.compact_blocks" type="number" min="1" placeholder="空=3" /></label>
          <label class="field"><span>自动压缩（到触发线就压，压到 50% 以下）</span><ToggleSwitch v-model="chat.auto_compact" /></label>
          <label class="field"><span>压缩触发 tokens（空=预算的 80%）</span><input v-model="chat.compact_trigger_tokens" type="number" min="1" placeholder="空=预算的 80%" /></label>
          <label class="field"><span>回传思考（默认关，省上下文）</span><ToggleSwitch v-model="chat.replay_reasoning" /></label>
          <div class="row"><button type="button" class="primary" @click="saveChat">保存</button><span class="status">{{ chatStatus }}</span></div>
        </section>
        <section v-show="tab === 'net'" id="panel-net" class="panel">
          <h3>后端连接</h3>
          <label class="field"><span>API 地址</span><input v-model="cli.api" placeholder="http://127.0.0.1:8787/api/v1" /></label>
          <label class="field"><span>Token</span><input v-model="cli.token" placeholder="可空" type="password" /></label>
          <div class="row"><button type="button" class="primary" @click="saveNet">保存</button><span class="status">{{ netStatus }}</span></div>
          <p class="hint">改完刷新页面生效。</p>
        </section>
        <section v-show="tab === 'session'" id="panel-session" class="panel">
          <h3>新会话缺省</h3>
          <label class="field"><span>渠道</span><input v-model="defaults.provider" placeholder="provider" /></label>
          <label class="field"><span>模型</span><input v-model="defaults.model" placeholder="model" /></label>
          <label class="field"><span>Agent</span><select v-model="defaults.agent">
            <option value="">内置默认</option>
            <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.name }}（{{ a.id }}）</option>
          </select></label>
          <div class="row"><button type="button" class="primary" @click="saveDefaults">保存缺省</button><span class="status">{{ defStatus }}</span></div>
          <h3>会话（{{ sessions.length }} 条，点进）</h3>
          <div id="session-mgr-list">
            <table class="plist">
              <thead><tr><th>标题</th><th>条数</th><th>模型</th><th></th></tr></thead>
              <tbody><tr v-for="s in sessions" :key="s.id" class="clickable" @click="gotoSession(s.id)">
                <td>{{ s.title || "新会话" }}</td>
                <td>{{ s.messages }}</td>
                <td>{{ s.provider }}/{{ s.model }}</td>
                <td class="ops">
                  <button type="button" class="link-btn" @click.stop="exportSessionRow(s)" title="导出对话记录（JSON：标题 + 每条的 role/正文 + 这条会话绑的渠道/模型/Agent）">导出</button>
                  <button type="button" class="link-btn danger" @click.stop="askDropSession(s.id, s.title)">删除</button>
                </td>
              </tr></tbody>
            </table>
          </div>
          <div class="row" v-if="sessionMgrStatus"><span class="status">{{ sessionMgrStatus }}</span></div>
          <!-- 导入区（2026-10-17 用户点名「混在一起、都乱了」）：两种格式各一张卡 ——
               各有一句「这是哪种文件、导进去会怎样」，动作都是按钮 + 隐藏文件框（原生 file 框太丑，
               而且两张卡长一样 ⇒ 分不清谁是谁）。 -->
          <h3>导入对话</h3>
          <div class="io-cards">
            <div class="io-card">
              <div class="io-head"><b>SillyTavern 记录</b><i class="io-tag">.jsonl</i></div>
              <p class="hint">SillyTavern 导出的<b>一行一条</b>记录：只认「谁说的 / 正文 / 思考 / 时间」四样，其余一概丢掉。</p>
              <div class="row">
                <button type="button" class="primary" @click="stInput?.click()">选择 .jsonl 文件…</button>
                <input ref="stInput" type="file" accept=".jsonl,.json,.txt" hidden @change="importSTFile" />
                <span class="status">{{ importStatus }}</span>
              </div>
              <label class="field"><span>标题</span><input v-model="importTitle" placeholder="空 = 用首句起" /></label>
            </div>
            <div class="io-card">
              <div class="io-head"><b>MicroChat 对话 JSON</b><i class="io-tag">.json</i></div>
              <p class="hint">本应用「导出」出来的那份：标题 + 每条的 role 与正文（正文里的 <code>&lt;state&gt;</code> 块留着 ⇒ 导回去状态照常现演），<b>还会带回这条会话当初的渠道 / 模型 / Agent</b>（照抄绑定；本机没有那个 Agent 会明说，人设回落内置默认）。</p>
              <div class="row">
                <button type="button" class="primary" @click="jsonInput?.click()">选择 .json 文件…</button>
                <input ref="jsonInput" type="file" accept=".json" hidden @change="importJSONFile" />
                <span class="status">{{ jsonStatus }}</span>
              </div>
            </div>
          </div>
          <p class="hint">两种导入都是<b>新建一条会话</b>，不动现有会话；导完左栏立刻能看到。</p>
        </section>
        <section v-show="tab === 'agent'" id="panel-agent" class="panel">
          <template v-if="!editAgent">
            <div class="panel-head">
              <h3>已有 Agent（点进二级改）</h3>
              <span class="head-btns">
                <button type="button" @click="cartridgeInput?.click()" title="导入 agent 卡带（JSON；段已拼平，无依赖）">导入卡带…</button>
                <input ref="cartridgeInput" type="file" accept=".json" hidden @change="importCartridge" />
                <button type="button" class="primary" @click="newFromTemplate('blank')">新建</button>
                <select v-model="presetPick" @change="presetNew" title="从模板创建">
                  <option value="">从模板创建…</option>
                  <option value="assistant">通用助手</option>
                  <option value="rp">角色扮演助手</option>
                </select>
              </span>
            </div>
            <div class="row"><span class="status">{{ cartridgeStatus }}</span></div>
            <div id="agent-list">
              <table class="plist">
                <thead><tr><th>名称</th><th>id</th><th>默认</th></tr></thead>
                <tbody><tr v-for="a in agents" :key="a.id" class="clickable" @click="editAgent = a">
                  <td>{{ a.name }}</td>
                  <td>{{ a.id }}</td>
                  <td>{{ a.id === curDefault ? "✓" : "" }}</td>
                </tr></tbody>
              </table>
            </div>
          </template>
          <AgentEditor
            v-if="editAgent"
            :agent="editAgent === 'new' || editAgent?.template ? null : editAgent"
            :template="editAgent?.template ?? null"
            :segments="segmentsLib"
            :is-default="editAgent !== 'new' && editAgent?.id === curDefault"
            :ask="ask"
            @close="editAgent = null"
            @saved="loadAgents"
          />

        </section>
        <section v-show="tab === 'prompt'" id="panel-prompt" class="panel">
          <template v-if="!editSeg">
            <div class="panel-head">
              <h3>提示词段（可复用的 sys prompt 段；「智能体」里逐段引用）</h3>
              <span class="head-btns"><button type="button" class="primary" @click="editSeg = 'new'">新建</button></span>
            </div>
            <table v-if="segmentsLib.length" class="plist">
              <thead><tr><th>id</th><th>预览</th><th>被谁引用</th></tr></thead>
              <tbody><tr v-for="s in segmentsLib" :key="s.id" class="clickable" @click="editSeg = s">
                <td>{{ s.id }}<i v-if="s.builtin" class="builtin-tag">内置</i><i v-if="s.imported" class="builtin-tag imported">导入</i></td>
                <td>{{ s.text.slice(0, 40) }}</td>
                <td>{{ segRefs(s.id).join("、") || "—" }}</td>
              </tr></tbody>
            </table>
            <div v-else class="status">（还没有段 —— 点「新建」起一段：比如教模型写 &lt;state&gt; 的那套、KaTeX 用法、联网搜索纪律）</div>
          </template>
          <SegmentEditor
            v-else
            :segment="editSeg === 'new' ? null : editSeg"
            :agents="agents"
            :ask="ask"
            @close="editSeg = null"
            @saved="loadAgents"
          />
        </section>
        <section v-show="tab === 'provider'" id="panel-provider" class="panel">
          <template v-if="!editProvider">
            <h3>渠道（点进二级改）</h3>
            <div id="provider-list">
              <table class="plist">
                <thead><tr><th>渠道</th><th>vendor</th><th>protocol</th><th>密钥</th><th>模型</th><th></th></tr></thead>
                <tbody><tr v-for="p in providers" :key="p.id" class="clickable" @click="editProvider = p">
                  <td>{{ p.id }}</td>
                  <td>{{ p.vendor ?? "?" }}</td>
                  <td>{{ p.protocol ?? "?" }}</td>
                  <td>{{ p.has_key ? "有" : "无" }}</td>
                  <td>{{ p.models?.length ?? 0 }}</td>
                  <td class="ops"><button type="button" class="link-btn danger" @click.stop="askDeleteProvider(p.id)">删除</button></td>
                </tr></tbody>
              </table>
            </div>
            <div class="row">
              <button type="button" class="primary" @click="editProvider = 'new'">新建</button>
              <span class="status">{{ routesStatus }}</span>
            </div>
          </template>
          <ProviderEditor
            v-else
            :provider="editProvider === 'new' ? null : editProvider"
            @close="editProvider = null"
            @saved="loadProviders"
          />
        </section>
      </div>
      </div>
    </div>
  </section>
</template>
