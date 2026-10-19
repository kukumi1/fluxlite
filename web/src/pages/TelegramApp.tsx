import { useEffect, useState } from "react";
import { LayoutDashboard, Network, Server, Waypoints } from "lucide-react";
import { api, setTelegramToken, type NodeInput } from "../api";
import { Banner } from "../components/Modal";
import { loadTelegramSDK, singleUseExchange, telegramTab, telegramDraftNonce, telegramMachineDraftInput, type TelegramTab } from "../lib/telegram";
import { Dashboard } from "./Dashboard";
import { Routes } from "./Routes";
import { Nodes } from "./Nodes";
import { SingBox } from "./SingBox";

// Telegram initData is single-use server-side. React StrictMode replays effects
// in development, so share the in-flight exchange rather than spending it twice.
const authenticate = singleUseExchange(api.telegramAuth);

const tabs = [
  { id: "dashboard", label: "仪表盘", icon: LayoutDashboard },
  { id: "routes", label: "链路", icon: Waypoints },
  { id: "nodes", label: "机器", icon: Server },
  { id: "singbox", label: "节点", icon: Network },
] as const;

export function TelegramApp() {
  const [draftQuery] = useState(() => new URLSearchParams(window.location.search).get("draft"));
  const [draft, setDraft] = useState<NodeInput>();
  const [draftError, setDraftError] = useState("");
  const [authenticated, setAuthenticated] = useState(false);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<TelegramTab>("dashboard");

  useEffect(() => {
    let active = true;
    let expiryTimer: number | undefined;
    setTelegramToken("");
    // Remove the capability from the address before loading any external SDK.
    if (draftQuery !== null) {
      const url = new URL(window.location.href);
      url.searchParams.delete("draft");
      window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
    }
    void (async () => {
      try {
        const app = await loadTelegramSDK();
        if (!app.initData) throw new Error("请从已绑定的 Telegram Bot 点击“打开面板”，不能直接访问此地址。");
        const session = await authenticate(app.initData);
        if (!active) return;
        const lifetime = Date.parse(session.expires_at) - Date.now();
        if (!session.token || !Number.isFinite(lifetime) || lifetime <= 0) throw new Error("登录已过期，请从 Bot 重新打开面板");
        setTelegramToken(session.token);
        setTab(telegramTab(new URLSearchParams(window.location.search).get("tab") ?? app.initDataUnsafe?.start_param));
        if (draftQuery !== null) {
          setTab("nodes");
          try {
            const nonce = telegramDraftNonce(draftQuery);
            if (!nonce) throw new Error("invalid draft");
            const result = await api.telegramMachineDraft(nonce);
            if (!active) return;
            setDraft(telegramMachineDraftInput(result));
          } catch {
            if (!active) return;
            setDraftError("机器草稿已过期或不可用，请回 Bot 重新添加机器");
          }
        }
        if (Date.parse(session.expires_at) <= Date.now()) {
          setTelegramToken("");
          throw new Error("登录已过期，请从 Bot 重新打开面板");
        }
        setAuthenticated(true);
        app.ready();
        app.expand();
        expiryTimer = window.setTimeout(() => {
          setTelegramToken("");
          setAuthenticated(false);
          setError("登录已过期，请关闭后从 Bot 重新打开面板。");
        }, Math.min(Date.parse(session.expires_at) - Date.now(), 2147483647));
      } catch (err) {
        if (active) setError(err instanceof Error ? err.message : "验证失败，请从 Bot 重新打开面板");
      }
    })();
    return () => {
      active = false;
      window.clearTimeout(expiryTimer);
      setTelegramToken("");
    };
  }, [draftQuery]);

  if (!authenticated) return <main className="telegram-entry"><h1>fluxlite · Telegram</h1>{error ? <Banner kind="err">{error}</Banner> : <p role="status">正在验证 Telegram 身份…</p>}<p className="hint">仅允许已在网页个人中心绑定的管理员使用。</p></main>;

  return <div className="telegram-app">
    <header className="telegram-header"><strong>fluxlite · Telegram</strong><span className="tag ok">管理员面板</span></header>
    <nav className="telegram-nav" aria-label="Telegram 面板导航">{tabs.map(({ id, label, icon: Icon }) => <button key={id} className={`btn${tab === id ? " primary" : ""}`} aria-current={tab === id ? "page" : undefined} onClick={() => { if (tab === "nodes" && id !== "nodes") setDraft(undefined); setTab(id); }}><Icon size={17} />{label}</button>)}</nav>
    <main className="telegram-content">
      {draftError && <Banner kind="err">{draftError}</Banner>}
      {tab === "dashboard" && <Dashboard onNavigate={setTab} />}
      {tab === "routes" && <Routes />}
      {tab === "nodes" && <Nodes telegram initialCreateInput={draft} onCreateDraftConsumed={() => setDraft(undefined)} />}
      {tab === "singbox" && <SingBox />}
    </main>
  </div>;
}
