// PWA 的 Service Worker 注册（**唯一一处**）—— 2026-10-20 用户定：从 `autoUpdate` 改成
// `prompt` + 一条**常驻更新条**。
//
// 为什么改（真实踩过，见 AGENTS.md「踩过的坑」）：
// `autoUpdate` 会在新 SW `activated` 时**自己** `location.reload()` —— 看着省事，实则：
//   · 你没在看那个标签页时，刷新**悄悄**发生（你甚至不知道发生过）；
//   · 更要命的是它**不告诉你"此刻看的是旧的"**：2026-10-20 排查了一轮才发现
//     "服务器上明明是新的、界面上却没有新选项"，最后靠清站点数据才揭穿是 SW 缓存的旧壳。
// 现在 `prompt`：检测到新版本 ⇒ 弹一条**不自动消失**的条，由你点了才切过去（发 skipWaiting）。
// 于是"我在看哪一版"变成一件说得出口的事。
//
// ⚠ 这段只在 **build/preview** 有意义（dev 不开 SW —— 见 vite.config.js；那时
// `registerSW` 拿到的是 dev 的 stub，回调永不触发，真跑也无害）。
//
// `virtual:pwa-register` 是 vite-plugin-pwa 提供的虚拟模块：它在**构建时**把
// `registerSW` 换成真正那份实现（含 workbox-window 的自动更新/等待逻辑）。
// 插件检测到这个 import 后会自己把 `injectRegister` 转成 `false`（不再往 index.html
// 塞那段没有回调的 registerSW.js）—— 所以**别在 vite.config.js 里手写 injectRegister**，
// 那会跟它的自动检测打架。
import { registerSW } from "virtual:pwa-register";

// 更新条的状态：`onUpdateReady` 由调用方（App.vue）挂进来 —— 这里只管"何时该弹"。
//
// 回调在**页面加载后异步**触发（SW 装完才回调），所以调用方得在启动时就把钩子挂好。
let notify = () => {};
export function onUpdateReady(handler) { notify = handler; }

// updateSW：点了更新条 ⇒ 切到新版本。
//   1. 先把"正在切"喊出去（更新条自己改成"更新中…"、按钮禁用）—— 别让你重复点；
//   2. `updateServiceWorker(true)` 发 skipWaiting ⇒ 新 SW 接管 ⇒ controlling ⇒
//      回调里 `window.location.reload()`（这是 vite-plugin-pwa 内建行为，不是我们写的）。
// 参数 `true` 是"要 reload"（0.13.2+ 起这个参数虽不再参与判断，但传着与官方示例一致）。
let updateSW = null;
export function startUpdate() {
  if (!updateSW) return;
  return updateSW(true);
}

// registerPWA：开机调一次 —— 装下 `onNeedRefresh` 回调，拿到 `updateSW`。
export function registerPWA() {
  updateSW = registerSW({
    // 检测到新版本（新 SW 装好、在等）⇒ 弹条。**不自动切**：切换由用户点。
    onNeedRefresh() { notify(); },
    // 首次装好（离线可用）⇒ 不打扰：那是正常状态，报了反而像出事了。
    onOfflineReady() {},
    onRegisterError(error) {
      // 注册失败照实记一条（控制台）—— 不弹条：SW 挂了不影响页面能用。
      console.warn("[pwa] Service Worker 注册失败：", error);
    },
  });
}

// ── 安装（2026-11-02）─────────────────────────────────────────────────────────
//
// 应用本身**是可安装的**（manifest + 192/512/maskable 图标 + SW 都在；Chrome 的
// `Page.getInstallabilityErrors` 在 localhost 上是空数组）。但"能装"不等于"找得到入口"：
//
//   · Chrome / Edge / 安卓：`beforeinstallprompt` 到了才有**一键安装**；
//   · Safari / iOS：从来没有这个事件，只有「分享 → 添加到主屏幕」；
//   · `http://192.168.x.x` 这类局域网地址：**不是安全上下文**，浏览器连 SW 都不给，
//     自然也不会给安装入口（需要 HTTPS，或 `localhost` / `127.0.0.1`）。
//
// 三种情况都要说得出人话 —— 不能让一个"安装"按钮点下去什么都不发生。
//
// ⚠ 监听必须在**模块求值时**就挂上（App 一 import 就执行），不能等组件 mounted：
//   事件通常在 SW 装好之后不久就来，挂晚了就永远收不到。
let deferredPrompt = null;
let installHandler = () => {};
if (typeof window !== "undefined") {
  window.addEventListener("beforeinstallprompt", (e) => {
    // 拦下浏览器的**自动**迷你条：我们有自己那颗按钮（在左栏底部）。地址栏的安装图标
    // 与菜单项不受影响 —— `preventDefault()` 拦的是那条自动弹的条，不是安装能力本身。
    e.preventDefault();
    deferredPrompt = e;
    installHandler(true);
  });
  window.addEventListener("appinstalled", () => {
    deferredPrompt = null;
    installHandler(false);
  });
}

// onInstallAvailable：把"现在能不能一键装"推给界面；挂上时立刻回调一次当前值。
export function onInstallAvailable(handler) {
  installHandler = handler;
  handler(!!deferredPrompt);
}

export function installReady() { return !!deferredPrompt; }

// promptInstall：用户点了安装按钮 ⇒ 调浏览器自己的安装框。
// 返回 `{outcome}`：accepted / dismissed / unavailable。
export async function promptInstall() {
  if (!deferredPrompt) return { outcome: "unavailable" };
  const prompt = deferredPrompt;
  // 一个事件只能用一次：先清掉，再等用户选。
  deferredPrompt = null;
  installHandler(false);
  try {
    await prompt.prompt();
    return await prompt.userChoice;
  } catch {
    return { outcome: "dismissed" };
  }
}
