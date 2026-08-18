import { useEffect, useState } from "react";
import {
  api,
  ApiError,
  DEFAULT_PORT_END,
  DEFAULT_PORT_START,
  type Node,
  type NodeInput,
  type NodeMetrics,
  type ProbeResult,
} from "../api";
import {
  ArrowDown,
  ArrowUp,
  LayoutGrid,
  List,
  Plus,
  Rows3,
  Server,
  TerminalSquare,
} from "lucide-react";
import { CopyButton } from "../components/CopyButton";
import { MetricBar } from "../components/MetricBar";
import { formatBytes, formatRate, formatUptime, percentOf } from "../lib/format";
import { NumberField } from "../components/NumberField";
import { Banner, Modal } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { EnrollDialog } from "./EnrollDialog";

const emptyInput: NodeInput = {
  name: "",
  host: "",
  ssh_port: 22,
  ssh_user: "root",
  auth_type: "password",
  secret: "",
  via_node_id: null,
  port_start: DEFAULT_PORT_START,
  port_end: DEFAULT_PORT_END,
  skip_udp_probe: false,
};

type NodeView = "card" | "compact" | "list";

const NODE_VIEW_KEY = "fluxlite-nodes-view";

const NODE_VIEW_OPTIONS = [
  { id: "card", label: "宽松卡片", icon: LayoutGrid },
  { id: "compact", label: "紧凑卡片", icon: Rows3 },
  { id: "list", label: "横向列表", icon: List },
] as const;

function storedNodeView(): NodeView {
  const saved = localStorage.getItem(NODE_VIEW_KEY);
  return saved === "card" || saved === "compact" || saved === "list" ? saved : "card";
}

// memCaveat explains why a container's memory figures may describe the wrong
// machine. An unprivileged container shares the host's /proc, so without a
// readable cgroup limit the totals are the host's — a 512 MB container drawn
// as a comfortable 4% of 64 GB.
function memCaveat(m: NodeMetrics | undefined): string | undefined {
  if (!m || !m.container || m.mem_source === "cgroup") return undefined;
  return `这是 ${m.container} 容器，但读不到它的 cgroup 限额，所以内存与 CPU 读数来自宿主机，描述的不是这台容器本身`;
}

function statusTag(n: Node) {
  const tone = n.status === "online" ? "ok" : n.status === "offline" ? "err" : "";
  const label = n.status === "online" ? "在线" : n.status === "offline" ? "离线" : "未知";
  return <span className={`tag ${tone}`}>{label}</span>;
}

function udpTag(n: Node) {
  if (n.skip_udp_probe) return <span className="tag">已跳过</span>;
  if (n.udp_capable === null) {
    return (
      <span className="tag" title="尚未测出结果，不等于不通">
        未知
      </span>
    );
  }
  return n.udp_capable ? <span className="tag ok">通</span> : <span className="tag err">不通</span>;
}

function systemCell(n: Node) {
  if (!n.arch) return <span className="tag warn">未探测</span>;
  return (
    <span className="muted">
      {n.os_id}/{n.arch} · {n.init_system} · realm {n.realm_version || "未装"}
    </span>
  );
}

function pctText(value: number | null) {
  return value === null ? <span className="muted">—</span> : `${value.toFixed(0)}%`;
}

function ratioText(used: number | null, total: number | null) {
  if (used === null || total === null) return <span className="muted">—</span>;
  return `${formatBytes(used)} / ${formatBytes(total)}`;
}

// uninstallCommand points the node at this panel. The browser is already
// talking to the right origin, so there is nothing to configure.
function uninstallCommand(): string {
  return `curl -fsSL ${window.location.origin}/uninstall.sh | sh`;
}

