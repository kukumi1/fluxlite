import { useEffect, useState } from "react";
import { api, ApiError, type Account as AccountInfo, type Enrollment } from "../api";
import { Banner } from "../components/Modal";

interface Props {
  onUsernameChanged: (name: string) => void;
  onSignedOut: () => void;
}

export function Account({ onUsernameChanged, onSignedOut }: Props) {
  const [me, setMe] = useState<AccountInfo | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function load() {
    try {
      const account = await api.me();
      setMe(account);
      onUsernameChanged(account.username);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取账号信息失败");
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const report = (err: unknown) =>
    setError(err instanceof ApiError ? err.message : "操作失败");

  const announce = (msg: string) => {
    setError("");
    setNotice(msg);
  };

  if (!me) {
    return (
      <>
        <h1>个人中心</h1>
        {error && <Banner kind="err">{error}</Banner>}
        {!error && <p className="muted">加载中…</p>}
      </>
    );
  }

  return (
    <>
      <h1>个人中心</h1>
      <p className="muted">账号、密码与登录保护。</p>

      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="card">
        <h2 style={{ marginTop: 0 }}>账号</h2>
        <dl className="facts">
          <dt>用户名</dt>
          <dd>{me.username}</dd>
          <dt>两步验证</dt>
          <dd>
            {me.totp_enrolled ? (
              <span className="tag ok">已开启</span>
            ) : (
              <span className="tag warn">未开启</span>
            )}
          </dd>
          <dt>当前登录数</dt>
          <dd>{me.sessions}</dd>
          <dt>创建于</dt>
          <dd>{new Date(me.created_at).toLocaleString()}</dd>
          <dt>凭据上次变更</dt>
          <dd>{new Date(me.updated_at).toLocaleString()}</dd>
        </dl>
      </div>

      <UsernameCard
        current={me.username}
        onDone={async (msg) => {
          announce(msg);
          await load();
        }}
        onError={report}
      />

      <PasswordCard onDone={announce} onError={report} />

      <TwoFactorCard
        enrolled={me.totp_enrolled}
        onDone={async (msg) => {
          announce(msg);
          await load();
        }}
        onError={report}
      />

      <div className="card">
        <h2 style={{ marginTop: 0 }}>登录会话</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          踢掉除当前浏览器以外的所有登录。怀疑在别处登录过、或用完公共电脑后可以点这里。
          修改密码时会自动执行一次。
        </p>
        <button
          className="btn"
          onClick={async () => {
            try {
              await api.revokeSessions();
              announce("其他登录已全部退出");
              await load();
            } catch (err) {
              report(err);
            }
          }}
        >
          退出其他所有登录
        </button>
        <button
          className="btn danger"
          style={{ marginLeft: 8 }}
          onClick={async () => {
            try {
              await api.logout();
            } finally {
              onSignedOut();
            }
          }}
        >
          退出当前登录
        </button>
      </div>
    </>
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
    <form
      className="card"
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
      <h2 style={{ marginTop: 0 }}>修改用户名</h2>
      <div className="grid2">
        <label>
          新用户名
          <input value={next} onChange={(e) => setNext(e.target.value)} required minLength={3} />
        </label>
        <label>
          当前密码
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="current-password"
          />
        </label>
      </div>
      <p className="hint">改名需要密码：能改名的人也能把你锁在账号外面。</p>
      <button className="btn primary" disabled={busy || next === current}>
        {busy ? "保存中…" : "保存"}
      </button>
    </form>
  );
}

function PasswordCard({
  onDone,
  onError,
}: {
  onDone: (msg: string) => void;
  onError: (err: unknown) => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);

  const mismatch = again !== "" && next !== again;

  return (
    <form
      className="card"
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
      <h2 style={{ marginTop: 0 }}>修改密码</h2>
      <label>
        当前密码
        <input
          type="password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          required
          autoComplete="current-password"
        />
      </label>
      <div className="grid2">
        <label>
          新密码
          <input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            required
            minLength={12}
            autoComplete="new-password"
          />
        </label>
        <label>
          再输一次
          <input
            type="password"
            value={again}
            onChange={(e) => setAgain(e.target.value)}
            required
            autoComplete="new-password"
          />
        </label>
      </div>
      {mismatch && <Banner kind="err">两次输入的新密码不一致</Banner>}
      <p className="hint">至少 12 位。改完后其他浏览器上的登录会立刻失效，当前这个不受影响。</p>
      <button className="btn primary" disabled={busy || mismatch}>
        {busy ? "保存中…" : "修改密码"}
      </button>
    </form>
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

  if (enrolled) {
    return (
      <form
        className="card"
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
        <h2 style={{ marginTop: 0 }}>
          两步验证 <span className="tag ok">已开启</span>
        </h2>
        <p className="hint" style={{ marginTop: 0 }}>
          关掉之后，密码就是进入面板的唯一凭据 —— 而面板持有全部节点的 root 权限。
          关闭需要密码加一个当前验证码：光有一个被盗用的浏览器会话，不该能卸掉这道锁。
        </p>
        <div className="grid2">
          <label>
            当前密码
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              autoComplete="current-password"
            />
          </label>
          <label>
            当前验证码
            <input
              value={code}
              onChange={(e) => setCode(e.target.value)}
              required
              inputMode="numeric"
              placeholder="6 位数字"
            />
          </label>
        </div>
        <button className="btn danger" disabled={busy}>
          {busy ? "处理中…" : "关闭两步验证"}
        </button>
      </form>
    );
  }

  return (
    <div className="card">
      <h2 style={{ marginTop: 0 }}>
        两步验证 <span className="tag warn">未开启</span>
      </h2>
      {!pending ? (
        <>
          <p className="hint" style={{ marginTop: 0 }}>
            开启后登录需要密码加一个动态验证码。密码泄露时，这是唯一还挡在攻击者和全部节点
            root 权限之间的东西。
          </p>
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
        </>
      ) : (
        <form
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
          <p className="hint" style={{ marginTop: 0 }}>
            把下面的密钥加进 Authenticator，再输入它生成的验证码确认。确认之前不会生效，
            所以中途放弃也不会把你挡在门外。
          </p>
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
            <input
              value={code}
              onChange={(e) => setCode(e.target.value)}
              required
              inputMode="numeric"
              placeholder="6 位数字"
            />
          </label>
          <button className="btn primary" disabled={busy}>
            {busy ? "确认中…" : "确认开启"}
          </button>
          <button
            type="button"
            className="btn"
            style={{ marginLeft: 8 }}
            onClick={() => {
              setPending(null);
              setCode("");
            }}
          >
            取消
          </button>
        </form>
      )}
    </div>
  );
}
