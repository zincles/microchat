<script setup>
// 设置弹窗（四页：服务端/客户端/Agent/Provider；关丢弃 ⇒ 不保存不写）。
// 状态全本地 ref，进弹窗拉一次；保存逐段 PUT。逻辑照 main.js wireSettings。
import { ref, onMounted, onUnmounted, watch, nextTick } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import AgentEditor from "./AgentEditor.vue";
import assistantPreset from "../../presets/assistant.json";
import rpPreset from "../../presets/rp-agent.json";
import ProviderEditor from "./ProviderEditor.vue";
import { createApi } from "../api/client.js";
import {
  SETTINGS_TABS,
} from "../utils/format.js";

const emit = defineEmits(["close", "notify", "sessions-changed", "goto-session"]);
function notify(t, k) { emit("notify", t, k); }

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });

const tab = ref("server");
// tab-thumb 跟随：量选中按钮相对 nav 的 left/width（切页 + 开弹窗 + 缩放都重算）。
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
const full = ref(localStorage.getItem("mc_settings_full") === "1");
const chat = ref({ title_chars: "", model_context_tokens: "", compact_blocks: "", compact_trigger_tokens: "", replay_reasoning: false });
const chatStatus = ref("");
const defaults = ref({ provider: "", model: "", agent: "" });
const defStatus = ref("");
const cli = ref({ api: API, token: "", preview: true, theme: "", send: "button" });
const cliStatus = ref("");
const agents = ref([]);
const curDefault = ref("");
const editAgent = ref(null); // null | 'new' | agent（二级编辑器）
const providers = ref([]);
const presets = ref([]);
const curProvider = ref(null); // 选中行（删除目标）
const editProvider = ref(null); // null | 'new' | provider（二级编辑器）
const routesStatus = ref("");

const PROTOCOLS = ["openai-chat-completion", "openai-response", "anthropic-messages", "gemini-generate-content", "systemone"];

function toggleFull() {
  full.value = !full.value;
  localStorage.setItem("mc_settings_full", full.value ? "1" : "0");
}

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
    theme: localStorage.getItem("mc_theme") || "breeze-dark",
    send: localStorage.getItem("mc_send") || "enter",
  };
}
function saveClient() {
  localStorage.setItem("mc_api", cli.value.api.trim() || "http://127.0.0.1:8787/api/v1");
  localStorage.setItem("mc_token", cli.value.token);
  localStorage.setItem("mc_preview", cli.value.preview ? "1" : "0");
  localStorage.setItem("mc_theme", cli.value.theme);
  localStorage.setItem("mc_send", cli.value.send);
  document.documentElement.setAttribute("data-theme", cli.value.theme || "breeze-dark");
  cliStatus.value = "已保存（API/Token 刷新页面生效，其余即时生效）";
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
    if (curProvider.value) {
      const still = list.find((p) => p.id === curProvider.value.id);
      curProvider.value = still ?? null;
    }
  } catch (e) { routesStatus.value = `渠道列表失败：${e.message}`; }
}

async function deleteProvider() {
  if (!curProvider.value) return;
  await api.deleteProvider(curProvider.value.id);
  curProvider.value = null;
  loadProviders().catch(() => {});
}

// 会话 tab 动作：进会话关设置面板（App 切），删走全屏确认口（ App 没有，用 notify 转）。
async function loadSessions() {
  try {
    sessions.value = await api.listSessions();
  } catch (e) { sessionMgrStatus.value = `拉列表失败：${e.message}`; }
}
async function newSession() {
  try {
    await api.createSession({});
    await loadSessions();
    emit("sessions-changed");
  } catch (e) { sessionMgrStatus.value = `新建失败：${e.message}`; }
}
function gotoSession(id) {
  emit("goto-session", id);
}
async function dropSession(id) {
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
  if (e.key === "Escape") emit("close");
}
</script>

