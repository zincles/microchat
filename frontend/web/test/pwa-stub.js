// SSR 冒烟用的 `virtual:pwa-register` 替身（线上那份由 vite-plugin-pwa 在构建时注入）。
// 冒烟只关心 "setup() 会不会抛 ReferenceError"，Service Worker 一条都不装。
export function registerSW() {
  return () => Promise.resolve();
}
