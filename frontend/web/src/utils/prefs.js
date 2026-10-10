// 界面偏好（localStorage `mc_*`）的唯一出口 + 共享 api 客户端。
// 为什么集中：以前每个组件各自 `localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1"`
// 抄一份（5 处），还有主题应用两份（一份忘了 TG 守卫）—— 全部收口到这里。
// 口径不变：**改完偏好刷新页面生效**（组件在装载时取一次；sharedApi 按偏好键缓存）。
import { createApi, DEFAULT_API_URL, normalizeApiBase } from "../api/client.js";

export const DEFAULT_THEME = "deepseek"; // App/SettingsModal 的主题默认（`??` 语义：null=没设过）
export const DEFAULT_SEND = "enter";

export function previewOn() { return localStorage.getItem("mc_preview") !== "0"; }

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
// 没存过：[跟页面同主机的 8787，**同源 /api/v1**]（后者兜底反代/单端口部署）。
// 非首选候选胜出时调用方要把它存下来（sharedApi 等一切读的都是 apiBase()）。
export function apiBaseCandidates() {
  const stored = localStorage.getItem("mc_api");
  if (stored) return [stored];
  const list = [defaultApiBase()];
  if (typeof location !== "undefined" && location.origin && location.origin !== "null") {
    const same = `${location.origin}/api/v1`;
    if (!list.includes(same)) list.push(same);
  }
  return list;
}
export function apiToken() { return localStorage.getItem("mc_token") || ""; }
export function themePref() {
  // `??` 而不是 `||`：设过空串 = "跟随系统"（空串会被 `||` 吞掉变成默认主题，踩过）。
  return localStorage.getItem("mc_theme") ?? DEFAULT_THEME;
}

// 主题应用（唯一一处）：TG Mini App 里 data-theme **根本不设**（用户主题由 TG 注入的
// --tg-theme-* 供给）—— 缺了这道守卫，TG 里保存一次设置就会把预设色压到 TG 注入色上。
// `theme` 为空串 = 跟随系统（摘掉属性）；不传 = 读偏好。
export function applyTheme(theme) {
  const t = theme === undefined ? themePref() : theme;
  if (window.Telegram?.WebApp) { document.documentElement.removeAttribute("data-theme"); return; }
  if (t) document.documentElement.setAttribute("data-theme", t);
  else document.documentElement.removeAttribute("data-theme");
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
