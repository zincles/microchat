// 冒烟：把 App 在 Node 里 SSR 渲染一遍 —— setup() 会真的执行。
// 目的只有一个：抓 `ReferenceError: xxx is not defined` 这类**构建与契约测试都看不见**的错
// （2026-10-29 真踩过：删掉 mqlRight 的声明，漏了底部两处监听还在用它）。
const stub = () => ({
  matches: false, addEventListener() {}, removeEventListener() {},
  addListener() {}, removeListener() {},
});
const noop = () => {};
const el = () => ({
  style: { setProperty: noop, removeProperty: noop },
  classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
  setAttribute: noop, removeAttribute: noop, getAttribute: () => null,
  appendChild: noop, removeChild: noop, addEventListener: noop, removeEventListener: noop,
  querySelector: () => null, querySelectorAll: () => [], getBoundingClientRect: () => ({ width: 0, height: 0, top: 0, left: 0 }),
  offsetHeight: 0, offsetWidth: 0, scrollHeight: 0, dataset: {},
});

export async function run() {
  globalThis.window = {
    matchMedia: stub, addEventListener: noop, removeEventListener: noop,
    location: { hostname: "127.0.0.1", origin: "http://127.0.0.1:8788", protocol: "http:", href: "http://127.0.0.1:8788/" },
    localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
    ResizeObserver: class { observe() {} unobserve() {} disconnect() {} },
    getComputedStyle: () => ({ getPropertyValue: () => "" }),
    setTimeout: () => 0, clearTimeout: noop, setInterval: () => 0, clearInterval: noop,
    requestAnimationFrame: () => 0, cancelAnimationFrame: noop,
  };
  globalThis.document = {
    documentElement: el(), body: el(),
    getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
    createElement: el, addEventListener: noop, removeEventListener: noop,
    title: "", visibilityState: "visible",
  };
  globalThis.location = globalThis.window.location;
  globalThis.localStorage = globalThis.window.localStorage;
  globalThis.ResizeObserver = globalThis.window.ResizeObserver;
  globalThis.matchMedia = stub;
  globalThis.getComputedStyle = globalThis.window.getComputedStyle;
  globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({}), text: async () => "" });
  // Node 24 自带只读的 `navigator` ⇒ 直接赋值会抛 "only a getter"。要改就用 defineProperty。
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: { userAgent: "node", language: "zh-CN", onLine: true },
  });

  const { createSSRApp } = await import("vue");
  // ⚠ 不能 `import("vue/server-renderer")`：vite.config.js 把 `vue` 别名到了
  // `vue/dist/vue.esm-bundler.js`，子路径会被拼成 `…esm-bundler.js/server-renderer`（不存在）。
  const { renderToString } = await import("@vue/server-renderer");
  const App = (await import("/home/zincles/microchat/frontend/web/src/App.vue")).default;
  return await renderToString(createSSRApp(App));
}
