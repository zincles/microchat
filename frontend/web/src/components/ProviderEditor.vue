<script setup>
// Provider 编辑器（设置视图 Provider tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 vendor/protocol/端点/key。
// isNew（boolean）：无 provider 快照=新建（空表单）；有=编辑（带入 provider，只发改过的格；key 另填=换，不填=不动）。
// 内置刷新模型列表 + 刷路由（编辑时直接点，不用退回一级）。
import { ref, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";
import { formatRoutesOutcome } from "../utils/format.js";

const props = defineProps({ provider: { type: Object, default: null } });
const emit = defineEmits(["close", "saved"]);

const api = sharedApi();

const PROTOCOLS = ["openai-chat-completion", "openai-response", "anthropic-messages", "gemini-generate-content", "systemone"];

const isNew = !props.provider;
const pid = ref("");
const vendor = ref("");
const protocol = ref("");
const baseUrl = ref("");
const key = ref("");
// filter：模型筛选器（只导入 id 含这段子串的模型；空 = 不筛）。
const filter = ref("");
// baseFilter：**已保存的**筛选器 —— 增量比较的基准。不直接比 props.provider：
// 改了筛选器会留在原地，而父组件的 editProvider 仍指着旧对象 ⇒ 拿它当基准会把"没改"误判成"改了"。
const baseFilter = ref("");
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
  filter.value = p.model_filter ?? "";
  baseFilter.value = filter.value;
  hasKey.value = !!p.has_key;
  modelCount.value = p.models?.length ?? 0;
}

async function save() {
  const wanted = filter.value.trim();
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
        ...(wanted ? { model_filter: wanted } : {}),
      });
      baseFilter.value = wanted;
      emit("saved");
      // 新建的渠道一个模型都没有；填了筛选器就顺手按它导入一次，然后**回列表**。
      // 不留在原地：这个二级页的 `isNew` 是挂载时定的常量，建完再留着会一直显示"新建渠道"，
      // 那时再点保存就是拿同一个 id 再建一次 ⇒ 409（无谓的报错）。
      if (wanted) { await reloadModels(id); }
      emit("close");
      return;
    }
    const body = {};
    if (vendor.value !== (props.provider.vendor ?? "")) body.vendor = vendor.value;
    if (protocol.value !== (props.provider.protocol ?? "")) body.protocol = protocol.value;
    if (baseUrl.value.trim() !== (props.provider.base_url ?? "")) body.base_url = baseUrl.value.trim();
    if (key.value) body.api_key = key.value;
    const filterChanged = wanted !== baseFilter.value;
    if (filterChanged) body.model_filter = wanted;
    if (!Object.keys(body).length) { status.value = "没改动"; return; }
    await api.patchProvider(props.provider.id, body);
    baseFilter.value = wanted;
    emit("saved");
    // 筛选器只管"导入哪些"：改完不重刷，界面上**一点区别都没有**（用户会以为没生效，
    // 而且列表里那些已经导入的行也还留着）。所以改了就就地刷一次、留在这儿 ——
    // 成功能看见结果，失败（没 key / 没网）也能看见原因，不用再猜。
    if (filterChanged) { await reloadModels(props.provider.id); return; }
    emit("close");
  } catch (e) { status.value = `保存失败：${e.message}`; }
}

// reloadModels：按当前配置重拉该渠道的模型清单（"刷新模型"按钮与"改完筛选器"共用这一处）。
async function reloadModels(id) {
  try {
    const view = await api.refreshProvider(id);
    modelCount.value = view?.models?.length ?? 0;
    status.value = `已导入 ${modelCount.value} 个模型` +
      (baseFilter.value ? `（筛选器：${baseFilter.value}）` : "");
    emit("saved");
  } catch (e) { status.value = `刷新失败（${e.message}）`; }
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
  await reloadModels(props.provider.id);
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
  <!-- 二级页（2026-10-10 起不再是弹窗）：嵌在设置视图的 Provider tab 里，列表 ⇄ 详情就地切换。 -->
  <div class="detail-pane">
    <div class="panel-head">
      <button class="icon-btn" type="button" title="返回列表（Esc）" @click="$emit('close')">←</button>
      <span class="status">{{ isNew ? "新建渠道" : `渠道「${pid}」（${modelCount} 个模型）` }}</span>
    </div>
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
    <h3>模型筛选器</h3>
    <label class="field"><span>只导入 id 含此子串的模型</span><input v-model="filter" placeholder="空=全部（例：gpt）" /></label>
    <p class="hint">
      子串匹配、不分大小写：填 <code>gpt</code> ⇒ <code>openai/gpt-5</code>、<code>gpt-6</code>、
      <code>openai/gpt-5.6-sol</code> 都会导入（<code>openai/</code> 这种前缀也参与比较）。
      不支持通配符（<code>*</code> 无效）；留空 = 不筛。保存后会自动按筛选器重新导入一次。
    </p>
    <div class="row">
      <button type="button" class="primary" @click="save">保存</button>
      <button v-if="!isNew" type="button" @click="refresh">刷新模型</button>
      <button v-if="!isNew" type="button" @click="refreshRoutes">刷路由</button>
      <button v-if="!isNew && (hasKey || key)" type="button" class="danger" @click="clearKey">清密钥</button>
      <span class="status">{{ status }}</span>
    </div>
  </div>
</template>
