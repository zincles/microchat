<script setup lang="ts">
// 外观面板（设置视图的「外观」区）。
//
// 这块**原本住在用户视图**（`UserView.vue`）。账号砍掉之后用户视图整个没了 ——
// 它原来那半：**个人偏好**（主题 / 缩放 / 背景图 / 滚动收起 / 发送键 /
// 预演 / 命令菜单）原样搬到这儿；2026-11-01 又加了一区 **markdown 渲染**。
//
// 2026-11-01 两件大事：
//   ① **全部偏好搬到服务器**（`data/config/prefs.json`，见 `utils/prefs.ts` 顶上的说明）。
//      背景图那张也搬了：从前是一枚 data URL 躺在 localStorage、5MB 配额说满就满；
//      现在落服务器上，换设备也在。
//   ② **markdown 逐类型开关**（用户口径：「所有的会被 Markdown 解析的非 <div> HTML 块，
//      我们为每个类型都加个开关。不然瞎解析不好。」）—— 类型清单由**后端**给
//      （`GET /config/prefs` 的 `markdown_types`），前端不许另抄一张：
//      少一条就留个点不动的死开关，多一条就是个后端不认识的选项。
//
// ⚠ 全部**即时生效**，没有"保存"按钮：改外观就该当场看见。`emit("prefs-saved")` 保留 ——
//   聊天壳上有活开关（命令菜单那颗 ☰）要跟着变。
import { VSwitch, VBtn, VTextField, VSelect, VSlider } from "vuetify/components";
import { ref, computed, onMounted } from "vue";
import {
  themePref, saveTheme, sendMode, saveSendMode,
  previewOn, savePreview,
  commandMenuOn, saveCommandMenu,
  chromeHideOn, saveChromeHide,
  textScale as textScalePref, saveTextScale, flushPrefs,
  fontBody as fontBodyPref, fontMono as fontMonoPref, saveFontBody, saveFontMono,
  markdownPrefs, markdownTypes, saveMarkdown, saveBackground,
  refreshPrefs,
  TEXT_SCALE_MIN, TEXT_SCALE_MAX, TEXT_SCALE_STEP,
} from "../utils/prefs.ts";
import {
  currentBackground, backgroundCssUrl, applyBackground, describeBackground,
  uploadBackground, clearBackground,
} from "../utils/background.ts";
import type { MarkdownType } from "../api/types.ts";

const emit = defineEmits<{ "prefs-saved": [] }>();

const theme = ref("");
const send = ref("enter");
const preview = ref(true);
// 命令菜单（默认关）：输入框左侧那颗 ☰ 与输入 "/" 弹出的面板。移动端碍事 ⇒ 想用的人才开。
const cmdMenu = ref(false);
// 界面缩放（2026-10-28）：**拖的时候就生效** —— 尺寸这东西得看着调。
const textScale = ref(1);
const scaleBounds = { min: TEXT_SCALE_MIN, max: TEXT_SCALE_MAX, step: TEXT_SCALE_STEP };
const scalePercent = computed(() => Math.round(textScale.value * 100));
// 字体（2026-11-04）：标准字体管全站，等宽字体管代码块；空 = 内置系统字族。
const fontBody = ref("");
const fontMono = ref("");
// 滚动隐藏上下栏（触屏；默认开）。
const chromeHide = ref(true);
const bgUrl = ref("");
const bgStatus = ref("");
const bgPreview = ref("");
const themeOptions = [
  { label: "亮色", value: "light" },
  { label: "暗色（默认）", value: "dark" },
  { label: "Breeze", value: "breeze" },
];
const sendOptions = [
  { label: "按回车发送（Shift+回车换行）", value: "enter" },
  { label: "按 Shift+回车发送", value: "shift-enter" },
  { label: "只用发送按钮（回车换行）", value: "button" },
];
const bgFile = ref<HTMLInputElement | null>(null);

