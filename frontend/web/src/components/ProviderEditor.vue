<script setup lang="ts">
// Provider 编辑器（设置视图 Provider tab 内的二级页，列表 ⇄ 详情就地切换）：新建 / 改 vendor/protocol/端点/key。
// isNew（boolean）：无 provider 快照=新建（空表单）；有=编辑（带入 provider，只发改过的格；key 另填=换，不填=不动）。
// 内置刷新模型列表 + 刷路由（编辑时直接点，不用退回一级）。
import { VBtn, VTextField, VSelect, VTable } from "vuetify/components";
import { ref, computed, onMounted, onUnmounted } from "vue";
import { sharedApi } from "../utils/prefs.ts";
import { formatTokens, providerIdForNextVendor } from "../utils/format.ts";
import ModelEditor from "./ModelEditor.vue";
import ModelIcon from "./ModelIcon.vue";
import type {
  CreateProviderRequest,
  ModelView,
  PresetInfo,
  ProtocolOption,
  ProviderView,
  TestProviderRequest,
  UpdateProviderRequest,
} from "../api/types.ts";

const props = withDefaults(defineProps<{ provider?: ProviderView | null }>(), { provider: null });
const emit = defineEmits<{ close: []; saved: [id: string]; "model-saved": [] }>();

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
// 同一渠道的 endpoint 并发上限（0 = 不限；高级压缩作业并发还会与它取 min）。
const maxConcurrency = ref(0);
// baseFilter：**已保存的**筛选器 —— 增量比较的基准。不直接比 props.provider：
// 改了筛选器会留在原地，而父组件的 editProvider 仍指着旧对象 ⇒ 拿它当基准会把"没改"误判成"改了"。
const baseFilter = ref("");
// 协议的可选项**不让界面自己列**（2026-10-28 用户：「vendor 和 protocol 应该用中文化的名字吧」）：
// 从前这张表写死在前端，既漏了中文名、又把 `Validate` 一律拒绝的两个占位协议
// （anthropic-messages / gemini-generate-content）摆进下拉 —— 那是递给人一个按下去必炸的选项。
// 现在全从 `GET /providers/presets` 的 `protocol_options` 来（后端是唯一权威，契约测试钉着）。
const hasKey = ref(false);
// 模型列表：能看见"这次刷新导入了什么"，点每行可编辑该模型的用户列。
const models = ref<ModelView[]>([]);
const editingModel = ref<ModelView | null>(null);
const modelCount = computed(() => models.value.length);
const presets = ref<PresetInfo[]>([]);
const status = ref("");

// 预设分两组：`primary`（后端说的"主选三种"）在前，其余便利预设殿后。
//
// 从前 11 个 vendor 平铺在一条下拉里，`OpenCode Go` 与 `OpenCode (Zen)` 挨着，
// 新人根本不知道"我该选哪个"。分组把"常见用法"和"我要特殊东西"分开 ——
// 分组是后端给的 `primary` 标记，不是前端另拍一套。
const primaryPresets = computed(() => presets.value.filter((p) => p.primary));
const otherPresets = computed(() => presets.value.filter((p) => !p.primary));
const vendorOptions = computed(() => [...primaryPresets.value, ...otherPresets.value].map((p) => ({
  label: p.name + (p.vendor ? `（${p.vendor}）` : ""),
  value: p.vendor,
})));

// 当前 vendor 的预设（vendor 一改，可选协议就跟着换）。
const curPreset = computed(() => presets.value.find((p) => p.vendor === vendor.value) ?? null);

// protocolChoices：这条渠道能换成哪些协议（带中文名）。
//
// 两处来源合并：① 当前 vendor 的 `protocol_options`（后端按兼容矩阵给）；
// ② 这条渠道**自己正在用的**那条 —— 老配置里可能是手写的、或 vendor 换了而协议还留着。
// 不把 ② 补进来，下拉会在"暂时没选"的瞬间把用户手写的那条显示成空白，一保存就悄悄换掉协议。
const protocolChoices = computed<ProtocolOption[]>(() => {
  const list: ProtocolOption[] = [...(curPreset.value?.protocol_options ?? [])];
  if (!protocol.value) return list;
  if (list.some((option) => option.protocol === protocol.value)) return list;
  const known = (props.provider?.protocol_options ?? []).find((option) => option.protocol === protocol.value);
  return [...list, { protocol: protocol.value, label: known?.label ?? protocol.value, hint: "当前渠道正在用的接口（不在这个服务商的常见选项里）" }];
});

