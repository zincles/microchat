// 悬浮输入行 / 顶栏的尺寸同步。
//
// 输入行是绝对定位的浮层（#composer），消息列底部留白必须知道它多高（textarea 会长高）；
// 顶栏在窄屏允许换行后也会变高，消息列顶部留白要跟着量。这里用两个 ResizeObserver
// 把实测高度写成 CSS 变量（--composer-h / --topbar-h），并让命令面板/picker 顶在输入行上方。
import type { Ref } from "vue";

interface OverlaysLift {
  lift(px: number): void;
}

export function useOverlayLayout(overlaysRef: Ref<OverlaysLift | null>) {
  let composerRO: ResizeObserver | null = null;
  let topbarRO: ResizeObserver | null = null;

  // liftOverlays：把面板/picker 顶到输入框上方（输入框长高多少就顶多少）。
  function liftOverlays(): void {
    // 输入框自身就是 #composer（组件根）——"#composer-vue" 那层包装早没了，别再查它。
    const form = document.querySelector<HTMLElement>("#composer");
    if (!form) return;
    const h = form.getBoundingClientRect().height;
    // 量不到就别写（2026-10-29）：切到上下文工作区时输入行是 `display:none`，量出来是 0 ——
    // 那不是"输入行 0 高"，是"这一下没在量"。照写会让 `--composer-h` 掉到 0，
    // 切回聊天时消息列底部的留白先塌一下、等 ResizeObserver 回调再补回来（一帧的跳动）。
    if (h <= 0) return;
    overlaysRef.value?.lift(Math.max(0, h - 60));
    // 输入行现在是**悬浮层**（绝对定位）⇒ 消息列底部留白得知道它多高。
    document.getElementById("chat-col")?.style.setProperty("--composer-h", `${Math.round(h)}px`);
  }

  function install(): void {
    if (typeof ResizeObserver !== "undefined" && !composerRO) {
      composerRO = new ResizeObserver(() => liftOverlays());
      const form = document.querySelector("#composer");
      if (form) composerRO.observe(form);
    }
    if (typeof ResizeObserver !== "undefined" && !topbarRO) {
      const bar = document.querySelector("#chat-col > header");
      if (bar) {
        const sync = (): void => {
          const h = bar.getBoundingClientRect().height;
          // 量不到（切工作区等）就别写 0 —— 那会让消息列顶到栏里去。
          if (h > 0) document.getElementById("chat-col")?.style.setProperty("--topbar-h", `${Math.round(h)}px`);
        };
        topbarRO = new ResizeObserver(sync);
        topbarRO.observe(bar);
        sync();
      }
    }
  }

  function uninstall(): void {
    composerRO?.disconnect();
    composerRO = null;
    topbarRO?.disconnect();
    topbarRO = null;
  }

  return { liftOverlays, install, uninstall };
}
