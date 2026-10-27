<script setup>
// 用户视图（2026-10-10 用户定）：与设置视图**平级**的另一半 ——
//   设置 = **实例/服务器**的事（聊天参数、渠道、Agent、会话、连接）；
//   用户 = **登录者自己**的事：个人偏好（外观/输入）现在就落在这儿，
//   将来的账号/凭证/多用户身份也归这儿。单用户实例先如实说明，不假装有登录。
// 视图口径同设置：占满内容区、[◧] 左栏开关 · 标题 · [×]，Esc 关闭（一次只关一层）。
import { ref, computed, onMounted, onUnmounted } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import { ICONS } from "./icons.js";
import { previewOn, themePref, applyTheme, sendMode, sharedApi, apiBase, saveNetPrefs, commandMenuOn, saveCommandMenu, chromeHideOn, saveChromeHide, composerStyle as composerStylePref, saveComposerStyle, textScale as textScalePref, saveTextScale, TEXT_SCALE_MIN, TEXT_SCALE_MAX, TEXT_SCALE_STEP } from "../utils/prefs.js";
import { authLogin } from "../api/client.js";
import { loadBackground, saveBackground, applyBackground, describeBackground, fileToBackground } from "../utils/background.js";

const emit = defineEmits(["close", "toggle-left", "prefs-saved"]);
// escBlocked：外面临时弹窗（会话设置）开着时，Esc 归它 —— 一次按键不许关两层。
const props = defineProps({ escBlocked: { type: Boolean, default: false } });
const leftSvg = ICONS.panelLeft;

const me = ref(null); // 我是谁（/auth/me）
const meErr = ref("");

// —— 改口令（自己；谁都行）——
const pass1 = ref("");
const pass2 = ref("");
const passStatus = ref("");
async function changeOwnPassword() {
  passStatus.value = "";
  if (pass1.value.length < 6) { passStatus.value = "口令至少 6 位"; return; }
  if (pass1.value !== pass2.value) { passStatus.value = "两次不一致"; return; }
  try {
    await sharedApi().setUserPassword(me.value.id, pass1.value);
    // 改口令会让 `ver+1`、当前这枚票立刻作废 —— 拿新口令**当场重登**换一枚（用户体验不断线）。
    const out = await authLogin(apiBase(), String(me.value.id), pass1.value);
    saveNetPrefs(apiBase(), out.token);
    pass1.value = ""; pass2.value = "";
    passStatus.value = "已改（本机凭据已换新）";
    load();
  } catch (e) { passStatus.value = `改口令失败：${e.message}`; }
}

// —— 用户管理（admin；规则：root 不许删、名字不许纯数字、删人对会话必须显式）——
const users = ref([]);
const usersStatus = ref("");
const newName = ref("");
const newPass = ref("");
const rowEdit = ref(null); // {id, kind: "name"|"pass", value}
const rowDel = ref(null);  // 正在展开"怎么处理它的会话"的那个 uid
async function refreshUsers() {
  try { users.value = await sharedApi().users() ?? []; }
  catch (e) { usersStatus.value = `拉用户列表失败：${e.message}`; }
}
async function addUser() {
  usersStatus.value = "";
  try {
    const u = await sharedApi().createUser({ name: newName.value.trim(), password: newPass.value });
    newName.value = ""; newPass.value = "";
    usersStatus.value = `已加 ${u.name}（uid ${u.id}）`;
    refreshUsers();
  } catch (e) { usersStatus.value = `加人失败：${e.message}`; }
}
async function saveRowEdit() {
  const r = rowEdit.value;
  if (!r) return;
  try {
    if (r.kind === "name") await sharedApi().patchUser(r.id, { name: r.value.trim() });
    else await sharedApi().setUserPassword(r.id, r.value);
    rowEdit.value = null;
    usersStatus.value = r.kind === "name" ? "已改名" : "已改口令（那人旧票全废）";
    refreshUsers();
  } catch (e) { usersStatus.value = `保存失败：${e.message}`; }
}
async function dropUser(uid, mode) {
  try {
    const out = await sharedApi().deleteUser(uid, mode);
    rowDel.value = null;
    usersStatus.value = mode === "purge"
      ? `已删（连会话，动了 ${out.sessions_affected} 条）`
      : `已删（${out.sessions_affected} 条会话转给 root）`;
    refreshUsers();
  } catch (e) { usersStatus.value = `删人失败：${e.message}`; }
}
const theme = ref("");
const send = ref("enter");
const preview = ref(true);
// 命令菜单（默认关）：输入框左侧那颗 ☰ 与输入 "/" 弹出的面板。移动端碍事 ⇒ 想用的人才开。
const cmdMenu = ref(false);
// 显示模式（**外观项**；2026-10-17 用户给这两个形态定了名）：
//   **正常模式** = float：圆角矩形输入框、浮在消息之上，框上方一行小字报状态；
//   **紧凑模式** = solid：输入框占满整行、方角贴底，省地方。
// 值名沿用 float/solid ⇒ 已经存过的偏好不作废。
// 与主题同类：改的是长相，所以住「外观」区；保存后当场生效（写的是根节点属性）。
const composerStyle = ref("float");
// 界面缩放（2026-10-28）：**拖的时候就生效**（不像别的偏好等"保存"）——
// 尺寸这东西得看着调，等你点保存再看效果等于摸黑。落盘也顺手做了（saveTextScale）。
const textScale = ref(1);
const scaleBounds = { min: TEXT_SCALE_MIN, max: TEXT_SCALE_MAX, step: TEXT_SCALE_STEP };
const scalePercent = computed(() => Math.round(textScale.value * 100));
function onScaleInput(event) {
  textScale.value = Number(event.target.value);
  saveTextScale(textScale.value);
}
function resetScale() {
  textScale.value = 1;
  saveTextScale(1);
}
// 滚动隐藏上下栏（触屏；默认开）：往下看顶栏让位、往回看底栏让位，进度跟位移成正比。
const chromeHide = ref(true);
const bgUrl = ref("");
const bgStatus = ref("");
const uiStatus = ref("");

