import { useEffect, useState } from "react";
import { api } from "./api";
import { Login } from "./pages/Login";
import { Nodes } from "./pages/Nodes";
import { Routes } from "./pages/Routes";
import { Audit } from "./pages/Audit";
import { Account } from "./pages/Account";

type Tab = "routes" | "nodes" | "audit" | "account";
type AuthState = "checking" | "in" | "out";

export function App() {
  const [authState, setAuthState] = useState<AuthState>("checking");
  const [username, setUsername] = useState("");
  const [tab, setTab] = useState<Tab>("routes");

  async function checkSession() {
    try {
      const me = await api.me();
      setUsername(me.username);
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

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          flux<span>lite</span>
        </div>
        <nav className="nav">
          <button className={tab === "routes" ? "active" : ""} onClick={() => setTab("routes")}>
            链路
          </button>
          <button className={tab === "nodes" ? "active" : ""} onClick={() => setTab("nodes")}>
            节点
          </button>
          <button className={tab === "audit" ? "active" : ""} onClick={() => setTab("audit")}>
            审计日志
          </button>
        </nav>
        <div className="sidebar-footer">
          <button
            className={`account-link ${tab === "account" ? "active" : ""}`}
            onClick={() => setTab("account")}
            title="个人中心"
          >
            <span className="avatar">{username.slice(0, 1).toUpperCase()}</span>
            <span className="account-name">{username}</span>
          </button>
          <button className="btn sm" onClick={() => void logout()}>
            退出登录
          </button>
        </div>
      </aside>

      <main className="main">
        {tab === "routes" && <Routes />}
        {tab === "nodes" && <Nodes />}
        {tab === "audit" && <Audit />}
        {tab === "account" && (
          <Account onUsernameChanged={setUsername} onSignedOut={() => setAuthState("out")} />
        )}
      </main>
    </div>
  );
}
