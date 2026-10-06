// overlays-vue 桥：Overlays.vue 挂 #overlays-vue（浮层三件：命令面板/picker/确认框）。
import Overlays from "./components/Overlays.vue";
import { mount } from "./mount.js";

let inst = null;

export function mountOverlays(handlers) {
  inst = mount(Overlays, "#overlays-vue", {
    "onRun-command": handlers.onRunCommand,
    onPick: handlers.onPick,
  });
}

export const overlays = {
  showPalette(items) { inst?.call("showPalette", items); },
  movePalette(d) { inst?.call("movePalette", d); },
  hidePalette() { inst?.call("hidePalette"); },
  showPicker(t, items, action) { inst?.call("showPicker", t, items, action); },
  movePicker(d) { inst?.call("movePicker", d); },
  hidePicker() { inst?.call("hidePicker"); },
  confirmAsk(text) { return inst?.call("confirmAsk", text) ?? Promise.resolve(false); },
  lift(px) { inst?.call("lift", px); },
  isPaletteOpen() { return inst?.call("isPaletteOpen") ?? false; },
  isPickerOpen() { return inst?.call("isPickerOpen") ?? false; },
  palSelected() { return inst?.call("palSelected"); },
  pickerSelected() { return inst?.call("pickerSelected"); },
  pickerAction() { return inst?.call("pickerAction"); },
};
