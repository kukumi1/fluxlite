import { lazy, Suspense, useEffect, useRef, useState } from "react";
import {
  LayoutDashboard,
  PanelLeftClose,
  PanelLeftOpen,
  Menu,
  X,
  ChevronRight,
  LogOut,
  Moon,
  ScrollText,
  Server,
  Sun,
  TerminalSquare,
  Network,
  Waypoints,
  Zap,
} from "lucide-react";
import { api } from "./api";
import { readPreference, writePreference } from "./lib/appearance";
import { ThemeProvider, useTheme, useThemeState } from "./theme";
import { Login } from "./pages/Login";
import { Dashboard } from "./pages/Dashboard";
import { Nodes } from "./pages/Nodes";
import { Routes } from "./pages/Routes";
import { Audit } from "./pages/Audit";
import { Account } from "./pages/Account";
import { SingBox } from "./pages/SingBox";

// 终端页连同 xterm 一起单独打包：不开终端的人不该为它下载一份终端模拟器。
const Console = lazy(() => import("./pages/Console").then((m) => ({ default: m.Console })));

type Tab = "dashboard" | "routes" | "nodes" | "singbox" | "console" | "audit" | "account";
type AuthState = "checking" | "in" | "out";

const MAIN_NAV = [
  { id: "dashboard", label: "仪表盘", icon: LayoutDashboard },
  { id: "routes", label: "链路", icon: Waypoints },
  { id: "nodes", label: "机器", icon: Server },
  { id: "singbox", label: "节点", icon: Network },
  { id: "console", label: "终端", icon: TerminalSquare },
] as const;

const ADMIN_NAV = [{ id: "audit", label: "审计日志", icon: ScrollText }] as const;

export function App() {
  const theme = useThemeState();
  return (
    <ThemeProvider value={theme}>
      <Shell />
    </ThemeProvider>
  );
}

