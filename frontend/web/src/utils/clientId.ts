import { uuidv7 } from "./ids.ts";

const CLIENT_ID_KEY = "mc_client_id";
const UUID_V7_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
let fallbackId: string | null = null;

export function getClientId(): string {
  if (fallbackId) return fallbackId;
  try {
    const existing = localStorage.getItem(CLIENT_ID_KEY);
    if (existing && UUID_V7_PATTERN.test(existing)) {
      fallbackId = existing;
      return existing;
    }
  } catch {
    // Disabled storage still permits this page to keep a stable ID in memory.
  }

  const id = uuidv7();
  fallbackId = id;
  try {
    localStorage.setItem(CLIENT_ID_KEY, id);
  } catch {
    // The in-memory ID is sufficient for this page when persistence is unavailable.
  }
  return id;
}
