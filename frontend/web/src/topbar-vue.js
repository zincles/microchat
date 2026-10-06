import TopBar from "./components/TopBar.vue";
import { mount } from "./mount.js";

let inst = null;

export function mountTopBar(handlers) {
  inst = mount(TopBar, "#topbar-vue", {
    "onToggle-left": handlers.onToggleLeft,
    "onToggle-right": handlers.onToggleRight,
    "onSelect-model": handlers.onSelectModel,
    "onSelect-agent": handlers.onSelectAgent,
  });
}

export const topBar = {
  setConn(t) { inst?.call("setConn", t); },
  setSession(info) { inst?.call("setSession", info); },
  setModels(groups, cur) { inst?.call("setModels", groups, cur); },
  setAgents(list, cur) { inst?.call("setAgents", list, cur); },
};
