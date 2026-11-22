// 设置视图的开合与独立入口。
//
// 设置与对话**同级**（不是弹窗，2026-10-10）；渠道/智能体仍从左栏独立入口进，
// 共用同一个视图（`:only` 只显示那一页）。会话列表已搬进设置目录（2026-11-22）。
import { ref } from "vue";

export function useSettingsView({ closeLeftDrawer }: { closeLeftDrawer: () => void }) {
  const settingsOpen = ref(false);
  // "" = 完整设置；"provider" / "agent" / "update" = 只显示那一页。
  const settingsOnly = ref("");

  // 2026-11-01：账号砍了 ⇒ 没有"只有管理员能进"这回事（本机单人使用）。
  function openSettings(): void {
    settingsOnly.value = "";
    settingsOpen.value = true;
    closeLeftDrawer();
  }

  function openManage(pane: string): void {
    // 渠道 / 智能体是左栏的独立管理页；更新由品牌头进。未知 pane 回完整设置目录。
    settingsOnly.value = ["provider", "agent", "update"].includes(pane) ? pane : "";
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
