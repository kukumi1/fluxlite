import { lazy, Suspense, useEffect, useId, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { ArrowRight, Eye, EyeOff, LockKeyhole, Moon, ShieldCheck, Sun, Zap } from "lucide-react";
import { api, type Enrollment } from "../api";
import { authErrorMessage } from "../lib/auth-errors";
import { Banner } from "../components/Modal";
import { LoginCat } from "../components/LoginCat";
import { useTheme } from "../theme";
import "./login.css";

// The dotted map is decoration only the sign-in page needs; keep it out of the main bundle.
const RelayMap = lazy(() => import("../components/RelayMap").then((module) => ({ default: module.RelayMap })));

type Stage = "loading" | "login" | "setup" | "enroll";

export function Login({ onAuthenticated, onForgotPassword }: {
  onAuthenticated: () => void;
  onForgotPassword?: () => void;
}) {
  const passwordInputID = useId();
  const [stage, setStage] = useState<Stage>("loading");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    api
      .setupStatus()
      .then((s) => { if (active) setStage(s.setup_needed ? "setup" : "login"); })
      .catch(() => { if (active) setStage("login"); });
    return () => { active = false; };
  }, []);

  const fail = (err: unknown) => setError(authErrorMessage(err));

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
    return <LoginLayout><p className="auth-loading" role="status">正在连接管理工作台…</p></LoginLayout>;
  }

  return (
    <LoginLayout alert={error}>

        {error && <Banner kind="err">{error}</Banner>}

        {stage === "setup" && (
          <form className="auth-form" onSubmit={submitSetup}>
            <h1>创建管理员</h1>
            <p className="auth-description">第一次使用，先建一个管理员账号。</p>
            <label>
              用户名
              <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required />
            </label>
            <label>
              密码（至少 12 位）
              <SecretInput
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </label>
            <button className="auth-submit" disabled={busy}>
              {busy ? "创建中…" : "创建账号"}<ArrowRight size={18} aria-hidden="true" />
            </button>
          </form>
        )}

        {stage === "enroll" && enrollment && (
          <form className="auth-form" onSubmit={submitEnroll}>
            <h1>绑定两步验证</h1>
            <p className="auth-description">
              将密钥添加至验证器后输入验证码。密钥仅显示一次。
            </p>
            <div className="auth-secret-code">{enrollment.secret}</div>
            <label>
              验证码
              <input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                required
              />
            </label>
            <button className="auth-submit" disabled={busy}>
              {busy ? "验证中…" : "完成绑定"}<ShieldCheck size={18} aria-hidden="true" />
            </button>
            {/* Skipping leaves the account unenrolled, which the login path
                treats as "no second factor" rather than "setup unfinished".
                Without this the secret shown above would be the only way in,
                and a closed tab would mean a lost panel. */}
            <button
              type="button"
              className="auth-secondary"
              disabled={busy}
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
          <form className="auth-form" onSubmit={submitLogin}>
            <h1>账号登录</h1>
            <p className="auth-description">欢迎回来，用管理员账号进入工作台。</p>
            <label>
              用户名
              <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" placeholder="输入管理员用户名" required />
            </label>
            <div className="auth-field">
              <div className="auth-label-row">
                <label htmlFor={passwordInputID}>密码</label>
                {onForgotPassword && <button type="button" className="auth-text-button" disabled={busy} onClick={onForgotPassword}>忘记密码？</button>}
              </div>
              <SecretInput
                id={passwordInputID}
                autoComplete="current-password"
                placeholder="输入密码"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            <label>
              两步验证码 <span className="auth-optional">选填</span>
              <input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                placeholder="未开启两步验证则留空"
              />
            </label>
            <button className="auth-submit" disabled={busy}>
              {busy ? "登录中…" : "登录工作台"}<ArrowRight size={18} aria-hidden="true" />
            </button>
          </form>
        )}
    </LoginLayout>
  );
}

/** Shared framing keeps login, setup and account recovery in one visual language. */
export function LoginLayout({ children, alert }: { children: ReactNode; alert?: string }) {
  const { theme, toggle } = useTheme();
  return (
    <main className="auth-page">
      <Suspense fallback={null}><RelayMap /></Suspense>
      <header className="auth-top">
        <span className="auth-brand"><span className="brand-mark"><Zap size={16} aria-hidden="true" /></span>fluxlite<span className="brand-dot">.</span></span>
        <span className="auth-tagline">Lightweight relay panel</span>
        <button type="button" className="icon-btn auth-theme-toggle" onClick={toggle} aria-label={theme === "dark" ? "切换浅色模式" : "切换深色模式"} title={theme === "dark" ? "切换浅色模式" : "切换深色模式"}>
          {theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}
        </button>
      </header>
      <div className="auth-stage">
        <LoginCat alert={alert} />
        <section className="auth-card" aria-label="账号验证">
          {children}
          <p className="auth-safe-note"><LockKeyhole size={13} aria-hidden="true" />仅限授权管理员访问</p>
        </section>
      </div>
      <footer className="auth-footer">fluxlite · 轻量网络管理面板</footer>
    </main>
  );
}

/** A password input with a reveal toggle. The toggle keeps focus in the field so typing carries on. */
export function SecretInput(props: Omit<InputHTMLAttributes<HTMLInputElement>, "type">) {
  const [shown, setShown] = useState(false);
  return (
    <span className="auth-secret">
      <input {...props} type={shown ? "text" : "password"} data-secret="" />
      <button
        type="button"
        className="auth-reveal"
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => setShown((value) => !value)}
        aria-label={shown ? "隐藏密码" : "显示密码"}
        aria-pressed={shown}
      >
        {shown ? <EyeOff size={17} /> : <Eye size={17} />}
      </button>
    </span>
  );
}
