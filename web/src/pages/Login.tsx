import { useEffect, useId, useState, type ReactNode } from "react";
import { ArrowRight, LockKeyhole, Moon, ShieldCheck, Sun, Zap } from "lucide-react";
import { api, type Enrollment } from "../api";
import { authErrorMessage } from "../lib/auth-errors";
import { Banner } from "../components/Modal";
import { useTheme } from "../theme";
import "./login.css";

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
    <LoginLayout>

        {error && <Banner kind="err">{error}</Banner>}

        {stage === "setup" && (
          <form className="auth-form" onSubmit={submitSetup}>
            <span className="auth-eyebrow">从这里开始</span>
            <h1>创建管理员</h1>
            <p className="auth-description">设置账号，开启你的管理工作台。创建后可绑定两步验证保护账号。</p>
            <label>
              用户名
              <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required />
            </label>
            <label>
              密码（至少 12 位）
              <input
                type="password"
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
            <span className="auth-eyebrow">多一重保护</span>
            <h1>绑定两步验证</h1>
            <p className="auth-description">
              把下面的密钥加入验证器应用，然后输入当前验证码完成绑定。这个密钥只显示这一次。
            </p>
            <div className="cmd">{enrollment.secret}</div>
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
            <span className="auth-eyebrow">你的连接，由你掌控</span>
            <h1>欢迎回来</h1>
            <p className="auth-description">登录管理工作台，继续管理机器、节点与链路。</p>
            <label>
              用户名
              <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" placeholder="输入管理员用户名" required />
            </label>
            <div className="auth-field">
              <div className="auth-label-row">
                <label htmlFor={passwordInputID}>密码</label>
                {onForgotPassword && <button type="button" className="auth-text-button" disabled={busy} onClick={onForgotPassword}>忘记密码？</button>}
              </div>
              <input
                id={passwordInputID}
                type="password"
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
export function LoginLayout({ children }: { children: ReactNode }) {
  const { theme, toggle } = useTheme();
  return (
    <main className="auth-page">
      <div className="auth-shell">
        <header className="auth-header">
          <span className="auth-brand"><span className="auth-brand-mark"><Zap size={22} aria-hidden="true" /></span>fluxlite<span className="auth-brand-dot">.</span></span>
          <span className="auth-workspace">管理工作台</span>
          <button type="button" className="auth-theme-toggle" onClick={toggle} aria-label={theme === "dark" ? "切换浅色模式" : "切换深色模式"} title={theme === "dark" ? "切换浅色模式" : "切换深色模式"}>
            {theme === "dark" ? <Sun size={19} /> : <Moon size={19} />}
          </button>
        </header>
        <div className="auth-stage">
          <section className="auth-panel" aria-label="账号验证">
            <span className="auth-panel-brand"><Zap size={19} aria-hidden="true" />fluxlite</span>
            {children}
            <p className="auth-safe-note"><LockKeyhole size={13} aria-hidden="true" />仅限授权管理员访问</p>
          </section>
          <aside className="auth-art" aria-label="管理工作台介绍">
            <div className="auth-art-copy"><span>让连接变得简单</span><h2>每一次连接，<br />都井然有序。</h2><p>机器 · 节点 · 链路，一处掌控。</p></div>
            <ConnectionScene />
            <span className="auth-art-caption"><span />轻盈界面，稳稳连接</span>
          </aside>
        </div>
      </div>
      <footer className="auth-footer">fluxlite · 轻量网络管理面板</footer>
    </main>
  );
}

function ConnectionScene() {
  const id = useId().replace(/:/g, "");
  const fill = (name: string) => `url(#${id}-${name})`;
  return (
    <svg className="auth-scene" viewBox="0 0 620 470" aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id={`${id}-platform`} x1="0" y1="0" x2="1" y2="1"><stop stopColor="#b798ff" /><stop offset="1" stopColor="#8261dc" /></linearGradient>
        <linearGradient id={`${id}-edge`} x2="0" y2="1"><stop stopColor="#8760d2" /><stop offset="1" stopColor="#6343ab" /></linearGradient>
        <linearGradient id={`${id}-white`} x1="0" y1="0" x2="1" y2="0.65"><stop stopColor="#fff" /><stop offset="0.48" stopColor="#fff9ff" /><stop offset="1" stopColor="#cbb8ec" /></linearGradient>
        <linearGradient id={`${id}-yellow`} x1="0" y1="0" x2="1" y2="0.4"><stop stopColor="#fff09b" /><stop offset="0.4" stopColor="#ffda63" /><stop offset="1" stopColor="#e9a53d" /></linearGradient>
        <linearGradient id={`${id}-mint`} x1="0" y1="0" x2="1" y2="1"><stop stopColor="#d1fff0" /><stop offset="1" stopColor="#66c6b2" /></linearGradient>
        <linearGradient id={`${id}-peach`} x1="0" y1="0" x2="1" y2="1"><stop stopColor="#ffcfad" /><stop offset="1" stopColor="#e99983" /></linearGradient>
        <radialGradient id={`${id}-orb`} cx="0.3" cy="0.25"><stop stopColor="#ffd5ae" /><stop offset="1" stopColor="#f0a353" /></radialGradient>
        <filter id={`${id}-shadow`} x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="12" /></filter>
      </defs>
      <ellipse cx="323" cy="406" rx="245" ry="35" fill="#925b9c" opacity="0.18" filter={fill("shadow")} />
      <path d="M54 291Q51 277 75 267L356 180Q374 174 395 182L583 286Q603 297 592 315L329 431Q306 440 284 431L69 332Q52 325 54 310Z" fill={fill("edge")} />
      <path d="M54 284Q51 272 75 263L356 174Q374 168 395 177L583 281Q606 293 584 304L326 420Q306 429 284 420L69 320Q54 313 54 301Z" fill={fill("platform")} />
      <path d="M83 298 300 399Q308 403 321 398L574 286" fill="none" stroke="#dbc4ff" opacity="0.6" />
      <g transform="translate(292 80) rotate(9)">
        <rect x="7" y="2" width="223" height="180" rx="21" fill="#9270c4" />
        <rect width="218" height="174" rx="20" fill={fill("white")} />
        <path d="M0 28V20Q0 0 20 0H198Q218 0 218 20V28" fill="#b497db" />
        <circle cx="17" cy="15" r="3.5" fill="#f8cfb5" /><circle cx="29" cy="15" r="3.5" fill="#ffe59b" /><circle cx="41" cy="15" r="3.5" fill="#b1efd5" />
        <rect x="29" y="52" width="159" height="34" rx="10" fill="#e1d5f1" /><rect x="29" y="99" width="159" height="34" rx="10" fill="#e1d5f1" />
        <circle cx="49" cy="69" r="5" fill="#76c4aa" /><circle cx="49" cy="116" r="5" fill="#aa88e1" />
        <path d="M70 69H131M70 116H151" stroke="#baa6d2" strokeWidth="6" strokeLinecap="round" />
        <path d="M163 65V73M173 65V73M163 112V120M173 112V120" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
      </g>
      <ellipse cx="207" cy="289" rx="46" ry="15" fill="#6442aa" opacity="0.3" filter={fill("shadow")} />
      <g className="auth-scene-friend">
        <path d="M165 244C149 232 142 213 145 181C149 142 169 122 197 129C226 136 223 169 224 195C224 221 238 217 255 203C261 236 244 264 217 272C194 279 176 266 165 244Z" fill={fill("yellow")} />
        <path d="M161 144 166 130 174 143M183 132 192 120 196 135" fill="#f7cf65" stroke="#ffe896" strokeWidth="2" />
        <ellipse cx="170" cy="178" rx="12" ry="16" fill="#fff8ee" /><ellipse cx="198" cy="182" rx="12" ry="16" fill="#fff8ee" />
        <ellipse cx="175" cy="181" rx="5" ry="7" fill="#423355" /><ellipse cx="203" cy="185" rx="5" ry="7" fill="#423355" />
        <path d="M180 206Q187 212 194 206" stroke="#ae793b" strokeWidth="3" strokeLinecap="round" fill="none" />
      </g>
      <ellipse cx="308" cy="326" rx="51" ry="16" fill="#6545ad" opacity="0.28" filter={fill("shadow")} />
      <g className="auth-scene-ghost">
        <path d="M266 315C272 295 264 282 266 251C268 220 281 198 303 198C330 198 344 221 344 251C344 278 349 301 361 322C348 333 334 321 324 327C312 336 304 322 293 326C280 332 270 326 266 315Z" fill={fill("white")} />
        <path d="M285 250Q281 271 288 288" stroke="#fff" strokeWidth="10" strokeLinecap="round" opacity="0.75" />
        <ellipse cx="293" cy="245" rx="5.5" ry="7.5" fill="#463458" /><ellipse cx="314" cy="245" rx="5.5" ry="7.5" fill="#463458" />
        <path d="M297 264Q303 272 310 263" stroke="#8c6e9b" strokeWidth="3" strokeLinecap="round" fill="none" />
        <ellipse cx="283" cy="259" rx="6" ry="3" fill="#f5d1e5" /><ellipse cx="323" cy="258" rx="6" ry="3" fill="#f5d1e5" />
      </g>
      <ellipse cx="468" cy="333" rx="60" ry="17" fill="#6545ad" opacity="0.2" filter={fill("shadow")} />
      <path d="M420 312V329Q420 350 469 350Q519 350 519 329V312" fill="#dba8b9" /><ellipse cx="469" cy="311" rx="49" ry="18" fill="#ffe8d6" />
      <path d="M439 288V311Q439 323 469 323Q500 323 500 311V288" fill={fill("peach")} /><ellipse cx="469" cy="288" rx="30" ry="12" fill="#f5c6d6" />
      <circle cx="472" cy="267" r="21" fill={fill("orb")} />
      <g transform="translate(103 279)"><path d="M0 0 21-9 42 1 21 12Z" fill="#ffcf9b" /><path d="M0 0V25L21 37V12Z" fill="#eda46a" /><path d="M21 12 42 1V26L21 37Z" fill="#cc8055" /></g>
      <g transform="translate(404 184) rotate(-18)"><path d="M0 0 21-9 42 1 21 12Z" fill="#defff1" /><path d="M0 0V25L21 37V12Z" fill={fill("mint")} /><path d="M21 12 42 1V26L21 37Z" fill="#72bda7" /></g>
      <path d="M203 345 218 338 234 345 218 353Z" fill="#fff4df" /><path d="M203 345V350L218 358 234 350V345L218 353Z" fill="#e0bdd9" />
      <path d="M370 373 383 367 397 374 383 381Z" fill="#ffdf7c" /><path d="M370 373V389L383 396V381Z" fill="#f4bd52" /><path d="M383 381 397 374V389L383 396Z" fill="#d6983c" />
      <circle cx="535" cy="171" r="17" fill={fill("orb")} /><circle cx="548" cy="365" r="9" fill={fill("white")} />
      <path d="M123 111H135M129 105V117M562 217H576M569 210V224" stroke="#fff5ff" strokeWidth="3" strokeLinecap="round" />
      <circle cx="249" cy="116" r="4" fill="#b7d9d2" /><circle cx="407" cy="408" r="4" fill="#ffdb9b" />
    </svg>
  );
}
