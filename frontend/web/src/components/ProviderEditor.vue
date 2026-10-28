<script setup>
// Provider 编辑器（设置视图 Provider tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 vendor/protocol/端点/key。
// isNew（boolean）：无 provider 快照=新建（空表单）；有=编辑（带入 provider，只发改过的格；key 另填=换，不填=不动）。
// 内置刷新模型列表 + 刷路由（编辑时直接点，不用退回一级）。
import { ref, computed, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.js";
import { formatRoutesOutcome, providerIdForNextVendor } from "../utils/format.js";

const props = defineProps({ provider: { type: Object, default: null } });
const emit = defineEmits(["close", "saved"]);

const api = sharedApi();


const isNew = !props.provider;
const pid = ref("");
const vendor = ref("");
const protocol = ref("");
const baseUrl = ref("");
const key = ref("");
// filter：模型筛选器（只导入 id 命中任意一条规则的模型；空 = 不筛）。
//
// 多条规则用**半角逗号**分隔，命中任意一条就算中（2026-10-28 用户定：「deepseek,mimo 会吧
// deepseek-v4-flash 和 mimo-v2.6-flash 都允许，因为它们分别满足了一个规则」）。
const filter = ref("");
// baseFilter：**已保存的**筛选器 —— 增量比较的基准。不直接比 props.provider：
// 改了筛选器会留在原地，而父组件的 editProvider 仍指着旧对象 ⇒ 拿它当基准会把"没改"误判成"改了"。
const baseFilter = ref("");
// 协议的可选项**不让界面自己列**（2026-10-28 用户：「vendor 和 protocol 应该用中文化的名字吧」）：
// 从前这张表写死在前端，既漏了中文名、又把 `Validate` 一律拒绝的两个占位协议
// （anthropic-messages / gemini-generate-content）摆进下拉 —— 那是递给人一个按下去必炸的选项。
// 现在全从 `GET /providers/presets` 的 `protocol_options` 来（后端是唯一权威，契约测试钉着）。
const hasKey = ref(false);
const modelCount = ref(0);
const presets = ref([]);
const status = ref("");

// 预设分两组：`primary`（后端说的"主选三种"）在前，其余便利预设殿后。
//
// 从前 11 个 vendor 平铺在一条下拉里，`OpenCode Go` 与 `OpenCode (Zen)` 挨着，
// 新人根本不知道"我该选哪个"。分组把"常见用法"和"我要特殊东西"分开 ——
// 分组是后端给的 `primary` 标记，不是前端另拍一套。
const primaryPresets = computed(() => presets.value.filter((p) => p.primary));
const otherPresets = computed(() => presets.value.filter((p) => !p.primary));

// 当前 vendor 的预设（vendor 一改，可选协议就跟着换）。
const curPreset = computed(() => presets.value.find((p) => p.vendor === vendor.value) ?? null);

// protocolChoices：这条渠道能换成哪些协议（带中文名）。
//
// 两处来源合并：① 当前 vendor 的 `protocol_options`（后端按兼容矩阵给）；
// ② 这条渠道**自己正在用的**那条 —— 老配置里可能是手写的、或 vendor 换了而协议还留着。
// 不把 ② 补进来，下拉会在"暂时没选"的瞬间把用户手写的那条显示成空白，一保存就悄悄换掉协议。
const protocolChoices = computed(() => {
  const list = [...(curPreset.value?.protocol_options ?? [])];
  if (!protocol.value) return list;
  if (list.some((option) => option.protocol === protocol.value)) return list;
  const known = (props.provider?.protocol_options ?? []).find((option) => option.protocol === protocol.value);
  return [...list, { protocol: protocol.value, label: known?.label ?? protocol.value, hint: "当前渠道正在用的协议（不在这个 vendor 的常规名单里）" }];
});

// protocolHint：选中那一条的一句人话 —— 下拉关起来之后，人只剩 id 可看。
const protocolHint = computed(() =>
  protocolChoices.value.find((option) => option.protocol === protocol.value)?.hint ??
  "空 = 用这个渠道的默认协议（对话补全）",
);

// effectiveBase：这一发真会打到哪儿 —— 端点留空时用预设的，所以"空"不等于"没有端点"。
// 从前这一格是空的、提示只是"空=预设端点"，人得自己去猜预设是哪条。
const effectiveBase = computed(() => {
  if (baseUrl.value.trim()) return baseUrl.value.trim();
  const preset = curPreset.value?.base_url ?? props.provider?.base_url ?? "";
  return preset || "（这个 vendor 没有预设端点，必须自己填）";
});

// vendorHint：选中的 vendor 的端点与"要不要密钥"，省得人再去别处查。
const vendorHint = computed(() => {
  const preset = curPreset.value;
  if (!preset) return "";
  const bits = [];
  if (preset.base_url) bits.push(preset.base_url);
  bits.push(preset.needs_key ? `需要密钥${preset.key_env ? `（也可用环境变量 ${preset.key_env}）` : ""}` : "不需要密钥");
  return bits.join(" · ");
});

// pickVendor：新建时换 vendor 顺手跟着换 id —— **只在 id 还是"上回自动填的那个"时才动**。
//
// 这条判据是整段的关键：用户已经手打了 `我的渠道`，再改 vendor 就把它冲掉，那是抢方向盘。
// 判"是不是自动填的"用 `autoId` 记住上一次自动填进去的值（空 = 从没自动填过 ⇒ 用户填的）。
const autoId = ref("");
function pickVendor(next) {
  vendor.value = next;
  if (!isNew) return;
  pid.value = providerIdForNextVendor(pid.value, autoId.value, next);
  autoId.value = pid.value;
}
// 手打了 id 就不再当它是自动值（之后改 vendor 不该覆盖）。
function touchId() {
  if (pid.value === autoId.value) return;
  autoId.value = "";
}

async function load() {
  const pres = await api.providerPresets().catch(() => []);
  presets.value = pres ?? [];
  if (isNew) {
    if (presets.value[0]) pickVendor(presets.value[0].vendor);
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
    <!-- 先问"走哪家"、再问 id（2026-10-28）：从前 id 排在最前面，可它恰恰是**最不该让人自己想**的那格 ——
         改 vendor 时它还会跟着变，先填等于白填。 -->
    <!-- vendor / protocol 一律**中文名在前、内部 id 附后**（2026-10-28 用户：「应该用中文化的名字吧」）。
         id 不藏起来：手改 config/providers.json 的人得能对上号，出错信息里也打的是 id。 -->
    <label class="field"><span>走哪家（vendor）</span><select :value="vendor" @change="pickVendor($event.target.value)">
      <optgroup label="常见">
        <option v-for="p in primaryPresets" :key="p.vendor" :value="p.vendor">{{ p.name }}（{{ p.vendor }}）</option>
      </optgroup>
      <optgroup v-if="otherPresets.length" label="其它预设">
        <option v-for="p in otherPresets" :key="p.vendor" :value="p.vendor">{{ p.name }}（{{ p.vendor }}）</option>
      </optgroup>
    </select></label>
    <p v-if="vendorHint" class="hint">{{ vendorHint }}</p>
    <label class="field"><span>渠道 id</span><input v-model="pid" :disabled="!isNew" placeholder="新建时填" @input="touchId" /></label>
    <p v-if="isNew" class="hint">id 是这条渠道在你机器上的名字（会写进 <code>providers.json</code>、也会出现在模型列表的「渠道」那列）。
      换 vendor 时它跟着变 —— 你手改过之后就不再动它了。</p>
    <label class="field"><span>说什么话（protocol）</span><select v-model="protocol">
      <option value="">用默认（对话补全）</option>
      <option v-for="option in protocolChoices" :key="option.protocol" :value="option.protocol">
        {{ option.label }}（{{ option.protocol }}）
      </option>
    </select></label>
    <p class="hint">{{ protocolHint }}</p>
    <label class="field"><span>端点（空 = 用预设的）</span><input v-model="baseUrl" :placeholder="effectiveBase" /></label>
    <p class="hint">这一发会打到 <code>{{ effectiveBase }}</code></p>
    <h3>密钥</h3>
    <label class="field"><span>{{ hasKey ? "已有密钥（填=换，不填=不动）" : "api_key（可空）" }}</span><input v-model="key" placeholder="api_key" type="password" /></label>
    <h3>模型筛选器</h3>
    <label class="field"><span>只导入 id 命中这些子串的模型</span>
      <input v-model="filter" placeholder="空=全部（例：deepseek,mimo）" />
    </label>
    <p class="hint">
      多条规则用<b>半角逗号</b>分隔，<b>命中任意一条就导入</b>：填 <code>deepseek,mimo</code> ⇒
      <code>deepseek-v4-flash</code> 与 <code>mimo-v2.6-flash</code> 都会进来。
      每条都是子串匹配、不分大小写（<code>openai/</code> 这种前缀也参与比较）；
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
