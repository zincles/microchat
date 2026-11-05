// 界面偏好 + 共享 api 客户端。
//
// 2026-11-01 用户定：「所有设置均存储在服务器上」—— 从前这些全在浏览器 localStorage
// （`mc_theme` 那一串）。现在**服务器是权威**（`data/config/prefs.json`），localStorage
// 只留一份**镜像**，用途只有一个：开机第一帧不闪（见 `MIRROR_KEY` 那段）。
//
// 两个例外**仍住 localStorage**，各有硬理由，别"顺手统一"掉：
//   - `mc_api`：它是"去哪个服务器"，在拿到地址之前没法请求 —— 放服务器是循环依赖。
//   - `mc_last_session`：**设备状态**而不是设置（手机与桌面各自回到自己上次看的那条），
//     而且每次切会话都要写，走一趟 HTTP 不值得。
//
// 为什么所有 getter 仍是**同步**的：调用点遍布 App 与各组件（`previewOn()` 在渲染路径上）。
// 若改成 async，半个应用要跟着改成 await —— 换来的只是"改成从服务器读"这一个结果。
// 所以：内存缓存（`cache`）是读取的唯一来源，`syncPrefs()` 负责把它与服务器对齐。
import { createApi, DEFAULT_API_URL, normalizeApiBase } from "../api/client.js";

// 主题（2026-10-28 用户定：「只保留 亮色 / 暗色 / Breeze 三种风格，不保留TG风格」）。
// `THEME_IDS` 是**唯一权威**：theme.css 的 `html[data-theme]` 与设置页那个下拉都只能取这三个，
// `contract.test.js` 钉着三处一致 —— 改一处忘另一处不会报错，只是页面安静退回默认配色。
export const THEME_IDS = ["light", "dark", "breeze"];
export const DEFAULT_THEME = "dark";
// 老 id → 新 id。**删主题不能只是删规则**：那些 id 还躺在别人的 localStorage 里，
// 匹配不到规则 = 页面退回默认 ⇒ 用户会发现"我的主题自己变了"。
// 搬到最接近的那套：深夜蓝/酒红/森绿/石墨 都是深色 ⇒ 暗色；日间白 ⇒ 亮色；Breeze Dark ⇒ Breeze。
const THEME_ALIASES = {
  graphite: "dark", deepseek: "dark", midnight: "dark", wine: "dark", forest: "dark",
  paper: "light", "breeze-dark": "breeze",
};
// 归一化：给什么 id 都换成 THEME_IDS 里的一个（认不出 ⇒ 默认）。
// 老版本的空串（"跟随系统"）也走这儿 —— 那个选项已经没了，空串现在就是"没设过"。
export function normalizeTheme(id) {
  if (THEME_IDS.includes(id)) return id;
  const mapped = THEME_ALIASES[id];
  return THEME_IDS.includes(mapped) ? mapped : DEFAULT_THEME;
}

// —— 界面缩放（2026-10-28，照 OpenWebUI 的 UI Scale）——
// 只乘**字号**：theme.css 的 `--app-text-scale` 乘到 body 的 font-size 上，
// 全项目尺寸几乎都是 em（0 处 rem、0 处 px 字号）⇒ 改这一格就是整页等比缩放。
// 范围/步长钉在这里，滑杆与守卫测试都读它（改一处忘另一处不会报错，只会滑不动或存不住）。
export const TEXT_SCALE_MIN = 0.85;
export const TEXT_SCALE_MAX = 1.3;
export const TEXT_SCALE_STEP = 0.05;
export const DEFAULT_TEXT_SCALE = 1;

// 归一化：认不出的（没存过、手改过的、NaN）一律回落 100%，
// 再夹到 [MIN, MAX] 并对齐到 5% 的格 —— 滑杆只会给出这些值，存储里什么都能塞。
export function normalizeTextScale(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return DEFAULT_TEXT_SCALE;
  const clamped = Math.min(TEXT_SCALE_MAX, Math.max(TEXT_SCALE_MIN, n));
  // toFixed 是为了掐掉 `17 * 0.05 = 0.8500000000000001` 这类二进制尾巴。
  return Number((Math.round(clamped / TEXT_SCALE_STEP) * TEXT_SCALE_STEP).toFixed(2));
}

