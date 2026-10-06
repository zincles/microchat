// overlays-vue 桥：Overlays.vue 挂 #overlays-vue（浮层三件：命令面板/picker/确认框）。
import { createApp, h } from "vue";
import Overlays from "./components/Overlays.vue";

let ovRef = null;

export function mountOverlays(handlers) {
  const app = createApp({
    render() {
      return h(Overlays, {
        ref: (el) => { ovRef = el; },
        "onRun-command": handlers.onRunCommand,
        onPick: handlers.onPick,
      });
    },
  });
  app.mount("#overlays-vue");
}

export const overlays = {
  showPalette(items) { ovRef?.showPalette(items); },
  movePalette(d) { ovRef?.movePalette(d); },
  hidePalette() { ovRef?.hidePalette(); },
  showPicker(t, items, action) { ovRef?.showPicker(t, items, action); },
  movePicker(d) { ovRef?.movePicker(d); },
  hidePicker() { ovRef?.hidePicker(); },
  confirmAsk(text) { return ovRef?.confirmAsk(text) ?? Promise.resolve(false); },
  isPaletteOpen() { return ovRef?.isPaletteOpen() ?? false; },
  isPickerOpen() { return ovRef?.isPickerOpen() ?? false; },
  palSelected() { return ovRef?.palSelected(); },
  pickerSelected() { return ovRef?.pickerSelected(); },
  pickerAction() { return ovRef?.pickerAction(); },
};
