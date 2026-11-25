// 消息列需要卡高，浮层需要卡的位置：桌面空草稿的输入卡并不贴底。
export function useOverlayLayout() {
  let observer: ResizeObserver | null = null;
  let app: HTMLElement | null = null;
  let chat: HTMLElement | null = null;
  let bar: HTMLElement | null = null;
  let form: HTMLElement | null = null;
  let overlays: HTMLElement | null = null;

  function liftOverlays(): void {
    if (!app || !chat || !bar || !form || !overlays) return;
    const header = bar.getBoundingClientRect();
    const rect = form.getBoundingClientRect();
    const h = rect.height;
    // 上下文工作区也使用顶栏高度；隐藏聊天时不拿零高度覆盖原来的消息留白。
    if (header.height > 0) chat.style.setProperty("--topbar-h", `${Math.round(header.height)}px`);
    if (h <= 0) return;
    const root = app.getBoundingClientRect();
    chat.style.setProperty("--composer-h", `${Math.round(h)}px`);

    // #app 随视觉视口平移，是 fixed 的包含块；不能拿 window.innerHeight 当底边。
    overlays.style.setProperty("--composer-bottom", `${root.bottom - rect.top}px`);
    overlays.style.setProperty("--composer-center", `${rect.left - root.left + rect.width / 2}px`);
    overlays.style.setProperty("--composer-width", `${rect.width}px`);
    overlays.style.setProperty("--composer-room", `${Math.max(0, rect.top - header.bottom)}px`);
  }

  function onMoveEnd(event: TransitionEvent): void {
    // 专注/滚动藏栏和工作区切换会平移输入卡，不改变它的尺寸。
    if (event.target === form || (event.target instanceof HTMLElement && event.target.classList.contains("v-window-item"))) liftOverlays();
  }

  function install(): void {
    app = document.getElementById("app");
    chat = document.getElementById("chat-col");
    bar = chat?.querySelector<HTMLElement>(":scope > header") ?? null;
    form = document.getElementById("composer");
    overlays = document.getElementById("overlays-vue");
    liftOverlays();
    chat?.addEventListener("transitionend", onMoveEnd);
    if (typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(liftOverlays);
      // 空草稿切换会改变消息区高度；视口或侧栏变化则会改变工作区。
      for (const el of [form, bar, form?.parentElement, document.getElementById("messages-vue")]) {
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