// 字体（2026-11-04 用户定）：标准字体管全站，等宽字体管代码块；空 = 内置系统字族。
// 自定义值按 CSS font-family 原文存（逗号分隔的后备字族都保留），应用时在末尾补内置栈。
export const DEFAULT_FONT_BODY = '"Roboto Flex", "Roboto", -apple-system, BlinkMacSystemFont, system-ui, "Segoe UI", "PingFang SC", "Noto Sans SC", sans-serif';
export const DEFAULT_FONT_MONO = 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace';

export function normalizeFontFamily(value) {
  const text = String(value ?? "").replace(/[\r\n]+/g, " ").trim();
  // 只挡会破坏声明的字符；引号、逗号、空格、括号（local() 之类）都留给 CSS。
  if (!text || /[;{}<>]/.test(text)) return "";
  return text;
}

export const DEFAULT_SEND = "enter";

// ── 服务器偏好的本地镜像与内存缓存 ──────────────────────────────────
//
// `MIRROR_KEY` 里放服务器那份 Prefs 的原样 JSON。它**不是**权威，只是"上一帧长什么样"：
// 开机时先拿它把主题/缩放过一遍，页面第一帧就是对的；随后 `syncPrefs()` 去问服务器，
// 不一致就改过来（那次改动是可见的，但它只在"别处改过设置"时发生 —— 正常情况两处一样）。
//
// ⚠ 别把新东西只写进镜像而不写服务器：镜像随时会被服务器整份覆盖。
const MIRROR_KEY = "mc_prefs";

// FALLBACK_PREFS：**服务器还没答话时**的缺省（第一帧用）。
// 必须与后端 `config.DefaultPrefs()` 逐个对齐 —— `contract.test.js` 钉着两处一致。
// 真正权威的缺省由 `GET /config/prefs` 的 `defaults` 给（在线之后就以它为准）。
const FALLBACK_PREFS = {
  markdown: { enabled: true, disabled: [] },
  theme: "",
  text_scale: "",
  font_body: "",
  font_mono: "",
  background: {},
  send: "",
  preview: true,
  payload_preview: true,
  command_menu: false,
  chrome_hide: true,
  payload_viz: false,
  bar_left_hidden: false,
};

// 内存缓存。读取的唯一来源（同步）。
let cache = FALLBACK_PREFS;
// 服务器给的 markdown 类型清单（`[{id,name,kind,sample}]`）；未同步前为空。
let mdTypes = [];
// 是否已经从服务器同步过一次。
let hydrated = false;

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

