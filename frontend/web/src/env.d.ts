/// <reference types="vite/client" />
/// <reference types="vite-plugin-pwa/client" />

declare const __BUILD_ID__: string;

interface Window {
  Telegram?: { WebApp?: unknown };
}
