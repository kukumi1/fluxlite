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
  type ProbeMachine,
  type ProbeResult,
  ipv6DialAddress,
} from "../api";
import {
  Activity,
  ArrowDown,
  ArrowUp,
  ChevronDown,
  Cpu,
  Download,
  Gauge,
  Hash,
  KeyRound,
  LayoutGrid,
  List,
  MemoryStick,
  Network,
  Pencil,
  Plus,
  Radio,
  RotateCcw,
  Rows3,
  Server,
  TerminalSquare,
  Trash2,
  type LucideIcon,
} from "lucide-react";
import { CopyButton, CopyIconButton } from "../components/CopyButton";
import { Flag } from "../components/Flag";
import { MetricBar } from "../components/MetricBar";
import { MetricRing } from "../components/MetricRing";
import { formatBytes, formatRate, formatUptime, percentOf } from "../lib/format";
import { memCaveat } from "../lib/metrics";
import { NumberField } from "../components/NumberField";
import { Banner, Modal } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { ActionButton } from "../components/ActionButton";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { ListToolbar } from "../components/ListToolbar";
import { PageHeader } from "../components/PageHeader";
import { Drawer } from "../components/Drawer";
import { ChoiceGroup, DrawerActions, FormBlock, SwitchField } from "../components/DrawerForm";
import "./management-redesign.css";
import "./probe.css";
import { regionOf, toneOf } from "../lib/region";
import { EnrollDialog } from "./EnrollDialog";
import { AgentEnrollDialog, flagCode, MachineDialog } from "./ProbeAdmin";
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
  return <span className={`pill ${tone}`}><i />{label}</span>;
}

// liveTag marks readings that come from the machine's agent, live, rather
// than the panel's SSH sample every ten seconds.
function liveTag(m: NodeMetrics | undefined) {
  return m?.source === "agent" ? <span className="tag live-tag" title="CPU、内存、网速来自这台机器上的 agent，每几秒更新">实时</span> : null;
}

// Agents report every few seconds; with one on a machine the page follows
// it closely enough to read as live.
const LIVE_REFRESH_MS = 3000;

function MachineBadge({ name, region }: { name: string; region?: string | null }) {
  const code = region ?? regionOf(name);
  return (
    <span className={`machine-badge tone-${toneOf(code)}`} title={code ?? undefined} aria-hidden>
      {code ? <>{code}<Flag code={code} /></> : <Server size={17} />}
    </span>
  );
}

// A machine known only by its agent: it reports, but the panel holds no SSH
// for it, so it shows the same way with nothing it cannot do.
type AgentRow = ProbeMachine & { id: string };

const asAgentRow = (m: ProbeMachine): AgentRow => ({ ...m, id: m.uuid });

const agentRegion = (m: ProbeMachine) => flagCode(m.display.region) ?? regionOf(m.name);

// reportMetrics reads an agent's report as the panel's own readings, so the
// machine is drawn by the same rings and bars as the rest.
function reportMetrics(m: ProbeMachine): NodeMetrics | undefined {
  const r = m.report;
  if (!r) return undefined;
  return {
    node_id: 0, cpu_percent: r.cpu.usage, cores: m.info.cpu_cores ?? null, cpu_model: m.info.cpu_name ?? "",
    mem_total: r.ram.total, mem_used: r.ram.used, swap_total: r.swap.total, swap_used: r.swap.used,
    disk_total: r.disk.total, disk_used: r.disk.used, load1: r.load.load1, load5: r.load.load5, load15: r.load.load15,
    uptime_sec: r.uptime, kernel: "", net_rx_bytes: r.network.totalDown, net_tx_bytes: r.network.totalUp,
    net_rx_rate: r.network.down, net_tx_rate: r.network.up, mem_source: "host", container: "", source: "agent",
    collected_at: new Date(m.at * 1000).toISOString(),
  };
}

function agentStatusTag(m: ProbeMachine) {
  if (m.online) return <span className="pill ok"><i />在线</span>;
  return m.at ? <span className="pill err"><i />离线</span> : <span className="pill"><i />等待上报</span>;
}