// 读镜像（同步、静默失败 —— 坏镜像当没有，不是错）。
function readMirror() {
  try {
    const raw = localStorage.getItem(MIRROR_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? parsed : null;
  } catch {
    return null;
  }
}

function writeMirror(prefs) {
  try {
    localStorage.setItem(MIRROR_KEY, JSON.stringify(prefs));
  } catch {
    // 配额满之类：镜像只是"让第一帧好看"，写不进就算了（顶多开机闪一下）。
  }
}

// 把一份 Prefs 合进缺省（缺字段 = 缺省，与后端"缺字段 = 默认"同一条口径）。
function merge(base, incoming) {
  const out = clone(base);
  if (!incoming || typeof incoming !== "object") return out;
  for (const key of Object.keys(out)) {
    if (incoming[key] === undefined || incoming[key] === null) continue;
    const value = incoming[key];
    if (value && typeof value === "object" && !Array.isArray(value)) {
      out[key] = { ...out[key], ...value };
    } else {
      out[key] = value;
    }
  }
  // `markdown.disabled` 必须是数组（后端也会校验；这里防一手坏镜像）。
  if (!Array.isArray(out.markdown?.disabled)) {
    out.markdown = { ...out.markdown, disabled: [] };
  }
  return out;
}

// 开机第一帧：先用镜像把 cache 铺上（与 `syncPrefs` 的说明一致）。
// 没有这一句，`applyTextScale()` 在服务器答话前读到的一定是 FALLBACK（100%），
// 刷新时就会先闪一下默认值 —— 而用户看到的是"我的缩放又丢了"。
{
  const mirrored = readMirror();
  if (mirrored) cache = merge(FALLBACK_PREFS, mirrored);
}

// —— 迁移：老浏览器里的 `mc_*` 散键 ⇒ 一份完整 Prefs ——
//
// 只在"三个条件同时成立"时做：镜像不存在、服务器上还是缺省、老键确实存过。
// 这三个一起成立 ⇒ 就是"这台浏览器从旧版本升级上来、而服务器上还没人设过"。
// 迁完立刻 PUT 上去，并写镜像；此后永远不再看老键（它们留着无害，也不再读）。
const LEGACY_KEYS = ["mc_theme", "mc_text_scale", "mc_preview",
  "mc_chrome_hide", "mc_cmd_menu", "mc_payload_viz", "mc_bar_left_hidden", "mc_send"];

function legacyPrefs() {
  if (!LEGACY_KEYS.some((key) => localStorage.getItem(key) !== null)) return null;
  const theme = localStorage.getItem("mc_theme");
  const scale = localStorage.getItem("mc_text_scale");
  // 旧布尔键有两种相反约定（`!== "0"` 与 `=== "1"`）⇒ 不能一个函数通吃，
  // 由调用处按各自的原意给缺省与判定。
  const notZero = (key, dflt) => {
    const v = localStorage.getItem(key);
    return v === null ? dflt : v !== "0";
  };
  return {
    markdown: { enabled: true, disabled: [] },
    theme: theme === null ? "" : normalizeTheme(theme),
    text_scale: scale === null ? "" : String(normalizeTextScale(scale)),
    background: {},
    send: localStorage.getItem("mc_send") || "",
    preview: notZero("mc_preview", true),
    command_menu: localStorage.getItem("mc_cmd_menu") === "1",
    chrome_hide: notZero("mc_chrome_hide", true),
    payload_viz: localStorage.getItem("mc_payload_viz") === "1",
    bar_left_hidden: localStorage.getItem("mc_bar_left_hidden") === "1",
  };
}

// —— 与服务器同步 / 回写 ——

// syncPrefs：启动时调一次（`App.vue` 的 boot）。顺带承担"迁移"（见 `legacyPrefs`）。
export async function syncPrefs(api) {
  const client = api ?? sharedApi();
  const view = await client.getPrefs();

  mdTypes = Array.isArray(view.markdown_types) ? view.markdown_types : [];
  const defaults = merge(FALLBACK_PREFS, view.defaults);
  let incoming = merge(defaults, view.prefs);

  // 三条件齐 ⇒ 迁移（写服务器，然后以迁完的为准）。
  const serverIsDefault = !view.prefs || Object.keys(defaults).every((key) =>
    JSON.stringify(view.prefs[key]) === JSON.stringify(defaults[key]));
  const legacy = readMirror() ? null : legacyPrefs();
  if (legacy && serverIsDefault) {
    incoming = merge(defaults, legacy);
    try {
      await client.putPrefs(incoming); // 迁不动也不致命：下面照样用它跑这一次
    } catch {
      // 服务器写不进去（只读文件系统之类）⇒ 本次仍按迁移来的值显示，下次再试。
    }
  }

  cache = incoming;
  writeMirror(cache);
  hydrated = true;
  applyAll();
}

// 防抖回写：一次交互常常连着改好几格（滑杆拖动），别每一下都发一次 PUT。
// 但**刷新/切后台不能把最后一格吃掉**：`flushPrefs()` 取消防抖并立刻发；
// 页面隐藏与卸载时也会自动 flush（fetch 带 keepalive）。
let putTimer = null;
let pendingPut = false;
let flushing = false;

function applyServerPrefs(view) {
  // 服务器可能把内容归一化了（例如 `disabled` 排序）⇒ 以它回的为准。
  if (view?.prefs) {
    cache = merge(merge(FALLBACK_PREFS, view.defaults), view.prefs);
    writeMirror(cache);
  }
}

function sendPrefs({ keepalive = true } = {}) {
  const snapshot = clone(cache);
  return sharedApi().putPrefs(snapshot, keepalive ? { keepalive: true } : undefined)
    .then(applyServerPrefs)
    .catch((err) => {
      // 写不进去要让**页面**知道（偏好只在这台浏览器上生效了，刷新就没了）。
      console.error("保存界面偏好失败：", err);
      if (typeof window !== "undefined") {
        window.dispatchEvent(new CustomEvent("mc-prefs-error", { detail: err }));
      }
      throw err;
    });
}

// flushPrefs：把还没发出去的那一格立刻发掉（滑杆松手、页面隐藏、刷新前）。
// 幂等；没有待发内容就什么都不做。
export function flushPrefs() {
  if (!pendingPut || flushing) return;
  pendingPut = false;
  flushing = true;
  if (putTimer) { clearTimeout(putTimer); putTimer = null; }
  sendPrefs({ keepalive: true }).catch(() => {}).finally(() => { flushing = false; });
}

function schedulePut() {
  pendingPut = true;
  if (putTimer) clearTimeout(putTimer);
  putTimer = setTimeout(() => {
    putTimer = null;
    if (!pendingPut) return;
    pendingPut = false;
    sendPrefs().catch(() => {});
  }, 250);
}

// 刷新 / 切后台：浏览器可能马上丢掉这一页，最后一格必须带 keepalive 发出去。
if (typeof window !== "undefined") {
  const flushOnHide = () => flushPrefs();
  window.addEventListener("pagehide", flushOnHide);
  if (typeof document !== "undefined") {
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "hidden") flushOnHide();
    });
  }
}

