import { useEffect, useRef, useState } from "react";
import { Play, Plus, ShieldCheck, TerminalSquare, Trash2, X } from "lucide-react";
import { api, ApiError, type ConsoleCommand, type Node } from "../api";
import { Banner, Modal } from "../components/Modal";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { useConfirm } from "../components/ConfirmDialog";
import {
  TerminalPane,
  type ConnectionState,
  type TerminalHandle,
} from "../components/TerminalPane";
import { useTheme } from "../theme";

interface Tab {
  key: string;
  nodeID: number;
  name: string;
}

export function Console({ initialNode }: { initialNode?: number | null }) {
  const { theme } = useTheme();
  const [unlocked, setUnlocked] = useState<boolean | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [active, setActive] = useState("");
  const [states, setStates] = useState<Record<string, ConnectionState>>({});
  const [commands, setCommands] = useState<ConsoleCommand[]>([]);
  const [picked, setPicked] = useState<number | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const [confirm, confirmDialog] = useConfirm();

  const handles = useRef(new Map<string, TerminalHandle>());
  const counter = useRef(0);
  const openedInitial = useRef(false);

  const fail = (err: unknown) => setError(err instanceof ApiError ? err.message : "请求失败");

  useEffect(() => {
    api
      .consoleStatus()
      .then((s) => setUnlocked(s.unlocked))
      .catch(() => setUnlocked(false));
    api
      .listNodes()
      .then((list) => setNodes(list ?? []))
      .catch(fail);
  }, []);

  useEffect(() => {
    if (!unlocked) return;
    api
      .consoleCommands()
      .then((list) => setCommands(list ?? []))
      .catch(fail);
  }, [unlocked]);

  // 从节点页点「终端」进来时直接开好那台。要等节点列表到位才知道它叫什么，
  // 也要等解锁通过，所以放在这里而不是挂载时。
  useEffect(() => {
    if (openedInitial.current) return;
    if (!initialNode || !unlocked || nodes.length === 0) return;
    openedInitial.current = true;
    openTab(initialNode);
    setPicked(initialNode);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialNode, unlocked, nodes]);

  // 切回一个标签时必须重算尺寸：它被隐藏期间容器宽高是 0，xterm 记下的行列
  // 数是错的，不重算就会看到折行错位。
  useEffect(() => {
    if (!active) return;
    const handle = handles.current.get(active);
    const timer = setTimeout(() => {
      handle?.refit();
      handle?.focus();
    }, 0);
    return () => clearTimeout(timer);
  }, [active]);

  function openTab(nodeID: number) {
    const node = nodes.find((n) => n.id === nodeID);
    if (!node) return;
    counter.current += 1;
    const key = `t${counter.current}`;
    setTabs((current) => [...current, { key, nodeID, name: node.name }]);
    setActive(key);
  }

  function closeTab(key: string) {
    handles.current.delete(key);
    setTabs((current) => {
      const next = current.filter((t) => t.key !== key);
      setActive((currentActive) => {
        if (currentActive !== key) return currentActive;
        return next.length > 0 ? next[next.length - 1].key : "";
      });
      return next;
    });
  }

  function runCommand(command: string) {
    const handle = handles.current.get(active);
    if (!handle) return;
    handle.send(command.endsWith("\n") ? command : command + "\n");
    handle.focus();
  }

  if (unlocked === null) {
    return (
      <>
        <PageHeader title="终端" />
        <p className="muted">读取中…</p>
      </>
    );
  }

  if (!unlocked) {
    return (
      <>
        <PageHeader title="终端" desc="在浏览器里直接连接节点的 SSH。" />
        <UnlockGate onUnlocked={() => setUnlocked(true)} />
      </>
    );
  }

  const activeState = states[active];

  return (
    <>
      <PageHeader
        title="终端"
        desc="在浏览器里直接连接节点的 SSH。凭据全程留在服务端。"
        actions={
          <>
            <select
              value={picked ?? ""}
              style={{ width: 200, marginTop: 0 }}
              onChange={(e) => setPicked(e.target.value ? Number(e.target.value) : null)}
            >
              <option value="">选择节点…</option>
              {nodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </select>
            <button
              className="btn primary"
              disabled={picked === null}
              onClick={() => picked !== null && openTab(picked)}
            >
              <Plus size={15} />
              新建终端
            </button>
          </>
        }
      />

      {error && <Banner kind="err">{error}</Banner>}

      {tabs.length === 0 ? (
        <Card>
          <EmptyState
            icon={<TerminalSquare size={22} />}
            title="还没有打开的终端"
            desc="从右上角选一台节点，新建一个终端。可以同时开多台，标签之间互不影响。"
          />
        </Card>
      ) : (
        <div className="console-layout">
          <Card className="flush console-main">
            <div className="console-tabs">
              {tabs.map((tab) => (
                <div
                  key={tab.key}
                  className={`console-tab ${tab.key === active ? "active" : ""}`}
                  onClick={() => setActive(tab.key)}
                >
                  <span className={`console-dot ${states[tab.key] ?? "connecting"}`} />
                  {tab.name}
                  <button
                    className="console-tab-close"
                    title="关闭"
                    onClick={(e) => {
                      e.stopPropagation();
                      closeTab(tab.key);
                    }}
                  >
                    <X size={12} />
                  </button>
                </div>
              ))}
              <span className="console-status muted">
                {activeState === "open"
                  ? "已连接"
                  : activeState === "connecting"
                    ? "连接中…"
                    : activeState === "closed"
                      ? "已断开"
                      : ""}
              </span>
            </div>

            {/* 非当前标签只是藏起来，不卸载 —— 卸载等于把那条 SSH 会话掐了。 */}
            {tabs.map((tab) => (
              <div
                key={tab.key}
                className="console-screen"
                style={{ display: tab.key === active ? "block" : "none" }}
              >
                <TerminalPane
                  nodeID={tab.nodeID}
                  theme={theme}
                  ref={(handle) => {
                    if (handle) handles.current.set(tab.key, handle);
                    else handles.current.delete(tab.key);
                  }}
                  onStateChange={(state) =>
                    setStates((current) => ({ ...current, [tab.key]: state }))
                  }
                />
              </div>
            ))}
          </Card>

          <Card className="console-side">
            <div className="spread">
              <h2 style={{ margin: 0 }}>快捷命令</h2>
              <button className="btn sm" onClick={() => setCreating(true)}>
                <Plus size={13} />
                新建
              </button>
            </div>
            <p className="hint" style={{ marginTop: 0 }}>
              保存常用命令，一键送进当前终端并执行。
            </p>

            {commands.length === 0 ? (
              <div className="muted" style={{ fontSize: 13, padding: "18px 0" }}>
                还没有快捷命令。
              </div>
            ) : (
              <div className="command-list">
                {commands.map((c) => (
                  <div key={c.id} className="command-item">
                    <div style={{ minWidth: 0 }}>
                      <div className="command-name">{c.name}</div>
                      <div className="command-body mono">{c.command}</div>
                    </div>
                    <div className="row nowrap" style={{ gap: 4 }}>
                      <button
                        className="icon-btn"
                        title={active ? "在当前终端执行" : "先打开一个终端"}
                        disabled={!active}
                        onClick={() => runCommand(c.command)}
                      >
                        <Play size={14} />
                      </button>
                      <button
                        className="icon-btn"
                        title="删除"
                        onClick={() => {
                          void (async () => {
                            const ok = await confirm({
                              title: `删除快捷命令「${c.name}」？`,
                              confirmLabel: "删除",
                              danger: true,
                            });
                            if (!ok) return;
                            try {
                              await api.deleteConsoleCommand(c.id);
                              setCommands((list) => list.filter((x) => x.id !== c.id));
                            } catch (err) {
                              fail(err);
                            }
                          })();
                        }}
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
            <p className="hint" style={{ marginBottom: 0 }}>
              执行会直接把命令发给当前激活的终端，立即生效。
            </p>
          </Card>
        </div>
      )}

      {creating && (
        <CommandForm
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCommands((list) => [...list, c].sort((a, b) => a.name.localeCompare(b.name)));
            setCreating(false);
          }}
          onError={fail}
        />
      )}

      {confirmDialog}
    </>
  );
}

// UnlockGate 要求在已登录的会话上再证明一次身份。
//
// 会话足够读面板、改转发，但不足以换来八台机器的 root shell —— 一个被偷走的
// cookie 应该止步于前者。
function UnlockGate({ onUnlocked }: { onUnlocked: () => void }) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.consoleUnlock(password, code);
      onUnlocked();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "请求失败");
    } finally {
      setBusy(false);
      setPassword("");
      setCode("");
    }
  }

  return (
    <Card style={{ maxWidth: 460 }}>
      <div className="row" style={{ gap: 8, marginBottom: 6 }}>
        <ShieldCheck size={18} style={{ color: "var(--warn)" }} />
        <h2 style={{ margin: 0 }}>先验证身份</h2>
      </div>
      <p className="hint">
        终端给的是节点上的 root shell。本次浏览器会话里只需验证一次，之后开终端不再询问。
      </p>
      {error && <Banner kind="err">{error}</Banner>}
      <form onSubmit={submit}>
        <label>
          登录密码
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoFocus
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
          {busy ? "验证中…" : "解锁终端"}
        </button>
      </form>
    </Card>
  );
}

function CommandForm({
  onClose,
  onSaved,
  onError,
}: {
  onClose: () => void;
  onSaved: (c: ConsoleCommand) => void;
  onError: (err: unknown) => void;
}) {
  const [name, setName] = useState("");
  const [command, setCommand] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      onSaved(await api.createConsoleCommand(name, command));
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title="新建快捷命令" onClose={onClose}>
      <form onSubmit={submit}>
        <label>
          名称
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus required />
        </label>
        <label>
          命令
          <textarea
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            rows={3}
            required
            style={{ fontFamily: 'ui-monospace, "Cascadia Code", Consolas, monospace' }}
          />
        </label>
        <p className="hint">执行时会原样发送并自动回车，请确认它在目标机器上是你想要的效果。</p>
        <button className="btn primary" disabled={busy} style={{ width: "100%" }}>
          {busy ? "保存中…" : "保存"}
        </button>
      </form>
    </Modal>
  );
}
