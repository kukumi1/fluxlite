import { useEffect, useRef, useState } from "react";
import { ChevronDown, Clock, Gauge, Info, Network, Pencil, Plus, QrCode, RefreshCw, RotateCcw, ScanSearch, Trash2, TriangleAlert, type LucideIcon } from "lucide-react";
import { api, type Node, type SingBoxDiscovery, type SingBoxPortMove, type SingBoxUser, type SingBoxUserInput } from "../api";
import { CopyButton } from "../components/CopyButton";
import { Flag } from "../components/Flag";
import { ListToolbar } from "../components/ListToolbar";
import { PageHeader } from "../components/PageHeader";
import { Banner, Modal } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import "./management-redesign.css";
import { formatBytes } from "../lib/format";
import { regionOf, toneOf } from "../lib/region";
import { normalizeSingBoxExport, type SingBoxAddressFamily, type SingBoxExportView } from "../lib/singbox-export";
import { CreateNodeDialog, protocolMeta, toDateTimeLocal } from "./SingBoxCreate";

function emptyInput(): SingBoxUserInput {
  return { node_id: 0, name: "", protocol: "vless-reality", cipher: "2022-blake3-aes-256-gcm", port: 0, quota_bytes: 0, access_days: 0, expires_at: "", upload_limit_mbps: 0, download_limit_mbps: 0 };
}

const DAY = 86_400_000;
const EXPIRING_WITHIN = 7 * DAY;

function statusLabel(user: SingBoxUser) {
  if (user.source === "external") return "外部节点";
  if (user.status === "exhausted") return "额度用尽";
  if (user.status === "expired") return "已过期";
  if (!user.enabled) return "已关闭";
  if (user.status === "running") return "在线";
  if (user.status === "stopped") return "离线";
  return "未知";
}

function hasExpiry(value: string | null): value is string {
  return !!value && !value.startsWith("0001-") && Number.isFinite(new Date(value).getTime());
}

