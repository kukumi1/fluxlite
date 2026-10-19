import type { NodeInput } from "../api";

export type TelegramTab = "dashboard" | "routes" | "nodes" | "singbox";

export function telegramTab(value: string | undefined): TelegramTab {
  return value === "routes" || value === "nodes" || value === "singbox" ? value : "dashboard";
}

export function safeBindingURL(value: string): string | null {
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.hostname === "t.me" && !url.username && !url.password ? url.href : null;
  } catch {
    return null;
  }
}

export interface TelegramWebApp {
  initData: string;
  initDataUnsafe?: { start_param?: string };
  ready: () => void;
  expand: () => void;
  close: () => void;
}

declare global {
  interface Window { Telegram?: { WebApp?: TelegramWebApp }; }
}

let sdkPromise: Promise<TelegramWebApp> | undefined;

export function loadTelegramSDK(): Promise<TelegramWebApp> {
  if (window.Telegram?.WebApp) return Promise.resolve(window.Telegram.WebApp);
  if (!sdkPromise) {
    sdkPromise = new Promise<TelegramWebApp>((resolve, reject) => {
      const script = document.createElement("script");
      script.src = "https://telegram.org/js/telegram-web-app.js";
      script.async = true;
      const timeout = window.setTimeout(() => finish(new Error("Telegram 组件加载超时，请重新打开面板")), 15000);
      function finish(error?: Error) {
        window.clearTimeout(timeout);
        script.onload = null;
        script.onerror = null;
        if (error) reject(error);
        else if (window.Telegram?.WebApp) resolve(window.Telegram.WebApp);
        else reject(new Error("请从 Telegram Bot 打开面板"));
      }
      script.onload = () => finish();
      script.onerror = () => finish(new Error("Telegram 组件加载失败，请重新打开面板"));
      document.head.append(script);
    });
  }
  return sdkPromise;
}

// A rejected exchange is deliberately retained: retrying single-use initData is
// not authentication recovery. The user must reopen from Bot with fresh data.
export function singleUseExchange<T>(request: (data: string) => Promise<T>): (data: string) => Promise<T> {
  let exchange: { data: string; promise: Promise<T> } | undefined;
  return (data) => {
    if (!exchange || exchange.data !== data) exchange = { data, promise: request(data) };
    return exchange.promise;
  };
}

// Draft links carry an opaque reference, never machine credentials.
export function telegramDraftNonce(value: string | null): string | null {
  return value !== null && /^[A-Za-z0-9_-]{16,256}$/.test(value) ? value : null;
}

export function telegramMachineDraftInput(value: { input: Omit<NodeInput, "secret">; expires_at: string }, now = Date.now()): NodeInput {
  const expiry = Date.parse(value.expires_at);
  const input = value.input;
  const port = (number: number) => Number.isInteger(number) && number >= 1 && number <= 65535;
  if (!Number.isFinite(expiry) || expiry <= now || !input ||
      typeof input.name !== "string" || typeof input.host !== "string" || typeof input.ssh_user !== "string" ||
      typeof input.ipv6_entry !== "string" || typeof input.skip_udp_probe !== "boolean" ||
      !port(input.ssh_port) || !port(input.port_start) || !port(input.port_end) || input.port_start > input.port_end ||
      (input.auth_type !== "password" && input.auth_type !== "key") ||
      (input.via_node_id !== null && (!Number.isSafeInteger(input.via_node_id) || input.via_node_id <= 0))) {
    throw new Error("机器草稿已过期或不可用，请回 Bot 重新添加机器");
  }
  return {
    name: input.name, host: input.host, ssh_port: input.ssh_port, ssh_user: input.ssh_user,
    auth_type: input.auth_type, secret: "", via_node_id: input.via_node_id,
    port_start: input.port_start, port_end: input.port_end,
    skip_udp_probe: input.skip_udp_probe, ipv6_entry: input.ipv6_entry,
  };
}