// protocolHint：选中那一条的一句人话 —— 下拉关起来之后，人只剩 id 可看。
const protocolSelectOptions = computed(() => [
  { label: "用默认（对话补全）", value: "" },
  ...protocolChoices.value.map((option) => ({ label: option.label, value: option.protocol })),
]);
const protocolHint = computed(() =>
  protocolChoices.value.find((option) => option.protocol === protocol.value)?.hint ??
  "留空 = 使用默认接口（对话补全）",
);

// effectiveBase：这一发真会打到哪儿 —— 端点留空时用预设的，所以"空"不等于"没有端点"。
// 从前这一格是空的、提示只是"空=预设端点"，人得自己去猜预设是哪条。
const effectiveBase = computed(() => {
  if (baseUrl.value.trim()) return baseUrl.value.trim();
  const preset = curPreset.value?.base_url ?? props.provider?.base_url ?? "";
  return preset || "（这个服务商没有默认地址，需要自己填）";
});

// modelContext：模型列表里那格"上下文" —— 用户覆盖优先，没配过就显示上游报的。
function modelContext(m: ModelView): string {
  const n = m?.context_override ?? m?.context_length;
  return n == null ? "—" : formatTokens(n);
}

// vendorHint：选中的 vendor 的端点与"要不要密钥"，省得人再去别处查。
const vendorHint = computed(() => {
  const preset = curPreset.value;
  if (!preset) return "";
  const bits: string[] = [];
  if (preset.base_url) bits.push(`默认地址：${preset.base_url}`);
  bits.push(preset.needs_key ? "需要 API Key" : "不需要 API Key");
  return bits.join(" · ");
});

// pickVendor：新建时换 vendor 顺手跟着换 id —— **只在 id 还是"上回自动填的那个"时才动**。
//
// 这条判据是整段的关键：用户已经手打了 `我的渠道`，再改 vendor 就把它冲掉，那是抢方向盘。
// 判"是不是自动填的"用 `autoId` 记住上一次自动填进去的值（空 = 从没自动填过 ⇒ 用户填的）。
const autoId = ref("");
function pickVendor(next: string): void {
  vendor.value = next;
  if (!isNew) return;
  pid.value = providerIdForNextVendor(pid.value, autoId.value, next);
  autoId.value = pid.value;
}
function onVendorChange(value: unknown): void {
  pickVendor(String(value ?? ""));
}
// 手打了 id 就不再当它是自动值（之后改 vendor 不该覆盖）。
function touchId(): void {
  if (pid.value === autoId.value) return;
  autoId.value = "";
}

async function load(): Promise<void> {
  const pres = await api.providerPresets().catch(() => []);
  presets.value = pres ?? [];
  if (isNew) {
    const first = presets.value[0];
    if (first) pickVendor(first.vendor);
    return;
  }
  const p = props.provider!;
  pid.value = p.id;
  vendor.value = p.vendor ?? "";
  protocol.value = p.protocol ?? "";
  baseUrl.value = p.base_url ?? "";
  filter.value = p.model_filter ?? "";
  maxConcurrency.value = p.max_concurrency ?? 0;
  baseFilter.value = filter.value;
  hasKey.value = !!p.has_key;
  models.value = p.models ?? [];
}

async function save(): Promise<void> {
  const wanted = filter.value.trim();
  try {
    let savedID = props.provider?.id ?? "";
    if (isNew) {
      const id = pid.value.trim();
      if (!id) { status.value = "给个渠道 id"; return; }
      const body: CreateProviderRequest = {
        id,
        ...(vendor.value ? { vendor: vendor.value } : {}),
        ...(protocol.value ? { protocol: protocol.value } : {}),
        ...(baseUrl.value.trim() ? { base_url: baseUrl.value.trim() } : {}),
        ...(key.value ? { api_key: key.value } : {}),
        ...(wanted ? { model_filter: wanted } : {}),
        ...(Number(maxConcurrency.value) > 0 ? { max_concurrency: Number(maxConcurrency.value) } : {}),
      };
      await api.createProvider(body);
      savedID = id;
    } else {
      const p = props.provider!;
      const nextID = pid.value.trim();
      if (!nextID) { status.value = "渠道标识不能为空"; return; }
      const body: UpdateProviderRequest = {};
      // 改名（2026-11-05 用户定）：只发改过的格 —— 没动就不带 new_id，
      // 免得每次保存都走一遍"配置 + 库"那两段写。
      if (nextID !== p.id) body.new_id = nextID;
      if (vendor.value !== (p.vendor ?? "")) body.vendor = vendor.value;
      if (protocol.value !== (p.protocol ?? "")) body.protocol = protocol.value;
      if (baseUrl.value.trim() !== (p.base_url ?? "")) body.base_url = baseUrl.value.trim();
      if (key.value) body.api_key = key.value;
      if (wanted !== baseFilter.value) body.model_filter = wanted;
      if (Number(maxConcurrency.value) !== Number(p.max_concurrency ?? 0)) {
        body.max_concurrency = Number(maxConcurrency.value) || 0;
      }
      if (Object.keys(body).length) await api.patchProvider(p.id, body);
      if (body.new_id) savedID = body.new_id;
    }
    baseFilter.value = wanted;
    // 带上 id：父组件要拿它把 editProvider 换成新对象（改名之后旧 props 的 id 已经不存在，
    // 留在原地再点保存会 404）。新建那条照旧用返回的 id。
    emit("saved", savedID);
    // 「保存并刷新」：保存完**总是**再拉一次模型（不是只在筛选器变了时）。
    // 新建的渠道刷新完就回列表（这个二级页的 isNew 是挂载时定的常量，留着再点保存会 409）；
    // 已有的留在原地，结果直接写在状态行里。
    await reloadModels(savedID);
    if (isNew) emit("close");
  } catch (e: unknown) {
    status.value = `保存失败：${e instanceof Error ? e.message : String(e)}`;
  }
}