// —— markdown 开关 ——
const mdEnabled = ref(true);
// 关掉的那些类型（id 集合；后端口径是"记住关掉的"，不是"记住开着的"——
// 这样新加的语法类型自动跟着开，加一条 `MarkdownTypes` 不用改任何文件）。
const mdDisabled = ref<string[]>([]);
const mdTypes = ref<MarkdownType[]>([]);
// 按 block / inline 分组（顺序沿用后端清单的顺序，界面不自己排）。
const mdGroups = computed<Array<{ kind: string; title: string; items: MarkdownType[] }>>(() => {
  const groups = [
    { kind: "block", title: "整段格式", items: [] as MarkdownType[] },
    { kind: "inline", title: "文字里的格式", items: [] as MarkdownType[] },
  ];
  for (const item of mdTypes.value) {
    const group = groups.find((g) => g.kind === item.kind) ?? groups[1];
    group.items.push(item);
  }
  return groups.filter((g) => g.items.length);
});

function onScaleInput(value: number | string): void {
  textScale.value = Number(value);
  saveTextScale(textScale.value);
  emit("prefs-saved");
}
function onTheme(value: unknown): void {
  theme.value = String(value ?? "");
  persist();
}
function onSend(value: unknown): void {
  send.value = String(value ?? "");
  persist();
}
function onScaleCommit(): void {
  // 松手就落盘，不等 250ms 防抖（用户可能随后马上刷新）。
  flushPrefs();
}
function resetScale(): void {
  textScale.value = 1;
  saveTextScale(1);
  emit("prefs-saved");
}

// loadFromPrefs：把内存里的偏好灌进这些 ref。
// 每次同步完都要重跑一次（服务器可能改过东西，例如迁移、或另一个标签页改的）。
function loadFromPrefs(): void {
  theme.value = themePref();
  send.value = sendMode();
  preview.value = previewOn();
  cmdMenu.value = commandMenuOn();
  textScale.value = textScalePref();
  fontBody.value = fontBodyPref();
  fontMono.value = fontMonoPref();
  chromeHide.value = chromeHideOn();
  const md = markdownPrefs();
  mdEnabled.value = md.enabled !== false;
  mdDisabled.value = [...(md.disabled ?? [])];
  mdTypes.value = markdownTypes();
  const bg = currentBackground();
  bgUrl.value = bg?.kind === "url" ? (bg.value ?? "") : "";
  bgPreview.value = backgroundCssUrl(bg);
  bgStatus.value = describeBackground(bg);
}

onMounted(loadFromPrefs);

// —— 每一项改了就落盘（服务器），并喊一声让聊天壳跟上 ——
function persist(): void {
  saveTheme(theme.value);
  saveSendMode(send.value);
  savePreview(preview.value);
  saveCommandMenu(cmdMenu.value);
  saveTextScale(textScale.value);
  saveFontBody(fontBody.value);
  saveFontMono(fontMono.value);
  saveChromeHide(chromeHide.value);
  emit("prefs-saved"); // 聊天壳上的活开关立刻跟上（命令菜单那颗 ☰）
}

// 开关不能写成 `v-model + @update:model-value="persist"`：两个监听同时挂在 update 事件上时，
// 显式那个**可能先跑** ⇒ `persist()` 读到的是旧值（实测：界面翻成 on，服务端还是 off）。
// 所以一律"先赋值，再存"——参数就是 VSwitch 给的新值。
function saveFonts(): void {
  // 规范化后的值写回输入框：`A; B` 这种破坏声明的输入会被清成空（回落系统字族）。
  fontBody.value = saveFontBody(fontBody.value);
  fontMono.value = saveFontMono(fontMono.value);
  emit("prefs-saved");
}
function resetFonts(): void {
  fontBody.value = "";
  fontMono.value = "";
  saveFonts();
}
function setChromeHide(v: boolean): void { chromeHide.value = v; persist(); }
function setPreview(v: boolean): void { preview.value = v; persist(); }
function setCmdMenu(v: boolean): void { cmdMenu.value = v; persist(); }
function setMdEnabled(v: boolean): void { mdEnabled.value = v; persistMarkdown(); }

// —— markdown ——
function persistMarkdown(): void {
  saveMarkdown({ enabled: mdEnabled.value, disabled: mdDisabled.value });
}
function toggleType(id: string, on: boolean): void {
  const set = new Set(mdDisabled.value);
  if (on) set.delete(id);
  else set.add(id);
  // 顺序按后端清单来（与后端 `Normalize` 同一个规矩：同一个状态写同一份文件）。
  mdDisabled.value = mdTypes.value.map((t) => t.id).filter((x) => set.has(x));
  persistMarkdown();
}
function typeOn(id: string): boolean {
  return !mdDisabled.value.includes(id);
}
// 全部关掉 / 全部打开（也走同一个落盘口）。
function disableAll(): void {
  mdDisabled.value = mdTypes.value.map((t) => t.id);
  persistMarkdown();
}
function enableAll(): void {
  mdDisabled.value = [];
  persistMarkdown();
}

