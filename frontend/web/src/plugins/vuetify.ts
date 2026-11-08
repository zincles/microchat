// Vuetify 4 入口：**唯一主题权威**（2026-11-06 用户定「Vuetify 控制主题强调色」）。
//
// 应用自己的 `--md-*` 在 theme.css 里只做映射：`rgb(var(--v-theme-*))`。
// 也就是说强调色/中性面/描边只有这一份色值；自定义 CSS 与 Vuetify 组件同时跟着它走。
import "vuetify/styles";
import { createVuetify } from "vuetify";
import { md3 } from "vuetify/blueprints";

// 暗色 = Gemini 暗色板（2026-11-06 用户定：「改成 gemini 的主题配色（暗色）」）。
// 色值直接取自 gemini.google.com 的 GM3 system colors（surface / surface-container-* /
// primary / secondary / error），不是另调的一版近似色：
//   bg #131314 · 容器 #1b1b1b/#1e1f20/#282a2c/#333537 · 主色 #a8c7fa · on-primary #062e6f
// 亮色保持原样；Breeze 已于 2026-11-09 删除。
const darkTheme = {
  dark: true,
  colors: {
    background: "#131314",
    surface: "#131314",
    "surface-dim": "#131314",
    "surface-bright": "#1e1f20",
    "surface-variant": "#444746",
    "on-surface": "#e3e3e3",
    "on-surface-variant": "#c4c7c5",
    "surface-container-lowest": "#0e0e0e",
    "surface-container-low": "#1b1b1b",
    "surface-container": "#1e1f20",
    "surface-container-high": "#282a2c",
    "surface-container-highest": "#333537",
    outline: "#8e918f",
    "outline-variant": "#444746",
    primary: "#a8c7fa",
    "on-primary": "#062e6f",
    "primary-container": "#1f3760",
    "on-primary-container": "#d3e3fd",
    secondary: "#7fcfff",
    "on-secondary": "#003355",
    "secondary-container": "#004a77",
    "on-secondary-container": "#c2e7ff",
    tertiary: "#6dd58c",
    "on-tertiary": "#0a3818",
    "tertiary-container": "#0f5223",
    "on-tertiary-container": "#c4eed0",
    error: "#f2b8b5",
    "on-error": "#601410",
    "error-container": "#601410",
    "on-error-container": "#f9dedc",
    "inverse-surface": "#e3e3e3",
    "inverse-on-surface": "#303030",
  },
};
const lightTheme = {
  dark: false,
  colors: {
    background: "#f8fafc",
    surface: "#ffffff",
    "surface-dim": "#e2e8f0",
    "surface-bright": "#ffffff",
    "surface-variant": "#cbd5e1",
    "on-surface": "#0f172a",
    "on-surface-variant": "#475569",
    "surface-container-lowest": "#ffffff",
    "surface-container-low": "#f8fafc",
    "surface-container": "#f1f5f9",
    "surface-container-high": "#e2e8f0",
    "surface-container-highest": "#cbd5e1",
    outline: "#94a3b8",
    "outline-variant": "#cbd5e1",
    primary: "#2563eb",
    "on-primary": "#ffffff",
    "primary-container": "#dbeafe",
    "on-primary-container": "#1e3a8a",
    secondary: "#475569",
    "on-secondary": "#ffffff",
    "secondary-container": "#e2e8f0",
    "on-secondary-container": "#1e293b",
    error: "#dc2626",
    "on-error": "#ffffff",
    "error-container": "#fee2e2",
    "on-error-container": "#7f1d1d",
    "inverse-surface": "#1e293b",
    "inverse-on-surface": "#f8fafc",
  },
};


export const vuetify = createVuetify({
  // MD3 blueprint = Vuetify 官方的 Material Design 3 预设（组件默认 / shape）。
  blueprint: md3,
  // MD3 默认形状：Vuetify 4 的 md3 blueprint 管组件内部，这里补一个全局胶囊按钮；
  // 具体组件写了 rounded 的仍以自己为准。
  defaults: {
    VBtn: { rounded: "pill" },
  },
  theme: {
    defaultTheme: "dark",
    themes: { dark: darkTheme, light: lightTheme },
  },
});

// installVuetifyThemeBridge：应用主题是 data-theme（light/dark/auto），Vuetify
// 只有 light/dark 两档。auto 用 matchMedia 跟随系统；data-theme 或系统偏好变化都重算。
export function installVuetifyThemeBridge(): void {
  const mql = window.matchMedia("(prefers-color-scheme: light)");
  const apply = (): void => {
    const id = document.documentElement.getAttribute("data-theme") || "auto";
    const resolved = id === "auto" ? (mql.matches ? "light" : "dark") : id === "light" ? "light" : "dark";
    vuetify.theme.global.name.value = resolved;
  };
  new MutationObserver(apply).observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"],
  });
  mql.addEventListener("change", apply);
  apply();
}