<template>
  <div id="settings" class="modal" @click.self="$emit('close')">
    <div class="modal-box settings-box" :class="{ full }">
      <div class="modal-head">
        <span>设置</span>
        <span class="head-btns">
          <button id="settings-full" class="icon-btn" type="button" title="全屏切换" @click="toggleFull">⛶</button>
          <button id="settings-close" class="icon-btn" type="button" title="关闭（不保存不写）" @click="$emit('close')">
            ×
          </button>
        </span>
      </div>
      <div class="settings-cols">
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
            {{ { server: "服务端", client: "客户端", session: "会话", agent: "Agent", provider: "Provider" }[t] }}
          </button>
        </nav>
        <section v-show="tab === 'server'" id="panel-server" class="panel">
          <h3>chat</h3>
          <label class="field"><span>标题字数</span><input v-model="chat.title_chars" type="number" min="1" /></label>
          <label class="field"><span>模型上下文 tokens</span><input v-model="chat.model_context_tokens" type="number" min="1" /></label>
          <label class="field"><span>压缩块数</span><input v-model="chat.compact_blocks" type="number" min="1" /></label>
          <label class="field"><span>压缩触发 tokens（空=默认）</span><input v-model="chat.compact_trigger_tokens" type="number" min="1" placeholder="空=默认" /></label>
          <label class="field"><span>回传思考（默认关，省上下文）</span><ToggleSwitch v-model="chat.replay_reasoning" /></label>
          <div class="row"><button type="button" class="primary" @click="saveChat">保存 chat</button><span class="status">{{ chatStatus }}</span></div>
          <h3>缺省三件</h3>
          <label class="field"><span>provider</span><input v-model="defaults.provider" placeholder="provider" /></label>
          <label class="field"><span>model</span><input v-model="defaults.model" placeholder="model" /></label>
          <label class="field"><span>缺省 Agent</span><select v-model="defaults.agent">
            <option value="">内置默认</option>
            <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.name }}（{{ a.id }}）</option>
          </select></label>
          <div class="row"><button type="button" class="primary" @click="saveDefaults">保存缺省</button><span class="status">{{ defStatus }}</span></div>
        </section>
        <section v-show="tab === 'client'" id="panel-client" class="panel">
          <label class="field"><span>API 地址</span><input v-model="cli.api" placeholder="http://127.0.0.1:8787/api/v1" /></label>
          <label class="field"><span>Token</span><input v-model="cli.token" placeholder="可空" type="password" /></label>
          <label class="field"><span>输入时预演载荷</span><ToggleSwitch v-model="cli.preview" /></label>
          <label class="field"><span>发送键</span><select v-model="cli.send">
            <option value="enter">回车发送（Shift+回车换行）</option>
            <option value="shift-enter">Shift+回车发送（回车换行）</option>
            <option value="button">仅按钮</option>
          </select></label>
          <label class="field"><span>主题</span><select v-model="cli.theme">
            <option value="breeze-dark">Breeze Dark（暗）</option>
            <option value="midnight">深夜蓝</option>
            <option value="wine">酒红</option>
            <option value="forest">森绿</option>
            <option value="breeze">Breeze（亮）</option>
            <option value="paper">日间白</option>
            <option value="">跟随系统</option>
          </select></label>
          <div class="row"><button type="button" class="primary" @click="saveClient">保存</button><span class="status">{{ cliStatus }}</span></div>
          <p class="hint">API/Token 改完刷新页面生效；其余即时生效。</p>
        </section>
        <section v-show="tab === 'session'" id="panel-session" class="panel">
          <div class="panel-head">
            <h3>会话（{{ sessions.length }} 条，点进）</h3>
            <button type="button" class="primary" @click="newSession">新建</button>
          </div>
          <div id="session-mgr-list">
            <table class="plist">
              <thead><tr><th>标题</th><th>条数</th><th>模型</th><th></th></tr></thead>
              <tbody><tr v-for="s in sessions" :key="s.id" class="clickable" @click="gotoSession(s.id)">
                <td>{{ s.title || "新会话" }}</td>
                <td>{{ s.messages }}</td>
                <td>{{ s.provider }}/{{ s.model }}</td>
                <td class="ops"><button type="button" class="link-btn danger" @click.stop="dropSession(s.id)">删除</button></td>
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
          <AgentEditor
            v-if="editAgent"
            :agent="editAgent === 'new' || editAgent?.template ? null : editAgent"
            :template="editAgent?.template ?? null"
            :is-default="editAgent !== 'new' && editAgent?.id === curDefault"
            @close="editAgent = null"
            @saved="loadAgents"
          />
        </section>
        <section v-show="tab === 'provider'" id="panel-provider" class="panel">
          <div id="provider-list">
            <table class="plist">
              <thead><tr><th>渠道</th><th>vendor</th><th>protocol</th><th>密钥</th><th>模型</th></tr></thead>
              <tbody><tr v-for="p in providers" :key="p.id" class="clickable" :class="{ sel: curProvider?.id === p.id }" @click="curProvider = p; editProvider = p">
                <td>{{ p.id }}</td>
                <td>{{ p.vendor ?? "?" }}</td>
                <td>{{ p.protocol ?? "?" }}</td>
                <td>{{ p.has_key ? "有" : "无" }}</td>
                <td>{{ p.models?.length ?? 0 }}</td>
              </tr></tbody>
            </table>
          </div>
          <div class="row">
            <button type="button" class="primary" @click="editProvider = 'new'">新建</button>
            <button type="button" class="danger" :disabled="!curProvider" @click="deleteProvider">删除</button>
            <span class="status">{{ routesStatus }}</span>
          </div>
          <ProviderEditor
            v-if="editProvider"
            :provider="editProvider === 'new' ? null : editProvider"
            @close="editProvider = null"
            @saved="loadProviders"
          />
          <div class="status">{{ routesStatus }}</div>
        </section>
      </div>
    </div>
  </div>
</template>