export function Nodes({ onOpenConsole }: { onOpenConsole: (nodeID: number) => void }) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState<Node | null>(null);
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [probe, setProbe] = useState<{ node: string; result: ProbeResult } | null>(null);
  const [enrolling, setEnrolling] = useState(false);
  const [removed, setRemoved] = useState<string | null>(null);
  const [metrics, setMetrics] = useState<Record<string, NodeMetrics>>({});
  const [view, setView] = useState<NodeView>(storedNodeView);
  const [confirm, confirmDialog] = useConfirm();

  function chooseView(next: NodeView) {
    setView(next);
    localStorage.setItem(NODE_VIEW_KEY, next);
  }

  const fail = (err: unknown) =>
    setError(err instanceof ApiError ? err.message : "请求失败");

  const viaName = (n: Node) =>
    n.via_node_id ? nodes.find((x) => x.id === n.via_node_id)?.name ?? n.via_node_id : "直连";

  function nodeActions(n: Node) {
    const busy = busyId === n.id;
    return (
      <div className="row nowrap">
        <button className="btn sm" title="在浏览器里打开这台机器的 SSH" onClick={() => onOpenConsole(n.id)}>
          <TerminalSquare size={13} />
          终端
        </button>
        <button className="btn sm" disabled={busy} onClick={() => void runProbe(n)}>
          {busy ? "探测中…" : "探测"}
        </button>
        {n.arch !== "" && !n.realm_version && (
          <button
            className="btn sm"
            disabled={busy}
            title="转发内核会在第一次下发链路时自动安装，这里可以提前装好"
            onClick={() => void installRealm(n)}
          >
            装内核
          </button>
        )}
        <button className="btn sm" onClick={() => setEditing(n)}>
          编辑
        </button>
        <button className="btn sm danger" disabled={busy} onClick={() => void remove(n)}>
          删除
        </button>
      </div>
    );
  }

  async function load() {
    try {
      const [list, snapshot] = await Promise.all([api.listNodes(), api.metrics()]);
      setNodes(list ?? []);
      setMetrics(snapshot ?? {});
    } catch (err) {
      fail(err);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // 指标随 5 分钟巡检更新，页面每分钟取一次即可跟上。取失败不弹横幅：
  // 一次轮询失败会把用户正在做的事盖掉，而屏幕上的旧数字仍然是对的。
  useEffect(() => {
    const timer = setInterval(() => {
      api
        .metrics()
        .then((snapshot) => setMetrics(snapshot ?? {}))
        .catch(() => {});
    }, 60000);
    return () => clearInterval(timer);
  }, []);

  async function runProbe(node: Node) {
    setBusyId(node.id);
    setError("");
    setNotice("");
    try {
      const result = await api.probeNode(node.id);
      setProbe({ node: node.name, result });
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusyId(null);
    }
  }

  async function installRealm(node: Node) {
    setBusyId(node.id);
    setError("");
    setNotice("");
    try {
      const { realm_version } = await api.installRealm(node.id);
      setNotice(`${node.name} 已装好转发内核 realm ${realm_version}`);
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusyId(null);
    }
  }

  async function remove(node: Node) {
    const ok = await confirm({
      title: `删除节点 ${node.name}？`,
      body: "面板只删除自己这边的记录。机器上的 realm、转发服务和面板的登录公钥不会被清理，删除后会给出卸载命令。",
      confirmLabel: "删除节点",
      danger: true,
    });
    if (!ok) return;
    setBusyId(node.id);
    setError("");
    try {
      await api.deleteNode(node.id);
      // The panel cannot reach into a machine it no longer manages, so the
      // leftovers — not least its own key in authorized_keys — have to be
      // removed from the node. Say so at the one moment the operator is
      // looking, instead of leaving root access behind silently.
      setRemoved(node.name);
      setNotice("");
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusyId(null);
    }
  }

  return (
    <div>
      <PageHeader
        title="节点"
        desc="转发链路的组成机器。内网节点需指定跳板，NAT 机器需按实际映射填写端口池。"
        actions={
          <>
            {nodes.length > 0 && (
              <div className="segmented" role="group" aria-label="视图">
                {NODE_VIEW_OPTIONS.map((option) => {
                  const Icon = option.icon;
                  return (
                    <button
                      key={option.id}
                      className={view === option.id ? "active" : ""}
                      title={option.label}
                      aria-pressed={view === option.id}
                      onClick={() => chooseView(option.id)}
                    >
                      <Icon size={15} />
                    </button>
                  );
                })}
              </div>
            )}
            <button className="btn" onClick={() => setCreating(true)}>
              手动添加
            </button>
            <button className="btn primary" onClick={() => setEnrolling(true)}>
              <Plus size={15} />
              一键注册
            </button>
          </>
        }
      />

      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      {nodes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Server size={22} />}
            title="还没有节点"
            desc="先添加一台机器，再做能力探测，之后才能用它组链路。"
            action={
              <button className="btn primary" onClick={() => setEnrolling(true)}>
                <Plus size={15} />
                一键注册
              </button>
            }
          />
        </Card>
      ) : view === "list" ? (
        <Card className="flush">
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>名称</th>
                  <th>地址</th>
                  <th>状态</th>
                  <th>CPU</th>
                  <th>内存</th>
                  <th>磁盘</th>
                  <th>系统</th>
                  <th>端口池</th>
                  <th>UDP</th>
                  <th>跳板</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {nodes.map((n) => {
                  const m = metrics[n.id];
                  return (
                    <tr key={n.id}>
                      <td>
                        <strong>{n.name}</strong>
                        {m?.container && (
                          <div className="muted" style={{ fontSize: 11 }}>{m.container}</div>
                        )}
                      </td>
                      <td className="mono nowrap">
                        {n.ssh_user}@{n.host}:{n.ssh_port}
                      </td>
                      <td>{statusTag(n)}</td>
                      <td className="nowrap">{pctText(m?.cpu_percent ?? null)}</td>
                      <td className="nowrap" title={memCaveat(m)}>
                        {ratioText(m?.mem_used ?? null, m?.mem_total ?? null)}
                      </td>
                      <td className="nowrap">
                        {ratioText(m?.disk_used ?? null, m?.disk_total ?? null)}
                      </td>
                      <td className="nowrap">{systemCell(n)}</td>
                      <td className="mono nowrap">
                        {n.port_start}-{n.port_end}
                      </td>
                      <td>{udpTag(n)}</td>
                      <td className="muted nowrap">{viaName(n)}</td>
                      <td>{nodeActions(n)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      ) : (
        <div className={`node-grid${view === "compact" ? " dense" : ""}`}>
          {nodes.map((n, cardIndex) => {
            const m = metrics[n.id];
            const caveat = memCaveat(m);
            return (
              <Card index={cardIndex} key={n.id}>
                <div className="spread" style={{ marginBottom: 8 }}>
                  <div style={{ minWidth: 0 }}>
                    <h2 style={{ marginBottom: 2 }}>{n.name}</h2>
                    <div className="mono muted" style={{ fontSize: 12 }}>
                      {n.ssh_user}@{n.host}:{n.ssh_port}
                    </div>
                  </div>
                  <div className="row" style={{ gap: 4, justifyContent: "flex-end" }}>
                    {statusTag(n)}
                    {m?.container && (
                      <span className="tag" title={caveat ?? "运行在容器里"}>
                        {m.container}
                      </span>
                    )}
                  </div>
                </div>

                <MetricBar
                  label="CPU"
                  percent={m?.cpu_percent ?? null}
                  detail={m?.cores ? `${m.cores} 核` : undefined}
                  caveat={caveat}
                />
                <MetricBar
                  label="内存"
                  percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)}
                  detail={ratioText(m?.mem_used ?? null, m?.mem_total ?? null)}
                  caveat={caveat}
                />
                <MetricBar
                  label="磁盘"
                  percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)}
                  detail={ratioText(m?.disk_used ?? null, m?.disk_total ?? null)}
                />
                {m?.swap_total ? (
                  <MetricBar
                    label="交换"
                    percent={percentOf(m.swap_used, m.swap_total)}
                    detail={ratioText(m.swap_used, m.swap_total)}
                  />
                ) : null}

                <dl className="node-facts">
                  <dt>负载</dt>
                  <dd>
                    {m?.load1 !== null && m?.load1 !== undefined
                      ? `${m.load1.toFixed(2)} / ${m.load5?.toFixed(2) ?? "—"} / ${
                          m.load15?.toFixed(2) ?? "—"
                        }`
                      : "—"}
                  </dd>
                  <dt>开机</dt>
                  <dd>{m?.uptime_sec ? formatUptime(m.uptime_sec) : "—"}</dd>
                  <dt>网速</dt>
                  <dd>
                    {m?.net_rx_rate !== null && m?.net_rx_rate !== undefined ? (
                      <>
                        <ArrowDown size={11} /> {formatRate(m.net_rx_rate)}
                        {"  "}
                        <ArrowUp size={11} /> {formatRate(m.net_tx_rate ?? 0)}
                      </>
                    ) : (
                      "—"
                    )}
                  </dd>
                  <dt>累计</dt>
                  <dd>
                    {m?.net_rx_bytes !== null && m?.net_rx_bytes !== undefined
                      ? `↓ ${formatBytes(m.net_rx_bytes)} ／ ↑ ${formatBytes(m.net_tx_bytes ?? 0)}`
                      : "—"}
                  </dd>
                  <dt>系统</dt>
                  <dd>{systemCell(n)}</dd>
                  {m?.kernel && (
                    <>
                      <dt>内核</dt>
                      <dd className="mono" style={{ fontSize: 11 }}>
                        {m.kernel}
                      </dd>
                    </>
                  )}
                  {m?.cpu_model && (
                    <>
                      <dt>处理器</dt>
                      <dd style={{ fontSize: 11 }}>{m.cpu_model}</dd>
                    </>
                  )}
                  <dt>端口池</dt>
                  <dd className="mono">
                    {n.port_start}-{n.port_end}
                  </dd>
                  <dt>UDP</dt>
                  <dd>{udpTag(n)}</dd>
                  <dt>跳板</dt>
                  <dd className="muted">{viaName(n)}</dd>
                </dl>

                <div style={{ marginTop: 14 }}>{nodeActions(n)}</div>
              </Card>
            );
          })}
        </div>
      )}

      {removed && (
        <Modal title={`节点 ${removed} 已从面板删除`} onClose={() => setRemoved(null)}>
          <Banner kind="warn">
            机器上还留着 realm、转发服务，以及面板的登录公钥 —— 面板已经管不到它了，
            需要在那台机器上执行卸载。
          </Banner>
          <p className="hint" style={{ marginTop: 0 }}>
            以 root 执行：
          </p>
          <pre className="cmd">{uninstallCommand()}</pre>
          <div className="row" style={{ marginBottom: 16 }}>
            <CopyButton text={uninstallCommand()} label="复制卸载命令" />
          </div>
          <p className="hint">
            脚本只删除注释以 <code>fluxlite-</code> 开头的公钥，你自己的密钥不受影响。
            转发内核 realm 如果还被别的服务引用会被保留，加 <code>--purge-realm</code> 可强制删除。
          </p>
        </Modal>
      )}

      {enrolling && (
        <EnrollDialog
          nodes={nodes}
          onClose={() => setEnrolling(false)}
          onEnrolled={() => void load()}
        />
      )}

      {(creating || editing) && (
        <NodeForm
          nodes={nodes}
          node={editing}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSaved={async (msg) => {
            setCreating(false);
            setEditing(null);
            setNotice(msg);
            await load();
          }}
          onError={fail}
        />
      )}

      {probe && (
        <Modal title={`探测结果 · ${probe.node}`} onClose={() => setProbe(null)}>
          <table>
            <tbody>
              <tr>
                <td className="muted">主机名</td>
                <td className="mono">{probe.result.facts.Hostname}</td>
              </tr>
              <tr>
                <td className="muted">系统 / 架构</td>
                <td className="mono">
                  {probe.result.facts.OSID} / {probe.result.facts.Arch}
                </td>
              </tr>
              <tr>
                <td className="muted">init</td>
                <td className="mono">{probe.result.facts.InitSystem}</td>
              </tr>
              <tr>
                <td className="muted">realm</td>
                <td className="mono">{probe.result.facts.RealmVersion || "未安装"}</td>
              </tr>
            </tbody>
          </table>

          <h2 style={{ marginTop: 18 }}>UDP 穿透</h2>
          {probe.result.udp?.Supported === true && (
            <Banner kind="ok">UDP 可穿透（探测方式：{probe.result.udp.Method}）</Banner>
          )}
          {probe.result.udp?.Supported === false && (
            <Banner kind="err">
              UDP 被丢弃，该节点不能承载 tcp+udp 链路。{probe.result.udp.Detail}
            </Banner>
          )}
          {(!probe.result.udp || probe.result.udp.Supported === null) && (
            <Banner kind="warn">
              未能判定 UDP 能力，已保持原值而非武断标记为不通。
              {probe.result.udp ? `原因：${probe.result.udp.Detail}` : ""}
            </Banner>
          )}
        </Modal>
      )}

      {confirmDialog}
    </div>
  );
}