// —— 背景图（外观）：URL / 上传二选一，**即时生效** ——
function applyBgUrl(): void {
  const value = bgUrl.value.trim();
  if (!value) {
    bgStatus.value = "先填图片地址（或改用上传）";
    return;
  }
  // `url` 型是纯元信息（服务器上没有文件）⇒ 直接写偏好那一格，不上传。
  saveBackground({ kind: "url", value });
  applyBackground();
  loadFromPrefs();
  emit("prefs-saved");
}

async function pickBgFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  bgStatus.value = "上传中…";
  try {
    await uploadBackground(file);       // 压好 + 传上去（服务器那边顺手登记元信息）
    await refreshPrefs();               // 拿回服务器登记好的那份（含版本号）
    loadFromPrefs();
    applyBackground();
    emit("prefs-saved");
  } catch (e: unknown) {
    bgStatus.value = `上传失败：${e instanceof Error ? e.message : String(e)}`;
  }
  input.value = "";
}

async function clearBg(): Promise<void> {
  try {
    await clearBackground();
    await refreshPrefs();
    loadFromPrefs();
    applyBackground();
    emit("prefs-saved");
  } catch (e: unknown) {
    bgStatus.value = `清除失败：${e instanceof Error ? e.message : String(e)}`;
  }
}
</script>

