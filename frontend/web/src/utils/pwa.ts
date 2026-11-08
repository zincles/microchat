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
let notify: () => void = () => {};
export function onUpdateReady(handler: () => void): void { notify = handler; }

// updateSW：点了更新条 ⇒ 切到新版本。
//   1. 先把"正在切"喊出去（更新条自己改成"更新中…"、按钮禁用）—— 别让你重复点；
//   2. `updateServiceWorker(true)` 发 skipWaiting ⇒ 新 SW 接管 ⇒ controlling ⇒
//      回调里 `window.location.reload()`（这是 vite-plugin-pwa 内建行为，不是我们写的）。
// 参数 `true` 是"要 reload"（0.13.2+ 起这个参数虽不再参与判断，但传着与官方示例一致）。
let updateSW: ((reloadPage?: boolean) => Promise<void>) | null = null;
export function startUpdate(): Promise<void> | undefined {
  if (!updateSW) return undefined;
  return updateSW(true);
}

// ── 手动核对 / 切换（2026-11-09 用户定：更新面板里的 PWA 小工具）─────────────
//
// 顶部更新条只在 SW **已经**把新版本装到 waiting 时出现；如果用户想主动核对
// "我现在看到的壳是不是旧的"，就得自己调 registration.update() 并看 waiting。
// 这里只做三件事：读状态、检查、切换；UI 在 UpdatePane。

export interface PwaBuild {
  build_id: string;
  built_at?: string;
}

export interface PwaSnapshot {
  supported: boolean;
  secure: boolean;
  registered: boolean;
  controlled: boolean;
  active: boolean;
  waiting: boolean;
  installing: boolean;
  scope: string;
  error: string;
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));
const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// 读服务器上**当前**的前端构建身份。build.json 由 vite.config.js 在每次构建时写出，
// 且**不进 Service Worker 预缓存**（globPatterns 不含 json）⇒ cache:"no-store" 拿到的是新壳。
export async function fetchLatestBuild(): Promise<PwaBuild> {
  const base = import.meta.env.BASE_URL || "/";
  const res = await fetch(`${base}build.json?t=${Date.now()}`, {
    cache: "no-store",
    headers: { Accept: "application/json" },
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = (await res.json()) as Partial<PwaBuild>;
  if (!data || typeof data.build_id !== "string" || !data.build_id) {
    throw new Error("build.json 缺少 build_id");
  }
  return { build_id: data.build_id, built_at: typeof data.built_at === "string" ? data.built_at : "" };
}

async function snapshot(reg?: ServiceWorkerRegistration | null): Promise<PwaSnapshot> {
  const supported = typeof navigator !== "undefined" && "serviceWorker" in navigator;
  const secure = typeof window !== "undefined" ? window.isSecureContext : false;
  const empty = {
    supported,
    secure,
    registered: false,
    controlled: false,
    active: false,
    waiting: false,
    installing: false,
    scope: "",
    error: "",
  };
  if (!supported) return empty;
  try {
    const r = reg === undefined ? await navigator.serviceWorker.getRegistration() : reg;
    return {
      ...empty,
      registered: !!r,
      controlled: !!navigator.serviceWorker.controller,
      active: !!r?.active,
      waiting: !!r?.waiting,
      installing: !!r?.installing,
      scope: r?.scope ?? "",
      error: "",
    };
  } catch (e) {
    return { ...empty, error: errText(e) };
  }
}

export async function readPwaSnapshot(): Promise<PwaSnapshot> {
  return snapshot();
}

async function waitForWaiting(reg: ServiceWorkerRegistration, timeout = 12000): Promise<boolean> {
  const deadline = Date.now() + timeout;
  let quietSince = 0;
  while (Date.now() < deadline) {
    if (reg.waiting) return true;
    if (reg.installing) {
      quietSince = 0;
    } else {
      // update() 已经返回、却连 installing 都没有：给 updatefound 一小段窗口，
      // 还没有就说明服务端 SW 没变 —— 别让「检查」白等 12 秒。
      if (!quietSince) quietSince = Date.now();
      if (Date.now() - quietSince > 1200) return false;
    }
    await sleep(200);
  }
  return !!reg.waiting;
}

// checkPwaUpdate：主动问一次浏览器有没有新的 Service Worker。
// 返回检查后的快照；拿不到 registration（dev / 非安全上下文 / 第一次安装还没完成）时原样返回。
export async function checkPwaUpdate(): Promise<PwaSnapshot> {
  const before = await snapshot();
  if (!before.supported) return before;
  try {
    const reg = await navigator.serviceWorker.getRegistration();
    if (!reg) return before;
    try { await reg.update(); } catch { /* 离线时 update 会 reject，但可能已经有 waiting */ }
    await waitForWaiting(reg);
    return snapshot(reg);
  } catch (e) {
    return { ...before, error: errText(e) };
  }
}

// applyPwaUpdate：优先让 waiting 的 SW skipWaiting（updateSW 会自己 reload）；
// 没有 waiting 就再 update 一次；仍然没有（或根本没有 SW）⇒ 普通 reload。
export async function applyPwaUpdate(): Promise<void> {
  if (typeof navigator === "undefined" || !("serviceWorker" in navigator)) {
    location.reload();
    return;
  }
  const reg = await navigator.serviceWorker.getRegistration().catch(() => null);
  if (!reg) {
    location.reload();
    return;
  }
  if (!reg.waiting) {
    try { await reg.update(); } catch { /* 同上 */ }
    await waitForWaiting(reg);
  }
  if (reg.waiting) {
    const p = startUpdate();
    if (p) {
      await p;
      return;
    }
    reg.waiting.postMessage({ type: "SKIP_WAITING" });
  }
  location.reload();
}

// forceReloadPwa：最后手段 —— 注销 SW、清掉这个 origin 的 Cache Storage，再重载。
// 会丢掉离线壳缓存（下次打开会重新装）；localStorage / IndexedDB 不动。
export async function forceReloadPwa(): Promise<void> {
  if (typeof navigator !== "undefined" && "serviceWorker" in navigator) {
    const regs = await navigator.serviceWorker.getRegistrations().catch(() => []);
    await Promise.all(regs.map((r) => r.unregister().catch(() => false)));
  }
  if (typeof caches !== "undefined") {
    const keys = await caches.keys().catch(() => []);
    await Promise.all(keys.map((key) => caches.delete(key).catch(() => false)));
  }
  location.reload();
}

// registerPWA：开机调一次 —— 装下 `onNeedRefresh` 回调，拿到 `updateSW`。
export function registerPWA(): void {
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
