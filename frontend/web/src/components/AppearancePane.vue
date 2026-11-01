<script setup>
// 外观面板（设置视图的「外观」区，2026-11-01 用户定：「把外观设置做到设置里」）。
//
// 这块**原本住在用户视图**（`UserView.vue`）。账号砍掉之后用户视图整个没了 ——
// 它原来那两半：**身份**（名字 / 管理员 / 退出登录 / 用户管理）跟着账号一起删，
// **个人偏好**（主题 / 缩放 / 背景图 / 显示模式 / 滚动收起 / 发送键 / 预演 / 命令菜单）
// 原样搬到这儿，一个字都不改口径。
//
// ⚠ 全部**即时生效**，没有"保存"按钮：改外观就该当场看见（从前那个"保存"只是顺手
//   落盘 + 打一句"已保存"，真正的应用发生在改的瞬间）。`emit("prefs-saved")` 保留 ——
//   聊天壳上有活开关（命令菜单那颗 ☰）要跟着变。
import { ref, computed, onMounted } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import {
  previewOn, themePref, applyTheme, sendMode,
  commandMenuOn, saveCommandMenu, chromeHideOn, saveChromeHide,
  composerStyle as composerStylePref, saveComposerStyle,
  textScale as textScalePref, saveTextScale,
  TEXT_SCALE_MIN, TEXT_SCALE_MAX, TEXT_SCALE_STEP,
} from "../utils/prefs.js";
import { loadBackground, saveBackground, applyBackground, describeBackground, fileToBackground } from "../utils/background.js";

const emit = defineEmits(["prefs-saved"]);

const theme = ref("");
const send = ref("enter");
const preview = ref(true);
// 命令菜单（默认关）：输入框左侧那颗 ☰ 与输入 "/" 弹出的面板。移动端碍事 ⇒ 想用的人才开。
const cmdMenu = ref(false);
// 显示模式（**外观项**；2026-10-17 用户给这两个形态定了名）：
//   **正常模式** = float：圆角矩形输入框、浮在消息之上，框上方一行小字报状态；
//   **紧凑模式** = solid：输入框占满整行、方角贴底，省地方。
// 值名沿用 float/solid ⇒ 已经存过的偏好不作废。
const composerStyle = ref("float");
// 界面缩放（2026-10-28）：**拖的时候就生效**（不像别的偏好等"保存"）——
// 尺寸这东西得看着调，等你点保存再看效果等于摸黑。落盘也顺手做了（saveTextScale）。
const textScale = ref(1);
const scaleBounds = { min: TEXT_SCALE_MIN, max: TEXT_SCALE_MAX, step: TEXT_SCALE_STEP };
const scalePercent = computed(() => Math.round(textScale.value * 100));
// 滚动隐藏上下栏（触屏；默认开）：往下看顶栏让位、往回看底栏让位，进度跟位移成正比。
const chromeHide = ref(true);
const bgUrl = ref("");
const bgStatus = ref("");

function onScaleInput(event) {
  textScale.value = Number(event.target.value);
  saveTextScale(textScale.value);
}
function resetScale() {
  textScale.value = 1;
  saveTextScale(1);
}

onMounted(() => {
  theme.value = themePref();
  send.value = sendMode();
  preview.value = previewOn();
  cmdMenu.value = commandMenuOn();
  composerStyle.value = composerStylePref();
  textScale.value = textScalePref();
  chromeHide.value = chromeHideOn();
  const bg = loadBackground();
  bgUrl.value = bg?.kind === "url" ? bg.value : "";
  bgStatus.value = describeBackground(bg);
});

// 背景图（外观）：URL / 上传二选一，**即时生效**（上传是异步的，攒着容易对不上账）。
function applyBgUrl() {
  const u = bgUrl.value.trim();
  if (!u) { bgStatus.value = "先填图片地址（或改用上传）"; return; }
  saveBackground({ kind: "url", value: u });
  applyBackground();
  bgStatus.value = describeBackground(loadBackground());
}
async function pickBgFile(event) {
  const f = event.target.files?.[0];
  if (!f) return;
  bgStatus.value = "读取图片…";
  try {
    let bg = await fileToBackground(f);
    try {
      saveBackground(bg);
    } catch {
      // 配额满：降档（长边 1280 / 质量 0.75）重压一次，再满就报错。
      bgStatus.value = "太大存不下，压小重试…";
      bg = await fileToBackground(f, { maxEdge: 1280, quality: 0.75 });
      saveBackground(bg);
    }
    applyBackground();
    bgStatus.value = describeBackground(bg);
  } catch (e) {
    bgStatus.value = `上传失败：${e.message}`;
  }
  event.target.value = "";
}
function clearBg() {
  saveBackground(null);
  applyBackground();
  bgUrl.value = "";
  bgStatus.value = describeBackground(null);
}
// 每一项都是**改了就落盘**（与从前的"保存"按钮同一个落点，只是少了那一次点击）。
function persist() {
  localStorage.setItem("mc_preview", preview.value ? "1" : "0");
  localStorage.setItem("mc_theme", theme.value);
  localStorage.setItem("mc_send", send.value);
  saveCommandMenu(cmdMenu.value);
  saveComposerStyle(composerStyle.value);
  saveTextScale(textScale.value);
  saveChromeHide(chromeHide.value);
  applyTheme(theme.value);
  emit("prefs-saved"); // 聊天壳上的活开关立刻跟上（命令菜单那颗 ☰）
}
</script>

