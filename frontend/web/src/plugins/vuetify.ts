// Vuetify 4 入口：**唯一主题权威**（2026-11-06 用户定「Vuetify 控制主题强调色」）。
//
// 应用自己的 `--md-*` 在 theme.css 里只做映射：`rgb(var(--v-theme-*))`。
// 也就是说强调色/中性面/描边只有这一份色值；自定义 CSS 与 Vuetify 组件同时跟着它走。
import "vuetify/styles";
import { createVuetify } from "vuetify";
import { md3 } from "vuetify/blueprints";

// OpenWebUI 风格：中性灰面 + 专业蓝强调色（不再是 MD3 baseline 的紫）。
const darkTheme = {
  dark: true,
  colors: {
    background: "#131313",
    surface: "#1c1c1c",
    "surface-dim": "#17181b",
    "surface-bright": "#2f333a",
    "surface-variant": "#3a3f47",
    "on-surface": "#e5e7eb",
    "on-surface-variant": "#a1a8b3",
    "surface-container-lowest": "#111214",
    "surface-container-low": "#202126",
    "surface-container": "#24262b",
    "surface-container-high": "#292c31",
    "surface-container-highest": "#30343a",
    outline: "#6b7280",
    "outline-variant": "#3a3f47",
    primary: "#2563eb",
    "on-primary": "#ffffff",
    "primary-container": "#1e3a8a",
    "on-primary-container": "#dbeafe",
    secondary: "#94a3b8",
    "on-secondary": "#0f172a",
    "secondary-container": "#334155",
    "on-secondary-container": "#e2e8f0",
    error: "#f87171",
    "on-error": "#450a0a",
    "error-container": "#7f1d1d",
    "on-error-container": "#fee2e2",
    "inverse-surface": "#e5e7eb",
    "inverse-on-surface": "#111827",
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
const breezeTheme = {
  dark: false,
  colors: {
    background: "#f6f8fa",
    surface: "#ffffff",
    "surface-dim": "#dde3e9",
    "surface-bright": "#ffffff",
    "surface-variant": "#c9d1d8",
    "on-surface": "#232629",
    "on-surface-variant": "#4f5b65",
    "surface-container-lowest": "#ffffff",
    "surface-container-low": "#f2f5f8",
    "surface-container": "#eef2f5",
    "surface-container-high": "#e8edf1",
    "surface-container-highest": "#e1e7ec",
    outline: "#7a858f",
    "outline-variant": "#c9d1d8",
    primary: "#0369a1",
    "on-primary": "#ffffff",
    "primary-container": "#e0f2fe",
    "on-primary-container": "#0c4a6e",
    secondary: "#475569",
    "on-secondary": "#ffffff",
    "secondary-container": "#e2e8f0",
    "on-secondary-container": "#1e293b",
    error: "#dc2626",
    "on-error": "#ffffff",
    "error-container": "#fee2e2",
    "on-error-container": "#7f1d1d",
    "inverse-surface": "#2b363d",
    "inverse-on-surface": "#eef3f7",
  },
};

export const vuetify = createVuetify({
  // MD3 blueprint = Vuetify 官方的 Material Design 3 预设（组件默认 / shape）。
  blueprint: md3,
  theme: {
    defaultTheme: "dark",
    themes: { dark: darkTheme, light: lightTheme, breeze: breezeTheme },
  },
});

// installVuetifyThemeBridge：应用主题是 data-theme（dark/light/breeze），Vuetify 主题是
// 同名三档。Vuetify 组件只用于输入栏/设置控件，先做**单向桥接**：data-theme 变化 ⇒
// 切 Vuetify theme；theme.css 的 --md-* 全部映射到 --v-theme-*，所以全站强调色跟着走。
export function installVuetifyThemeBridge(): void {
  const apply = (): void => {
    const id = document.documentElement.getAttribute("data-theme") || "dark";
    vuetify.theme.global.name.value = id === "breeze" ? "breeze" : id === "light" ? "light" : "dark";
  };
  new MutationObserver(apply).observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"],
  });
  apply();
}