const agentSystem = (m: ProbeMachine) => [m.info.os, m.info.arch].filter(Boolean).join(" · ") || "agent 还没上报系统信息";

const agentOnlyTag = <span className="tag" title="面板没有这台机器的 SSH，所以不能转发、测速、开终端；点「补 SSH」补上">仅 agent</span>;

function SummaryFigure({ icon: Icon, tone, label, value, note }: { icon: LucideIcon; tone: string; label: string; value: string; note?: string }) {
  return (
    <div className={`tone-${tone}`}>
      <i><Icon size={16} /></i>
      <dt>{label}</dt>
      <dd>{value}{note && <small>{note}</small>}</dd>
    </div>
  );
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
  const [probeMachines, setProbeMachines] = useState<ProbeMachine[]>([]);
  const [editingProbe, setEditingProbe] = useState<ProbeMachine | null>(null);
  const [agentEnrolling, setAgentEnrolling] = useState(false);
  const [enrollDraft, setEnrollDraft] = useState<{ name: string; host: string } | null>(null);
  const [view, setView] = useState<NodeView>(storedNodeView);
  const [confirm, confirmDialog] = useConfirm();

  const order = useManualOrder({
    items: nodes, setItems: setNodes, save: api.reorderNodes, reload: api.listNodes, onError: setError,
    disabled: busyId !== null || creating || !!editing || enrolling || !!reinstalling,
  });

  const agentRows = probeMachines.filter((m) => m.node_id === null).map(asAgentRow);
  const agentOrder = useManualOrder<string, AgentRow>({
    items: agentRows,
    setItems: (rows) => setProbeMachines((cur) => [...cur.filter((m) => m.node_id !== null), ...rows]),
    save: api.reorderProbeMachines,
    reload: async () => (await loadProbe())?.filter((m) => m.node_id === null).map(asAgentRow) ?? null,
    onError: setError,
    disabled: creating || !!editing || !!editingProbe,
  });

  function agentActions(m: AgentRow, iconOnly: boolean) {
    return (
      <div className="machine-actions">
        <div className="machine-actions-main">
          <ActionButton iconOnly={iconOnly} label="补 SSH" icon={KeyRound} title="生成一条命令到这台机器上执行，面板就能 SSH 进去转发、测速、开终端，现有的 agent 保留" disabled={agentOrder.pending} onClick={() => addSSH(m)} />
          <ActionButton iconOnly={iconOnly} label="探针" icon={Radio} title="agent、公开页上的价格到期等" onClick={() => setEditingProbe(m)} />
        </div>
        <div className="machine-actions-side">
          <ActionButton iconOnly={iconOnly} label="删除" icon={Trash2} className="ghost danger" disabled={agentOrder.pending} onClick={() => void removeProbe(m)} />
        </div>
      </div>
    );
  }

  function chooseView(next: NodeView) {
    setView(next);
    localStorage.setItem(NODE_VIEW_KEY, next);
  }

  const fail = (err: unknown) =>
    setError(err instanceof ApiError ? err.message : "请求失败");

  const viaName = (n: Node) =>
    n.via_node_id ? nodes.find((x) => x.id === n.via_node_id)?.name ?? n.via_node_id : "直连";

  function nodeActions(n: Node, iconOnly: boolean) {
    const busy = busyId === n.id || order.pending;
    const probing = busyId === n.id;
    return (
      <div className="machine-actions">
        <div className="machine-actions-main">
          {onOpenConsole && <ActionButton iconOnly={iconOnly} label="终端" icon={TerminalSquare} title="在浏览器里打开这台机器的 SSH" onClick={() => onOpenConsole(n.id)} />}
          <ActionButton iconOnly={iconOnly} label={probing ? "探测中…" : "探测"} icon={Activity} className={probing ? "is-busy" : ""} disabled={busy} onClick={() => void runProbe(n)} />
          {!telegram && n.arch !== "" && !n.realm_version && (
            <ActionButton iconOnly={iconOnly} label="装内核" icon={Download} title="转发内核会在第一次下发链路时自动安装，这里可以提前装好" disabled={busy} onClick={() => void installRealm(n)} />
          )}
          <ActionButton iconOnly={iconOnly} label="编辑" icon={Pencil} disabled={order.pending} onClick={() => setEditing(n)} />
          {probeByNode.get(n.id) && <ActionButton iconOnly={iconOnly} label="探针" icon={Radio} title="agent、公开页上的价格到期等" onClick={() => setEditingProbe(probeByNode.get(n.id) ?? null)} />}
          {!telegram && (
            <ActionButton iconOnly={iconOnly} label={busyId === -n.id ? "安装中…" : probeByNode.get(n.id) ? "重装 agent" : "装 agent"} icon={Download}
              className={busyId === -n.id ? "is-busy" : ""}
              title={probeByNode.get(n.id) ? "面板 SSH 进去用这台机器原来的身份重装 agent，agent 连不上、版本太旧时用" : "面板 SSH 进去装上探针 agent，之后这台机器显示实时数据"}
              disabled={busy || busyId === -n.id || n.status !== "online"} onClick={() => void installAgent(n)} />
          )}
        </div>
        <div className="machine-actions-side">
          {!telegram && <ActionButton iconOnly={iconOnly} label="重装" icon={RotateCcw} className="ghost" title="机器重装过系统？在这里拿一条命令重新接管，链路不用重建" disabled={order.pending} onClick={() => setReinstalling(n)} />}
          <ActionButton iconOnly={iconOnly} label="删除" icon={Trash2} className="ghost danger" disabled={busy} onClick={() => void remove(n)} />
        </div>
      </div>
    );
  }

  // The probe is optional to this page: if it cannot be read, the machines
  // still show with their SSH samples.
  const loadProbe = () => api.probeOverview().then((o) => o.machines, () => null);

  async function load() {
    const version = order.readVersion();
    try {
      const [list, snapshot, probes] = await Promise.all([api.listNodes(), api.metrics(), loadProbe()]);
      if (order.acceptsRead(version)) setNodes(list ?? []);
      setMetrics(snapshot ?? {});
      if (probes) setProbeMachines(probes);
    } catch (err) {
      fail(err);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // 后台每 10 秒采一次资源并据此判定在线，页面同频取机器列表和指标。取失败
  // 不弹横幅：一次轮询失败会把用户正在做的事盖掉，而屏幕上的旧数字仍然是对的。
  useEffect(() => {
    const timer = setInterval(() => {
      const version = order.readVersion();
      Promise.all([api.listNodes(), api.metrics(), loadProbe()])
        .then(([list, snapshot, probes]) => {
          if (order.acceptsRead(version)) setNodes(list ?? []);
          setMetrics(snapshot ?? {});
          if (probes) setProbeMachines(probes);
        })
        .catch(() => {});
    }, 10000);
    return () => clearInterval(timer);
  }, []);

  const hasAgents = probeMachines.length > 0;
  useEffect(() => {
    if (!hasAgents) return;
    const timer = setInterval(() => {
      Promise.all([api.metrics(), loadProbe()])
        .then(([snapshot, probes]) => {
          setMetrics(snapshot ?? {});
          if (probes) setProbeMachines(probes);
        })
        .catch(() => {});
    }, LIVE_REFRESH_MS);
    return () => clearInterval(timer);
  }, [hasAgents]);

  async function removeProbe(m: ProbeMachine) {
    const ok = await confirm({
      title: `删除探针机器 ${m.name}？`,
      body: "这台机器的探针历史会一起删掉，它的 agent 也会因为 token 失效而连不上。要重新接入得在机器上重新装 agent。",
      confirmLabel: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteProbeMachine(m.uuid);
      setNotice(`已删除探针机器 ${m.name}`);
      await load();
    } catch (err) {
      fail(err);
    }
  }

  // Giving an agent-only machine SSH is the same one command as enrolling a
  // new one: run on the machine, it lets the panel in and keeps the agent.
  // The Telegram app has no enrollment, so there it is the manual form.
  function addSSH(m: ProbeMachine) {
    const draft = { name: m.name, host: m.info.ipv4 || m.info.ipv6 || "" };
    if (telegram) {
      setCreateInput({ ...emptyInput, ...draft });
      setCreating(true);
      return;
    }
    setEnrollDraft(draft);
    setEnrolling(true);
  }

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

  // The busy marker is the node's negated ID, so the agent's install does
  // not show as a probe of the same machine.
  async function installAgent(node: Node) {
    setBusyId(-node.id);
    setError("");
    setNotice("");
    try {
      await api.installNodeAgent(node.id);
      setNotice(`${node.name} 的 agent 已装好，几秒后开始显示实时数据`);
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

  const query = search.trim().toLowerCase();
  const searchedNodes = nodes.filter((node) => `${node.name} ${node.host} ${node.os_id} ${node.arch}`.toLowerCase().includes(query));
  const shownNodes = searchedNodes.filter((node) => statusFilter === "all" || node.status === statusFilter);
  const unknownCount = searchedNodes.filter((n) => n.status === "unknown").length;
  const visibleIDs = shownNodes.map(item => item.id);

  const agentStatus = (m: ProbeMachine) => (m.online ? "online" : "offline");
  const searchedAgents = agentRows.filter((m) => `${m.name} ${m.info.ipv4 ?? ""} ${m.info.ipv6 ?? ""} ${m.info.os ?? ""}`.toLowerCase().includes(query));
  const shownAgents = searchedAgents.filter((m) => statusFilter === "all" || agentStatus(m) === statusFilter);
  const visibleAgentIDs = shownAgents.map((m) => m.id);
  const agentMetrics = new Map(shownAgents.map((m) => [m.id, reportMetrics(m)]));
  const shownCount = shownNodes.length + shownAgents.length;
  const totalCount = nodes.length + agentRows.length;

  // 汇总只算量到了的机器：没读数的机器不是空闲，把它当 0 平均进去会把数字拉低。
  const readings = [...shownNodes.map((n) => metrics[n.id]), ...agentMetrics.values()].filter((m): m is NodeMetrics => !!m);
  const cpuReadings = readings.map((m) => m.cpu_percent).filter((v): v is number => v !== null);
  const memUsed = readings.reduce((sum, m) => sum + (m.mem_used ?? 0), 0);
  const memTotal = readings.reduce((sum, m) => sum + (m.mem_total ?? 0), 0);
  const netRate = readings.reduce((sum, m) => sum + (m.net_rx_rate ?? 0) + (m.net_tx_rate ?? 0), 0);
  const onlineCount = shownNodes.filter((n) => n.status === "online").length + shownAgents.filter((m) => m.online).length;
  const probeByNode = new Map(probeMachines.filter((m) => m.node_id !== null).map((m) => [m.node_id as number, m]));

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
            {!telegram && <button className="btn" onClick={() => setAgentEnrolling(true)} title="在机器上装 agent，实时上报 CPU、内存、网速">
              <Radio size={15} />
              接入 agent
            </button>}
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

      <ListToolbar
        search={search}
        onSearch={setSearch}
        searchLabel="搜索机器"
        placeholder="搜索机器名称、地址或系统…"
        filterLabel="筛选机器状态"
        filter={statusFilter}
        onFilter={setStatusFilter}
        filters={[
          { value: "all", label: "全部", count: searchedNodes.length + searchedAgents.length },
          { value: "online", label: "在线", count: searchedNodes.filter((n) => n.status === "online").length + searchedAgents.filter((m) => m.online).length, tone: "ok" },
          { value: "offline", label: "离线", count: searchedNodes.filter((n) => n.status === "offline").length + searchedAgents.filter((m) => !m.online).length, tone: "err" },
          ...(unknownCount > 0 || statusFilter === "unknown" ? [{ value: "unknown", label: "未知", count: unknownCount, tone: "warn" as const }] : []),
        ]}
      />
      {shownCount > 0 && (
        <dl className="stat-strip">
          <SummaryFigure icon={Server} tone="lime" label="在线" value={`${onlineCount}`} note={`/ ${shownCount} 台`} />
          <SummaryFigure icon={Cpu} tone="sky" label="平均 CPU" value={cpuReadings.length ? `${Math.round(cpuReadings.reduce((a, b) => a + b, 0) / cpuReadings.length)}%` : "—"} note={cpuReadings.length ? `${cpuReadings.length} 台有读数` : undefined} />
          <SummaryFigure icon={MemoryStick} tone="lilac" label="内存占用" value={memTotal ? formatBytes(memUsed) : "—"} note={memTotal ? `/ ${formatBytes(memTotal)}` : undefined} />
          <SummaryFigure icon={Gauge} tone="peach" label="实时网速 · 入 + 出" value={readings.length ? formatRate(netRate) : "—"} />
        </dl>
      )}
      {totalCount > 0 && shownCount === 0 && <Card><p className="muted">没有符合筛选条件的机器。</p></Card>}
      {totalCount === 0 ? (
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
        // One floating row per machine; its connection details fold out
        // underneath, inside the same row.
        <div className="lane-list machine-lanes" role="list" aria-label="机器列表">
          <div className="lane-head" aria-hidden="true"><span>机器</span><span>状态</span><span>CPU</span><span>内存</span><span>磁盘</span><span>操作</span></div>
            {shownNodes.map((n) => {
              const m = metrics[n.id];
              const caveat = memCaveat(m);
              const sshAddress = `${n.ssh_user}@${n.host.includes(":") ? `[${n.host}]` : n.host}:${n.ssh_port}`;
              const ipv6 = ipv6DialAddress(n);
              const target = order.targetProps(n.id, visibleIDs);
              return (
                <div role="listitem" key={n.id} {...target} className={`lane-row machine-lane tone-${toneOf(regionOf(n.name))} ${target.className}`} data-details-expanded={!!expandedDetails[n.id]}>
                  <div className="machine-resource-row">
                    <div className="machine-identity-cell">
                      <div className="machine-list-identity">
                        <SortHandle {...order.handleProps(n.id, n.name, visibleIDs)} />
                        <MachineBadge name={n.name} />
                        <div><div className="machine-list-name"><strong>{n.name}</strong><button type="button" className="machine-detail-toggle" aria-expanded={!!expandedDetails[n.id]} aria-controls={`${detailsPrefix}-${n.id}`} aria-label={`${expandedDetails[n.id] ? "收起" : "展开"} ${n.name} 连接详情`} title="连接详情" onClick={() => setExpandedDetails((current) => ({ ...current, [n.id]: !current[n.id] }))}><ChevronDown size={15} /></button></div><div className="machine-list-system">{systemCell(n)}</div>{m?.container && <span className="tag" title={caveat ?? "运行在容器里"}>{m.container}</span>}</div>
                      </div>
                    </div>
                    <div className="machine-list-status">{statusTag(n)}{liveTag(m)}</div>
                    <div className="machine-list-resource machine-list-cpu"><MetricBar label="CPU" percent={m?.cpu_percent ?? null} detail={m?.cores ? `${m.cores} 核` : undefined} caveat={caveat} /></div>
                    <div className="machine-list-resource machine-list-memory"><MetricBar label="内存" percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)} detail={ratioText(m?.mem_used ?? null, m?.mem_total ?? null)} caveat={caveat} /></div>
                    <div className="machine-list-resource machine-list-disk"><MetricBar label="磁盘" percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)} detail={ratioText(m?.disk_used ?? null, m?.disk_total ?? null)} /></div>
                    <div className="machine-list-actions">{nodeActions(n, true)}</div>
                  </div>
                  <div id={`${detailsPrefix}-${n.id}`} className="machine-address-row" hidden={!expandedDetails[n.id]}>
                    <div className="machine-addresses">
                      <span className="machine-address"><span className="machine-address-label">SSH</span><code>{sshAddress}</code><CopyIconButton text={sshAddress} title="复制 SSH 地址" /></span>
                      {ipv6 && <span className="machine-address" title={v6Title(n)}><span className="machine-address-label">IPv6</span><code>{ipv6}</code><CopyIconButton text={ipv6} title="复制 IPv6 地址" />{n.ipv6_entry && <span className="tag">映射</span>}</span>}
                    </div>
                    <div className="machine-address-meta"><span>端口池 <code>{n.port_start}–{n.port_end}</code></span><span>UDP {udpTag(n)}</span><span>跳板 <strong>{viaName(n)}</strong></span></div>
                  </div>
                </div>
              );
            })}
            {shownAgents.map((a) => {
              const m = agentMetrics.get(a.id);
              const target = agentOrder.targetProps(a.id, visibleAgentIDs);
              const region = agentRegion(a);
              return (
                <div role="listitem" key={a.id} {...target} className={`lane-row machine-lane tone-${toneOf(region)} ${target.className}`}>
                  <div className="machine-resource-row">
                    <div className="machine-identity-cell">
                      <div className="machine-list-identity">
                        <SortHandle {...agentOrder.handleProps(a.id, a.name, visibleAgentIDs)} />
                        <MachineBadge name={a.name} region={region} />
                        <div><div className="machine-list-name"><strong>{a.name}</strong></div><div className="machine-list-system">{agentSystem(a)}</div>{agentOnlyTag}</div>
                      </div>
                    </div>
                    <div className="machine-list-status">{agentStatusTag(a)}{a.online && liveTag(m)}</div>
                    <div className="machine-list-resource machine-list-cpu"><MetricBar label="CPU" percent={m?.cpu_percent ?? null} detail={m?.cores ? `${m.cores} 核` : undefined} /></div>
                    <div className="machine-list-resource machine-list-memory"><MetricBar label="内存" percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)} detail={ratioText(m?.mem_used ?? null, m?.mem_total ?? null)} /></div>
                    <div className="machine-list-resource machine-list-disk"><MetricBar label="磁盘" percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)} detail={ratioText(m?.disk_used ?? null, m?.disk_total ?? null)} /></div>
                    <div className="machine-list-actions">{agentActions(a, true)}</div>
                  </div>
                </div>
              );
            })}
        </div>
      ) : (
        <div className={`node-grid${view === "compact" ? " dense" : ""}`}>
          {shownNodes.map((n, cardIndex) => {
            const m = metrics[n.id];
            const caveat = memCaveat(m);
            const target = order.targetProps(n.id, visibleIDs);
            const sshAddress = `${n.ssh_user}@${n.host.includes(":") ? `[${n.host}]` : n.host}:${n.ssh_port}`;
            const ipv6 = ipv6DialAddress(n);
            return (
              <Card index={cardIndex} key={n.id} {...target} className={`machine-card tone-${toneOf(regionOf(n.name))} ${target.className}`}>
                <div className="machine-card-head">
                  <SortHandle {...order.handleProps(n.id, n.name, visibleIDs)} />
                  <MachineBadge name={n.name} />
                  <div className="machine-card-title">
                    <h2>{n.name}</h2>
                    <div className="machine-card-system" title={[m?.cpu_model, m?.kernel].filter(Boolean).join(" · ") || undefined}>{systemCell(n)}</div>
                  </div>
                  <div className="machine-card-status">
                    {statusTag(n)}
                    {liveTag(m)}
                    {m?.container && <span className="tag" title={caveat ?? "运行在容器里"}>{m.container}</span>}
                  </div>
                </div>

                <div className="machine-rings">
                  <MetricRing label="CPU" percent={m?.cpu_percent ?? null} detail={m?.cores ? `${m.cores} 核` : undefined} caveat={caveat} />
                  <MetricRing label="内存" percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)} detail={ratioText(m?.mem_used ?? null, m?.mem_total ?? null)} caveat={caveat} />
                  <MetricRing label="磁盘" percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)} detail={ratioText(m?.disk_used ?? null, m?.disk_total ?? null)} />
                  {m?.swap_total ? <MetricRing label="交换" percent={percentOf(m.swap_used, m.swap_total)} detail={ratioText(m.swap_used, m.swap_total)} /> : null}
                </div>

                <dl className="leader-rows">
                  <div><dt>网速</dt><dd>{m?.net_rx_rate !== null && m?.net_rx_rate !== undefined ? <><ArrowDown size={11} />{formatRate(m.net_rx_rate)} <ArrowUp size={11} />{formatRate(m.net_tx_rate ?? 0)}</> : "—"}</dd></div>
                  <div><dt>累计</dt><dd>{m?.net_rx_bytes !== null && m?.net_rx_bytes !== undefined ? `↓ ${formatBytes(m.net_rx_bytes)} ／ ↑ ${formatBytes(m.net_tx_bytes ?? 0)}` : "—"}</dd></div>
                  <div><dt>负载</dt><dd>{m?.load1 !== null && m?.load1 !== undefined ? `${m.load1.toFixed(2)} / ${m.load5?.toFixed(2) ?? "—"} / ${m.load15?.toFixed(2) ?? "—"}` : "—"}</dd></div>
                  <div><dt>开机</dt><dd>{m?.uptime_sec ? formatUptime(m.uptime_sec) : "—"}</dd></div>
                </dl>

                <div className="machine-chips">
                  <span className="machine-chip"><b>SSH</b><code>{sshAddress}</code><CopyIconButton text={sshAddress} title="复制 SSH 地址" /></span>
                  {ipv6 && <span className="machine-chip" title={v6Title(n)}><b>IPv6</b><code>{ipv6}</code>{n.ipv6_entry && <span className="tag">映射</span>}<CopyIconButton text={ipv6} title="复制 IPv6 地址" /></span>}
                  <span className="machine-chip"><b>端口池</b><code>{n.port_start}–{n.port_end}</code></span>
                  <span className="machine-chip"><b>UDP</b>{udpTag(n)}</span>
                  <span className="machine-chip"><b>跳板</b>{viaName(n)}</span>
                </div>

                {nodeActions(n, false)}
              </Card>
            );
          })}
          {shownAgents.map((a, i) => {
            const m = agentMetrics.get(a.id);
            const target = agentOrder.targetProps(a.id, visibleAgentIDs);
            const region = agentRegion(a);
            return (
              <Card index={shownNodes.length + i} key={a.id} {...target} className={`machine-card tone-${toneOf(region)} ${target.className}`}>
                <div className="machine-card-head">
                  <SortHandle {...agentOrder.handleProps(a.id, a.name, visibleAgentIDs)} />
                  <MachineBadge name={a.name} region={region} />
                  <div className="machine-card-title">
                    <h2>{a.name}</h2>
                    <div className="machine-card-system" title={m?.cpu_model || undefined}>{agentSystem(a)}</div>
                  </div>
                  <div className="machine-card-status">
                    {agentStatusTag(a)}
                    {a.online && liveTag(m)}
                  </div>
                </div>

                <div className="machine-rings">
                  <MetricRing label="CPU" percent={m?.cpu_percent ?? null} detail={m?.cores ? `${m.cores} 核` : undefined} />
                  <MetricRing label="内存" percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)} detail={ratioText(m?.mem_used ?? null, m?.mem_total ?? null)} />
                  <MetricRing label="磁盘" percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)} detail={ratioText(m?.disk_used ?? null, m?.disk_total ?? null)} />
                  {m?.swap_total ? <MetricRing label="交换" percent={percentOf(m.swap_used, m.swap_total)} detail={ratioText(m.swap_used, m.swap_total)} /> : null}
                </div>

                <dl className="leader-rows">
                  <div><dt>网速</dt><dd>{m ? <><ArrowDown size={11} />{formatRate(m.net_rx_rate ?? 0)} <ArrowUp size={11} />{formatRate(m.net_tx_rate ?? 0)}</> : "—"}</dd></div>
                  <div><dt>累计</dt><dd>{m ? `↓ ${formatBytes(m.net_rx_bytes ?? 0)} ／ ↑ ${formatBytes(m.net_tx_bytes ?? 0)}` : "—"}</dd></div>
                  <div><dt>负载</dt><dd>{m ? `${(m.load1 ?? 0).toFixed(2)} / ${(m.load5 ?? 0).toFixed(2)} / ${(m.load15 ?? 0).toFixed(2)}` : "—"}</dd></div>
                  <div><dt>开机</dt><dd>{m?.uptime_sec ? formatUptime(m.uptime_sec) : "—"}</dd></div>
                </dl>

                <div className="machine-chips">
                  {agentOnlyTag}
                  {a.info.ipv4 && <span className="machine-chip"><b>IPv4</b><code>{a.info.ipv4}</code><CopyIconButton text={a.info.ipv4} title="复制 IPv4 地址" /></span>}
                  {a.info.ipv6 && <span className="machine-chip"><b>IPv6</b><code>{a.info.ipv6}</code><CopyIconButton text={a.info.ipv6} title="复制 IPv6 地址" /></span>}
                </div>

                {agentActions(a, false)}
              </Card>
            );
          })}
        </div>
      )}

      {editingProbe && (
        <MachineDialog
          machine={editingProbe}
          nodes={nodes.map((n) => ({ id: n.id, name: n.name }))}
          onClose={() => setEditingProbe(null)}
          onSaved={() => {
            setEditingProbe(null);
            setNotice("已保存");
            void load();
          }}
        />
      )}

      {agentEnrolling && <AgentEnrollDialog onClose={() => setAgentEnrolling(false)} />}

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
          initial={enrollDraft ?? undefined}
          onClose={() => {
            setEnrolling(false);
            setEnrollDraft(null);
          }}
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
    <Drawer
      title={node ? `编辑机器 · ${node.name}` : "添加机器"}
      subtitle="面板通过 SSH 登录这台机器，下发并管理转发。"
      onClose={onClose}
    >
      {formError && <Banner kind="err">{formError}</Banner>}
      <form className="drawer-form" onSubmit={submit}>
        <FormBlock title="基本信息" icon={Server}>
          <label>
            名称
            <input
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder="随便起，支持中文"
              required
            />
          </label>
          <div className="grid2 address-pair">
            <label>
              主机地址
              <input className="mono" value={form.host} onChange={(e) => set("host", e.target.value)} required />
            </label>
            <label>
              SSH 端口
              <NumberField
                value={form.ssh_port}
                onChange={(v) => set("ssh_port", v)}
                required
              />
            </label>
          </div>
        </FormBlock>

        <FormBlock title="登录凭据" icon={KeyRound} note={node ? "密码或私钥不回显，留空表示不修改。" : undefined}>
          <label>
            登录用户
            <input
              value={form.ssh_user}
              onChange={(e) => set("ssh_user", e.target.value)}
              required
            />
          </label>
          <ChoiceGroup
            label="认证方式"
            value={form.auth_type}
            options={[
              { value: "password", label: "密码" },
              { value: "key", label: "私钥" },
            ]}
            onChange={(v) => set("auth_type", v)}
          />
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
        </FormBlock>

        <FormBlock title="端口池" icon={Hash} note="自动分配池内空闲端口；NAT 机器仅填写实际映射端口。">
          <div className="grid2">
            <label>
              起始
              <NumberField
                value={form.port_start}
                onChange={(v) => set("port_start", v)}
                required
              />
            </label>
            <label>
              结束
              <NumberField
                value={form.port_end}
                onChange={(v) => set("port_end", v)}
                required
              />
            </label>
          </div>
        </FormBlock>

        <FormBlock title="网络" icon={Network}>
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
          <p className="hint">仅填写服务商映射到本机 IPv4 的公网 IPv6 入口。原生 IPv6 留空。</p>

          <SwitchField
            label="跳过 UDP 检测"
            note="不使用 UDP 链路时可跳过。"
            checked={form.skip_udp_probe}
            onChange={(on) => set("skip_udp_probe", on)}
          />
        </FormBlock>

        <DrawerActions busy={busy} onCancel={onClose} />
      </form>
    </Drawer>
  );
}