// setPref：改一格并回写（内部用；外部用下面那些具名 save* 函数）。
//
// `path` 走 `"markdown.disabled"` 这种点号形式 —— 偏好是浅层的，不值得引一个 set 库。
function setPref(path, value) {
  const keys = path.split(".");
  let node = cache;
  for (let i = 0; i < keys.length - 1; i++) {
    if (!node[keys[i]] || typeof node[keys[i]] !== "object") node[keys[i]] = {};
    node = node[keys[i]];
  }
  node[keys[keys.length - 1]] = value;
  writeMirror(cache);
  schedulePut();
}

// prefsSnapshot：当前偏好的只读快照（设置界面渲染用）。
export function prefsSnapshot() { return clone(cache); }

// prefsHydrated：已经从服务器同步过了吗（设置界面据此说"保存中 / 离线"）。
export function prefsHydrated() { return hydrated; }

// markdownTypes：后端给的语法类型清单（未同步前是空数组）。
export function markdownTypes() { return mdTypes; }

// markdownPrefs：当前的 markdown 开关（`{enabled, disabled: []}`）。
export function markdownPrefs() { return clone(cache.markdown ?? FALLBACK_PREFS.markdown); }

// markdownTypeOn：某一类语法此刻要不要渲染。
//
// ⚠ 这是**另一份**判定，权威在后端 `Prefs.Markdown.TypeOn`；这里只服务渲染。
// 两边口径必须一样（缺省全开），`contract.test.js` 钉着。
export function markdownTypeOn(id) {
  const md = cache.markdown ?? FALLBACK_PREFS.markdown;
  return !(md.disabled ?? []).includes(id);
}

// saveMarkdown：整份改 markdown 开关（总开关 + 关掉哪几类）。
export function saveMarkdown({ enabled, disabled } = {}) {
  const next = { ...(cache.markdown ?? {}) };
  if (enabled !== undefined) next.enabled = !!enabled;
  if (disabled !== undefined) next.disabled = [...disabled];
  setPref("markdown", next);
}

// saveBackground：写背景那一格。
//
// `url` 型由这里设（服务器上不涉及文件）；`file` 型**别走这儿** ——
// 它必须经过上传端点（图要先落盘），见 `utils/background.js` 的 `uploadBackground`。
export function saveBackground(bg) {
  setPref("background", bg ?? {});
}

// refreshPrefs：重新问一次服务器（上传/删背景图之后要调：那两条口子会改服务器上的元信息，
// 而本地缓存并不知道）。与 `syncPrefs` 同一套逻辑 —— 迁移那步在镜像已存在时会自己跳过。
export function refreshPrefs() {
  return syncPrefs();
}

// —— 主题 ——

export function themePref() {
  // 空串 / 认不出的 id 都归一到 THEME_IDS 里的一档（见 normalizeTheme）。
  // 空串 = "没设过"（"跟随系统"那个选项已经没了）。
  return normalizeTheme(cache.theme || localStorage.getItem("mc_theme"));
}

