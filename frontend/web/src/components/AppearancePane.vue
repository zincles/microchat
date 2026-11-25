<script setup lang="ts">
// 设置中的外观、输入和 Markdown 页共用此面板；每次只保存当前控件改动的偏好，
// 不用另一个页面还留在内存中的旧值覆盖服务器偏好。缩放必须显式保存。
import { VSwitch, VBtn, VTextField, VSelect, VSlider } from "vuetify/components";
import { ref, shallowRef, computed, onMounted } from "vue";
import {
  themePref, saveTheme, sendMode, saveSendMode,
  previewOn, savePreview,
  commandMenuOn, saveCommandMenu,
  chromeHideOn, saveChromeHide,
  textScale as textScalePref, saveTextScale,
  fontBody as fontBodyPref, fontMono as fontMonoPref, saveFontBody, saveFontMono,
  userName as userNamePref, saveUserName,
  markdownPrefs, markdownTypes, saveMarkdown, saveBackground,
  refreshPrefs,
  TEXT_SCALE_MIN, TEXT_SCALE_MAX, TEXT_SCALE_STEP,
} from "../utils/prefs.ts";
import {
  currentBackground, backgroundCssUrl, applyBackground, describeBackground,
  uploadBackground, clearBackground,
} from "../utils/background.ts";
import type { MarkdownType } from "../api/types.ts";
const props = withDefaults(defineProps<{
  section?: "appearance" | "input" | "markdown";
}>(), { section: "appearance" });

const emit = defineEmits<{ "prefs-saved": [] }>();

const theme = ref("");
const send = ref("enter");
const preview = ref(true);
// 命令菜单（默认关）：输入框左侧那颗 ☰ 与输入 "/" 弹出的面板。移动端碍事 ⇒ 想用的人才开。
const cmdMenu = ref(false);
// 缩放滑杆与复位只改草稿，保存时才应用。
const textScale = ref(1);
const scaleDirty = ref(false);
const scaleBounds = { min: TEXT_SCALE_MIN, max: TEXT_SCALE_MAX, step: TEXT_SCALE_STEP };
const scalePercent = computed(() => Math.round(textScale.value * 100));
// 字体（2026-11-04）：标准字体管全站，等宽字体管代码块；空 = 内置系统字族。
const fontBody = ref("");
const fontMono = ref("");
// 用户名称：{{user_name}} / {{user}} 的值（只服务宏替换，不是账号）。
const userName = ref("");
// 滚动隐藏上下栏（触屏；默认开）。
const chromeHide = ref(true);
const bgUrl = ref("");
const bgStatus = ref("");
const bgPreview = ref("");
const bgAction = shallowRef<"upload" | "remove" | null>(null);
const themeOptions = [
  { label: "亮色", value: "light" },
  { label: "暗色", value: "dark" },
  { label: "自动（跟随系统）", value: "auto" },
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
  // 只改草稿值，**不应用**；点「保存缩放」才落盘 + 改 CSS 变量。
  textScale.value = Number(value);
  scaleDirty.value = true;
}
function onTheme(value: unknown): void {
  theme.value = String(value ?? "");
  saveTheme(theme.value);
  emit("prefs-saved");
}
function onSend(value: unknown): void {
  send.value = String(value ?? "");
  saveSendMode(send.value);
  emit("prefs-saved");
}
function saveScale(): void {
  saveTextScale(textScale.value);
  scaleDirty.value = false;
  emit("prefs-saved");
}
function resetScale(): void {
  textScale.value = 1;
  scaleDirty.value = true;
}

function loadFromPrefs(): void {
  switch (props.section) {
    case "appearance":
      theme.value = themePref();
      textScale.value = textScalePref();
      fontBody.value = fontBodyPref();
      fontMono.value = fontMonoPref();
      chromeHide.value = chromeHideOn();
      loadBackground();
      break;
    case "input":
      userName.value = userNamePref();
      send.value = sendMode();
      preview.value = previewOn();
      cmdMenu.value = commandMenuOn();
      break;
    case "markdown": {
      const md = markdownPrefs();
      mdEnabled.value = md.enabled !== false;
      mdDisabled.value = md.disabled ?? [];
      mdTypes.value = markdownTypes();
      break;
    }
  }
}

function loadBackground(): void {
  const bg = currentBackground();
  bgUrl.value = bg?.kind === "url" ? (bg.value ?? "") : "";
  bgPreview.value = backgroundCssUrl(bg);
  bgStatus.value = describeBackground(bg);
}

onMounted(loadFromPrefs);

function saveFonts(): void {
  // 规范化后的值写回输入框：`A; B` 这种破坏声明的输入会被清成空（回落系统字族）。
  fontBody.value = saveFontBody(fontBody.value);
  fontMono.value = saveFontMono(fontMono.value);
  emit("prefs-saved");
}
function saveName(): void {
  userName.value = String(userName.value ?? "").trim();
  saveUserName(userName.value);
  emit("prefs-saved");
}
function resetFonts(): void {
  fontBody.value = "";
  fontMono.value = "";
  saveFonts();
}
function setChromeHide(v: boolean): void {
  chromeHide.value = v;
  saveChromeHide(v);
  emit("prefs-saved");
}