// reloadModels：按当前配置重拉该渠道的模型清单（「保存并刷新」的刷新半程）。
async function reloadModels(id: string): Promise<void> {
  // dummy 没有上游可拉：保存成功就够了，别报一条看起来像失败的刷新错误。
  if (vendor.value === "dummy") {
    status.value = "已保存（本地示例：不联网，没有模型可刷新）";
    emit("saved", id);
    return;
  }
  try {
    const view = await api.refreshProvider(id);
    models.value = view?.models ?? [];
    status.value = `已保存并刷新：导入 ${modelCount.value} 个模型` +
      (baseFilter.value ? `（筛选：${baseFilter.value}）` : "");
    emit("saved", id);
  } catch (e: unknown) {
    status.value = `已保存，但刷新失败：${e instanceof Error ? e.message : String(e)}`;
  }
}

// testConnection：只拉一次 /models，**不保存、不落库**。测的是表单里此刻的值，
// 所以新建/改到一半也能先测；后端给 15 秒短上限。
async function testConnection(): Promise<void> {
  const id = pid.value.trim();
  if (!id) { status.value = "先填渠道 id 再测"; return; }
  status.value = "测试中…（最多 15 秒）";
  try {
    const body: TestProviderRequest = {
      vendor: vendor.value,
      protocol: protocol.value,
      base_url: baseUrl.value.trim(),
      model_filter: filter.value.trim(),
    };
    if (key.value) body.api_key = key.value;
    status.value = "测试中…（最多 15 秒）";
    const out = await api.testProvider(id, body);
    if (out?.ok) {
      status.value = `连接成功：${out.models} 个模型` +
        (out.filtered ? `（筛掉 ${out.filtered} 个）` : "") +
        (out.note ? ` · ${out.note}` : "");
    } else {
      status.value = `连接失败：${out?.error || "未知原因"}`;
    }
  } catch (e: unknown) {
    status.value = `测试失败：${e instanceof Error ? e.message : String(e)}`;
  }
}

async function clearKey(): Promise<void> {
  if (isNew) { key.value = ""; return; }
  try {
    await api.patchProvider(props.provider!.id, { api_key: "" });
    hasKey.value = false;
    key.value = "";
    status.value = "密钥已清";
  } catch (e: unknown) { status.value = `清密钥失败：${e instanceof Error ? e.message : String(e)}`; }
}

