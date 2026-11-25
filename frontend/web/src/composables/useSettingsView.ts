// 设置与聊天同级；左栏进入目录，品牌和工作流保留指向具体页面的入口。
import { ref } from "vue";

export function useSettingsView({ closeLeftDrawer }: { closeLeftDrawer: () => void }) {
  const settingsOpen = ref(false);
  const settingsOnly = ref<"" | "agent" | "update">("");

  // 2026-11-01：账号砍了 ⇒ 没有"只有管理员能进"这回事（本机单人使用）。
  function openSettings(): void {
    settingsOnly.value = "";
    settingsOpen.value = true;
    closeLeftDrawer();
  }

  function openManage(pane: "agent" | "update"): void {
    settingsOnly.value = pane;
    settingsOpen.value = true;
    closeLeftDrawer();
  }

  function closeSettings(): void {
    settingsOpen.value = false;
    settingsOnly.value = "";
    closeLeftDrawer();
  }

  // backToChat：**侧栏动作要把人带回对话区**（点会话 / 点＋新对话 / 当前会话被删）—— 唯一出口。
  // ⚠ 只给侧栏动作用：命令面板与启动恢复路径不经过设置视图，不需要它。
  function backToChat(): void {
    closeSettings();
  }

  return { settingsOpen, settingsOnly, openSettings, openManage, closeSettings, backToChat };
}
