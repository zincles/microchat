// 界面偏好（localStorage `mc_*`）的唯一出口 + 共享 api 客户端。
// 为什么集中：以前每个组件各自 `localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1"`
// 抄一份（5 处），还有主题应用两份（一份忘了 TG 守卫）—— 全部收口到这里。
// 口径不变：**改完偏好刷新页面生效**（组件在装载时取一次；sharedApi 按偏好键缓存）。
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

// 归一化：认不出的（老版本没存过、手改过的、NaN）一律回落 100%，
// 再夹到 [MIN, MAX] 并对齐到 5% 的格 —— 滑杆只会给出这些值，localStorage 什么都能塞。
export function normalizeTextScale(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return DEFAULT_TEXT_SCALE;
  const clamped = Math.min(TEXT_SCALE_MAX, Math.max(TEXT_SCALE_MIN, n));
  // toFixed 是为了掐掉 `17 * 0.05 = 0.8500000000000001` 这类二进制尾巴。
  return Number((Math.round(clamped / TEXT_SCALE_STEP) * TEXT_SCALE_STEP).toFixed(2));
}

export function textScale() { return normalizeTextScale(localStorage.getItem("mc_text_scale")); }

export function applyTextScale(scale) {
  const s = scale === undefined ? textScale() : normalizeTextScale(scale);
  document.documentElement.style.setProperty("--app-text-scale", String(s));
}

export function saveTextScale(scale) {
  const s = normalizeTextScale(scale);
  localStorage.setItem("mc_text_scale", String(s));
  applyTextScale(s);
}

export const DEFAULT_SEND = "enter";

export function previewOn() { return localStorage.getItem("mc_preview") !== "0"; }

// 滚动隐藏上下栏（2026-10-17 用户定，**默认开**；只在触屏上生效）：跟浏览器一个味道 ——
// 往下看（内容前进）顶栏按位移**逐步**收起，往回看（内容后退）底栏逐步收起；反方向把对面那栏放回来。
export function chromeHideOn() { return localStorage.getItem("mc_chrome_hide") !== "0"; }
export function saveChromeHide(on) { localStorage.setItem("mc_chrome_hide", on ? "1" : "0"); }

// 命令菜单（2026-10-17 用户定，**默认关**）：输入框左侧那颗 ☰ 与输入 "/" 自动弹出的命令面板。
// 移动端那玩意儿纯碍事 ⇒ 默认不给入口；关着也能直接打 `/name` 回车执行（命令本身没消失，
// 只是没有"菜单"这一层）。开关在用户视图 → 输入。
export function commandMenuOn() { return localStorage.getItem("mc_cmd_menu") === "1"; }
export function saveCommandMenu(on) { localStorage.setItem("mc_cmd_menu", on ? "1" : "0"); }

// —— 输入栏形态（2026-10-20 用户定：这是**主题项**，不是输入选项）——
// 两种形态，用户自己挑"要效率还是要美观"：
//   用户口径（2026-10-17 定名）：**正常模式** = float、**紧凑模式** = solid。
//   float（默认）= 正常：输入框是一颗圆角卡，浮在消息列上、两侧留白（状态小字在框上方）；
//   solid        = 紧凑：栏自己实心、方角、边到边（外侧没 padding、输入框没圆角）。
// 值名不改（改用 normal/compact 会把已经存下的偏好作废）。
// 为什么归"外观"而不是"输入"：它改的是**界面长相**，与"回车发不发"那类操作习惯不是一类。
// 载体是**根节点的 `data-composer` 属性**（照 applyTheme 那套）—— CSS 只认属性，组件一个 prop 都不用传。
// ⚠ **不要**跟 `applyTheme` 那条 TG 守卫：它守的是**配色来源**（TG 里颜色由 TG 注入，我们不许覆盖）；
// 而输入栏形态是**布局**，TG Mini App 里照样该由用户自己挑 —— 两件事，别照抄。
export const DEFAULT_COMPOSER = "float";
export function composerStyle() {
  // `??` 语义：空串 = "没设过" ⇒ 回落默认；真存了 "solid" 就得用 —— 别被 `||` 吞掉。
  const v = localStorage.getItem("mc_composer_style");
  return v === "solid" ? "solid" : DEFAULT_COMPOSER;
}
export function applyComposerStyle(style) {
  const s = style === undefined ? composerStyle() : style;
  const root = document.documentElement;
  if (s === "solid") root.setAttribute("data-composer", "solid");
  else root.removeAttribute("data-composer"); // 缺省 = float（属性不出现）
}
export function saveComposerStyle(style) {
  localStorage.setItem("mc_composer_style", style === "solid" ? "solid" : DEFAULT_COMPOSER);
  applyComposerStyle(style);
}