onMounted(() => {
  load().catch((e: unknown) => { status.value = `载入失败：${e instanceof Error ? e.message : String(e)}`; });
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
function onKey(e: KeyboardEvent): void {
  // 模型编辑弹窗开着时，Esc 只关弹窗；别一下退到渠道列表（两层一起关）。
  if (e.key === "Escape" && !editingModel.value) emit("close");
}
function onModelSaved(updated: ModelView): void {
  models.value = models.value.map((item) => item.upstream_id === updated.upstream_id ? updated : item);
  editingModel.value = null;
  // 顶栏模型下拉读的是另一个口（/models）：显示名/图标改完要让它重拉。
  emit("model-saved");
}

</script>

<template>
  <!-- 二级页（2026-10-10 起不再是弹窗）：嵌在设置视图的 Provider tab 里，列表 ⇄ 详情就地切换。 -->
  <div class="detail-pane">
    <div class="panel-head">
      <VBtn icon variant="text" size="small" type="button" title="返回列表（Esc）" @click="emit('close')">←</VBtn>
      <span class="status">{{ isNew ? "新建渠道" : `渠道「${pid}」（${modelCount} 个模型）` }}</span>
    </div>
    <h3>基本</h3>
    <!-- 先问"服务商"、再问 id（2026-10-28）：从前 id 排在最前面，可它恰恰是**最不该让人自己想**的那格 ——
         改 vendor 时它还会跟着变，先填等于白填。 -->
    <!-- vendor / protocol 一律**中文名在前、内部 id 附后**（2026-10-28 用户：「应该用中文化的名字吧」）。
         id 不藏起来：手改 config/providers.json 的人得能对上号，出错信息里也打的是 id。 -->
    <label class="field"><span>服务商</span><VSelect :model-value="vendor" :items="vendorOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onVendorChange" /></label>
    <p v-if="vendorHint" class="hint">{{ vendorHint }}</p>
    <label class="field"><span>渠道标识</span><VTextField density="compact" variant="outlined" hide-details v-model="pid" placeholder="新建时自动生成" @update:model-value="touchId" /></label>
    <p v-if="isNew" class="hint">这个标识是渠道在本机的名字，会出现在模型列表和配置文件里。换服务商时会自动跟着变；你手改过之后就不再自动改。</p>
    <p v-else class="hint">改名会一起改掉已绑定这条渠道的会话与模型清单；库里已有同名数据时会被拒绝（不合并两个渠道）。</p>
    <label class="field"><span>接口类型</span><VSelect v-model="protocol" :items="protocolSelectOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details /></label>
    <p class="hint">{{ protocolHint }}</p>
    <label class="field"><span>接口地址</span><VTextField density="compact" variant="outlined" hide-details v-model="baseUrl" :placeholder="effectiveBase" /></label>
    <p class="hint">实际请求地址：<code>{{ effectiveBase }}</code></p>
    <h3>密钥</h3>
    <label class="field"><span>{{ hasKey ? "API Key（留空=不改）" : "API Key（可空）" }}</span><VTextField density="compact" variant="outlined" hide-details v-model="key" placeholder="API Key" type="password" /></label>
    <h3>模型筛选（可选）</h3>
    <label class="field"><span>只导入匹配的模型</span>
      <VTextField density="compact" variant="outlined" hide-details v-model="filter" placeholder="留空 = 全部（例如：deepseek,mimo）" />
    </label>
    <p class="hint">
      用半角逗号分隔几个关键词；模型名包含<b>任意一个</b>就会导入，不区分大小写。
      不支持 <code>*</code> 通配符；留空 = 全部导入。
    </p>
    <label class="field"><span>最大并发（0 = 不限）</span><VTextField density="compact" variant="outlined" hide-details v-model="maxConcurrency" type="number" min="0" /></label>
    <p class="hint">同一渠道同时最多几个请求；高级压缩的作业并发会与它取较小值。</p>
    <div class="row">
      <VBtn color="primary" variant="flat" size="small" type="button" @click="save">保存并刷新</VBtn>
      <VBtn variant="text" size="small" type="button" @click="testConnection">测试连通性</VBtn>
      <VBtn color="error" variant="text" size="small" v-if="!isNew && (hasKey || key)" type="button" @click="clearKey">清密钥</VBtn>
      <span class="status">{{ status }}</span>
    </div>
    <!-- 模型列表（2026-11-02 用户定）：渠道详情里要看得见「这个渠道导入了哪些模型」。
         2026-11-07：每行可编辑模型自己的用户列（显示名 / 上下文 / 参数）。 -->
    <h3>模型（{{ modelCount }}）</h3>
    <p class="hint">这个渠道从上游导入的模型；点「编辑」改显示名、上下文覆盖或采样参数。</p>
    <div v-if="isNew || !modelCount" class="status">
      {{ isNew ? "新建渠道保存并刷新后，导入的模型会出现在这里。" : "还没有导入模型 —— 点「保存并刷新」试试。" }}
    </div>
    <div v-else class="provider-models">
      <VTable class="plist">
        <thead><tr><th>模型</th><th>上下文</th><th></th></tr></thead>
        <tbody><tr v-for="m in models" :key="m.upstream_id">
          <td :title="m.display_name || m.name || m.upstream_id">
            <span class="model-name-line"><ModelIcon :icon="m.icon" :brand="m.brand" />{{ m.name || m.upstream_id }}</span>
            <div class="model-upstream" :title="m.upstream_id">{{ m.upstream_id }}<template v-if="m.matched_rules?.length"> · {{ m.matched_rules.join(" / ") }}</template></div>
          </td>
          <td>{{ modelContext(m) }}</td>
          <td class="ops"><VBtn variant="text" size="small" type="button" @click="editingModel = m">编辑</VBtn></td>
        </tr></tbody>
      </VTable>
    </div>
    <ModelEditor
      v-if="editingModel"
      :provider="pid"
      :model="editingModel"
      @close="editingModel = null"
      @saved="onModelSaved"
    />
  </div>
</template>
