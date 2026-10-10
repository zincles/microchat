// 界面偏好（localStorage `mc_*`）的唯一出口 + 共享 api 客户端。
// 为什么集中：以前每个组件各自 `localStorage.getItem("mc_api") || "http://127.0.0.1:8787/api/v1"`
// 抄一份（5 处），还有主题应用两份（一份忘了 TG 守卫）—— 全部收口到这里。
// 口径不变：**改完偏好刷新页面生效**（组件在装载时取一次；sharedApi 按偏好键缓存）。
import { createApi, DEFAULT_API_URL } from "../api/client.js";

export const DEFAULT_THEME = "deepseek"; // App/SettingsModal 的主题默认（`??` 语义：null=没设过）
export const DEFAULT_SEND = "enter";

export function previewOn() { return localStorage.getItem("mc_preview") !== "0"; }
export function apiBase() { return localStorage.getItem("mc_api") || DEFAULT_API_URL; }
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
export function sendHintText(mode = sendMode()) {
  return mode === "enter" ? "输入消息（回车发送，Shift/Ctrl/Alt+回车换行）；/ 开头进命令"
    : mode === "shift-enter" ? "输入消息（Shift+回车发送，回车换行）；/ 开头进命令"
    : "输入消息，点发送发出（回车换行）；/ 开头进命令";
}