function load() {
  sharedApi().me().then((m) => {
    me.value = m;
    if (m?.is_admin) refreshUsers(); // 用户列表只在 admin 眼里存在
  }).catch((e) => { meErr.value = e?.message ?? String(e); });
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
}
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
// 个人偏好（外观/输入）：即时生效（主题应用收口在 applyTheme，含 TG 守卫与老 id 的归一化）。
function savePrefs() {
  localStorage.setItem("mc_preview", preview.value ? "1" : "0");
  localStorage.setItem("mc_theme", theme.value);
  localStorage.setItem("mc_send", send.value);
  saveCommandMenu(cmdMenu.value);
  saveComposerStyle(composerStyle.value);
  saveTextScale(textScale.value);
  saveChromeHide(chromeHide.value);
  applyTheme(theme.value);
  emit("prefs-saved"); // 聊天壳上的活开关立刻跟上（命令菜单那颗 ☰）
  uiStatus.value = "已保存（即时生效）";
}
// 退出登录：JWT 无状态（服务端没东西可清）—— 通知一声、清掉本地凭据、刷新回门板。
function logout() {
  sharedApi().logout().catch(() => {});
  saveNetPrefs(apiBase(), "");
  location.reload();
}
function onKey(e) {
  if (e.key !== "Escape" || props.escBlocked) return;
  emit("close");
}
onMounted(() => {
  load();
  document.addEventListener("keydown", onKey);
});
onUnmounted(() => document.removeEventListener("keydown", onKey));
</script>

