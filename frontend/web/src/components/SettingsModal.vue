<script setup>
// 设置**视图**（2026-10-10 起与对话同级，不再是弹窗 —— OpenWebUI 那种）：占满左栏右边的整块区域，
// 自带小头（[◧] 左栏开关 · 设置 · [×] 返回对话）。子编辑器（Agent/Provider）仍是覆盖弹窗。
// 关走 close（Esc/×）—— 视图不卸载就丢状态的口径照旧：v-if 挂载，进视图拉一次。
import { ref, onMounted, onUnmounted, watch, nextTick } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import AgentEditor from "./AgentEditor.vue";
import assistantPreset from "../../presets/assistant.json";
import rpPreset from "../../presets/rp-agent.json";
import ProviderEditor from "./ProviderEditor.vue";
import { ICONS } from "./icons.js";
import { createApi } from "../api/client.js";
import {
  SETTINGS_TABS,
} from "../utils/format.js";

const emit = defineEmits(["close", "toggle-left", "sessions-changed", "goto-session"]);
// ask：全局确认框（App 的 Overlays.confirmAsk）—— 设置里的删除（会话/渠道/Agent）也必须先摊开再点头。
// escBlocked：外面临时弹窗（会话设置）开着时，Esc 归它 —— 一次按键不许关两层。
const props = defineProps({
  ask: { type: Function, default: null },
  escBlocked: { type: Boolean, default: false },
});

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });
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
const chat = ref({ title_chars: "", model_context_tokens: "", compact_blocks: "", compact_trigger_tokens: "", replay_reasoning: false });
const chatStatus = ref("");
const defaults = ref({ provider: "", model: "", agent: "" });
const defStatus = ref("");
const cli = ref({ api: API, token: "", preview: true, theme: "", send: "button" });
const uiStatus = ref("");
const netStatus = ref("");
const agents = ref([]);
const curDefault = ref("");
const editAgent = ref(null); // null | 'new' | agent（二级编辑器）
const providers = ref([]);
const presets = ref([]);
const editProvider = ref(null); // null | 'new' | provider（二级编辑器）
const routesStatus = ref("");

const PROTOCOLS = ["openai-chat-completion", "openai-response", "anthropic-messages", "gemini-generate-content", "systemone"];