// 上次在看的那条会话（2026-10-11 用户定）：刷新 / 被安卓丢弃后重载 ⇒ 回到它，不再是一张空草稿。
// 空串 = 没记（或人在草稿）⇒ 开机进草稿。**不按用户分键**：换人登录时那条会话取不到 ⇒ 回落草稿。
export function lastSessionId() { return localStorage.getItem("mc_last_session") || ""; }
export function saveLastSession(id) { localStorage.setItem("mc_last_session", id ?? ""); }

// 载荷栏"分块视图"（2026-10-11 用户点名）：开着就把 body.messages 一块块画出来（比裸 JSON 直观）。
export function payloadVizOn() { return localStorage.getItem("mc_payload_viz") === "1"; }
export function savePayloadViz(on) { localStorage.setItem("mc_payload_viz", on ? "1" : "0"); }

// defaultApiBase：**跟着"你从哪儿打开这个页面"走**（location.hostname）——
// 手机从 http://192.168.0.110:8788 打开，默认后端就是 http://192.168.0.110:8787/api/v1，
// 探通直接进聊天、探不通时门板也按这个预填（从前钉死 127.0.0.1，在手机上指的是手机自己）。
// 前端 8788 / 后端 8787 是我们的成对默认；IPv6 主机名要套方括号（location.hostname 不带）。
export function defaultApiBase() {
  const host = (typeof location !== "undefined" && location.hostname) || "";
  if (!host) return DEFAULT_API_URL; // file:// 或非浏览器环境
  const h = host.includes(":") ? `[${host}]` : host;
  return `http://${h}:8787/api/v1`;
}
export function apiBase() { return localStorage.getItem("mc_api") || defaultApiBase(); }

// apiBaseCandidates：启动自检按顺序试的候选 —— 有存过就只认存的（人定的最大）；
// 没存过：[**同源 /api/v1**（首选：单容器部署与 dev 都靠它 —— vite 把 /api 代理给后端，
// 见 vite.config.js；浏览器因此永远同源、不用填地址），跟页面同主机的 8787（兜底：
// 前端被别的静态服务器端出来、没有代理时）]。
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
export function apiToken() { return localStorage.getItem("mc_token") || ""; }
export function themePref() {
  // 空串 / 认不出的 id 都归一到 THEME_IDS 里的一档（见 normalizeTheme）。
  // 从前这里要 `??` 保住空串（那是"跟随系统"），现在那个选项没了 ⇒ 空串就是"没设过"。
  return normalizeTheme(localStorage.getItem("mc_theme"));
}

// 主题应用（唯一一处）：TG Mini App 里 data-theme **根本不设**（用户主题由 TG 注入的
// --tg-theme-* 供给）—— 缺了这道守卫，TG 里保存一次设置就会把预设色压到 TG 注入色上。
export function applyTheme(theme) {
  if (window.Telegram?.WebApp) { document.documentElement.removeAttribute("data-theme"); return; }
  document.documentElement.setAttribute("data-theme", theme === undefined ? themePref() : normalizeTheme(theme));
}

// 连接偏好的唯一写入口（连接门板与设置页共用）：地址先过 normalizeApiBase。
export function saveNetPrefs(base, token) {
  localStorage.setItem("mc_api", normalizeApiBase(base) || defaultApiBase());
  localStorage.setItem("mc_token", token ?? "");
}

// 共享 api 客户端：按 (base, token) 缓存 —— 组件与 App 拿的是同一个实例；
// 偏好改了（刷新后）第一次调用就会重建，不需要各自再抄构造代码。
let cached = null;
let cachedKey = "";
export function sharedApi() {
  const base = apiBase();
  const token = apiToken();
  const key = `${base}\u0000${token}`;
  if (!cached || key !== cachedKey) {
    cached = createApi({ base, token });
    cachedKey = key;
  }
  return cached;
}

// 发送键三档（App 的 keydown 判定与 Composer 的占位提示同源）：这一下回车算不算"发送"。
export function wantsSend(e, mode = sendMode()) {
  if (mode === "enter") return !(e.shiftKey || e.ctrlKey || e.metaKey || e.altKey);
  if (mode === "shift-enter") return e.shiftKey && !e.ctrlKey && !e.metaKey;
  return false; // button 档：裸回车一律换行，只能点按钮
}
export function sendMode() { return localStorage.getItem("mc_send") || DEFAULT_SEND; }
