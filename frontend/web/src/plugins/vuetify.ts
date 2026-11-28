// 一份 Vuetify 实例贯穿所有风格，切换不重挂组件。
import "vuetify/styles";
import { createVuetify } from "vuetify";
import { md3 } from "vuetify/blueprints";

import { themePref, themeStylePref } from "../utils/prefs.ts";
import { appThemes, resolveThemeName } from "./themePalettes.ts";

export const vuetify = createVuetify({
  // MD3 blueprint = Vuetify 官方的 Material Design 3 预设（组件默认 / shape）。
  blueprint: md3,
  // MD3 默认形状：Vuetify 4 的 md3 blueprint 管组件内部，这里补一个全局胶囊按钮；
  // 具体组件写了 rounded 的仍以自己为准。
  defaults: {
    VBtn: { rounded: "pill" },
    // 全站布尔开关统一使用 Vuetify 提供的 MD3 outlined-track 变体。
    VSwitch: { inset: "material" },
  },
  theme: {
    defaultTheme: "dark",
    themes: appThemes,
  },
});

// 启动时先用偏好镜像；服务器回执和系统亮暗变化共用这条路径。
export function installVuetifyThemeBridge(): void {
  const systemMode = window.matchMedia("(prefers-color-scheme: dark)");
  const { documentElement, body } = document;
  const themeColor = document.querySelector('meta[name="theme-color"]');
  let restoreTransition = 0;
  const apply = (): void => {
    const name = resolveThemeName(themeStylePref(), themePref(), systemMode.matches);
    const palette = appThemes[name];
    if (vuetify.theme.name.value !== name) {
      body.classList.add("theme-changing");
      cancelAnimationFrame(restoreTransition);
      void vuetify.theme.change(name, false);
      restoreTransition = requestAnimationFrame(() => {
        restoreTransition = requestAnimationFrame(() => body.classList.remove("theme-changing"));
      });
    }
    documentElement.dataset.colorMode = palette.dark ? "dark" : "light";
    themeColor?.setAttribute("content", palette.colors.background);
  };
  window.addEventListener("mc-theme-changed", apply);
  systemMode.addEventListener("change", () => {
    if (themePref() === "auto") window.dispatchEvent(new Event("mc-theme-changed"));
  });
  apply();
}