// 主题应用（唯一一处）：TG Mini App 里 data-theme **根本不设**（用户主题由 TG 注入的
// --tg-theme-* 供给）—— 缺了这道守卫，TG 里保存一次设置就会把预设色压到 TG 注入色上。
export function applyTheme(theme) {
  if (window.Telegram?.WebApp) {
    document.documentElement.removeAttribute("data-theme");
    syncThemeColor();
    return;
  }
  document.documentElement.setAttribute("data-theme", theme === undefined ? themePref() : normalizeTheme(theme));
  syncThemeColor();
}

export function saveTheme(theme) {
  setPref("theme", normalizeTheme(theme));
  applyTheme(theme);
}

// 手机状态栏 / PWA 标题栏那根 `<meta name="theme-color">`：**写死不行** —— 三套主题底色不同，
// 写死一个值在另外两套下就是一条突兀的色边（亮色主题配深色状态栏是最扎眼的）。
// 值从 `--tg-bg` 现读（`var()` 在计算值阶段就替换掉了，读到的是真颜色；TG 注入时也是对的）。
function syncThemeColor() {
  const meta = document.querySelector('meta[name="theme-color"]');
  if (!meta) return;
  const bg = getComputedStyle(document.documentElement).getPropertyValue("--tg-bg").trim();
  if (bg) meta.setAttribute("content", bg);
}

// —— 界面缩放 ——

export function textScale() {
  return normalizeTextScale(cache.text_scale || localStorage.getItem("mc_text_scale"));
}

export function applyTextScale(scale) {
  const s = scale === undefined ? textScale() : normalizeTextScale(scale);
  document.documentElement.style.setProperty("--app-text-scale", String(s));
}

export function saveTextScale(scale) {
  const s = normalizeTextScale(scale);
  setPref("text_scale", String(s));
  applyTextScale(s);
}

// —— 字体（2026-11-04）——

export function fontBody() { return normalizeFontFamily(cache.font_body); }
export function fontMono() { return normalizeFontFamily(cache.font_mono); }

// 自定义字族放在内置栈**前面**：中文字形或没装的字体仍能落到后面的系统字族，不会整页豆腐块。
export function applyFonts() {
  const root = document.documentElement;
  const body = fontBody();
  const mono = fontMono();
  if (body) root.style.setProperty("--app-font-body", `${body}, ${DEFAULT_FONT_BODY}`);
  else root.style.removeProperty("--app-font-body");
  if (mono) root.style.setProperty("--app-font-mono", `${mono}, ${DEFAULT_FONT_MONO}`);
  else root.style.removeProperty("--app-font-mono");
}

export function saveFontBody(value) {
  const body = normalizeFontFamily(value);
  setPref("font_body", body);
  applyFonts();
  return body;
}
export function saveFontMono(value) {
  const mono = normalizeFontFamily(value);
  setPref("font_mono", mono);
  applyFonts();
  return mono;
}

// —— 布尔开关（一律走同一对面）——

function flagPref(key, fallback) {
  const value = cache[key];
  return typeof value === "boolean" ? value : fallback;
}

export function previewOn() { return flagPref("preview", true); }
export function savePreview(on) { setPref("preview", !!on); }

// 载荷面板预览（2026-11-04）：与输入框预演分开。默认开；关掉后载荷 tab 不请求 /outgoing。
export function payloadPreviewOn() { return flagPref("payload_preview", true); }
export function savePayloadPreview(on) { setPref("payload_preview", !!on); }

// 滚动隐藏上下栏（2026-10-17 用户定，**默认开**；只在触屏上生效）：跟浏览器一个味道 ——
// 往下看（内容前进）顶栏按位移**逐步**收起，往回看（内容后退）底栏逐步收起；反方向把对面那栏放回来。
export function chromeHideOn() { return flagPref("chrome_hide", true); }
export function saveChromeHide(on) { setPref("chrome_hide", !!on); }

// 命令菜单（2026-10-17 用户定，**默认关**）：输入框左侧那颗 ☰ 与输入 "/" 自动弹出的命令面板。
// 移动端那玩意儿纯碍事 ⇒ 默认不给入口；关着也能直接打 `/name` 回车执行（命令本身没消失，
// 只是没有"菜单"这一层）。
export function commandMenuOn() { return flagPref("command_menu", false); }
export function saveCommandMenu(on) { setPref("command_menu", !!on); }