async function loadChat() {
  try {
    const c = await api.getChat();
    chat.value = {
      title_chars: c.title_chars ?? "",
      model_context_tokens: c.model_context_tokens ?? "",
      compact_blocks: c.compact_blocks ?? "",
      compact_trigger_tokens: c.compact_trigger_tokens ?? "",
      replay_reasoning: !!c.replay_reasoning,
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
  const fullBody = { ...cur, ...body, replay_reasoning: !!chat.value.replay_reasoning };
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

// 已有对话管理器：ST 导入（文件框 → POST /sessions/import-st，导完刷会话 tab + 左栏）。
const importTitle = ref("");
const importStatus = ref("");
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
  cli.value = {
    api: localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1",
    token: localStorage.getItem("mc_token") || "",
    preview: localStorage.getItem("mc_preview") !== "0",
    // `??`：没设过(null)=默认 deepseek；设过空串("")=跟随系统 —— 不能用 `||`（空串会被吞掉）。
    theme: localStorage.getItem("mc_theme") ?? "deepseek",
    send: localStorage.getItem("mc_send") || "enter",
  };
}
// 连接（net）：地址与口令 —— 刷新页面才生效。
function saveNet() {
  localStorage.setItem("mc_api", cli.value.api.trim() || "http://127.0.0.1:8787/api/v1");
  localStorage.setItem("mc_token", cli.value.token);
  netStatus.value = "已保存（刷新页面生效）";
}
// 界面（ui）：主题 / 发送键 / 预演 —— 即时生效。
function saveUi() {
  localStorage.setItem("mc_preview", cli.value.preview ? "1" : "0");
  localStorage.setItem("mc_theme", cli.value.theme);
  localStorage.setItem("mc_send", cli.value.send);
  // 空串 = 跟随系统：要把 data-theme 摘掉（留个空属性会一直盖住 :root，prefers-color-scheme 就不生效了）。
  if (cli.value.theme) document.documentElement.setAttribute("data-theme", cli.value.theme);
  else document.documentElement.removeAttribute("data-theme");
  uiStatus.value = "已保存（即时生效）";
}

async function loadAgents() {
  try {
    const data = await api.agents();
    curDefault.value = data.default_agent ?? "";
    agents.value = data.agents ?? [];
  } catch {}
}

// 预设模板（随 git 走：presets/*.json）： लंबे提示词不硬编码，前端不存第二份真相。
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
    const [list, pres] = await Promise.all([api.providers(), api.providerPresets()]);
    providers.value = list ?? [];
    presets.value = pres ?? [];
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
    await api.closeSession(id);
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
  if (editAgent.value || editProvider.value || props.escBlocked) return;
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
          :key="t"
          :id="`tab-${t}`"
          type="button"
          class="tab"
          :class="{ sel: tab === t }"
          @click="tab = t"
        >
          {{ { chat: "聊天", ui: "界面", net: "连接", session: "会话", agent: "Agent", provider: "Provider" }[t] }}
        </button>
      </nav>
      <button id="settings-close" class="icon-btn" type="button" title="返回对话（Esc）" @click="$emit('close')">
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
          <label class="field"><span>压缩块数</span><input v-model="chat.compact_blocks" type="number" min="1" /></label>
          <label class="field"><span>压缩触发 tokens（空=默认）</span><input v-model="chat.compact_trigger_tokens" type="number" min="1" placeholder="空=默认" /></label>
          <label class="field"><span>回传思考（默认关，省上下文）</span><ToggleSwitch v-model="chat.replay_reasoning" /></label>
          <div class="row"><button type="button" class="primary" @click="saveChat">保存</button><span class="status">{{ chatStatus }}</span></div>
        </section>
        <section v-show="tab === 'ui'" id="panel-ui" class="panel">
          <h3>外观</h3>
          <label class="field"><span>主题</span><select v-model="cli.theme">
            <option value="deepseek">DeepSeek（暗，默认）</option>
            <option value="breeze-dark">Breeze Dark（暗）</option>
            <option value="midnight">深夜蓝</option>
            <option value="wine">酒红</option>
            <option value="forest">森绿</option>
            <option value="breeze">Breeze（亮）</option>
            <option value="paper">日间白</option>
            <option value="">跟随系统</option>
          </select></label>
          <h3>输入</h3>
          <label class="field"><span>发送键</span><select v-model="cli.send">
            <option value="enter">回车发送（Shift/Ctrl/Alt+回车换行）</option>
            <option value="shift-enter">Shift+回车发送（回车换行）</option>
            <option value="button">仅按钮</option>
          </select></label>
          <label class="field"><span>输入时预演载荷</span><ToggleSwitch v-model="cli.preview" /></label>
          <div class="row"><button type="button" class="primary" @click="saveUi">保存</button><span class="status">{{ uiStatus }}</span></div>
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
                <td class="ops"><button type="button" class="link-btn danger" @click.stop="askDropSession(s.id, s.title)">删除</button></td>
              </tr></tbody>
            </table>
          </div>
          <div class="row"><span class="status">{{ sessionMgrStatus }}</span></div>
          <h3>从 SillyTavern 导入</h3>
          <label class="field"><span>导入标题（空=首句起）</span><input v-model="importTitle" placeholder="空=首句起" /></label>
          <label class="field"><span>JSONL 文件</span><input type="file" accept=".jsonl,.json,.txt" @change="importSTFile" /></label>
          <div class="row"><span class="status">{{ importStatus }}</span></div>
        </section>
        <section v-show="tab === 'agent'" id="panel-agent" class="panel">
          <template v-if="!editAgent">
            <div class="panel-head">
              <h3>已有 Agent（点进二级改）</h3>
              <span class="head-btns">
                <button type="button" class="primary" @click="newFromTemplate('blank')">新建</button>
                <select v-model="presetPick" @change="presetNew" title="从模板创建">
                  <option value="">从模板创建…</option>
                  <option value="assistant">通用助手</option>
                  <option value="rp">角色扮演助手</option>
                </select>
              </span>
            </div>
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
            v-else
            :agent="editAgent === 'new' || editAgent?.template ? null : editAgent"
            :template="editAgent?.template ?? null"
            :is-default="editAgent !== 'new' && editAgent?.id === curDefault"
            :ask="ask"
            @close="editAgent = null"
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
