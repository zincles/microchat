<script setup>
// 用户视图（2026-10-10 用户定）：与设置视图**平级**的另一半 ——
//   设置 = **实例/服务器**的事（聊天参数、渠道、Agent、会话、连接）；
//   用户 = **登录者自己**的事：个人偏好（外观/输入）现在就落在这儿，
//   将来的账号/凭证/多用户身份也归这儿。单用户实例先如实说明，不假装有登录。
// 视图口径同设置：占满内容区、[◧] 左栏开关 · 标题 · [×]，Esc 关闭（一次只关一层）。
import { ref, onMounted, onUnmounted } from "vue";
import ToggleSwitch from "./ToggleSwitch.vue";
import { ICONS } from "./icons.js";
import { previewOn, themePref, applyTheme, sendMode, sharedApi, apiBase, saveNetPrefs } from "../utils/prefs.js";
import { authLogin } from "../api/client.js";
import { loadBackground, saveBackground, applyBackground, describeBackground, fileToBackground } from "../utils/background.js";

const emit = defineEmits(["close", "toggle-left"]);
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
// 个人偏好（外观/输入）：即时生效（主题应用收口在 applyTheme，含 TG 守卫、空串=跟随系统）。
function savePrefs() {
  localStorage.setItem("mc_preview", preview.value ? "1" : "0");
  localStorage.setItem("mc_theme", theme.value);
  localStorage.setItem("mc_send", send.value);
  applyTheme(theme.value);
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
            <label class="field"><span>主题</span><select v-model="theme">
              <option value="deepseek">DeepSeek（暗，默认）</option>
              <option value="breeze-dark">Breeze Dark（暗）</option>
              <option value="midnight">深夜蓝</option>
              <option value="wine">酒红</option>
              <option value="forest">森绿</option>
              <option value="breeze">Breeze（亮）</option>
              <option value="paper">日间白</option>
              <option value="">跟随系统</option>
            </select></label>
            <label class="field"><span>背景图 URL</span><input v-model="bgUrl" placeholder="https://…（浏览器直接加载，跨域无妨）" /></label>
            <div class="row">
              <button type="button" class="primary" @click="applyBgUrl">应用 URL</button>
              <label class="link-btn" style="cursor: pointer">上传图片…
                <input type="file" accept="image/*" hidden @change="pickBgFile" />
              </label>
              <button type="button" class="link-btn danger" @click="clearBg">清除</button>
            </div>
            <div class="row"><span class="status">当前背景：{{ bgStatus }}</span></div>
            <h3>输入</h3>
            <label class="field"><span>发送键</span><select v-model="send">
              <option value="enter">回车发送（Shift/Ctrl/Alt+回车换行）</option>
              <option value="shift-enter">Shift+回车发送（回车换行）</option>
              <option value="button">仅按钮</option>
            </select></label>
            <label class="field"><span>输入时预演载荷</span><ToggleSwitch v-model="preview" /></label>
            <div class="row"><button type="button" class="primary" @click="savePrefs">保存</button><span class="status">{{ uiStatus }}</span></div>
          </section>
        </div>
      </div>
    </div>
  </section>
</template>