// 载荷栏"分块视图"（2026-10-11 用户点名）：开着就把 body.messages 一块块画出来（比裸 JSON 直观）。
export function payloadVizOn() { return flagPref("payload_viz", false); }
export function savePayloadViz(on) { setPref("payload_viz", !!on); }

// 左栏收着没有（2026-11-01 起住服务器：换设备也该是同一个工作台）。
export function barLeftHidden() { return flagPref("bar_left_hidden", false); }
export function saveBarLeftHidden(on) { setPref("bar_left_hidden", !!on); }

// —— 发送键三档（App 的 keydown 判定与 Composer 的占位提示同源）——

export function sendMode() {
  return cache.send || localStorage.getItem("mc_send") || DEFAULT_SEND;
}
export function saveSendMode(mode) { setPref("send", mode || DEFAULT_SEND); }

// 这一下回车算不算"发送"。
export function wantsSend(e, mode = sendMode()) {
  if (mode === "enter") return !(e.shiftKey || e.ctrlKey || e.metaKey || e.altKey);
  if (mode === "shift-enter") return e.shiftKey && !e.ctrlKey && !e.metaKey;
  return false; // button 档：裸回车一律换行，只能点按钮
}

// —— 仍住 localStorage 的两样（理由见文件顶）——

// 上次在看的那条会话（2026-10-11 用户定）：刷新 / 被安卓丢弃后重载 ⇒ 回到它，不再是一张空草稿。
// 空串 = 没记（或人在草稿）⇒ 开机进草稿。
export function lastSessionId() { return localStorage.getItem("mc_last_session") || ""; }
export function saveLastSession(id) { localStorage.setItem("mc_last_session", id ?? ""); }

// defaultApiBase：**跟着"你从哪儿打开这个页面"走**（location.hostname）——
// 手机从 http://192.168.0.110:8788 打开，默认后端就是 http://192.168.0.110:8787/api/v1。
// 前端 8788 / 后端 8787 是我们的成对默认；IPv6 主机名要套方括号（location.hostname 不带）。
export function defaultApiBase() {
  const host = (typeof location !== "undefined" && location.hostname) || "";
  if (!host) return DEFAULT_API_URL; // file:// 或非浏览器环境
  const h = host.includes(":") ? `[${host}]` : host;
  return `http://${h}:8787/api/v1`;
}
export function apiBase() { return localStorage.getItem("mc_api") || defaultApiBase(); }

// apiBaseCandidates：启动自检按顺序试的候选 —— 有存过就只认存的（人定的最大）；
// 没存过：[**同源 /api/v1**（首选：单容器部署与 dev 都靠它 —— vite 把 /api 代理给后端），
// 跟页面同主机的 8787（兜底：前端被别的静态服务器端出来、没有代理时）]。
// 非首选候选胜出时调用方要把它存下来（sharedApi 等一切读的都是 apiBase()）。
export function apiBaseCandidates() {
  const stored = localStorage.getItem("mc_api");
  if (stored) return [stored];
  const list = [];
  if (typeof location !== "undefined" && location.origin && location.origin !== "null") {
    list.push(`${location.origin}/api/v1`);
  }
  const dflt = defaultApiBase();
  if (!list.includes(dflt)) list.push(dflt);
  return list;
}

// 连接偏好的唯一写入口：地址先过 normalizeApiBase。
export function saveNetPrefs(base) {
  localStorage.setItem("mc_api", normalizeApiBase(base) || defaultApiBase());
}

// applyAll：把"住在 DOM/CSS 上的偏好"整份重新应用一遍。
// `syncPrefs` 拿到服务器的值之后调它 —— 这几样各有 apply*，但漏一样就是
// "设置里改了、这一帧没生效"（`@prefs-saved` 那条教训）。
export function applyAll() {
  applyTheme();
  applyTextScale();
  applyFonts();
}

// 共享 api 客户端：按 base 缓存 —— 组件与 App 拿的是同一个实例。
let cached = null;
let cachedKey = "";
export function sharedApi() {
  const base = apiBase();
  if (!cached || base !== cachedKey) {
    cached = createApi({ base });
    cachedKey = base;
  }
  return cached;
}
