import { lazy, Suspense, useEffect, useState } from "react";
import {
  LayoutDashboard,
  LogOut,
  Moon,
  ScrollText,
  Server,
  Sun,
  TerminalSquare,
  Waypoints,
  Zap,
} from "lucide-react";
import { api } from "./api";
import { ThemeProvider, useTheme, useThemeState } from "./theme";
import { Login } from "./pages/Login";
import { Dashboard } from "./pages/Dashboard";
import { Nodes } from "./pages/Nodes";
import { Routes } from "./pages/Routes";
import { Audit } from "./pages/Audit";
import { Account } from "./pages/Account";

// 终端页连同 xterm 一起单独打包：不开终端的人不该为它下载一份终端模拟器。
const Console = lazy(() => import("./pages/Console").then((m) => ({ default: m.Console })));

type Tab = "dashboard" | "routes" | "nodes" | "console" | "audit" | "account";
type AuthState = "checking" | "in" | "out";

const MAIN_NAV = [
  { id: "dashboard", label: "仪表盘", icon: LayoutDashboard },
  { id: "routes", label: "链路", icon: Waypoints },
  { id: "nodes", label: "节点", icon: Server },
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

  function openConsole(nodeID: number) {
    setConsoleNode(nodeID);
    setTab("console");
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
      setAuthState("out");
    }
  }

  function navButton(item: { id: string; label: string; icon: typeof Server }) {
    const Icon = item.icon;
    return (
      <button
        key={item.id}
        className={tab === item.id ? "active" : ""}
        onClick={() => setTab(item.id as Tab)}
      >
        <Icon className="nav-icon" size={16} />
        {item.label}
      </button>
    );
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-head">
          <div className="brand">
            <span className="brand-mark">
              <Zap size={16} />
            </span>
            fluxlite
          </div>
          <button
            className="icon-btn"
            onClick={toggle}
            title={theme === "dark" ? "切换到亮色" : "切换到暗色"}
          >
            {theme === "dark" ? <Sun size={16} /> : <Moon size={16} />}
          </button>
        </div>

        <div className="sidebar-group-label">主菜单</div>
        <nav className="nav">{MAIN_NAV.map(navButton)}</nav>

        <div className="sidebar-group-label">管理</div>
        <nav className="nav">{ADMIN_NAV.map(navButton)}</nav>

        <div className="sidebar-footer">
          <div className="row" style={{ flexWrap: "nowrap", gap: 6 }}>
            <button
              className={`account-link ${tab === "account" ? "active" : ""}`}
              onClick={() => setTab("account")}
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
            <button className="icon-btn" onClick={() => void logout()} title="退出登录">
              <LogOut size={16} />
            </button>
          </div>
        </div>
      </aside>

      <main className="main">
        <div className="main-inner">
          {tab === "dashboard" && <Dashboard onNavigate={setTab} />}
          {tab === "routes" && <Routes />}
          {tab === "nodes" && <Nodes onOpenConsole={openConsole} />}
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