function Shell() {
  const [authState, setAuthState] = useState<AuthState>("checking");
  const [username, setUsername] = useState("");
  const [avatar, setAvatar] = useState("");
  const [tab, setTab] = useState<Tab>("dashboard");
  const [consoleNode, setConsoleNode] = useState<number | null>(null);
  const { theme, toggle } = useTheme();
  const [collapsed, setCollapsed] = useState(() => readPreference("fluxlite-sidebar-collapsed") === "true");
  const [mobileOpen, setMobileOpen] = useState(false);
  const sidebarRef = useRef<HTMLElement>(null);

  function navigate(next: Tab) {
    setTab(next);
    setMobileOpen(false);
  }

  function toggleSidebar() {
    const next = !collapsed;
    writePreference("fluxlite-sidebar-collapsed", String(next));
    setCollapsed(next);
  }

  useEffect(() => {
    if (!mobileOpen) return;
    const previous = document.body.style.overflow;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const controls = () => Array.from(sidebarRef.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []).filter((element) => element.getClientRects().length > 0);
    document.body.style.overflow = "hidden";
    controls()[0]?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMobileOpen(false);
      if (event.key !== "Tab") return;
      const buttons = controls();
      const first = buttons[0];
      const last = buttons[buttons.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    };
    const onResize = () => {
      if (window.innerWidth > 860) setMobileOpen(false);
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("resize", onResize);
    return () => {
      document.body.style.overflow = previous;
      previousFocus?.focus();
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("resize", onResize);
    };
  }, [mobileOpen]);

  function openConsole(nodeID: number) {
    setConsoleNode(nodeID);
    navigate("console");
  }

  async function checkSession() {
    try {
      const me = await api.me();
      setUsername(me.username);
      setAvatar(me.avatar);
      setAuthState("in");
    } catch {
      setAuthState("out");
    }
  }

  useEffect(() => {
    void checkSession();
  }, []);

  if (authState === "checking") {
    return <div className="login-wrap muted">加载中…</div>;
  }
  if (authState === "out") {
    return <Login onAuthenticated={() => void checkSession()} />;
  }

  async function logout() {
    try {
      await api.logout();
    } finally {
      setMobileOpen(false);
      setAuthState("out");
    }
  }

  function navButton(item: { id: string; label: string; icon: typeof Server }) {
    const Icon = item.icon;
    return (
      <button
        key={item.id}
        className={tab === item.id ? "active" : ""}
        onClick={() => navigate(item.id as Tab)}
        title={item.label}
        aria-current={tab === item.id ? "page" : undefined}
      >
        <Icon className="nav-icon" size={16} />
        <span className="nav-label-text">{item.label}</span>
      </button>
    );
  }

  return (
    <div className={`app${collapsed ? " sidebar-collapsed" : ""}${mobileOpen ? " mobile-open" : ""}`}>
      {mobileOpen && <button className="sidebar-scrim" aria-label="关闭导航" onClick={() => setMobileOpen(false)} />}
      <aside ref={sidebarRef} className="sidebar" id="main-navigation" aria-label="主导航">
        <div className="sidebar-head">
          <div className="brand">
            <span className="brand-mark">
              <Zap size={16} />
            </span>
            <span className="brand-name">fluxlite<span className="brand-dot">.</span></span>
          </div>
          <button className="icon-btn mobile-close" onClick={() => setMobileOpen(false)} aria-label="关闭导航"><X size={18} /></button>
        </div>

        <div className="workspace-label"><span className="workspace-mark"><Network size={16} /></span><span className="workspace-copy"><strong>管理工作台</strong><small>机器 · 节点 · 链路</small></span></div>
        <div className="sidebar-group-label">工作空间</div>
        <nav className="nav">{MAIN_NAV.map(navButton)}</nav>

        <div className="sidebar-group-label">管理</div>
        <nav className="nav">{ADMIN_NAV.map(navButton)}</nav>

        <div className="sidebar-footer">
          <div className="row" style={{ flexWrap: "nowrap", gap: 6 }}>
            <button
              className={`account-link ${tab === "account" ? "active" : ""}`}
              onClick={() => navigate("account")}
              title="个人中心"
            >
              {avatar ? (
                <img className="avatar" src={avatar} alt="" />
              ) : (
                <span className="avatar">{username.slice(0, 1).toUpperCase()}</span>
              )}
              <span className="account-meta">
                <span className="account-name">{username}</span>
                <span className="account-role">管理员</span>
              </span>
            </button>
            <button className="icon-btn logout-button" onClick={() => void logout()} title="退出登录" aria-label="退出登录">
              <LogOut size={16} />
            </button>
          </div>
        </div>
      </aside>

      <main className="main">
        <header className="topbar">
          <div className="breadcrumb">
            <button className="icon-btn desktop-collapse" onClick={toggleSidebar} aria-label={collapsed ? "展开侧边栏" : "收起侧边栏"} aria-expanded={!collapsed} aria-controls="main-navigation">{collapsed ? <PanelLeftOpen size={18} /> : <PanelLeftClose size={18} />}</button>
            <button className="icon-btn mobile-menu" onClick={() => setMobileOpen(true)} aria-label="打开导航" aria-expanded={mobileOpen} aria-controls="main-navigation"><Menu size={18} /></button>
            <span className="breadcrumb-root">工作空间</span><ChevronRight size={14} className="breadcrumb-root" />
            <strong>{[...MAIN_NAV, ...ADMIN_NAV].find((item) => item.id === tab)?.label ?? "个人中心"}</strong>
          </div>
          <div className="topbar-actions">
            <button className="icon-btn" onClick={toggle} title={theme === "dark" ? "切换到亮色" : "切换到暗色"} aria-label={theme === "dark" ? "切换到亮色" : "切换到暗色"}>{theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}</button>
            <button className="topbar-profile" onClick={() => navigate("account")} title="个人中心">{avatar ? <img src={avatar} alt="" /> : <span>{username.slice(0, 1).toUpperCase()}</span>}<span className="topbar-username">{username}</span></button>
          </div>
        </header>
        <div className="main-inner">
          {tab === "dashboard" && <Dashboard onNavigate={navigate} />}
          {tab === "routes" && <Routes />}
          {tab === "nodes" && <Nodes onOpenConsole={openConsole} />}
          {tab === "singbox" && <SingBox />}
          {tab === "console" && (
            <Suspense fallback={<p className="muted">正在载入终端…</p>}>
              <Console initialNode={consoleNode} />
            </Suspense>
          )}
          {tab === "audit" && <Audit />}
          {tab === "account" && (
            <Account
              onUsernameChanged={setUsername}
              onAvatarChanged={setAvatar}
              onSignedOut={() => setAuthState("out")}
            />
          )}
        </div>
      </main>
    </div>
  );
}