<template>
  <section id="panel-appearance" class="panel">
    <h3>外观</h3>
    <!-- 只有三套（2026-10-28 用户定）。id 必须与 prefs.js 的 THEME_IDS 逐个对得上，
         contract.test.js 钉着 —— 这里多一个少一个都不会报错，只会让人选到没规则的主题。 -->
    <label class="field"><span>主题</span><select v-model="theme" @change="persist">
      <option value="light">亮色</option>
      <option value="dark">暗色（默认）</option>
      <option value="breeze">Breeze</option>
    </select></label>
    <!-- 界面缩放（2026-10-28，照 OpenWebUI 的 UI Scale）：**拖动即生效**，不用点保存。
         只乘字号 ⇒ 整页等比（尺寸几乎都是 em）；圆角是 px，放大时相对显小，可接受。 -->
    <!-- `.field` 是两列网格（label | 控件）：滑杆与百分比必须包成**一个**子元素，
         否则百分比会被挤到第二行第一列去（看着像标签下面多了一行孤立数字）。 -->
    <label class="field"><span>界面缩放</span>
      <span class="scale-wrap">
        <input
          type="range"
          :min="scaleBounds.min"
          :max="scaleBounds.max"
          :step="scaleBounds.step"
          :value="textScale"
          @input="onScaleInput"
          aria-label="界面缩放"
        />
        <b class="scale-value">{{ scalePercent }}%</b>
      </span>
    </label>
    <div class="row">
      <button type="button" class="link-btn" @click="resetScale">复位 100%</button>
      <span class="status">拖动即生效；只放大字号，布局留白跟着等比</span>
    </div>
    <label class="field"><span>背景图 URL</span><input v-model="bgUrl" placeholder="https://…（浏览器直接加载，跨域无妨）" /></label>
    <div class="row">
      <button type="button" class="primary" @click="applyBgUrl">应用 URL</button>
      <label class="link-btn" style="cursor: pointer">上传图片…
        <input type="file" accept="image/*" hidden @change="pickBgFile" />
      </label>
      <button type="button" class="link-btn danger" @click="clearBg">清除</button>
    </div>
    <div class="row"><span class="status">当前背景：{{ bgStatus }}</span></div>
    <!-- 输入栏形态（2026-10-20 用户定）：**外观项** —— 要效率还是要美观，自己挑。
         与主题同类（改的是长相），所以住这一区；保存后当场生效（写根节点属性，不刷新）。 -->
    <label class="field"><span>显示模式</span><select v-model="composerStyle" @change="persist">
      <option value="float">正常模式（圆角浮空输入框，状态小字在框上方）</option>
      <option value="solid">紧凑模式（输入框占满整行、方角，省地方）</option>
    </select></label>
    <label class="field"><span>滚动时收起顶栏/底栏（触屏）</span><ToggleSwitch v-model="chromeHide" @update:model-value="persist" /></label>
    <p class="hint">往下看（内容前进）顶栏按位移逐步收起，往回看底栏逐步收起 —— 一次只藏一边，反向滚动就放回来；到顶/见底立刻全放回。只在触屏上生效。</p>
    <p class="hint"><b>正常</b> = 输入框是一颗圆角卡、浮在消息上（两侧留白，背景图透得出来），状态那行小字在它上面；<b>紧凑</b> = 输入框边到边、方角贴底 —— 省出一点地方。</p>

    <h3>输入</h3>
    <label class="field"><span>发送键</span><select v-model="send" @change="persist">
      <option value="enter">回车发送（Shift/Ctrl/Alt+回车换行）</option>
      <option value="shift-enter">Shift+回车发送（回车换行）</option>
      <option value="button">仅按钮</option>
    </select></label>
    <label class="field"><span>输入时预演载荷</span><ToggleSwitch v-model="preview" @update:model-value="persist" /></label>
    <label class="field"><span>命令菜单（输入框左侧 ☰ 与 / 面板）</span><ToggleSwitch v-model="cmdMenu" @update:model-value="persist" /></label>
    <p class="hint">这一页**没有保存按钮**：改了就生效、也顺手存下了（2026-11-01 起）。</p>
  </section>
</template>