function setPreview(v: boolean): void {
  preview.value = v;
  savePreview(v);
  emit("prefs-saved");
}

function setCmdMenu(v: boolean): void {
  cmdMenu.value = v;
  saveCommandMenu(v);
  emit("prefs-saved");
}

// —— Markdown：总开关和关闭类型列表分别保存 ——
function setMdEnabled(v: boolean): void {
  mdEnabled.value = v;
  saveMarkdown({ enabled: v });
  emit("prefs-saved");
}

function setDisabledTypes(disabled: string[]): void {
  mdDisabled.value = disabled;
  saveMarkdown({ disabled });
  emit("prefs-saved");
}
function toggleType(id: string, on: boolean): void {
  const set = new Set(mdDisabled.value);
  if (on) set.delete(id);
  else set.add(id);
  // 顺序按后端清单来（与后端 `Normalize` 同一个规矩：同一个状态写同一份文件）。
  setDisabledTypes(mdTypes.value.map((t) => t.id).filter((x) => set.has(x)));
}
function typeOn(id: string): boolean {
  return !mdDisabled.value.includes(id);
}
// 全部关掉 / 全部打开（也走同一个落盘口）。
function disableAll(): void {
  setDisabledTypes(mdTypes.value.map((t) => t.id));
}
function enableAll(): void {
  setDisabledTypes([]);
}

// —— 背景图（外观）：URL / 上传二选一，**即时生效** ——
function applyBgUrl(): void {
  if (bgAction.value) return;
  const value = bgUrl.value.trim();
  if (!value) {
    bgStatus.value = "先填图片地址（或改用上传）";
    return;
  }
  // `url` 型是纯元信息（服务器上没有文件）⇒ 直接写偏好那一格，不上传。
  saveBackground({ kind: "url", value });
  applyBackground();
  loadBackground();
  emit("prefs-saved");
}

