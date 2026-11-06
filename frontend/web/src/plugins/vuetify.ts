// Vuetify 4 入口：**只配置一次**，组件按需引入（vite-plugin-vuetify autoImport）。
//
// 2026-11-06 用户定「先改底部输入栏，使用 Vuetify 内置 MD3 主题，而不是手搓 CSS」。
// 这是渐进替换的第一块：插件/主题先落地，组件只从 Composer + ThinkingMenu 开始用。
import "vuetify/styles";
import { createVuetify } from "vuetify";
import { md3 } from "vuetify/blueprints";

// 颜色不是"手搓组件的 CSS"，而是 **Vuetify 官方主题配置**：把 theme.css 里那套 MD3
// baseline 角色喂给 Vuetify（它据此生成 --v-theme-* 与组件状态色）。md3 blueprint 只给
// 组件默认（shape/variant），不会替换 Vuetify 默认的 Material 蓝，所以这里必须显式给三套。
const darkTheme = {
  dark: true,
  colors: {
    background: "#131313",
    surface: "#141218",
    "surface-dim": "#141218",
    "surface-bright": "#3b383e",
    "surface-variant": "#49454f",
    "on-surface": "#e6e0e9",
    "on-surface-variant": "#cac4d0",
    "surface-container-lowest": "#0f0d13",
    "surface-container-low": "#1d1b20",
    "surface-container": "#211f26",
    "surface-container-high": "#2b2930",
    "surface-container-highest": "#36343b",
    outline: "#938f99",
    "outline-variant": "#49454f",
    primary: "#d0bcff",
    "on-primary": "#381e72",
    "primary-container": "#4f378b",
    "on-primary-container": "#eaddff",
    secondary: "#ccc2dc",
    "on-secondary": "#332d41",
    "secondary-container": "#4a4458",
    "on-secondary-container": "#e8def8",
    error: "#f2b8b5",
    "on-error": "#601410",
    "error-container": "#8c1d18",
    "on-error-container": "#f9dedc",
    "inverse-surface": "#e6e0e9",
    "inverse-on-surface": "#322f35",
  },
};
const lightTheme = {
  dark: false,
  colors: {
    background: "#fef7ff",
    surface: "#fef7ff",
    "surface-dim": "#ded8e1",
    "surface-bright": "#fef7ff",
    "surface-variant": "#e6e0e9",
    "on-surface": "#1d1b20",
    "on-surface-variant": "#49454f",
    "surface-container-lowest": "#ffffff",
    "surface-container-low": "#f7f2fa",
    "surface-container": "#f3edf7",
    "surface-container-high": "#ece6f0",
    "surface-container-highest": "#e6e0e9",
    outline: "#79747e",
    "outline-variant": "#cac4d0",
    primary: "#6750a4",
    "on-primary": "#ffffff",
    "primary-container": "#eaddff",
    "on-primary-container": "#21005d",
    secondary: "#625b71",
    "on-secondary": "#ffffff",
    "secondary-container": "#e8def8",
    "on-secondary-container": "#1d192b",
    error: "#b3261e",
    "on-error": "#ffffff",
    "error-container": "#f9dedc",
    "on-error-container": "#410e0b",
    "inverse-surface": "#322f35",
    "inverse-on-surface": "#f5eff7",
  },
};
const breezeTheme = {
  dark: false,
  colors: {
    background: "#f6f8fa",
    surface: "#f6f8fa",
    "surface-dim": "#dde3e9",
    "surface-bright": "#ffffff",
    "surface-variant": "#e1e7ec",
    "on-surface": "#232629",
    "on-surface-variant": "#4f5b65",
    "surface-container-lowest": "#ffffff",
    "surface-container-low": "#f2f5f8",
    "surface-container": "#eef2f5",
    "surface-container-high": "#e8edf1",
    "surface-container-highest": "#e1e7ec",
    outline: "#7a858f",
    "outline-variant": "#c9d1d8",
    primary: "#2470a2",
    "on-primary": "#ffffff",
    "primary-container": "#d3e7f5",
    "on-primary-container": "#0a3a55",
    secondary: "#4f6675",
    "on-secondary": "#ffffff",
    "secondary-container": "#dbe9f3",
    "on-secondary-container": "#16394d",
    error: "#b3261e",
    "on-error": "#ffffff",
    "error-container": "#f9dedc",
    "on-error-container": "#410e0b",
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
// dark/light 两个名字。Vuetify 组件只用于输入栏，先做**单向桥接**：data-theme 变化 ⇒
// 把 Vuetify 切到 dark 或 light（breeze 是浅色变体 ⇒ light）。以后整站迁 Vuetify 时，
// 再把 breeze 也注册成真正的 Vuetify theme（这里刻意不复制色值）。
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
