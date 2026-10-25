import { useEffect, useRef, useState } from "react";
import { LockKeyhole, Play, Plus, TerminalSquare, Trash2, X, Zap } from "lucide-react";
import { api, ApiError, type ConsoleCommand, type Node } from "../api";
import { Banner, Modal } from "../components/Modal";
import { Flag } from "../components/Flag";
import { PageHeader } from "../components/PageHeader";
import { useConfirm } from "../components/ConfirmDialog";
import {
  TerminalPane,
  type ConnectionState,
  type TerminalHandle,
} from "../components/TerminalPane";
import { regionOf, toneOf } from "../lib/region";
import "./management-redesign.css";

const STATE_LABEL: Record<ConnectionState, string> = { open: "已连接", connecting: "连接中…", closed: "已断开" };

function MachineFlag({ name }: { name: string }) {
  const code = regionOf(name);
  return code ? <Flag code={code} className="chip-flag" /> : <TerminalSquare size={14} />;
}

interface Tab {
  key: string;
  nodeID: number;
  name: string;
}

export function Console({ initialNode }: { initialNode?: number | null }) {
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
        <PageHeader title="终端" desc="在浏览器里直接连接机器的 SSH。" />
        <UnlockGate onUnlocked={() => setUnlocked(true)} />
      </>
    );
  }

  const activeTab = tabs.find((tab) => tab.key === active);
  const activeState = states[active] ?? "connecting";
  const activeNode = activeTab ? nodes.find((n) => n.id === activeTab.nodeID) : undefined;

  const launcher = (
    <div className="picker-pill">
      <select
        aria-label="选择机器"
        value={picked ?? ""}
        onChange={(e) => setPicked(e.target.value ? Number(e.target.value) : null)}
      >
        <option value="">选择机器…</option>
        {nodes.map((n) => (
          <option key={n.id} value={n.id}>
            {n.name}
          </option>
        ))}
      </select>
      <button className="btn" disabled={picked === null} onClick={() => picked !== null && openTab(picked)}>
        <Plus size={15} />
        新建终端
      </button>
    </div>
  );

  return (
    <div className="management-page console-management">
      <PageHeader title="终端" desc="在浏览器里直接连接机器的 SSH。凭据全程留在服务端。" actions={tabs.length > 0 ? launcher : undefined} />

      {error && <Banner kind="err">{error}</Banner>}

      {tabs.length === 0 ? (
        <section className="console-launch">
          <div className="console-launch-head">
            <span className="console-launch-icon"><TerminalSquare size={20} /></span>
            <div>
              <h2>开一个终端</h2>
              <p>点一台机器就连上。可以同时开多台，标签之间互不影响。</p>
            </div>
          </div>
          <div className="console-launch-grid">
            {nodes.map((n) => (
              <button
                key={n.id}
                type="button"
                className={`console-launch-item tone-${toneOf(regionOf(n.name))}`}
                onClick={() => {
                  setPicked(n.id);
                  openTab(n.id);
                }}
              >
                <span className="console-launch-flag"><MachineFlag name={n.name} /></span>
                <span className="console-launch-text">
                  <strong>{n.name}</strong>
                  <small>{n.ssh_user}@{n.host}:{n.ssh_port}</small>
                </span>
                <i className={`choice-dot ${n.status}`} title={n.status === "online" ? "在线" : n.status === "offline" ? "离线" : "状态未知"} />
              </button>
            ))}
          </div>
        </section>
      ) : (
        <div className="console-layout">
          <div className="console-window">
            <div className="console-tabs" role="tablist" aria-label="终端标签">
              <span className="terminal-dots" aria-hidden><i /><i /><i /></span>
              {tabs.map((tab) => (
                <div
                  key={tab.key}
                  role="tab"
                  aria-selected={tab.key === active}
                  tabIndex={0}
                  className={`console-tab ${tab.key === active ? "active" : ""}`}
                  onClick={() => setActive(tab.key)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") setActive(tab.key);
                  }}
                >
                  <span className={`console-dot ${states[tab.key] ?? "connecting"}`} />
                  {tab.name}
                  <button
                    className="console-tab-close"
                    title="关闭"
                    aria-label={`关闭 ${tab.name} 终端`}
                    onClick={(e) => {
                      e.stopPropagation();
                      closeTab(tab.key);
                    }}
                  >
                    <X size={12} />
                  </button>
                </div>
              ))}
            </div>
            <div className="console-statusbar">
              <span className={`console-dot ${activeState}`} />
              <span>{STATE_LABEL[activeState]}</span>
              {activeNode && <code>{activeNode.ssh_user}@{activeNode.host}:{activeNode.ssh_port}</code>}
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
          </div>

          <aside className="console-side">
            <header>
              <h2><Zap size={15} />快捷命令</h2>
              <button className="btn sm" onClick={() => setCreating(true)}>
                <Plus size={13} />
                新建
              </button>
            </header>
            <p className="console-side-hint">一键把命令送进当前终端并回车执行，立即生效。</p>

            {commands.length === 0 ? (
              <p className="console-side-empty">还没有快捷命令。</p>
            ) : (
              <div className="command-list">
                {commands.map((c) => (
                  <div key={c.id} className="command-item">
                    <button
                      className="command-run"
                      title={active ? `在 ${activeTab?.name ?? "当前终端"} 执行` : "先打开一个终端"}
                      aria-label={`执行 ${c.name}`}
                      disabled={!active}
                      onClick={() => runCommand(c.command)}
                    >
                      <Play size={13} />
                    </button>
                    <div className="command-text">
                      <div className="command-name">{c.name}</div>
                      <div className="command-body" title={c.command}>{c.command}</div>
                    </div>
                    <button
                      className="icon-btn command-delete"
                      title="删除"
                      aria-label={`删除 ${c.name}`}
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
                ))}
              </div>
            )}
          </aside>
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
    </div>
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
    <div className="console-gate">
      <span className="console-gate-lock"><LockKeyhole size={24} /></span>
      <h2>先验证身份</h2>
      <p>
        终端给的是机器上的 root shell。本次浏览器会话里只需验证一次，之后开终端不再询问。
      </p>
      {error && <Banner kind="err">{error}</Banner>}
      <form className="dialog-form" onSubmit={submit}>
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
            className="console-gate-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            inputMode="numeric"
            maxLength={6}
            placeholder="未开启两步验证则留空"
          />
        </label>
        <button className="btn primary console-gate-submit" disabled={busy}>
          <LockKeyhole size={15} />
          {busy ? "验证中…" : "解锁终端"}
        </button>
      </form>
    </div>
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
    <Modal title="新建快捷命令" subtitle="执行时会原样发送并自动回车，请确认它在目标机器上是你想要的效果。" onClose={onClose}>
      <form className="dialog-form" onSubmit={submit}>
        <label>
          名称
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 看负载" autoFocus required />
        </label>
        <div className="field">
          <span className="field-label">命令</span>
          <div className="term-block command-editor">
            <span className="terminal-prompt" aria-hidden>$</span>
            <textarea
              aria-label="命令"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              rows={3}
              placeholder="uptime && free -h"
              spellCheck={false}
              required
            />
          </div>
        </div>
        <div className="modal-actions">
          <button type="button" className="btn ghost" onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
