// 消息列需要卡高，浮层需要卡的位置：桌面空草稿的输入卡并不贴底。
export function useOverlayLayout() {
  let observer: ResizeObserver | null = null;
  let app: HTMLElement | null = null;
  let chat: HTMLElement | null = null;
  let controls: HTMLElement | null = null;
  let form: HTMLElement | null = null;
  let overlays: HTMLElement | null = null;

  function liftOverlays(): void {
    if (!app || !chat || !controls || !form || !overlays) return;
    const above = controls.getBoundingClientRect();
    const rect = form.getBoundingClientRect();
    const h = rect.height;
    // 聊天和上下文共用控件的让位高度；隐藏时保留上一次的值。
    if (above.height > 0) chat.style.setProperty("--chat-controls-h", `${Math.round(above.height)}px`);
    if (h <= 0) return;
    const root = app.getBoundingClientRect();
    chat.style.setProperty("--composer-h", `${Math.round(h)}px`);

    // #app 随视觉视口平移，是 fixed 的包含块；不能拿 window.innerHeight 当底边。
    overlays.style.setProperty("--composer-bottom", `${root.bottom - rect.top}px`);
    overlays.style.setProperty("--composer-center", `${rect.left - root.left + rect.width / 2}px`);
    overlays.style.setProperty("--composer-width", `${rect.width}px`);
    overlays.style.setProperty("--composer-room", `${Math.max(0, rect.top - above.bottom)}px`);
  }

  function onMoveEnd(event: TransitionEvent): void {
    // 滚动藏栏和工作区切换会平移输入卡，不改变它的尺寸。
    if (event.target === form || event.target === controls || (event.target instanceof HTMLElement && event.target.classList.contains("v-window-item"))) liftOverlays();
  }

  function install(): void {
    app = document.getElementById("app");
    chat = document.getElementById("chat-col");
    controls = document.getElementById("chat-controls");
    form = document.getElementById("composer");
    overlays = document.getElementById("overlays-vue");
    liftOverlays();
    chat?.addEventListener("transitionend", onMoveEnd);
    if (typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(liftOverlays);
      // 空草稿切换会改变消息区高度；视口或侧栏变化则会改变工作区。
      for (const el of [form, controls, form?.parentElement, document.getElementById("messages-vue")]) {
        if (el) observer.observe(el);
      }
    }
  }

  function uninstall(): void {
    observer?.disconnect();
    chat?.removeEventListener("transitionend", onMoveEnd);
  }

  return { liftOverlays, install, uninstall };
}