<template>
  <section id="panel-appearance" class="panel">
    <section class="settings-card">
      <h3>外观</h3>
      <!-- 只有三套（2026-10-28 用户定）。id 必须与 prefs.ts 的 THEME_IDS 逐个对得上，
           contract.test.js 钉着 —— 这里多一个少一个都不会报错，只会让人选到没规则的主题。 -->
      <label class="field"><span>主题</span><VSelect :model-value="theme" :items="themeOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onTheme" /></label>
      <!-- 界面缩放（2026-10-28，照 OpenWebUI 的 UI Scale）：**拖动即生效**，不用点保存。
           `.field` 是两列网格（label | 控件）：滑杆与百分比必须包成**一个**子元素，
           否则百分比会被挤到第二行第一列去。 -->
      <label class="field"><span>界面缩放</span>
        <span class="scale-wrap">
          <VSlider
            :model-value="textScale"
            :min="scaleBounds.min"
            :max="scaleBounds.max"
            :step="scaleBounds.step"
            color="primary"
            density="compact"
            hide-details
            aria-label="界面缩放"
            @update:model-value="onScaleInput"
            @end="onScaleCommit"
          />
          <b class="scale-value">{{ scalePercent }}%</b>
        </span>
      </label>
      <div class="row">
        <VBtn variant="text" size="small" type="button" @click="resetScale">复位 100%</VBtn>
        <span class="status">拖动立即生效；字号和留白会一起等比缩放</span>
      </div>
      <label class="field"><span>标准字体</span><VTextField density="compact" variant="outlined" hide-details v-model="fontBody" @change="saveFonts" placeholder="留空 = 系统默认（CSS font-family）" /></label>
      <label class="field"><span>等宽字体（代码块）</span><VTextField density="compact" variant="outlined" hide-details v-model="fontMono" @change="saveFonts" placeholder="留空 = 系统等宽" /></label>
      <p class="hint">可按 CSS 写法给多个后备字体，例如 <code>"Noto Sans SC", sans-serif</code>。标准字体用于全站，等宽字体只用于代码块；留空恢复内置系统字族。</p>
      <div class="row"><VBtn variant="text" size="small" type="button" @click="resetFonts">恢复默认字体</VBtn></div>
      <label class="field"><span>背景图地址</span><VTextField density="compact" variant="outlined" hide-details v-model="bgUrl" placeholder="https://…（跨域也可）" /></label>
      <div class="row">
        <VBtn color="primary" variant="flat" size="small" type="button" @click="applyBgUrl">应用地址</VBtn>
        <VBtn variant="text" size="small" type="button" @click="bgFile?.click()">上传图片…</VBtn>
        <input ref="bgFile" type="file" accept="image/*" hidden @change="pickBgFile" />
        <VBtn color="error" variant="text" size="small" type="button" @click="clearBg">清除</VBtn>
      </div>
      <div class="row"><span class="status">当前背景：{{ bgStatus }}</span></div>
      <p v-if="bgPreview" class="hint">背景图保存在服务器上 —— 换设备也在。</p>
    </section>

    <section class="settings-card">
      <h3>布局</h3>
      <label class="field"><span>滚动时自动收起顶栏/底栏（仅触屏）</span><VSwitch :model-value="chromeHide" @update:model-value="(v) => setChromeHide(v === true)" color="primary" density="compact" hide-details /></label>
      <p class="hint">向下浏览时顶栏逐步收起，往回看时底栏逐步收起 —— 一次只收一边，反向滚动就放回来；到顶或到底立刻全部放回。只在触屏上生效。</p>
    </section>

    <section class="settings-card">
      <h3>输入</h3>
      <label class="field"><span>发送方式</span><VSelect :model-value="send" :items="sendOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onSend" /></label>
      <label class="field"><span>输入时预览将要发送的内容</span><VSwitch :model-value="preview" @update:model-value="(v) => setPreview(v === true)" color="primary" density="compact" hide-details /></label>
      <label class="field"><span>命令菜单</span><VSwitch :model-value="cmdMenu" @update:model-value="(v) => setCmdMenu(v === true)" color="primary" density="compact" hide-details /></label>
      <p class="hint">发送方式决定回车怎么用；预览会在输入时先算一遍会发给模型的内容（可在上下文工作区查看）；打开命令菜单后，输入框左侧会出现 ☰，输入 <code>/</code> 会弹出命令列表。</p>
    </section>

    <section class="settings-card">
      <!-- ── markdown 渲染（2026-11-01）──
           用户口径：「不然瞎解析不好」⇒ 每一类都能单独关掉，关掉的含义是**原样显示**
           （把 `**粗**` 照字面画出来，而不是悄悄变成"粗"、更不是吃掉标记）。 -->
      <h3>文本格式（Markdown）</h3>
      <label class="field"><span>解析 Markdown 格式</span><VSwitch :model-value="mdEnabled" @update:model-value="(v) => setMdEnabled(v === true)" color="primary" density="compact" hide-details /></label>
      <p class="hint">
        关闭后，整条消息按<b>原文显示</b>（标记不转换，但换行保留）。
        开启时，下面每一类格式还能单独关闭 —— 关闭的那一类同样按原文显示。
      </p>
      <!-- 类型清单是**后端给的**（`GET /config/prefs` 的 `markdown_types`）：
           名字与例子都从那儿来，前端不另抄一张表。 -->
      <div v-for="group in mdGroups" :key="group.kind" class="md-group">
        <div class="md-group-head">
          <span>{{ group.title }}</span>
          <span class="md-group-ops">
            <VBtn variant="text" size="small" type="button" :disabled="!mdEnabled" @click="disableAll">全部关闭</VBtn>
            <VBtn variant="text" size="small" type="button" :disabled="!mdEnabled" @click="enableAll">全部开启</VBtn>
          </span>
        </div>
        <label v-for="item in group.items" :key="item.id" class="field md-row">
          <span>
            {{ item.name }}
            <code class="md-sample">{{ item.sample }}</code>
          </span>
          <VSwitch :model-value="typeOn(item.id)" :disabled="!mdEnabled" @update:model-value="(on) => toggleType(item.id, on === true)" color="primary" density="compact" hide-details />
        </label>
      </div>
      <p v-if="!mdEnabled" class="hint">总开关关闭时，下面的单项开关不生效（设置会保留，重新打开就恢复）。</p>
      <p class="hint">
        <b>HTML 标签一律不解析</b>：程序会先把 <code>&amp;</code> 和 <code>&lt;</code> 转义掉，防止注入脚本。
      </p>
      <p class="hint">这一页<b>没有保存按钮</b>：改动立即生效，并保存到服务器。</p>
    </section>
  </section>
</template>
