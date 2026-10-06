import SessionBar from "./components/SessionBar.vue";
import { mount } from "./mount.js";

let inst = null;

export function mountSessionBar(handlers) {
  inst = mount(SessionBar, "#sessionbar-vue", {
    onSelect: handlers.onSelect,
    onSettings: handlers.onSettings,
    onClose: handlers.onClose,
    onNew: handlers.onNew,
    "onOpen-settings": handlers.onOpenSettings,
  });
}

export const sessionBar = {
  setSessions(list, cur) { inst?.call("setSessions", list, cur); },
};
