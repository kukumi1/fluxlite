import { useEffect, useState } from "react";
import { api, type RecoverySettings as Settings } from "../api";
import { LifeBuoy } from "lucide-react";
import { Banner } from "../components/Modal";
import { SettingSection } from "../components/SettingSection";

export function RecoverySettings({ enrolled, onDone, onError }: {
  enrolled: boolean;
  onDone: (message: string) => void;
  onError: (error: unknown) => void;
}) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [again, setAgain] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [retry, setRetry] = useState(0);
  const mismatch = again !== "" && answer !== again;

  useEffect(() => {
    let active = true;
    setLoadFailed(false);
    api.recoverySettings().then((value) => {
      if (!active) return;
      setSettings(value);
      setQuestion(value.question);
    }).catch((err: unknown) => {
      if (!active) return;
      setLoadFailed(true);
      onError(err);
    });
    return () => { active = false; };
  }, [retry]);

  function clearProofs() {
    setAnswer("");
    setAgain("");
    setPassword("");
    setCode("");
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (busy || answer !== again) return;
    setBusy(true);
    // A failed rotation must not leave a stale code presented as current.
    setRecoveryCode("");
    try {
      const result = await api.configureRecovery({ question: question.trim(), answer, password, code });
      setSettings({ enabled: true, question: question.trim() });
      setRecoveryCode(result.recovery_code);
      clearProofs();
      onDone("密码找回已设置。请立即保存新恢复码，旧恢复码已失效。");
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  async function disable() {
    if (busy || !password || (enrolled && !/^[0-9]{6}$/.test(code))) return;
    if (!window.confirm("关闭后，安全问题和恢复码将失效。确定关闭密码找回？")) return;
    setBusy(true);
    try {
      await api.disableRecovery(password, code);
      setSettings({ enabled: false, question: "" });
      setQuestion("");
      setRecoveryCode("");
      clearProofs();
      onDone("密码找回已关闭，原恢复码已失效。");
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  const status = !settings ? undefined : settings.enabled ? <span className="pill ok"><i />已设置</span> : <span className="pill warn"><i />未设置</span>;

  return (
    <SettingSection icon={LifeBuoy} tone="sun" title="密码找回" summary="忘记密码时，用安全问题和一次性恢复码重置" status={status}>
      <form className="dialog-form setting-form" onSubmit={save}>
        <p className="setting-note">已开启两步验证时仍需验证码。答案请使用难以猜中的私密长句，不要填写生日、姓名等公开信息。</p>
        {!settings ? (
          loadFailed ? <button type="button" className="btn" onClick={() => setRetry((value) => value + 1)}>重新读取</button> : <p className="muted">正在读取设置…</p>
        ) : (
          <>
            {recoveryCode && <div className="recovery-reveal" role="status">
              <Banner kind="ok">恢复码只展示这一次。请离线保存；不要发送给他人。使用后需重新设置。</Banner>
              <label>新恢复码<input className="mono" value={recoveryCode} readOnly autoComplete="off" spellCheck={false} onFocus={(e) => e.currentTarget.select()} /></label>
              <button type="button" className="btn" onClick={() => setRecoveryCode("")}>已保存，隐藏恢复码</button>
            </div>}
            <label>安全问题<input value={question} onChange={(e) => setQuestion(e.target.value)} minLength={3} maxLength={120} placeholder="输入只有自己知道答案的问题" required /></label>
            <div className="grid2">
              <label>安全问题答案<input type="password" autoComplete="off" value={answer} onChange={(e) => setAnswer(e.target.value)} minLength={4} maxLength={256} required /></label>
              <label>确认答案<input type="password" autoComplete="off" value={again} onChange={(e) => setAgain(e.target.value)} required maxLength={256} /></label>
            </div>
            {mismatch && <Banner kind="err">两次输入的答案不一致</Banner>}
            <div className="grid2">
              <label>当前密码<input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
              {enrolled && <label>两步验证码<input className="otp-input" autoComplete="one-time-code" inputMode="numeric" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required /></label>}
            </div>
            <p className="field-hint flush">答案去除首尾空格后区分大小写。重新设置会替换问题、答案和恢复码；恢复成功后所有登录会话退出。恢复码不会保存到当前浏览器。</p>
            <div className="setting-actions">
              <button className="btn primary" disabled={busy || mismatch}>{busy ? "处理中…" : settings.enabled ? "更新问题并生成新恢复码" : "设置并生成恢复码"}</button>
              {settings.enabled && <button type="button" className="btn danger" disabled={busy || !password || (enrolled && !/^[0-9]{6}$/.test(code))} onClick={() => void disable()}>关闭密码找回</button>}
            </div>
          </>
        )}
      </form>
    </SettingSection>
  );
}
