<script setup>
// 设置弹窗（四页：服务端/客户端/Agent/Provider；关丢弃 ⇒ 不保存不写）。
// 状态全本地 ref，进弹窗拉一次；保存逐段 PUT。逻辑照 main.js wireSettings。
import { ref, onMounted, onUnmounted, watch, nextTick } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import AgentEditor from "./AgentEditor.vue";
import ProviderEditor from "./ProviderEditor.vue";
import { createApi } from "../api/client.js";
import {
  SETTINGS_TABS,
} from "../utils/format.js";

const emit = defineEmits(["close", "notify"]);
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

async function loadProviders() {
  try {
    const [list, pres] = await Promise.all([api.providers(), api.providerPresets()]);
    providers.value = list ?? [];
    presets.value = pres ?? [];
    if (curProvider.value) {
      const still = list.find((p) => p.id === curProvider.value.id);
      curProvider.value = still ?? null;
    }
  } catch {}
}

async function deleteProvider() {
  if (!curProvider.value) return;
  await api.deleteProvider(curProvider.value.id);
  curProvider.value = null;
  loadProviders().catch(() => {});
}


onMounted(() => {
  loadClient();
  loadChat().catch(() => {});
  loadDefaults().catch(() => {});
  loadAgents().catch(() => {});
  loadProviders().catch(() => {});
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
            {{ { server: "服务端", client: "客户端", agent: "Agent", provider: "Provider" }[t] }}
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
          <label class="field"><span>agent</span><input v-model="defaults.agent" placeholder="agent" /></label>
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
        <section v-show="tab === 'agent'" id="panel-agent" class="panel">
          <div class="panel-head">
            <h3>已有 Agent（点进二级改）</h3>
            <button type="button" class="primary" @click="editAgent = 'new'">新建</button>
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
            :agent="editAgent === 'new' ? null : editAgent"
            :is-default="editAgent !== 'new' && editAgent.id === curDefault"
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