function formatDateTime(value: string): string {
  if (!hasExpiry(value)) return "—";
  return new Date(value).toLocaleString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

function usageLabel(user: SingBoxUser, used: number, total: number): string {
  if (user.source === "external") return "—";
  if (total === 0) return used > 0 ? formatBytes(used) : "--";
  return `${formatBytes(used)} / ${formatBytes(total)}`;
}

function rateLabel(user: SingBoxUser): string {
  if (!user.upload_limit_bps && !user.download_limit_bps) return "未限速";
  return `↑ ${Math.round(user.upload_limit_bps / 1_000_000)} · ↓ ${Math.round(user.download_limit_bps / 1_000_000)} Mbps`;
}

function statusTone(user: SingBoxUser): "ok" | "err" | "warn" | "" {
  if (user.status === "exhausted" || user.status === "expired") return "err";
  if (user.enabled && user.status === "running") return "ok";
  if (user.enabled && user.status === "stopped") return "warn";
  return "";
}

/** 到期这件事最要紧的是「还有多久」，日期留给悬停。 */
function ExpiryChip({ value }: { value: string | null }) {
  if (!hasExpiry(value)) return <span className="expiry-chip">永久</span>;
  const left = new Date(value).getTime() - Date.now();
  const days = Math.ceil(left / DAY);
  const tone = left <= 0 ? "err" : left <= EXPIRING_WITHIN ? "warn" : "";
  return (
    <span className={`expiry-chip ${tone}`} title={formatDateTime(value)}>
      {left <= 0 ? `已过期 ${Math.abs(days)} 天` : days <= 1 ? "今天到期" : `${days} 天后到期`}
    </span>
  );
}

function ProtocolBadge({ protocol }: { protocol: SingBoxUser["protocol"] }) {
  const meta = protocolMeta(protocol);
  return <span className={`proto-badge tone-${meta.tone}`} title={meta.label}>{meta.short}</span>;
}

function WhereLine({ user }: { user: SingBoxUser }) {
  const code = regionOf(user.node_name);
  return (
    <span className="sb-where">
      {code && <Flag code={code} className="sb-where-flag" />}
      {user.node_name ?? `机器 #${user.node_id}`}
      <span className="sb-where-sep">·</span>
      <code>:{user.port}</code>
      <span className="sb-where-sep">·</span>
      {protocolMeta(user.protocol).label}
    </span>
  );
}

function SummaryFigure({ icon: Icon, tone, label, value, note }: { icon: LucideIcon; tone: string; label: string; value: number; note?: string }) {
  return (
    <div className={`tone-${tone}`}>
      <i><Icon size={16} /></i>
      <dt>{label}</dt>
      <dd>{value}{note && <small>{note}</small>}</dd>
    </div>
  );
}

export function SingBox() {
  const [creating, setCreating] = useState(false);
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [confirm, confirmDialog] = useConfirm();
  const [users, setUsers] = useState<SingBoxUser[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [discovered, setDiscovered] = useState<SingBoxDiscovery[]>([]);
  const [input, setInput] = useState<SingBoxUserInput>(emptyInput);
  const [exported, setExported] = useState<SingBoxExportView | null>(null);
  const [exportDialog, setExportDialog] = useState<{ id: number; family?: SingBoxAddressFamily; families: SingBoxAddressFamily[] } | null>(null);
  const [exportError, setExportError] = useState("");
  const exportRequest = useRef(0);
  const exportPending = useRef(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [warning, setWarning] = useState("");
  const [busy, setBusy] = useState(false);
  const [quotaDialog, setQuotaDialog] = useState<{ user: SingBoxUser; adopt: boolean; reduce: boolean } | null>(null);
  const [quotaGB, setQuotaGB] = useState("100");
  const [expiryAt, setExpiryAt] = useState("");
  const [infoUser, setInfoUser] = useState<SingBoxUser | null>(null);
  const [editUser, setEditUser] = useState<SingBoxUser | null>(null);
  const [editName, setEditName] = useState("");
  const [editPort, setEditPort] = useState("");
  const [editQuotaGB, setEditQuotaGB] = useState("");
  const [editPeriodDays, setEditPeriodDays] = useState("30");
  const [editEnabled, setEditEnabled] = useState(true);
  const [editUploadMbps, setEditUploadMbps] = useState("0");
  const [editDownloadMbps, setEditDownloadMbps] = useState("0");
  const [externalOpen, setExternalOpen] = useState(false);
  const [scanNodeID, setScanNodeID] = useState(0);

  async function load(selectDefaultNode = true) {
    try {
      const [u, n] = await Promise.all([api.listSingBoxUsers(), api.listNodes()]);
      setUsers(u ?? []); setNodes(n ?? []);
      if (selectDefaultNode && n?.length) setInput((old) => old.node_id ? old : ({ ...old, node_id: n[0].id }));
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  }

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    async function poll() {
      try {
        const [nextUsers, nextNodes] = await Promise.all([api.listSingBoxUsers(), api.listNodes()]);
        if (cancelled) return;
        setUsers(nextUsers ?? []);
        setNodes(nextNodes ?? []);
        if (nextNodes?.length) setInput((old) => old.node_id ? old : { ...old, node_id: nextNodes[0].id });
      } catch (failure) {
        if (!cancelled) setError(failure instanceof Error ? failure.message : String(failure));
      } finally {
        if (!cancelled) timer = window.setTimeout(() => void poll(), 5000);
      }
    }
    void poll();
    return () => { cancelled = true; window.clearTimeout(timer); exportRequest.current++; exportPending.current = false; };
  }, []);

  async function create() {
    if (!input.name.trim() || !input.node_id) { setError("请输入节点名称并选择机器"); return; }
    if (!Number.isInteger(input.port) || input.port < 0 || input.port > 65535) { setError("端口必须是 0-65535 的整数"); return; }
    if (!Number.isFinite(input.quota_bytes) || input.quota_bytes < 0) { setError("请输入有效流量额度"); return; }
    if (![input.upload_limit_mbps, input.download_limit_mbps].every((value) => Number.isInteger(value) && value >= 0 && value <= 100000)) { setError("限速必须是 0-100000 的整数 Mbps"); return; }
    if (input.expires_at && (!hasExpiry(input.expires_at) || new Date(input.expires_at).getTime() <= Date.now())) { setError("请选择未来的过期时间，或留空表示永久"); return; }
    setBusy(true); setError(""); setNotice("");
    try { await api.createSingBoxUser({ ...input, expires_at: input.expires_at ? new Date(input.expires_at).toISOString() : null }); setNotice("用户节点已创建"); setCreating(false); setInput((old) => ({ ...emptyInput(), node_id: old.node_id })); await load(); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  }

  async function action(fn: () => Promise<unknown>, ok: string) {
    setBusy(true); setError(""); setWarning("");
    try { await fn(); setNotice(ok); await load(); return true; }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); return false; }
    finally { setBusy(false); }
  }

  async function exportUser(id: number, family?: SingBoxAddressFamily) {
    if (!exportDialog && (busy || exportPending.current)) return;
    const request = ++exportRequest.current;
    exportPending.current = true;
    setExportDialog((current) => ({ id, family, families: current?.id === id ? current.families : [] }));
    setBusy(true); setExported(null); setExportError(""); setError(""); setNotice("");
    try {
      const out = normalizeSingBoxExport(await api.exportSingBoxUser(id, family));
      if (request === exportRequest.current) {
        setExported(out);
        setExportDialog({ id, family: out.addressFamily, families: out.availableFamilies });
      }
    } catch (e) {
      if (request === exportRequest.current) setExportError(e instanceof Error ? e.message : String(e));
    } finally {
      if (request === exportRequest.current) { exportPending.current = false; setBusy(false); }
    }
  }

  function closeExportDialog() {
    exportRequest.current++;
    exportPending.current = false;
    setBusy(false);
    setExported(null);
    setExportDialog(null);
    setExportError("");
  }

  function openQuotaDialog(user: SingBoxUser, adopt: boolean, reduce = false) {
    setError("");
    setQuotaGB("100");
    setQuotaDialog({ user, adopt, reduce });
  }

  function closeQuotaDialog() {
    if (busy) return;
    setQuotaDialog(null);
  }

  async function confirmQuotaDialog() {
    if (!quotaDialog) return;
    const gb = Number(quotaGB);
    if (!Number.isFinite(gb) || gb <= 0) { setError("请输入大于 0 的额度"); return; }
    const bytes = Math.round(gb * 1024 ** 3);
    let saved = false;
    if (quotaDialog.adopt) {
      saved = await action(() => api.adoptSingBoxUser(quotaDialog.user.id, bytes), "外部节点已接管");
    } else if (quotaDialog.reduce) {
      saved = await action(() => api.reduceSingBoxQuota(quotaDialog.user.id, bytes), "本周期额度已减少");
    } else {
      saved = await action(() => api.topUpSingBoxUser(quotaDialog.user.id, bytes), "本周期额度已增加");
    }
    if (saved) setQuotaDialog(null);
  }

  function openEditDialog(user: SingBoxUser) {
    setError("");
    setEditUser(user);
    setEditName(user.name);
    setEditPort(String(user.port));
    const currentQuota = user.base_quota_bytes + user.top_up_bytes;
    setEditQuotaGB(currentQuota ? String(currentQuota / 1024 ** 3) : "");
    setEditPeriodDays(String(user.period_days || 30));
    setExpiryAt(hasExpiry(user.expires_at) ? toDateTimeLocal(new Date(user.expires_at)) : "");
    setEditEnabled(user.enabled);
    setEditUploadMbps(String(Math.round(user.upload_limit_bps / 1_000_000)));
    setEditDownloadMbps(String(Math.round(user.download_limit_bps / 1_000_000)));
  }

  async function saveEditDialog() {
    if (!editUser) return;
    const expiry = new Date(expiryAt);
    if (expiryAt && (!Number.isFinite(expiry.getTime()) || expiry.getTime() <= Date.now())) { setError("请选择未来的过期时间"); return; }
    if (!editName.trim()) { setError("请输入节点名称"); return; }
    const quota = editQuotaGB === "" ? 0 : Number(editQuotaGB) * 1024 ** 3;
    if (!Number.isFinite(quota) || quota < 0) { setError("请输入有效额度"); return; }
    const periodDays = Number(editPeriodDays);
    if (!Number.isInteger(periodDays) || periodDays < 1 || periodDays > 3650) { setError("流量周期必须是 1-3650 天"); return; }
    const upload = Number(editUploadMbps), download = Number(editDownloadMbps);
    if (![upload, download].every((value) => Number.isInteger(value) && value >= 0 && value <= 100000)) { setError("限速必须是 0-100000 的整数 Mbps"); return; }
    const port = Number(editPort);
    if (!Number.isInteger(port) || port < 1 || port > 65535) { setError("端口必须是 1-65535 的整数"); return; }
    const update = { name: editName.trim(), quota_bytes: Math.round(quota), period_days: periodDays, expires_at: expiryAt ? (hasExpiry(editUser.expires_at) && expiryAt === toDateTimeLocal(new Date(editUser.expires_at)) ? editUser.expires_at : expiry.toISOString()) : null, enabled: editEnabled, upload_limit_mbps: upload, download_limit_mbps: download };
    if (port === editUser.port) {
      if (await action(() => api.updateSingBoxUser(editUser.id, update), "客户端已更新")) setEditUser(null);
      return;
    }
    // 端口先单独换：它要在机器上重写配置并重启服务，失败时机器会退回旧端口，
    // 其余设置也就不该再提交。
    setBusy(true); setError(""); setNotice(""); setWarning("");
    let moved: SingBoxPortMove | null = null;
    try {
      moved = await api.changeSingBoxPort(editUser.id, port);
      await api.updateSingBoxUser(editUser.id, update);
      setNotice(`端口已改为 ${moved.port}，客户端里的端口也要跟着改成 ${moved.port}`);
      const landing = moved.landing_routes ?? [];
      if (landing.length) setWarning(`链路 ${landing.join("、")} 的落地地址还是旧端口 ${editUser.port}，请到链路页把落地地址改成新端口 ${moved.port}，否则这些链路会不通。`);
      setEditUser(null);
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setError(moved ? `端口已改为 ${moved.port}，但其他设置保存失败：${message}` : message);
    } finally {
      setBusy(false);
      await load();
    }
  }

  async function scanExternal() {
    if (!scanNodeID) return;
    setBusy(true); setError("");
    try { setDiscovered((await api.scanSingBox(scanNodeID)) ?? []); setExternalOpen(true); await load(); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  }

  async function removeUser(user: SingBoxUser) {
    if (!await confirm({ title: user.source === "external" ? "移除扫描记录？" : "删除节点？", body: user.source === "external" ? "只移除面板记录，不会修改机器上的配置或服务。" : `将停止并删除 ${user.name} 在机器上的独立服务和配置，此操作不可撤销。`, danger: true, confirmLabel: "确认删除" })) return;
    await action(async () => { await api.deleteSingBoxUser(user.id); setDiscovered((items) => items.filter((item) => item.node_id !== user.node_id || item.port !== user.port)); }, "节点记录已删除");
  }

  async function resetUser(user: SingBoxUser) {
    if (!await confirm({ title: "清零本周期流量？", body: `将清零 ${user.name} 的已用流量，额度、到期时间及周期结束时间保持不变。`, danger: true, confirmLabel: "确认清零" })) return;
    await action(() => api.resetSingBoxUser(user.id), "流量已重置");
  }

  function managedCard(user: SingBoxUser) {
    const used = user.used_in + user.used_out;
    const total = user.base_quota_bytes + user.top_up_bytes;
    const pct = total > 0 ? Math.min(100, (used / total) * 100) : null;
    const barTone = pct === null ? "" : pct >= 100 ? "err" : pct >= 85 ? "warn" : "";
    const periodLeft = Math.max(0, Math.ceil((new Date(user.period_ends_at).getTime() - Date.now()) / DAY));
    const locked = user.status === "expired" || user.status === "exhausted";
    return (
      <article className={`sb-card tone-${toneOf(regionOf(user.node_name))}${user.enabled ? "" : " is-off"}`} key={user.id}>
        <header className="sb-card-head">
          <ProtocolBadge protocol={user.protocol} />
          <div className="sb-card-title">
            <h3>{user.name}</h3>
            <WhereLine user={user} />
          </div>
          <span className={`pill ${statusTone(user)}`}><i />{statusLabel(user)}</span>
          <button
            className={`toggle-switch ${user.enabled ? "on" : ""}`}
            role="switch"
            aria-checked={user.enabled}
            aria-label={user.enabled ? "关闭节点" : "开启节点"}
            title={locked ? "额度用尽或已过期，先加量或延期" : user.enabled ? "关闭节点" : "开启节点"}
            disabled={busy || locked}
            onClick={() => void action(() => user.enabled ? api.stopSingBoxUser(user.id) : api.startSingBoxUser(user.id), user.enabled ? "已关闭" : "已开启")}
          >
            <span />
          </button>
        </header>

        <div className="sb-usage">
          <div className="sb-usage-figure">
            <b>{formatBytes(used)}</b>
            <span>{total > 0 ? `/ ${formatBytes(total)}` : "不限额度"}</span>
            {pct !== null && <em className={barTone}>{pct.toFixed(0)}%</em>}
          </div>
          <div className="progress">
            {pct !== null && <div className={`progress-bar ${barTone}`} style={{ width: `${Math.max(pct, 1)}%` }} />}
          </div>
          <p className="sb-usage-meta" title={`本周期 ${formatDateTime(user.period_started_at)} 起，${formatDateTime(user.period_ends_at)} 结束`}>
            本周期还剩 {periodLeft} 天 · {user.period_days} 天一周期
          </p>
        </div>

        <dl className="leader-rows">
          <div><dt>到期</dt><dd><ExpiryChip value={user.expires_at} /></dd></div>
          <div><dt>限速</dt><dd>{rateLabel(user)}{user.rate_limit_error && <span className="tag err" title={user.rate_limit_error}>未生效</span>}</dd></div>
        </dl>

        <div className="sb-actions">
          <div className="sb-actions-main">
            <button className="btn sm export-btn" disabled={busy} onClick={() => void exportUser(user.id)}><QrCode size={14} />导出</button>
            <button className="btn sm" disabled={busy} onClick={() => openQuotaDialog(user, false)}>加量</button>
            <button className="btn sm" disabled={busy || total === 0} onClick={() => openQuotaDialog(user, false, true)}>减量</button>
          </div>
          <div className="sb-actions-side">
            <button className="btn sm ghost icon-action" aria-label="客户端信息" title="客户端信息" disabled={busy} onClick={() => setInfoUser(user)}><Info size={15} /></button>
            <button className="btn sm ghost icon-action" aria-label="清零本周期流量" title="清零本周期流量" disabled={busy} onClick={() => void resetUser(user)}><RotateCcw size={15} /></button>
            <button className="btn sm ghost icon-action" aria-label="编辑" title="编辑" disabled={busy} onClick={() => openEditDialog(user)}><Pencil size={15} /></button>
            <button className="btn sm ghost icon-action danger" aria-label="删除节点" title="删除节点" disabled={busy} onClick={() => void removeUser(user)}><Trash2 size={15} /></button>
          </div>
        </div>
      </article>
    );
  }

  const managedUsers = users.filter((user) => user.source === "managed");
  const externalUsers = users.filter((user) => user.source === "external");

  const searchMatches = (user: SingBoxUser) =>
    `${user.name} ${user.node_name ?? ""} ${user.protocol} ${user.port}`.toLowerCase().includes(search.trim().toLowerCase());
  const filterMatches = (user: SingBoxUser, value: string) =>
    value === "all" || (value === "enabled" ? user.enabled : value === "disabled" ? !user.enabled : user.status === value && (value !== "running" || user.enabled));
  const matches = (user: SingBoxUser) => searchMatches(user) && filterMatches(user, filter);
  const searchedManaged = managedUsers.filter(searchMatches);
  const countOf = (value: string) => searchedManaged.filter((user) => filterMatches(user, value)).length;
  const filterOptions = [
    { value: "all", label: "全部", count: searchedManaged.length },
    { value: "running", label: "在线", count: countOf("running"), tone: "ok" as const },
    { value: "stopped", label: "离线", count: countOf("stopped"), tone: "warn" as const },
    { value: "disabled", label: "已关闭", count: countOf("disabled"), tone: "off" as const },
    { value: "exhausted", label: "额度用尽", count: countOf("exhausted"), tone: "err" as const },
    { value: "expired", label: "已过期", count: countOf("expired"), tone: "err" as const },
  ].filter((option) => option.value === "all" || option.value === "running" || option.count > 0 || option.value === filter);
  const shownManaged = managedUsers.filter(matches);
  const expiringSoon = managedUsers.filter((user) => hasExpiry(user.expires_at) && new Date(user.expires_at).getTime() > Date.now() && new Date(user.expires_at).getTime() - Date.now() <= EXPIRING_WITHIN).length;

  return (
    <div className="management-page singbox-management">
      <PageHeader title="节点" actions={<button className="btn primary" onClick={() => { setError(""); setCreating(true); }}><Plus size={15} />创建节点</button>} />
      {error && !creating && !editUser && !quotaDialog && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}
      {warning && <Banner kind="warn">{warning}</Banner>}

      <dl className="stat-strip">
        <SummaryFigure icon={Network} tone="lime" label="在线" value={managedUsers.filter((user) => user.enabled && user.status === "running").length} note={`/ ${managedUsers.length} 个`} />
        <SummaryFigure icon={Gauge} tone="peach" label="额度用尽" value={managedUsers.filter((user) => user.status === "exhausted").length} />
        <SummaryFigure icon={Clock} tone="sun" label="7 天内到期" value={expiringSoon} />
        <SummaryFigure icon={TriangleAlert} tone="lilac" label="已过期" value={managedUsers.filter((user) => user.status === "expired").length} />
      </dl>

      <ListToolbar
        search={search}
        onSearch={setSearch}
        searchLabel="搜索节点"
        placeholder="搜索名称、机器、协议或端口…"
        filterLabel="筛选节点状态"
        filter={filter}
        onFilter={setFilter}
        filters={filterOptions}
      />

      <section className="sb-section">
        <header className="sb-section-head">
          <h2>面板创建<b>{managedUsers.length}</b></h2>
          <button className="icon-btn" aria-label="刷新" title="刷新" onClick={() => void load()}><RefreshCw size={15} /></button>
        </header>
        {shownManaged.length > 0
          ? <div className="sb-grid">{shownManaged.map(managedCard)}</div>
          : <p className="sb-empty">{managedUsers.length ? "没有符合筛选条件的节点。" : "还没有节点，点右上角「创建节点」部署第一个。"}</p>}
      </section>

      <section className="sb-section">
        <header className="sb-section-head">
          <button className="sb-section-toggle" aria-expanded={externalOpen} onClick={() => setExternalOpen((open) => !open)}>
            <ChevronDown size={16} />
            <h2>外部扫描<b>{externalUsers.length}</b></h2>
          </button>
          <div className="sb-scan">
            <select aria-label="选择要扫描的机器" value={scanNodeID} onChange={(event) => setScanNodeID(Number(event.target.value))}>
              <option value={0}>选择机器</option>
              {nodes.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}
            </select>
            <button className="btn" disabled={busy || !scanNodeID} onClick={() => void scanExternal()}><ScanSearch size={15} />扫描已有节点</button>
          </div>
        </header>
        {externalOpen && <>
          <p className="sb-section-note">机器上已有、不是面板建的 sing-box 入站。扫描和移除记录都不会改动机器上的配置；接管后才由面板管理。</p>
          {discovered.length > 0 && (
            <div className="sb-discovered">
              <span>本次扫描发现</span>
              {discovered.map((d) => (
                <span key={`${d.path}-${d.port}`} className="sb-discovered-item" title={d.path}>
                  <ProtocolBadge protocol={d.protocol} />{d.tag}<code>:{d.port}</code><i className={d.running ? "ok" : ""} />
                </span>
              ))}
            </div>
          )}
          {externalUsers.filter(matches).length > 0 ? (
            <div className="sb-external">
              {externalUsers.filter(matches).map((user) => (
                <div className="sb-external-row" key={user.id}>
                  <ProtocolBadge protocol={user.protocol} />
                  <div className="sb-card-title"><h3>{user.name}</h3><WhereLine user={user} /></div>
                  <button className="btn sm" disabled={busy} onClick={() => openQuotaDialog(user, true)}>接管</button>
                  <button className="btn sm ghost icon-action danger" aria-label="移除记录" title="只从面板移除记录，不删除远程配置" disabled={busy} onClick={() => void removeUser(user)}><Trash2 size={15} /></button>
                </div>
              ))}
            </div>
          ) : <p className="sb-empty">暂无外部节点，选一台机器扫描看看。</p>}
        </>}
      </section>

      {creating && (
        <CreateNodeDialog
          nodes={nodes}
          input={input}
          onChange={setInput}
          busy={busy}
          error={error}
          onCreate={() => void create()}
          onClose={() => setCreating(false)}
        />
      )}

      {exportDialog && (
        <Modal title="导出节点" subtitle="扫码导入，或复制链接 / 客户端配置。" size="wide" onClose={closeExportDialog}>
          <div className="sb-export">
            {exportDialog.families.length > 1 && (
              <div className="segmented labels" role="radiogroup" aria-label="导出地址">
                {exportDialog.families.map((family) => (
                  <button
                    key={family}
                    type="button"
                    role="radio"
                    aria-checked={exportDialog.family === family}
                    className={exportDialog.family === family ? "active" : ""}
                    onClick={() => void exportUser(exportDialog.id, family)}
                  >
                    {family === "ipv4" ? "IPv4 地址" : "IPv6 地址"}
                  </button>
                ))}
              </div>
            )}
            {exportError ? <><Banner kind="err">{exportError}</Banner><button className="btn" onClick={() => void exportUser(exportDialog.id, exportDialog.family)}>重试</button></> : !exported && <p className="muted" role="status">正在生成节点链接、二维码与客户端配置…</p>}
            {exported && <>
              {exported.warnings.map((warning, index) => <Banner kind="warn" key={index}>{warning}</Banner>)}
              <div className="sb-export-main">
                <div className="sb-qr">
                  {exported.qr
                    ? <img src={exported.qr} width={220} height={220} alt="节点二维码" onError={() => setExported((current) => current?.qr === exported.qr && current?.link === exported.link ? { ...current, qr: undefined, qrError: "二维码加载失败，仍可复制节点链接或客户端配置导入。" } : current)} />
                    : <p>{exported.qrError ?? "没有二维码"}</p>}
                </div>
                <div className="term-block sb-link">
                  <div className="terminal-bar">
                    <span className="terminal-dots" aria-hidden><i /><i /><i /></span>
                    <span className="terminal-title">节点链接</span>
                    <CopyButton text={exported.link} label="复制链接" />
                  </div>
                  <code className="terminal-body">{exported.link}</code>
                </div>
              </div>
              <details className="advanced sb-config">
                <summary>客户端配置（JSON）<span className="advanced-summary">sing-box outbound</span></summary>
                <div className="sb-config-actions"><CopyButton text={exported.config} label="复制客户端配置" /></div>
                <pre className="code-block" tabIndex={0} aria-label="客户端 JSON 配置">{exported.config}</pre>
              </details>
            </>}
          </div>
        </Modal>
      )}

      {infoUser && (
        <Modal title={infoUser.name} subtitle="客户端信息" onClose={() => setInfoUser(null)}>
          <dl className="leader-rows single">
            <div><dt>机器</dt><dd>{infoUser.node_name ?? infoUser.node_id}</dd></div>
            <div><dt>协议</dt><dd>{protocolMeta(infoUser.protocol).label}</dd></div>
            <div><dt>端口</dt><dd>{infoUser.port}</dd></div>
            <div><dt>流量</dt><dd>{usageLabel(infoUser, infoUser.used_in + infoUser.used_out, infoUser.base_quota_bytes + infoUser.top_up_bytes)}</dd></div>
            <div><dt>连接有效期</dt><dd>{hasExpiry(infoUser.expires_at) ? formatDateTime(infoUser.expires_at) : "永久"}</dd></div>
            <div><dt>周期结束</dt><dd>{formatDateTime(infoUser.period_ends_at)}</dd></div>
            <div><dt>限速</dt><dd>{rateLabel(infoUser)}</dd></div>
            <div><dt>服务</dt><dd>{infoUser.service_name}</dd></div>
          </dl>
        </Modal>
      )}

      {quotaDialog && (
        <Modal
          title={quotaDialog.adopt ? "接管外部节点" : quotaDialog.reduce ? "减少本周期额度" : "增加本周期额度"}
          subtitle={`${quotaDialog.user.name} · ${quotaDialog.user.node_name ?? ""}`}
          onClose={closeQuotaDialog}
        >
          <form className="dialog-form" onSubmit={(event) => { event.preventDefault(); void confirmQuotaDialog(); }}>
            {error && <Banner kind="err">{error}</Banner>}
            <p className="field-hint flush">{quotaDialog.adopt ? "面板会读取当前服务端配置，备份原片段并迁移到独立服务。" : quotaDialog.reduce ? "不能减少到低于已使用流量；无限额度不能直接减量。" : "周期结束时间和已用流量保持不变。"}</p>
            <div className="field">
              <span className="field-label">额度</span>
              <div className="preset-row">
                {["10", "50", "100", "200", "500"].map((gb) => (
                  <button key={gb} type="button" className={quotaGB === gb ? "active" : ""} onClick={() => setQuotaGB(gb)}>{gb} GB</button>
                ))}
                <label className="preset-custom">
                  <input autoFocus type="number" min={1} aria-label="额度（GB）" value={quotaGB} disabled={busy} onChange={(e) => setQuotaGB(e.target.value)} />
                  <span>GB</span>
                </label>
              </div>
            </div>
            <div className="modal-actions">
              <button type="button" className="btn ghost" disabled={busy} onClick={closeQuotaDialog}>取消</button>
              <button className="btn primary" disabled={busy}>{quotaDialog.adopt ? "接管" : quotaDialog.reduce ? "减少" : "增加"}</button>
            </div>
          </form>
        </Modal>
      )}

      {editUser && (
        <Modal title="编辑节点" subtitle={`${protocolMeta(editUser.protocol).label} · ${editUser.node_name ?? ""} : ${editUser.port}`} size="wide" onClose={() => setEditUser(null)}>
          <form className="dialog-form" onSubmit={(event) => { event.preventDefault(); void saveEditDialog(); }}>
            {error && <Banner kind="err">{error}</Banner>}
            <div className="advanced-grid two">
              <label>节点名称<input autoFocus value={editName} onChange={(e) => setEditName(e.target.value)} /></label>
              <label>端口<input type="number" min={1} max={65535} value={editPort} onChange={(e) => setEditPort(e.target.value)} /></label>
            </div>
            {editPort !== String(editUser.port) && <p className="field-hint flush">改端口会重启这个节点的服务，正在连接的客户端会断开一次；客户端和以它为落地的链路都要改成新端口。</p>}
            <div className="advanced-grid two">
              <label>基础额度（GB）<input type="number" min={0} placeholder="不限" value={editQuotaGB} onChange={(e) => setEditQuotaGB(e.target.value)} /></label>
              <label>流量周期（天）<input type="number" min={1} max={3650} value={editPeriodDays} onChange={(e) => setEditPeriodDays(e.target.value)} /></label>
              <label>上行限速（Mbps）<input type="number" min={0} max={100000} placeholder="不限" value={editUploadMbps === "0" ? "" : editUploadMbps} onChange={(e) => setEditUploadMbps(e.target.value || "0")} /></label>
              <label>下行限速（Mbps）<input type="number" min={0} max={100000} placeholder="不限" value={editDownloadMbps === "0" ? "" : editDownloadMbps} onChange={(e) => setEditDownloadMbps(e.target.value || "0")} /></label>
            </div>
            <label>连接过期时间<input type="datetime-local" value={expiryAt} onChange={(e) => setExpiryAt(e.target.value)} /></label>
            <p className="field-hint flush">留空表示永久。</p>
            <div className="switch-row">
              <div><strong>启用节点</strong><span>关闭后机器上的服务停止，配置保留。</span></div>
              <button type="button" className={`toggle-switch ${editEnabled ? "on" : ""}`} role="switch" aria-checked={editEnabled} aria-label="启用节点" onClick={() => setEditEnabled((on) => !on)}><span /></button>
            </div>
            <div className="modal-actions">
              <button type="button" className="btn ghost" onClick={() => setEditUser(null)}>取消</button>
              <button className="btn primary" disabled={busy}>保存</button>
            </div>
          </form>
        </Modal>
      )}
      {confirmDialog}
    </div>
  );
}
