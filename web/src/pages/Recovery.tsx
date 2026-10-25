import { useState } from "react";
import { api } from "../api";
import { authErrorMessage } from "../lib/auth-errors";
import { Banner } from "../components/Modal";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { LoginLayout, SecretInput } from "./Login";

// Proofs stay in component memory, never in URLs or browser storage.
export function Recovery({ onBack }: { onBack: () => void }) {
  const [username, setUsername] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [challenge, setChallenge] = useState<{ question: string; totp_required: boolean } | null>(null);
  const [answer, setAnswer] = useState("");
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const mismatch = again !== "" && password !== again;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy || (challenge && password !== again)) return;
    setBusy(true);
    setError("");
    try {
      const proof = { username: username.trim(), recovery_code: recoveryCode.trim() };
      if (!challenge) {
        setChallenge(await api.recoveryQuestion(proof));
      } else {
        await api.resetForgottenPassword({ ...proof, answer, code, password });
        setAnswer("");
        setRecoveryCode("");
        setPassword("");
        setAgain("");
        setCode("");
        setDone(true);
      }
    } catch (err) {
      setError(authErrorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <LoginLayout alert={error}>
      <form className="auth-form" onSubmit={submit}>
        <h1>{done ? "密码已重置" : "找回密码"}</h1>
        {error && <Banner kind="err">{error}</Banner>}
        {done ? (
          <>
            <p className="auth-description">所有旧登录已退出，恢复码已失效。请用新密码登录，再到个人中心重新设置密码找回。</p>
            <button type="button" className="auth-submit" onClick={onBack}>返回登录<ArrowRight size={18} aria-hidden="true" /></button>
          </>
        ) : (
          <>
            {!challenge ? (
              <>
                <p className="auth-description">需要提前在个人中心设置安全问题并保存恢复码。未设置或遗失恢复码时，请通过服务器管理渠道恢复账号。</p>
                <label>用户名<input autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required maxLength={128} /></label>
                <label>一次性恢复码<input autoComplete="off" spellCheck={false} type="password" value={recoveryCode} onChange={(e) => setRecoveryCode(e.target.value)} required maxLength={128} /></label>
              </>
            ) : (
              <>
                <p className="auth-description">账号：{username}。安全问题答案区分大小写；恢复不会关闭两步验证。</p>
                <label>安全问题<input value={challenge.question} readOnly /></label>
                <label>安全问题答案<input type="password" autoComplete="off" value={answer} onChange={(e) => setAnswer(e.target.value)} required maxLength={256} /></label>
                {challenge.totp_required && <label>两步验证码<input autoComplete="one-time-code" inputMode="numeric" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required /></label>}
                <label>新密码<SecretInput autoComplete="new-password" minLength={12} value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
                <label>确认新密码<input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required /></label>
                {mismatch && <Banner kind="err">两次输入的新密码不一致</Banner>}
              </>
            )}
            <button className="auth-submit" disabled={busy || mismatch}>{busy ? "验证中…" : challenge ? "重置密码" : "验证恢复码"}<ArrowRight size={18} aria-hidden="true" /></button>
            <button type="button" className="auth-secondary" onClick={onBack} disabled={busy}><ArrowLeft size={14} aria-hidden="true" />返回登录</button>
          </>
        )}
      </form>
    </LoginLayout>
  );
}