async function pickBgFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file || bgAction.value) return;
  bgAction.value = "upload";
  try {
    await uploadBackground(file);       // 压好 + 传上去（服务器那边顺手登记元信息）
    await refreshPrefs();               // 拿回服务器登记好的那份（含版本号）
    loadBackground();
    applyBackground();
    emit("prefs-saved");
  } catch (e: unknown) {
    bgStatus.value = `上传失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    input.value = "";
    bgAction.value = null;
  }
}

async function clearBg(): Promise<void> {
  if (bgAction.value) return;
  bgAction.value = "remove";
  try {
    await clearBackground();
    await refreshPrefs();
    loadBackground();
    applyBackground();
    emit("prefs-saved");
  } catch (e: unknown) {
    bgStatus.value = `清除失败：${e instanceof Error ? e.message : String(e)}`;
  } finally {
    bgAction.value = null;
  }
}
</script>

<template>
  <section :id="`panel-${props.section}`" class="panel">

    <template v-if="props.section === 'appearance'">
      <section class="settings-card">
        <h3>外观</h3>
        <!-- 只有三套（2026-10-28 用户定）。id 必须与 prefs.ts 的 THEME_IDS 逐个对得上，
             contract.test.js 钉着 —— 这里多一个少一个都不会报错，只会让人选到没规则的主题。 -->
        <label id="setting-theme" tabindex="-1" class="field"><span>主题</span><VSelect :model-value="theme" :items="themeOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onTheme" /></label>
        <!-- 滑杆与百分比放在同一列；缩放只在点击「保存缩放」后应用。 -->
        <label id="setting-scale" tabindex="-1" class="field"><span>界面缩放</span>
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
            />
            <b class="scale-value">{{ scalePercent }}%</b>
          </span>
        </label>
        <div class="row">
          <VBtn color="primary" variant="flat" size="small" type="button" :disabled="!scaleDirty" @click="saveScale">保存缩放</VBtn>
          <VBtn variant="text" size="small" type="button" @click="resetScale">复位 100%</VBtn>
          <span class="status">{{ scaleDirty ? "未保存：点「保存缩放」后才会应用" : "字号和留白等比缩放，点「保存缩放」才应用" }}</span>
        </div>
        <label id="setting-font-body" tabindex="-1" class="field"><span>标准字体</span><VTextField density="compact" variant="outlined" hide-details v-model="fontBody" @change="saveFonts" placeholder="留空 = 系统默认（CSS font-family）" /></label>
        <label id="setting-font-mono" tabindex="-1" class="field"><span>等宽字体（代码块）</span><VTextField density="compact" variant="outlined" hide-details v-model="fontMono" @change="saveFonts" placeholder="留空 = 系统等宽" /></label>
        <p class="hint">可按 CSS 写法给多个后备字体，例如 <code>"Noto Sans SC", sans-serif</code>。标准字体用于全站，等宽字体只用于代码块；留空恢复内置系统字族。</p>
        <div class="row"><VBtn variant="text" size="small" type="button" @click="resetFonts">恢复默认字体</VBtn></div>
        <div id="setting-background" tabindex="-1">
          <label class="field"><span>背景图地址</span><VTextField density="compact" variant="outlined" hide-details v-model="bgUrl" placeholder="https://…（跨域也可）" /></label>
          <div class="row">
            <VBtn color="primary" variant="flat" size="small" type="button" :disabled="!!bgAction" @click="applyBgUrl">应用地址</VBtn>
            <VBtn variant="text" size="small" type="button" :loading="bgAction === 'upload'" :disabled="!!bgAction" @click="bgFile?.click()">上传图片…</VBtn>
            <input ref="bgFile" type="file" accept="image/*" hidden @change="pickBgFile" />
            <VBtn color="error" variant="text" size="small" type="button" :loading="bgAction === 'remove'" :disabled="!!bgAction" @click="clearBg">清除</VBtn>
            <span class="status" role="status">当前背景：{{ bgStatus }}</span>
          </div>
          <p v-if="bgPreview" class="hint">背景图保存在服务器上 —— 换设备也在。</p>
        </div>
      </section>

      <section class="settings-card">
        <h3>布局</h3>
        <label id="setting-chrome-hide" tabindex="-1" class="field"><span>滚动时自动收起顶栏/底栏（仅触屏）</span><VSwitch :model-value="chromeHide" @update:model-value="(v) => setChromeHide(v === true)" color="primary" density="compact" hide-details /></label>
        <p class="hint">往下看时收起顶栏，往回看时收起底栏；反向滚动时恢复，到顶或到底时全部恢复。</p>
      </section>
    </template>

    <section v-else-if="props.section === 'input'" class="settings-card">
      <h3>输入</h3>
      <label id="setting-user-name" tabindex="-1" class="field"><span>用户名称</span><VTextField density="compact" variant="outlined" hide-details v-model="userName" @change="saveName" placeholder="留空 = 你" /></label>
      <p class="hint">用于提示词与消息里的 <code v-text="'{{user_name}}'"></code> / <code v-text="'{{user}}'"></code> 宏；只做替换，不影响世界状态。</p>
      <label id="setting-send-mode" tabindex="-1" class="field"><span>发送方式</span><VSelect :model-value="send" :items="sendOptions" item-title="label" item-value="value" density="compact" variant="outlined" hide-details @update:model-value="onSend" /></label>
      <label id="setting-input-preview" tabindex="-1" class="field"><span>输入时预览将要发送的内容</span><VSwitch :model-value="preview" @update:model-value="(v) => setPreview(v === true)" color="primary" density="compact" hide-details /></label>
      <p class="hint">输入时预演将发给模型的内容，可在上下文工作区查看。</p>
      <label id="setting-command-menu" tabindex="-1" class="field"><span>命令菜单</span><VSwitch :model-value="cmdMenu" @update:model-value="(v) => setCmdMenu(v === true)" color="primary" density="compact" hide-details /></label>
      <p class="hint">开启后输入框左侧会出现 ☰，输入 <code>/</code> 会弹出命令列表。</p>
    </section>

    <section v-else class="settings-card">
      <h3>文本格式（Markdown）</h3>
      <label id="setting-markdown-enabled" tabindex="-1" class="field"><span>解析 Markdown 格式</span><VSwitch :model-value="mdEnabled" @update:model-value="(v) => setMdEnabled(v === true)" color="primary" density="compact" hide-details /></label>
      <p class="hint">
        关闭后整条消息按<b>原文显示</b>，保留换行和标记。开启时可以单独关闭某类格式，让它按原文显示。改动会自动保存。
      </p>
      <p v-if="!mdEnabled" class="hint">各类格式的设置会保留，打开总开关后恢复生效。</p>
      <p class="hint">
        <b>HTML 标签一律不解析</b>：程序会先把 <code>&amp;</code> 和 <code>&lt;</code> 转义掉，防止注入脚本。
      </p>
      <div v-if="mdGroups.length" class="row md-group-ops">
        <VBtn variant="text" size="small" type="button" :disabled="!mdEnabled" @click="disableAll">全部关闭</VBtn>
        <VBtn variant="text" size="small" type="button" :disabled="!mdEnabled" @click="enableAll">全部开启</VBtn>
      </div>
      <!-- 格式名称及示例来自后端的 markdown_types。 -->
      <div v-for="group in mdGroups" :key="group.kind" class="md-group">
        <div class="md-group-head">{{ group.title }}</div>
        <label v-for="item in group.items" :key="item.id" :id="`setting-markdown-${item.id}`" tabindex="-1" class="field md-row">
          <span>
            {{ item.name }}
            <code class="md-sample">{{ item.sample }}</code>
          </span>
          <VSwitch :model-value="typeOn(item.id)" :disabled="!mdEnabled" @update:model-value="(on) => toggleType(item.id, on === true)" color="primary" density="compact" hide-details />
        </label>
      </div>
    </section>
  </section>
</template>
