import { computed, inject, onScopeDispose, provide, shallowRef, type InjectionKey, type ShallowRef } from "vue";

export type ChatMenu = "model" | "context" | "agent" | "thinking" | "tools";
const activeKey: InjectionKey<ShallowRef<ChatMenu | null>> = Symbol("chat-menu");

/** 每个聊天界面只有一个打开的选择器；不影响设置页或对话框自己的浮层。 */
export function provideChatMenus() {
  const active = shallowRef<ChatMenu | null>(null);
  provide(activeKey, active);
  return active;
}

export function useChatMenu(name: ChatMenu) {
  const active = inject(activeKey);
  if (!active) throw new Error("聊天选择器缺少上层菜单状态");
  const open = computed({
    get: () => active.value === name,
    set: (show: boolean) => {
      if (show) active.value = name;
      // 旧浮层的外点、离场或卸载回调不能把刚打开的另一扇关掉。
      else if (active.value === name) active.value = null;
    },
  });
  onScopeDispose(() => { open.value = false; });
  return open;
}
