// PWA 安装的"说人话"部分（2026-11-02）。
//
// 为什么不塞进 pwa.js：那个文件 import 了 `virtual:pwa-register`（vite 虚拟模块），
// Node 的契约测试解析不了它。把**纯判断**放这里，测试才能直接跑；
// 事件捕获（beforeinstallprompt）与 `registerSW` 留在 pwa.js。

// 已经在独立窗口里跑（装过、从桌面图标打开）⇒ 不该再劝人安装。
export function isStandalone() {
  if (typeof window === "undefined") return false;
  return !!window.matchMedia?.("(display-mode: standalone)")?.matches || window.navigator?.standalone === true;
}

export function isSecureOrigin() {
  if (typeof window === "undefined") return true;
  return !!window.isSecureContext;
}

// iOS / iPadOS：Safari 没有 beforeinstallprompt；iPadOS 13+ 的 UA 会伪装成 Mac，
// 所以再看一眼"触摸点 > 1"。
export function isIOS() {
  if (typeof navigator === "undefined") return false;
  const ua = navigator.userAgent || "";
  return /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && (navigator.maxTouchPoints ?? 0) > 1);
}

// installGuidance：不能一键装时，界面该说的那一句（纯函数，可注入状态做测试）。
export function installGuidance({
  secure = isSecureOrigin(),
  standalone = isStandalone(),
  ios = isIOS(),
  prompt = false,
  origin = typeof location !== "undefined" ? location.origin : "",
} = {}) {
  if (standalone) return "已经在独立窗口中运行。";
  if (!secure) {
    return `当前地址不是安全上下文（${origin || "http://…"}）。PWA 只能用 HTTPS 或 localhost / 127.0.0.1 安装。`;
  }
  if (ios) return "Safari：点「分享」→「添加到主屏幕」。";
  if (prompt) return "浏览器已准备好：点「安装应用」即可。";
  return "浏览器没有提供安装入口：Chrome 菜单 →「安装 MicroChat」；或确认地址是 HTTPS / localhost。";
}