<template>
  <section id="user-view">
    <div class="settings-bar">
      <button
        class="icon-btn"
        type="button"
        title="显示/隐藏会话栏"
        aria-label="显示/隐藏会话栏"
        @click="$emit('toggle-left')"
        v-html="leftSvg"
      ></button>
      <span class="view-title">用户</span>
      <button id="user-close" class="icon-btn" type="button" title="关闭（Esc）" aria-label="关闭" @click="$emit('close')">×</button>
    </div>
    <div class="settings-body">
      <div class="settings-box">
        <div class="settings-cols">
          <section class="panel">
            <h3>身份</h3>
            <p class="hint" v-if="me">
              当前登录：<b>{{ me.name }}</b>（uid {{ me.id }}）{{ me.is_admin ? " · 管理员" : "" }}{{ me.sys ? " · 系统（环回）" : "" }}
            </p>
            <p class="hint" v-else-if="meErr">身份读取失败：{{ meErr }}</p>
            <div class="row" v-if="me">
              <button type="button" @click="logout">退出登录</button>
            </div>
            <h3 v-if="me">改口令（自己）</h3>
            <template v-if="me && me.id !== 0">
              <label class="field"><span>新口令（≥6 位）</span><input v-model="pass1" type="password" /></label>
              <label class="field"><span>再输一次</span><input v-model="pass2" type="password" /></label>
              <div class="row"><button type="button" class="primary" @click="changeOwnPassword">改口令</button><span class="status">{{ passStatus }}</span></div>
            </template>
            <p v-else-if="me" id="root-pass-note" class="status">
              root 的口令只能从本机重设：TUI 里打 <code>/passwd</code>，或 <code>./microchat -debug set-root-password</code>（网络口不给这个能力 —— 连 root 自己的票也不行）
            </p>
            <template v-if="me?.is_admin">
              <h3>用户管理（root）</h3>
              <div id="user-mgr-list">
                <table class="plist">
                  <thead><tr><th>uid</th><th>名字</th><th>角色</th><th>口令</th><th>操作</th></tr></thead>
                  <tbody>
                    <template v-for="u in users" :key="u.id">
                      <tr>
                        <td>{{ u.id }}</td>
                        <td>{{ u.name }}</td>
                        <td>{{ u.is_admin ? "root" : "普通" }}</td>
                        <td>{{ u.has_password ? "有" : "无" }}</td>
                        <td class="ops">
                          <button type="button" class="link-btn" @click="rowEdit = { id: u.id, kind: 'name', value: u.name }; rowDel = null">改名</button>
                          <button v-if="u.id !== 0" type="button" class="link-btn" @click="rowEdit = { id: u.id, kind: 'pass', value: '' }; rowDel = null">改口令</button>
                          <!-- root 的口令 = **CLI 专属**（网络口一律 403）：这儿不给按钮 —— 给了就是死路（点了必失败）。 -->
                          <span v-else class="status" title="root 的口令只能从本机重设：TUI 里 /passwd，或 ./microchat -debug set-root-password">口令走 CLI</span>
                          <button v-if="u.id !== 0" type="button" class="link-btn danger" @click="rowDel = rowDel === u.id ? null : u.id; rowEdit = null">删除</button>
                          <span v-else class="status">root 不许删</span>
                        </td>
                      </tr>
                      <tr v-if="rowEdit && rowEdit.id === u.id">
                        <td colspan="5">
                          <div class="row">
                            <input v-if="rowEdit.kind === 'name'" v-model="rowEdit.value" placeholder="新名字（不许纯数字）" />
                            <input v-else v-model="rowEdit.value" type="password" placeholder="新口令（≥6 位，旧票全废）" />
                            <button type="button" class="primary" @click="saveRowEdit">保存</button>
                            <button type="button" @click="rowEdit = null">取消</button>
                          </div>
                        </td>
                      </tr>
                      <tr v-if="rowDel === u.id">
                        <td colspan="5">
                          <div class="row">
                            <span class="status">删「{{ u.name }}」之前：它的会话怎么处理？</span>
                            <button type="button" @click="dropUser(u.id, 'transfer')">转给 root</button>
                            <button type="button" class="link-btn danger" @click="dropUser(u.id, 'purge')">连会话一起删</button>
                            <button type="button" @click="rowDel = null">取消</button>
                          </div>
                        </td>
                      </tr>
                    </template>
                  </tbody>
                </table>
              </div>
              <div class="row">
                <input v-model="newName" placeholder="新用户名（不许纯数字）" />
                <input v-model="newPass" type="password" placeholder="口令（≥6 位）" />
                <button type="button" class="primary" @click="addUser">加人</button>
                <span class="status">{{ usersStatus }}</span>
              </div>
            </template>
            <h3>外观</h3>
            <!-- 只有三套（2026-10-28 用户定）。id 必须与 prefs.js 的 THEME_IDS 逐个对得上，
                 contract.test.js 钉着 —— 这里多一个少一个都不会报错，只会让人选到没规则的主题。 -->
            <label class="field"><span>主题</span><select v-model="theme">
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
            <label class="field"><span>显示模式</span><select v-model="composerStyle">
              <option value="float">正常模式（圆角浮空输入框，状态小字在框上方）</option>
              <option value="solid">紧凑模式（输入框占满整行、方角，省地方）</option>
            </select></label>
            <label class="field"><span>滚动时收起顶栏/底栏（触屏）</span><ToggleSwitch v-model="chromeHide" /></label>
            <p class="hint">往下看（内容前进）顶栏按位移逐步收起，往回看底栏逐步收起 —— 一次只藏一边，反向滚动就放回来；到顶/见底立刻全放回。只在触屏上生效。</p>
            <p class="hint"><b>正常</b> = 输入框是一颗圆角卡、浮在消息上（两侧留白，背景图透得出来），状态那行小字在它上面；<b>紧凑</b> = 输入框边到边、方角贴底，状态与任务那两行收进栏里 —— 多出两行字的地儿。</p>
            <h3>输入</h3>
            <label class="field"><span>发送键</span><select v-model="send">
              <option value="enter">回车发送（Shift/Ctrl/Alt+回车换行）</option>
              <option value="shift-enter">Shift+回车发送（回车换行）</option>
              <option value="button">仅按钮</option>
            </select></label>
            <label class="field"><span>输入时预演载荷</span><ToggleSwitch v-model="preview" /></label>
            <label class="field"><span>命令菜单（输入框左侧 ☰ 与 / 面板）</span><ToggleSwitch v-model="cmdMenu" /></label>
            <div class="row"><button type="button" class="primary" @click="savePrefs">保存</button><span class="status">{{ uiStatus }}</span></div>
          </section>
        </div>
      </div>
    </div>
  </section>
</template>