interface NodeFormProps {
  nodes: Node[];
  node: Node | null;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onError: (err: unknown) => void;
}

function NodeForm({ nodes, node, onClose, onSaved, onError }: NodeFormProps) {
  const [form, setForm] = useState<NodeInput>(
    node
      ? {
          name: node.name,
          host: node.host,
          ssh_port: node.ssh_port,
          ssh_user: node.ssh_user,
          auth_type: node.auth_type,
          secret: "",
          via_node_id: node.via_node_id,
          port_start: node.port_start,
          port_end: node.port_end,
          skip_udp_probe: node.skip_udp_probe,
        }
      : emptyInput,
  );
  const [busy, setBusy] = useState(false);

  const set = <K extends keyof NodeInput>(key: K, value: NodeInput[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      if (node) {
        await api.updateNode(node.id, form);
        await onSaved(`节点 ${form.name} 已更新`);
      } else {
        await api.createNode(form);
        await onSaved(`节点 ${form.name} 已添加，请执行探测`);
      }
    } catch (err) {
      onError(err);
      setBusy(false);
    }
  }

  return (
    <Modal title={node ? `编辑节点 · ${node.name}` : "添加节点"} onClose={onClose}>
      <form onSubmit={submit}>
        <label>
          名称
          <input
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="随便起，支持中文"
            required
          />
        </label>

        <div className="grid2">
          <label>
            主机地址
            <input value={form.host} onChange={(e) => set("host", e.target.value)} required />
          </label>
          <label>
            SSH 端口
            <NumberField
              value={form.ssh_port}
              onChange={(v) => set("ssh_port", v)}
              required
            />
          </label>
          <label>
            登录用户
            <input
              value={form.ssh_user}
              onChange={(e) => set("ssh_user", e.target.value)}
              required
            />
          </label>
          <label>
            认证方式
            <select
              value={form.auth_type}
              onChange={(e) => set("auth_type", e.target.value as NodeInput["auth_type"])}
            >
              <option value="password">密码</option>
              <option value="key">私钥</option>
            </select>
          </label>
        </div>

        <label>
          {form.auth_type === "key" ? "私钥内容（PEM）" : "密码"}
          <input
            type={form.auth_type === "key" ? "text" : "password"}
            value={form.secret}
            onChange={(e) => set("secret", e.target.value)}
            placeholder={node ? "留空表示不修改" : ""}
            required={!node}
          />
        </label>

        <div className="grid2">
          <label>
            端口池起始
            <NumberField
              value={form.port_start}
              onChange={(v) => set("port_start", v)}
              required
            />
          </label>
          <label>
            端口池结束
            <NumberField
              value={form.port_end}
              onChange={(v) => set("port_end", v)}
              required
            />
          </label>
        </div>
        <p className="hint">
          分配时会从起始端口向上取第一个空闲端口，并自动跳过节点上已被占用的端口。
          起始端口默认避开 1-9999。
          NAT 机器只能填服务商实际映射到本机的端口范围。
        </p>

        <label>
          跳板节点
          <select
            value={form.via_node_id ?? ""}
            onChange={(e) => set("via_node_id", e.target.value ? Number(e.target.value) : null)}
          >
            <option value="">直连（无需跳板）</option>
            {nodes
              .filter((n) => n.id !== node?.id)
              .map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
          </select>
        </label>
        <p className="hint">仅对控制器无法直连的内网节点设置，下发时会自动经跳板。</p>

        <label style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
          <input
            type="checkbox"
            checked={form.skip_udp_probe}
            onChange={(e) => set("skip_udp_probe", e.target.checked)}
            style={{ width: "auto", marginTop: 3 }}
          />
          <span>跳过 UDP 检测</span>
        </label>
        <p className="hint">多数 NAT 机器只映射 TCP，不跑 UDP 链路可勾选，每次探测省十几秒。</p>

        <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
