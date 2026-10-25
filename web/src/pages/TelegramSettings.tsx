import { useEffect, useState } from "react";
import { RefreshCw, Send } from "lucide-react";
import { api, type TelegramSettings as Settings } from "../api";
import { Banner } from "../components/Modal";
import { SettingSection } from "../components/SettingSection";
import { safeBindingURL } from "../lib/telegram";

export function TelegramSettings({ enrolled }: { enrolled: boolean }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [panelURL, setPanelURL] = useState("");
  const [token, setToken] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [binding, setBinding] = useState<{ url: string; expires_at: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [retry, setRetry] = useState(0);
  const proofReady = password !== "" && (!enrolled || /^\d{6}$/.test(code));
  const bindingURL = binding ? safeBindingURL(binding.url) : null;
  useEffect(() => {
    let active = true;
    api.telegramSettings().then((value) => {
      if (!active) return;
      setSettings(value); setEnabled(value.enabled); setPanelURL(value.panel_url); setError("");
    }).catch((err: unknown) => {
      if (active) setError(err instanceof Error ? err.message : "读取 Telegram 设置失败");
    });
    return () => { active = false; };
  }, [retry]);
  async function run(action: "save" | "bind" | "unbind") {
    if (busy || !proofReady) return;
    if (action === "unbind" && !window.confirm("解除绑定会撤销此 Telegram 账号的 Bot 和内嵌面板访问权限。确定继续？")) return;
    setBusy(true); setError(""); setNotice(""); setBinding(null);
    try {
      if (action === "save") {
        const url = new URL(panelURL);
        if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash) throw new Error("请输入无账号信息、查询参数和片段的 HTTPS 面板地址");
        await api.configureTelegram({ enabled, token: token.trim() || undefined, panel_url: panelURL.trim(), password, code });
        setToken(""); setNotice("Telegram 设置已保存");
      } else if (action === "bind") {
        const result = await api.bindTelegram(password, code);
        if (!safeBindingURL(result.url)) throw new Error("服务器返回了无效的绑定地址");
        setBinding(result); setNotice("用自己的 Telegram 账号打开链接，绑定后刷新状态。");
      } else {
        await api.unbindTelegram(password, code); setNotice("已解除 Telegram 绑定");
      }
      setPassword(""); setCode(""); setRetry((value) => value + 1);
    } catch (err) { setError(err instanceof Error ? err.message : "操作失败，请稍后重试"); }
    finally { setBusy(false); }
  }
  const summary = !settings
    ? error ? "读取设置失败" : "读取中…"
    : settings.bound_user_id ? `已绑定${settings.bound_username ? ` @${settings.bound_username}` : ` ${settings.bound_user_id}`}，可在 Telegram 里管理面板` : "用 Bot 接收通知，并在 Telegram 里打开面板";
  const status = settings && (settings.enabled && settings.configured
    ? <span className="pill ok"><i />{settings.status || "已开启"}</span>
    : <span className="pill"><i />{settings.configured ? "已关闭" : "未配置"}</span>);
  return <SettingSection icon={Send} tone="sky" title="Telegram Bot" summary={summary} status={status}>
    {error && <Banner kind="err">{error}</Banner>}{notice && <Banner kind="ok">{notice}</Banner>}
    {!settings ? <button className="btn" onClick={() => setRetry((value) => value + 1)}>重新读取设置</button> : <>
      <dl className="leader-rows single telegram-facts"><div><dt>连接状态</dt><dd>{settings.status || (settings.configured ? "已配置" : "未配置")}</dd></div><div><dt>Bot 用户名</dt><dd>{settings.bot_username ? `@${settings.bot_username}` : "—"}</dd></div><div><dt>绑定管理员</dt><dd>{settings.bound_user_id ? `${settings.bound_username ? `@${settings.bound_username} · ` : ""}${settings.bound_user_id}` : "尚未绑定"}</dd></div></dl>
      <form className="dialog-form setting-form" autoComplete="off" onSubmit={(event) => { event.preventDefault(); void run("save"); }}>
        <div className="switch-row"><div><strong>启用 Telegram Bot</strong><span>更改后点击「保存 Bot 设置」生效</span></div><button type="button" role="switch" aria-label="启用 Telegram Bot" aria-checked={enabled} disabled={busy} className={`toggle-switch${enabled ? " on" : ""}`} onClick={() => setEnabled((value) => !value)}><span /></button></div>
        <label>Bot Token<input type="password" name="telegram-bot-token" autoComplete="new-password" value={token} onChange={(event) => setToken(event.target.value)} placeholder={settings.configured ? "已保存；留空保留原 Token" : "从 BotFather 获取 Token"} required={!settings.configured && enabled} /></label>
        <p className="field-hint flush">Token 不回显；绑定链接请勿转发。</p>
        <label>面板 HTTPS 地址<input type="url" value={panelURL} onChange={(event) => setPanelURL(event.target.value)} placeholder="https://panel.example.com" required /></label>
        <div className="grid2"><label>当前管理员密码<input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>{enrolled && <label>两步验证码<input className="otp-input" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(event) => setCode(event.target.value)} required /></label>}</div>
        <p className="field-hint flush">保存、绑定和解绑都需要重新输入密码。</p>
        <div className="setting-actions telegram-settings-actions"><button className="btn primary" disabled={busy || !proofReady}>{busy ? "处理中…" : "保存 Bot 设置"}</button><button type="button" className="btn" disabled={busy || !proofReady || !settings.enabled || !settings.configured} onClick={() => void run("bind")}>生成一次性绑定链接</button><button type="button" className="btn danger" disabled={busy || !proofReady || !settings.bound_user_id} onClick={() => void run("unbind")}>解除绑定</button><button type="button" className="btn ghost" disabled={busy} onClick={() => setRetry((value) => value + 1)}><RefreshCw size={14} aria-hidden="true" />刷新状态</button></div>
      </form>
      {binding && bindingURL && <div className="telegram-binding" role="status"><p>有效期至：{new Date(binding.expires_at).toLocaleString()}</p><a className="btn" href={bindingURL} target="_blank" rel="noopener noreferrer">打开 Telegram 完成绑定</a><button className="btn" onClick={() => { void navigator.clipboard.writeText(bindingURL).then(() => setNotice("绑定链接已复制，请勿转发"), () => setError("复制失败，请使用打开 Telegram 按钮")); }}>复制绑定链接</button></div>}
    </>}
  </SettingSection>;
}
