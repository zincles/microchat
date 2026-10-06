<script setup>
// Provider 编辑器（二级弹窗，盖在设置窗口之上）：新建 / 改 vendor/protocol/端点/key。
// mode: "create"（空表单）/ "edit"（带入 provider，只发改过的格；key 另填=换，不填=不动）。
// 内置刷新模型列表 + 刷路由（编辑时直接点，不用退回一级）。
import { ref, onMounted, onUnmounted } from "vue";
import { createApi } from "../api/client.js";
import { formatRoutesOutcome } from "../utils/format.js";

const props = defineProps({ provider: { type: Object, default: null } });
const emit = defineEmits(["close", "saved"]);

const API = localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1";
const TOKEN = localStorage.getItem("mc_token") || "";
const api = createApi({ base: API, token: TOKEN });

const PROTOCOLS = ["openai-chat-completion", "openai-response", "anthropic-messages", "gemini-generate-content", "systemone"];

const isNew = !props.provider;
const pid = ref("");
const vendor = ref("");
const protocol = ref("");
const baseUrl = ref("");
const key = ref("");
const hasKey = ref(false);
const modelCount = ref(0);
const presets = ref([]);
const status = ref("");

async function load() {
  const pres = await api.providerPresets().catch(() => []);
  presets.value = pres ?? [];
  if (isNew) {
    if (presets.value[0]) vendor.value = presets.value[0].vendor;
    return;
  }
  const p = props.provider;
  pid.value = p.id;
  vendor.value = p.vendor ?? "";
  protocol.value = p.protocol ?? "";
  baseUrl.value = p.base_url ?? "";
  hasKey.value = !!p.has_key;
  modelCount.value = p.models?.length ?? 0;
}

async function save() {
  try {
    if (isNew) {
      const id = pid.value.trim();
      if (!id) { status.value = "给个渠道 id"; return; }
      await api.createProvider({
        id,
        vendor: vendor.value || undefined,
        ...(protocol.value ? { protocol: protocol.value } : {}),
        ...(baseUrl.value.trim() ? { base_url: baseUrl.value.trim() } : {}),
        api_key: key.value || undefined,
      });
    } else {
      const body = {};
      if (vendor.value !== (props.provider.vendor ?? "")) body.vendor = vendor.value;
      if (protocol.value !== (props.provider.protocol ?? "")) body.protocol = protocol.value;
      if (baseUrl.value.trim() !== (props.provider.base_url ?? "")) body.base_url = baseUrl.value.trim();
      if (key.value) body.api_key = key.value;
      if (!Object.keys(body).length) { status.value = "没改动"; return; }
      await api.patchProvider(props.provider.id, body);
    }
    emit("saved");
    emit("close");
  } catch (e) { status.value = `保存失败：${e.message}`; }
}

async function clearKey() {
  if (isNew) { key.value = ""; return; }
  try {
    await api.patchProvider(props.provider.id, { api_key: "" });
    hasKey.value = false;
    key.value = "";
    status.value = "密钥已清";
  } catch (e) { status.value = `清密钥失败：${e.message}`; }
}

async function refresh() {
  if (isNew) return;
  try {
    await api.refreshProvider(props.provider.id);
    status.value = "模型已刷新";
    emit("saved");
  } catch (e) { status.value = `刷新失败（${e.message}）`; }
}

async function refreshRoutes() {
  if (isNew) return;
  try {
    const out = await api.refreshRoutes(props.provider.id);
    status.value = `路由缓存：${formatRoutesOutcome(out)}`;
  } catch (e) { status.value = `路由缓存刷新失败（${e.message}）`; }
}

onMounted(() => {
  load().catch((e) => { status.value = `载入失败：${e.message}`; });
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e) {
  if (e.key === "Escape") emit("close");
}
</script>

<template>
  <div class="modal submodal" @click.self="$emit('close')">
    <div class="modal-box">
      <div class="modal-head">
        <span>{{ isNew ? "新建渠道" : `渠道 ${pid}（${modelCount} 个模型）` }}</span>
        <span class="head-btns">
          <button class="icon-btn" type="button" title="关闭（不保存不写）" @click="$emit('close')">
            ×
          </button>
        </span>
      </div>
      <div class="settings-cols">
        <section class="panel">
          <h3>基本</h3>
          <label class="field"><span>渠道 id</span><input v-model="pid" :disabled="!isNew" placeholder="新建时填" /></label>
          <label class="field"><span>vendor</span><select v-model="vendor">
            <option v-for="p in presets" :key="p.vendor" :value="p.vendor">{{ p.name }}（{{ p.vendor }}）</option>
          </select></label>
          <label class="field"><span>protocol（空=默认）</span><select v-model="protocol">
            <option value="">空=默认</option>
            <option v-for="pr in PROTOCOLS" :key="pr" :value="pr">{{ pr }}</option>
          </select></label>
          <label class="field"><span>端点（空=预设）</span><input v-model="baseUrl" placeholder="空=预设端点" /></label>
          <h3>密钥</h3>
          <label class="field"><span>{{ hasKey ? "已有密钥（填=换，不填=不动）" : "api_key（可空）" }}</span><input v-model="key" placeholder="api_key" type="password" /></label>
          <div class="row">
            <button type="button" class="primary" @click="save">保存</button>
            <button v-if="!isNew" type="button" @click="refresh">刷新模型</button>
            <button v-if="!isNew" type="button" @click="refreshRoutes">刷路由</button>
            <button v-if="!isNew && (hasKey || key)" type="button" class="danger" @click="clearKey">清密钥</button>
            <span class="status">{{ status }}</span>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
