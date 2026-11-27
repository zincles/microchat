import { shallowRef, onMounted, onUnmounted } from "vue";
import { getClientId } from "../utils/clientId.ts";
import type { LivePresenceEvent, LiveSessionEvent } from "../api/types.ts";

interface LiveApi {
  liveHeartbeat: (clientId: string) => Promise<{ online_clients: number }>;
  liveEventsUrl: () => string;
}

interface UseLiveSyncOptions {
  api: LiveApi;
  onSessionChanged: (event: LiveSessionEvent) => void;
  onSessionDeleted: (event: LiveSessionEvent) => void;
  onForeground: (hadHiddenChanges: boolean) => void;
  onAgentsChanged: () => void;
}

function onlineCount(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isFinite(value) || !Number.isInteger(value) || value < 0) return null;
  return value;
}

function eventPayload<T>(event: MessageEvent): T | null {
  try {
    const value: unknown = JSON.parse(event.data);
    return value !== null && typeof value === "object" ? value as T : null;
  } catch {
    return null;
  }
}

export function useLiveSync(options: UseLiveSyncOptions) {
  const onlineClients = shallowRef<number | null>(null);
  let timer: number | null = null;
  let source: EventSource | null = null;
  let inFlight = false;
  let heartbeatAgain = false;
  let dirtyWhileHidden = false;
  let wakeTimer: number | null = null;

  function heartbeat(): void {
    if (document.visibilityState !== "visible") return;
    if (inFlight) {
      heartbeatAgain = true;
      return;
    }
    inFlight = true;
    void options.api.liveHeartbeat(getClientId())
      .then((response) => {
        const count = onlineCount(response?.online_clients);
        if (count !== null) onlineClients.value = count;
      })
      .catch(() => {})
      .finally(() => {
        inFlight = false;
        if (heartbeatAgain && document.visibilityState === "visible") {
          heartbeatAgain = false;
          heartbeat();
        }
      });
  }

  function foreground(): void {
    if (document.visibilityState !== "visible") return;
    heartbeat();
    if (wakeTimer !== null) window.clearTimeout(wakeTimer);
    wakeTimer = window.setTimeout(() => {
      wakeTimer = null;
      const hadHiddenChanges = dirtyWhileHidden;
      dirtyWhileHidden = false;
      options.onForeground(hadHiddenChanges);
    }, 300);
  }

  function changed(event: MessageEvent): void {
    const payload = eventPayload<LiveSessionEvent>(event);
    if (!payload || typeof payload.session_id !== "string" || !payload.session_id ||
        typeof payload.revision !== "number" || !Number.isFinite(payload.revision)) return;
    if (document.visibilityState !== "visible") {
      dirtyWhileHidden = true;
      return;
    }
    options.onSessionChanged(payload);
  }

  function deleted(event: MessageEvent): void {
    const payload = eventPayload<LiveSessionEvent>(event);
    if (!payload || typeof payload.session_id !== "string" || !payload.session_id ||
        typeof payload.revision !== "number" || !Number.isFinite(payload.revision)) return;
    if (document.visibilityState !== "visible") {
      dirtyWhileHidden = true;
      return;
    }
    options.onSessionDeleted(payload);
  }

  function presence(event: MessageEvent): void {
    const payload = eventPayload<LivePresenceEvent>(event);
    const count = onlineCount(payload?.online_clients);
    if (count !== null) onlineClients.value = count;
  }
  function agentsChanged(): void {
    if (document.visibilityState !== "visible") return;
    options.onAgentsChanged();
  }

  function onVisibilityChange(): void {
    if (document.visibilityState === "visible") foreground();
  }
  function onEventSourceOpen(): void {
    if (document.visibilityState !== "visible") {
      dirtyWhileHidden = true;
      return;
    }
    foreground();
  }


  onMounted(() => {
    heartbeat();
    timer = window.setInterval(heartbeat, 22_000);
    source = new EventSource(options.api.liveEventsUrl());
    source.addEventListener("open", onEventSourceOpen);
    source.addEventListener("session_changed", changed);
    source.addEventListener("session_deleted", deleted);
    source.addEventListener("agents_changed", agentsChanged);
    source.addEventListener("presence", presence);
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("pageshow", foreground);
    window.addEventListener("focus", foreground);
    window.addEventListener("online", heartbeat);
  });

  onUnmounted(() => {
    if (timer !== null) window.clearInterval(timer);
    if (wakeTimer !== null) window.clearTimeout(wakeTimer);
    source?.removeEventListener("open", onEventSourceOpen);
    source?.removeEventListener("agents_changed", agentsChanged);
    source?.close();
    document.removeEventListener("visibilitychange", onVisibilityChange);
    window.removeEventListener("pageshow", foreground);
    window.removeEventListener("focus", foreground);
    window.removeEventListener("online", heartbeat);
  });

  return { onlineClients };
}
