import { useEffect, useState } from "react";
import { api, ApiError, type Enrollment } from "../api";
import { Banner } from "../components/Modal";

type Stage = "loading" | "login" | "setup" | "enroll";

export function Login({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [stage, setStage] = useState<Stage>("loading");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .setupStatus()
      .then((s) => setStage(s.setup_needed ? "setup" : "login"))
      .catch(() => setStage("login"));
  }, []);

  const fail = (err: unknown) =>
    setError(err instanceof ApiError ? err.message : "请求失败，请检查网络");

  async function submitSetup(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const result = await api.setup(username, password);
      setEnrollment(result);
      setStage("enroll");
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  async function submitEnroll(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.confirmSetup(username, code);
      setCode("");
      setStage("login");
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  async function submitLogin(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(username, password, code);
      onAuthenticated();
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  if (stage === "loading") {
    return <div className="login-wrap muted">加载中…</div>;
  }

  return (
    <div className="login-wrap">
      <div className="login-box">
        <div className="brand" style={{ textAlign: "center", paddingBottom: 20 }}>
          flux<span>lite</span>
        </div>

        {error && <Banner kind="err">{error}</Banner>}

        {stage === "setup" && (
          <form className="card" onSubmit={submitSetup}>
            <h2>初始化管理员</h2>
            <p className="hint">首次使用需创建管理员账号，创建后必须绑定两步验证才能登录。</p>
            <label>
              用户名
              <input value={username} onChange={(e) => setUsername(e.target.value)} required />
            </label>
            <label>
              密码（至少 12 位）
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </label>
            <button className="btn primary" disabled={busy} style={{ width: "100%" }}>
              {busy ? "创建中…" : "创建账号"}
            </button>
          </form>
        )}

        {stage === "enroll" && enrollment && (
          <form className="card" onSubmit={submitEnroll}>
            <h2>绑定两步验证</h2>
            <p className="hint">
              把下面的密钥加入 Authenticator，然后输入当前验证码完成绑定。这个密钥只显示这一次。
            </p>
            <div
              className="mono"
              style={{
                wordBreak: "break-all",
                background: "var(--bg)",
                padding: 10,
                borderRadius: 6,
                marginBottom: 14,
              }}
            >
              {enrollment.secret}
            </div>
            <label>
              验证码
              <input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                inputMode="numeric"
                maxLength={6}
                required
              />
            </label>
            <button className="btn primary" disabled={busy} style={{ width: "100%" }}>
              {busy ? "验证中…" : "完成绑定"}
            </button>
            {/* Skipping leaves the account unenrolled, which the login path
                treats as "no second factor" rather than "setup unfinished".
                Without this the secret shown above would be the only way in,
                and a closed tab would mean a lost panel. */}
            <button
              type="button"
              className="btn"
              style={{ width: "100%", marginTop: 8 }}
              onClick={() => {
                setCode("");
                setStage("login");
              }}
            >
              暂时跳过（之后可在个人中心开启）
            </button>
          </form>
        )}

        {stage === "login" && (
          <form className="card" onSubmit={submitLogin}>
            <h2>登录</h2>
            <label>
              用户名
              <input value={username} onChange={(e) => setUsername(e.target.value)} required />
            </label>
            <label>
              密码
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </label>
            <label>
              两步验证码
              <input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                inputMode="numeric"
                maxLength={6}
                placeholder="未开启两步验证则留空"
              />
            </label>
            <button className="btn primary" disabled={busy} style={{ width: "100%" }}>
              {busy ? "登录中…" : "登录"}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}
