import { useEffect, useRef, useState } from "react";
import { Camera, KeyRound, LogOut, MonitorSmartphone, ShieldCheck, ShieldAlert, UserRound } from "lucide-react";
import { api, ApiError, type Account as AccountInfo, type Enrollment } from "../api";
import { Banner } from "../components/Modal";
import { PageHeader } from "../components/PageHeader";
import { SettingSection } from "../components/SettingSection";
import { AVATAR_SIZE, toAvatarPNG } from "../lib/avatar";
import { TelegramSettings } from "./TelegramSettings";
import { RecoverySettings } from "./RecoverySettings";
import { authErrorMessage } from "../lib/auth-errors";
import "./management-redesign.css";

interface Props {
  onUsernameChanged: (name: string) => void;
  onAvatarChanged: (avatar: string) => void;
  onSignedOut: () => void;
}

const DAY_MS = 86_400_000;

function sinceLabel(iso: string): string {
  const days = Math.floor((Date.now() - new Date(iso).getTime()) / DAY_MS);
  if (days <= 0) return "今天";
  if (days === 1) return "昨天";
  return `${days} 天前`;
}

export function Account({ onUsernameChanged, onAvatarChanged, onSignedOut }: Props) {
  const [me, setMe] = useState<AccountInfo | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function load() {
    try {
      const account = await api.me();
      setMe(account);
      onUsernameChanged(account.username);
      onAvatarChanged(account.avatar);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取账号信息失败");
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const report = (err: unknown) => {
    setNotice("");
    setError(authErrorMessage(err));
  };

  const announce = (msg: string) => {
    setError("");
    setNotice(msg);
  };

  const reload = async (msg: string) => {
    announce(msg);
    await load();
  };

  if (!me) {
    return (
      <>
        <PageHeader title="个人中心" />
        {error && <Banner kind="err">{error}</Banner>}
        {!error && <p className="muted">加载中…</p>}
      </>
    );
  }

  return (
    <div className="management-page account-page">
      <PageHeader title="个人中心" desc="账号资料、登录安全和 Telegram Bot。修改任何一项都需要重新输入当前密码。" />

      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="account-layout">
        <ProfileCard me={me} onDone={reload} onError={report} onSignedOut={onSignedOut} />

        <div className="account-settings">
          <h2 className="account-group">登录凭据</h2>
          <UsernameCard current={me.username} onDone={reload} onError={report} />
          <PasswordCard changedAt={me.updated_at} onDone={announce} onError={report} />

          <h2 className="account-group">安全</h2>
          <TwoFactorCard enrolled={me.totp_enrolled} onDone={reload} onError={report} />
          <RecoverySettings enrolled={me.totp_enrolled} onDone={announce} onError={report} />

          <h2 className="account-group">集成</h2>
          <TelegramSettings enrolled={me.totp_enrolled} />
        </div>
      </div>
    </div>
  );
}

function ProfileCard({
  me,
  onDone,
  onError,
  onSignedOut,
}: {
  me: AccountInfo;
  onDone: (msg: string) => Promise<void>;
  onError: (err: unknown) => void;
  onSignedOut: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const others = Math.max(0, me.sessions - 1);

  async function act(task: () => Promise<unknown>, msg: string) {
    setBusy(true);
    try {
      await task();
      await onDone(msg);
    } catch (err) {
      // 「不是图片」「canvas 不可用」这类失败来自浏览器而非接口，一样要说出来，
      // 否则点了没反应会被当成卡住。
      onError(err instanceof Error && !(err instanceof ApiError) ? err.message : err);
    } finally {
      setBusy(false);
    }
  }

  async function pick(file: File | undefined) {
    if (!file) return;
    await act(async () => api.setAvatar(await toAvatarPNG(file)), "头像已更新");
    if (input.current) input.current.value = "";
  }

  return (
    <aside className="account-profile">
      <div className="account-cover" aria-hidden />
      <div className="account-avatar">
        {me.avatar ? (
          <img className="avatar-preview" src={me.avatar} alt="当前头像" />
        ) : (
          <span className="avatar-preview avatar-fallback">{me.username.slice(0, 1).toUpperCase()}</span>
        )}
        <button
          type="button"
          className="account-avatar-edit"
          disabled={busy}
          onClick={() => input.current?.click()}
          aria-label={me.avatar ? "更换头像" : "上传头像"}
          title={`居中裁剪为 ${AVATAR_SIZE}px 方形，并移除 EXIF 信息`}
        >
          <Camera size={15} />
        </button>
        <input ref={input} type="file" accept="image/*" hidden onChange={(e) => void pick(e.target.files?.[0])} />
      </div>

      <h2 className="account-name-big">{me.username}</h2>
      <span className="pill account-role-pill">管理员</span>
      {me.avatar && (
        <button type="button" className="account-avatar-clear" disabled={busy} onClick={() => void act(() => api.clearAvatar(), "头像已移除，恢复为首字母")}>
          移除头像
        </button>
      )}

      <div className={`account-shield ${me.totp_enrolled ? "tone-lime" : "warn"}`}>
        {me.totp_enrolled ? <ShieldCheck size={18} /> : <ShieldAlert size={18} />}
        <span>
          <strong>{me.totp_enrolled ? "两步验证已开启" : "两步验证未开启"}</strong>
          <small>{me.totp_enrolled ? "登录需要密码和验证码" : "只凭密码就能登录，建议开启"}</small>
        </span>
      </div>

      <dl className="leader-rows single account-facts">
        <div><dt>当前登录</dt><dd>{me.sessions} 处</dd></div>
        <div><dt>创建于</dt><dd>{new Date(me.created_at).toLocaleDateString("zh-CN")}</dd></div>
        <div><dt>凭据上次变更</dt><dd title={new Date(me.updated_at).toLocaleString("zh-CN")}>{sinceLabel(me.updated_at)}</dd></div>
      </dl>

      <div className="account-sessions">
        <button className="btn" disabled={busy || others === 0} onClick={() => void act(() => api.revokeSessions(), "其他登录已全部退出")}>
          <MonitorSmartphone size={15} />
          {others === 0 ? "没有其他登录" : `退出其他 ${others} 处登录`}
        </button>
        <button
          className="btn danger"
          onClick={async () => {
            try {
              await api.logout();
            } finally {
              onSignedOut();
            }
          }}
        >
          <LogOut size={15} />
          退出当前登录
        </button>
      </div>
    </aside>
  );
}

function UsernameCard({
  current,
  onDone,
  onError,
}: {
  current: string;
  onDone: (msg: string) => Promise<void>;
  onError: (err: unknown) => void;
}) {
  const [next, setNext] = useState(current);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <SettingSection icon={UserRound} tone="sky" title="用户名" summary={<>当前为 <b>{current}</b>，登录时使用</>}>
      <form
        className="dialog-form setting-form"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await api.changeUsername(password, next);
            setPassword("");
            await onDone(`用户名已改为 ${next}`);
          } catch (err) {
            onError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="grid2">
          <label>
            新用户名
            <input value={next} onChange={(e) => setNext(e.target.value)} required minLength={3} />
          </label>
          <label>
            当前密码
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="current-password" />
          </label>
        </div>
        <div className="setting-actions">
          <button className="btn primary" disabled={busy || next === current}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </SettingSection>
  );
}

function PasswordCard({
  changedAt,
  onDone,
  onError,
}: {
  changedAt: string;
  onDone: (msg: string) => void;
  onError: (err: unknown) => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);

  const mismatch = again !== "" && next !== again;

  return (
    <SettingSection icon={KeyRound} tone="lilac" title="密码" summary={`凭据上次变更于${sinceLabel(changedAt)}`}>
      <form
        className="dialog-form setting-form"
        onSubmit={async (e) => {
          e.preventDefault();
          if (mismatch) return;
          setBusy(true);
          try {
            await api.changePassword(current, next);
            setCurrent("");
            setNext("");
            setAgain("");
            onDone("密码已修改，其他浏览器上的登录已全部失效");
          } catch (err) {
            onError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          当前密码
          <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} required autoComplete="current-password" />
        </label>
        <div className="grid2">
          <label>
            新密码
            <input type="password" value={next} onChange={(e) => setNext(e.target.value)} required minLength={12} autoComplete="new-password" />
          </label>
          <label>
            再输一次
            <input type="password" value={again} onChange={(e) => setAgain(e.target.value)} required autoComplete="new-password" />
          </label>
        </div>
        {mismatch && <Banner kind="err">两次输入的新密码不一致</Banner>}
        <p className="field-hint flush">至少 12 位；修改后其他登录立即失效，当前会话保留。</p>
        <div className="setting-actions">
          <button className="btn primary" disabled={busy || mismatch}>
            {busy ? "保存中…" : "修改密码"}
          </button>
        </div>
      </form>
    </SettingSection>
  );
}

function TwoFactorCard({
  enrolled,
  onDone,
  onError,
}: {
  enrolled: boolean;
  onDone: (msg: string) => Promise<void>;
  onError: (err: unknown) => void;
}) {
  const [pending, setPending] = useState<Enrollment | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  const status = enrolled ? <span className="pill ok"><i />已开启</span> : <span className="pill warn"><i />未开启</span>;

  if (enrolled) {
    return (
      <SettingSection icon={ShieldCheck} tone="lime" title="两步验证" summary="登录时需要密码和验证器里的 6 位验证码" status={status}>
        <form
          className="dialog-form setting-form"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            try {
              await api.disableTOTP(password, code);
              setPassword("");
              setCode("");
              await onDone("两步验证已关闭，之后只需密码即可登录");
            } catch (err) {
              onError(err);
            } finally {
              setBusy(false);
            }
          }}
        >
          <p className="setting-note">关闭后仅凭密码即可登录。需输入密码和当前验证码。</p>
          <div className="grid2">
            <label>
              当前密码
              <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="current-password" />
            </label>
            <label>
              当前验证码
              <input className="otp-input" value={code} onChange={(e) => setCode(e.target.value)} required inputMode="numeric" placeholder="6 位数字" />
            </label>
          </div>
          <div className="setting-actions">
            <button className="btn danger" disabled={busy}>
              {busy ? "处理中…" : "关闭两步验证"}
            </button>
          </div>
        </form>
      </SettingSection>
    );
  }

  return (
    <SettingSection icon={ShieldAlert} tone="lime" title="两步验证" summary="开启后，登录需输入密码和动态验证码" status={status} defaultOpen>
      {!pending ? (
        <div className="setting-actions">
          <button
            className="btn primary"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                setPending(await api.beginTOTP());
              } catch (err) {
                onError(err);
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy ? "生成中…" : "开启两步验证"}
          </button>
        </div>
      ) : (
        <form
          className="dialog-form setting-form"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            try {
              await api.enableTOTP(code);
              setPending(null);
              setCode("");
              await onDone("两步验证已开启");
            } catch (err) {
              onError(err);
            } finally {
              setBusy(false);
            }
          }}
        >
          <p className="setting-note">将密钥添加至验证器，输入验证码后生效。</p>
          <label>
            密钥
            <input className="mono" value={pending.secret} readOnly onFocus={(e) => e.target.select()} />
          </label>
          <label>
            otpauth 链接
            <input className="mono" value={pending.url} readOnly onFocus={(e) => e.target.select()} />
          </label>
          <label>
            验证码
            <input className="otp-input" value={code} onChange={(e) => setCode(e.target.value)} required inputMode="numeric" placeholder="6 位数字" />
          </label>
          <div className="setting-actions">
            <button className="btn primary" disabled={busy}>
              {busy ? "确认中…" : "确认开启"}
            </button>
            <button
              type="button"
              className="btn"
              onClick={() => {
                setPending(null);
                setCode("");
              }}
            >
              取消
            </button>
          </div>
        </form>
      )}
    </SettingSection>
  );
}
