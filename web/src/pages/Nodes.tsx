import { SortHandle, useManualOrder } from "../components/ManualOrder";
import { useEffect, useId, useState } from "react";
import {
  api,
  ApiError,
  DEFAULT_PORT_END,
  DEFAULT_PORT_START,
  type Node,
  type NodeInput,
  type NodeMetrics,
  type ProbeResult,
  ipv6DialAddress,
} from "../api";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  LayoutGrid,
  List,
  Plus,
  Rows3,
  Search,
  Server,
  TerminalSquare,
} from "lucide-react";
import { CopyButton, CopyIconButton } from "../components/CopyButton";
import { MetricBar } from "../components/MetricBar";
import { formatBytes, formatRate, formatUptime, percentOf } from "../lib/format";
import { memCaveat } from "../lib/metrics";
import { NumberField } from "../components/NumberField";
import { Banner, Modal } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { Drawer } from "../components/Drawer";
import "./management-redesign.css";
import { EnrollDialog } from "./EnrollDialog";
import { ReinstallDialog } from "./ReinstallDialog";

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
  ipv6_entry: "",
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
  return saved === "card" || saved === "compact" || saved === "list" ? saved : "list";
}

function v6Title(n: Node): string {
  return n.ipv6_entry
    ? "手填的 IPv6 入口：服务商把这个地址转进本机的 IPv4，链路按 IPv4 监听"
    : "探测到的公网 IPv6 地址，可作为链路的 IPv6 入口";
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

function ratioText(used: number | null, total: number | null) {
  if (used === null || total === null) return <span className="muted">—</span>;
  return `${formatBytes(used)} / ${formatBytes(total)}`;
}

// uninstallCommand points the node at this panel. The browser is already
// talking to the right origin, so there is nothing to configure.
function uninstallCommand(): string {
  return `curl -fsSL ${window.location.origin}/uninstall.sh | sh`;
}

export function Nodes({ onOpenConsole, telegram = false, initialCreateInput, onCreateDraftConsumed }: { onOpenConsole?: (nodeID: number) => void; telegram?: boolean; initialCreateInput?: NodeInput; onCreateDraftConsumed?: () => void }) {
  const detailsPrefix = useId();
  const [expandedDetails, setExpandedDetails] = useState<Record<number, boolean>>({});
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState<Node | null>(null);
  const [creating, setCreating] = useState(!!initialCreateInput);
  const [createInput, setCreateInput] = useState(initialCreateInput);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [probe, setProbe] = useState<{ node: string; result: ProbeResult } | null>(null);
  const [enrolling, setEnrolling] = useState(false);
  const [reinstalling, setReinstalling] = useState<Node | null>(null);
  const [removed, setRemoved] = useState<string | null>(null);
  const [metrics, setMetrics] = useState<Record<string, NodeMetrics>>({});
  const [view, setView] = useState<NodeView>(storedNodeView);
  const [confirm, confirmDialog] = useConfirm();

  const order = useManualOrder({
    items: nodes, setItems: setNodes, save: api.reorderNodes, reload: api.listNodes, onError: setError,
    disabled: busyId !== null || creating || !!editing || enrolling || !!reinstalling,
  });

  function chooseView(next: NodeView) {
    setView(next);
    localStorage.setItem(NODE_VIEW_KEY, next);
  }

  const fail = (err: unknown) =>
    setError(err instanceof ApiError ? err.message : "请求失败");

  const viaName = (n: Node) =>
    n.via_node_id ? nodes.find((x) => x.id === n.via_node_id)?.name ?? n.via_node_id : "直连";

  function nodeActions(n: Node) {
    const busy = busyId === n.id || order.pending;
    return (
      <div className="row nowrap">
        {onOpenConsole && <button className="btn sm" title="在浏览器里打开这台机器的 SSH" onClick={() => onOpenConsole(n.id)}>
          <TerminalSquare size={13} />
          终端
        </button>}
        <button className="btn sm" disabled={busy} onClick={() => void runProbe(n)}>
          {busyId === n.id ? "探测中…" : "探测"}
        </button>
        {!telegram && n.arch !== "" && !n.realm_version && (
          <button
            className="btn sm"
            disabled={busy}
            title="转发内核会在第一次下发链路时自动安装，这里可以提前装好"
            onClick={() => void installRealm(n)}
          >
            装内核
          </button>
        )}
        <button className="btn sm" disabled={order.pending} onClick={() => setEditing(n)}>
          编辑
        </button>
        {!telegram && <button
          className="btn sm"
          title="机器重装过系统？在这里拿一条命令重新接管，链路不用重建"
          disabled={order.pending} onClick={() => setReinstalling(n)}
        >
          重装
        </button>}
        <button className="btn sm danger" disabled={busy} onClick={() => void remove(n)}>
          删除
        </button>
      </div>
    );
  }

  async function load() {
    const version = order.readVersion();
    try {
      const [list, snapshot] = await Promise.all([api.listNodes(), api.metrics()]);
      if (order.acceptsRead(version)) setNodes(list ?? []);
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
      body: "将删除面板机器记录及其托管的 sing-box 服务和配置；仅扫描的外部配置不受影响。链路或跳板仍引用此机器时无法删除。realm 和面板登录公钥不会被清理，删除后会给出卸载命令。",
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

  const shownNodes = nodes.filter((node) => `${node.name} ${node.host} ${node.os_id} ${node.arch}`.toLowerCase().includes(search.trim().toLowerCase()) && (statusFilter === "all" || node.status === statusFilter));
  const visibleIDs = shownNodes.map(item => item.id);

  return (
    <div className="management-page machines-management">
      <PageHeader
        title="机器"
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
            <button className="btn" disabled={order.pending} onClick={() => setCreating(true)}>
              手动添加
            </button>
            {!telegram && <button className="btn primary" disabled={order.pending} onClick={() => setEnrolling(true)}>
              <Plus size={15} />
              一键注册
            </button>}
          </>
        }
      />

      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="management-toolbar">
        <label className="management-search"><Search size={16} /><input aria-label="搜索机器" placeholder="搜索机器名称、地址或系统…" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
        <select aria-label="筛选机器状态" value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}><option value="all">全部状态</option><option value="online">在线</option><option value="offline">离线</option><option value="unknown">未知</option></select>
        <span className="management-result-count">{shownNodes.length} / {nodes.length} 台机器</span>
      </div>
      {nodes.length > 0 && shownNodes.length === 0 && <Card><p className="muted">没有符合筛选条件的机器。</p></Card>}
      {nodes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Server size={22} />}
            title="还没有机器"
            desc="添加并探测机器后，即可创建链路。"
            action={
              <button className="btn primary" onClick={() => telegram ? setCreating(true) : setEnrolling(true)}>
                <Plus size={15} />
                {telegram ? "添加机器" : "一键注册"}
              </button>
            }
          />
        </Card>
      ) : view === "list" ? (
        <Card className="flush machine-list-panel">
          <table className="machine-resource-table">
            <thead><tr><th>机器</th><th>状态</th><th>CPU</th><th>内存</th><th>磁盘</th><th>操作</th></tr></thead>
            {shownNodes.map((n) => {
              const m = metrics[n.id];
              const caveat = memCaveat(m);
              const sshAddress = `${n.ssh_user}@${n.host.includes(":") ? `[${n.host}]` : n.host}:${n.ssh_port}`;
              const ipv6 = ipv6DialAddress(n);
              return (
                <tbody key={n.id} {...order.targetProps(n.id, visibleIDs)} data-details-expanded={!!expandedDetails[n.id]}>
                  <tr className="machine-resource-row">
                    <td className="machine-identity-cell">
                      <div className="machine-list-identity">
                        <SortHandle {...order.handleProps(n.id, n.name, visibleIDs)} />
                        <span className="machine-list-icon"><Server size={18} /></span>
                        <div><div className="machine-list-name"><strong>{n.name}</strong><button type="button" className="machine-detail-toggle" aria-expanded={!!expandedDetails[n.id]} aria-controls={`${detailsPrefix}-${n.id}`} aria-label={`${expandedDetails[n.id] ? "收起" : "展开"} ${n.name} 连接详情`} title="连接详情" onClick={() => setExpandedDetails((current) => ({ ...current, [n.id]: !current[n.id] }))}><ChevronDown size={15} /></button></div><div className="machine-list-system">{systemCell(n)}</div>{m?.container && <span className="tag" title={caveat ?? "运行在容器里"}>{m.container}</span>}</div>
                      </div>
                    </td>
                    <td className="machine-list-status">{statusTag(n)}</td>
                    <td className="machine-list-resource machine-list-cpu"><MetricBar label="CPU" percent={m?.cpu_percent ?? null} detail={m?.cores ? `${m.cores} 核` : undefined} caveat={caveat} /></td>
                    <td className="machine-list-resource machine-list-memory"><MetricBar label="内存" percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)} detail={ratioText(m?.mem_used ?? null, m?.mem_total ?? null)} caveat={caveat} /></td>
                    <td className="machine-list-resource machine-list-disk"><MetricBar label="磁盘" percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)} detail={ratioText(m?.disk_used ?? null, m?.disk_total ?? null)} /></td>
                    <td className="machine-list-actions">{nodeActions(n)}</td>
                  </tr>
                  <tr id={`${detailsPrefix}-${n.id}`} className="machine-address-row" hidden={!expandedDetails[n.id]}><td colSpan={6}>
                    <div className="machine-addresses">
                      <span className="machine-address"><span className="machine-address-label">SSH</span><code>{sshAddress}</code><CopyIconButton text={sshAddress} title="复制 SSH 地址" /></span>
                      {ipv6 && <span className="machine-address" title={v6Title(n)}><span className="machine-address-label">IPv6</span><code>{ipv6}</code><CopyIconButton text={ipv6} title="复制 IPv6 地址" />{n.ipv6_entry && <span className="tag">映射</span>}</span>}
                    </div>
                    <div className="machine-address-meta"><span>端口池 <code>{n.port_start}–{n.port_end}</code></span><span>UDP {udpTag(n)}</span><span>跳板 <strong>{viaName(n)}</strong></span></div>
                  </td></tr>
                </tbody>
              );
            })}
          </table>
        </Card>
      ) : (
        <div className={`node-grid${view === "compact" ? " dense" : ""}`}>
          {shownNodes.map((n, cardIndex) => {
            const m = metrics[n.id];
            const caveat = memCaveat(m);
            return (
              <Card index={cardIndex} key={n.id} {...order.targetProps(n.id, visibleIDs)}>
                <div className="spread" style={{ marginBottom: 8 }}>
                  <div style={{ minWidth: 0 }}>
                    <h2 className="sortable-title" style={{ marginBottom: 2 }}><SortHandle {...order.handleProps(n.id, n.name, visibleIDs)} />{n.name}</h2>
                    <div className="mono muted" style={{ fontSize: 12 }}>
                      {n.ssh_user}@{n.host}:{n.ssh_port}
                    </div>
                    {ipv6DialAddress(n) && (
                      <div className="mono muted" style={{ fontSize: 12 }} title={v6Title(n)}>
                        {ipv6DialAddress(n)}
                        {n.ipv6_entry && <span className="tag" style={{ marginLeft: 6 }}>映射</span>}
                      </div>
                    )}
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
            托管的 sing-box 服务和配置已删除；仅扫描的外部配置不受影响。机器上仍可能留有 realm 和面板登录公钥，面板已不再管理此机器，
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

      {!telegram && reinstalling && (
        <ReinstallDialog
          node={reinstalling}
          onClose={() => setReinstalling(null)}
          onDone={() => void load()}
        />
      )}

      {!telegram && enrolling && (
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
          initialInput={createInput}
          onClose={() => {
            setCreating(false);
            setCreateInput(undefined);
            onCreateDraftConsumed?.();
            setEditing(null);
          }}
          onSaved={async (msg) => {
            setCreating(false);
            setCreateInput(undefined);
            onCreateDraftConsumed?.();
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
              UDP 能力未知，保留原值。
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
  initialInput?: NodeInput;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onError: (err: unknown) => void;
}

function NodeForm({ nodes, node, initialInput, onClose, onSaved, onError }: NodeFormProps) {
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
          ipv6_entry: node.ipv6_entry,
        }
      : { ...(initialInput ?? emptyInput), secret: "" },
  );
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");

  const set = <K extends keyof NodeInput>(key: K, value: NodeInput[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setFormError("");
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
      setFormError(err instanceof Error ? err.message : "请求失败");
      onError(err);
      setBusy(false);
    }
  }

  return (
    <Drawer title={node ? `编辑机器 · ${node.name}` : "添加机器"} onClose={onClose}>
      {formError && <Banner kind="err">{formError}</Banner>}
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
          自动分配池内空闲端口；NAT 机器仅填写实际映射端口。
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
        <p className="hint">仅内网机器需要跳板。</p>

        <label>
          IPv6 入口地址（选填）
          <input
            value={form.ipv6_entry}
            onChange={(e) => set("ipv6_entry", e.target.value)}
            placeholder={node?.ipv6_address ? `已探测到 ${node.ipv6_address}，无需填写` : "例如 2400:c620:22:282::10"}
            className="mono"
          />
        </label>
        <p className="hint">
          仅填写服务商映射到本机 IPv4 的公网 IPv6 入口。原生 IPv6 留空。
        </p>

        <label style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
          <input
            type="checkbox"
            checked={form.skip_udp_probe}
            onChange={(e) => set("skip_udp_probe", e.target.checked)}
            style={{ width: "auto", marginTop: 3 }}
          />
          <span>跳过 UDP 检测</span>
        </label>
        <p className="hint">不使用 UDP 链路时可跳过。</p>


        <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </Drawer>
  );
}
