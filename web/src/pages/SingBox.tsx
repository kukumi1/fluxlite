import { useEffect, useState } from "react";
import { ChevronDown, ChevronRight, Copy, Pencil, Plus, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { api, type Node, type SingBoxCipher, type SingBoxDiscovery, type SingBoxProtocol, type SingBoxUser, type SingBoxUserInput } from "../api";
import { Card } from "../components/Card";
import { CopyButton } from "../components/CopyButton";
import { PageHeader } from "../components/PageHeader";
import { Banner, Modal } from "../components/Modal";
import { formatBytes } from "../lib/format";

const protocols: Array<[SingBoxProtocol, string]> = [
  ["ss2022", "SS2022"], ["anytls", "AnyTLS"], ["vless-reality", "VLESS + Reality"],
  ["hysteria2", "Hysteria2"], ["tuic", "TUIC"], ["trojan", "Trojan"],
  ["vmess", "VMess"], ["vless-tls", "VLESS + TLS"],
];

function toDateTimeLocal(date: Date): string {
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
  return local.toISOString().slice(0, 16);
}

function emptyInput(): SingBoxUserInput {
  return { node_id: 0, name: "", protocol: "ss2022", cipher: "2022-blake3-aes-256-gcm", port: 0, quota_bytes: 0, access_days: 0, expires_at: "" };
}

const ciphers: Array<[SingBoxCipher, string]> = [
  ["2022-blake3-aes-128-gcm", "AES-128-GCM"],
  ["2022-blake3-aes-256-gcm", "AES-256-GCM"],
  ["2022-blake3-chacha20-poly1305", "ChaCha20-Poly1305"],
];

function statusLabel(user: SingBoxUser) {
  if (user.source === "external") return "外部节点";
  if (user.status === "exhausted") return "额度用尽";
  if (user.status === "expired") return "已过期";
  if (user.status === "running") return "在线";
  if (user.status === "stopped") return "离线";
  return "未知";
}

function formatDateTime(value: string): string {
  return new Date(value).toLocaleString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

function usageLabel(user: SingBoxUser, used: number, total: number): string {
  if (user.source === "external") return "—";
  if (total === 0) return used > 0 ? formatBytes(used) : "--";
  return `${formatBytes(used)} / ${formatBytes(total)}`;
}

export function SingBox() {
  const [users, setUsers] = useState<SingBoxUser[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [discovered, setDiscovered] = useState<SingBoxDiscovery[]>([]);
  const [input, setInput] = useState<SingBoxUserInput>(emptyInput);
  const [exported, setExported] = useState<{ link: string; config: string; qr?: string } | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [quotaDialog, setQuotaDialog] = useState<{ user: SingBoxUser; adopt: boolean } | null>(null);
  const [quotaGB, setQuotaGB] = useState("100");
  const [expiryDialog, setExpiryDialog] = useState<SingBoxUser | null>(null);
  const [expiryAt, setExpiryAt] = useState("");
  const [managedOpen, setManagedOpen] = useState(true);
  const [externalOpen, setExternalOpen] = useState(false);
  const [discoveredOpen, setDiscoveredOpen] = useState(false);

  async function load() {
    try {
      const [u, n] = await Promise.all([api.listSingBoxUsers(), api.listNodes()]);
      setUsers(u ?? []); setNodes(n ?? []);
      if (!input.node_id && n?.length) setInput((old) => ({ ...old, node_id: n[0].id }));
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  }

  useEffect(() => { void load(); }, []);

  async function create() {
    setBusy(true); setError(""); setNotice("");
    try { await api.createSingBoxUser({ ...input, expires_at: input.expires_at ? new Date(input.expires_at).toISOString() : null }); setNotice("用户节点已创建"); setInput((old) => ({ ...emptyInput(), node_id: old.node_id })); await load(); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  }

  async function action(fn: () => Promise<unknown>, ok: string) {
    setBusy(true); setError("");
    try { await fn(); setNotice(ok); await load(); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  }

  async function exportUser(id: number) {
    try { const out = await api.exportSingBoxUser(id); setExported({ link: out.link, config: JSON.stringify(out.config, null, 2), qr: out.qr_data_url }); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
  }

  function openQuotaDialog(user: SingBoxUser, adopt: boolean) {
    setQuotaGB("100");
    setQuotaDialog({ user, adopt });
  }

  async function confirmQuotaDialog() {
    if (!quotaDialog) return;
    const gb = Number(quotaGB);
    if (!Number.isFinite(gb) || gb <= 0) { setError("请输入大于 0 的额度"); return; }
    const bytes = Math.round(gb * 1024 ** 3);
    if (quotaDialog.adopt) {
      await action(() => api.adoptSingBoxUser(quotaDialog.user.id, bytes), "外部节点已接管");
    } else {
      await action(() => api.topUpSingBoxUser(quotaDialog.user.id, bytes), "本周期额度已增加");
    }
    setQuotaDialog(null);
  }

  function openExpiryDialog(user: SingBoxUser) { setExpiryAt(user.expires_at ? toDateTimeLocal(new Date(user.expires_at)) : ""); setExpiryDialog(user); }

  async function confirmExpiryDialog() {
    if (!expiryDialog) return;
    const expiresAt = new Date(expiryAt);
    if (expiryAt && (!Number.isFinite(expiresAt.getTime()) || expiresAt.getTime() <= Date.now())) { setError("请选择未来的过期时间"); return; }
    await action(() => api.setSingBoxExpiry(expiryDialog.id, expiryAt ? expiresAt.toISOString() : null), "连接有效期已更新");
    setExpiryDialog(null);
  }

  async function scanExternal() {
    if (!input.node_id) return;
    setBusy(true); setError("");
    try { setDiscovered((await api.scanSingBox(input.node_id)) ?? []); setDiscoveredOpen(true); setExternalOpen(true); await load(); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  }

  const managedUsers = users.filter((user) => user.source === "managed");
  const externalUsers = users.filter((user) => user.source === "external");

  function userTable(list: SingBoxUser[]) {
    if (list.length === 0) return <p className="muted">暂无节点。</p>;
    return (
      <div className="singbox-user-list">{list.map((user) => {
            const used = user.used_in + user.used_out;
            const total = user.base_quota_bytes + user.top_up_bytes;
            return (
              <article className="singbox-user-card" key={user.id}>
                <div className="singbox-user-head">
                  <div className="singbox-user-title"><strong>{user.name}</strong><span className="muted">{protocols.find(([protocol]) => protocol === user.protocol)?.[1] ?? user.protocol}</span><span className={`tag ${user.status === "running" ? "ok" : user.status === "exhausted" || user.status === "expired" ? "err" : ""}`}>{statusLabel(user)}</span></div>
                  <div className="singbox-actions">
                    {user.source === "external" ? <>
                      <button className="btn sm" disabled={busy} onClick={() => openQuotaDialog(user, true)}>接管</button>
                      <button className="btn sm danger" disabled={busy} title="只从面板移除记录，不删除远程配置" onClick={() => void action(async () => { await api.deleteSingBoxUser(user.id); setDiscovered((items) => items.filter((item) => item.node_id !== user.node_id || item.port !== user.port)); }, "外部节点记录已移除")}>移除记录</button>
                    </> : <>
                      <button className={`toggle-switch ${user.enabled ? "on" : ""}`} aria-label={user.enabled ? "关闭节点" : "开启节点"} aria-pressed={user.enabled} disabled={busy || user.status === "expired" || user.status === "exhausted"} onClick={() => void action(() => user.enabled ? api.stopSingBoxUser(user.id) : api.startSingBoxUser(user.id), user.enabled ? "已关闭" : "已开启")}><span /></button>
                      <button className="btn sm" disabled={busy} onClick={() => openExpiryDialog(user)}><Pencil size={13} />编辑</button>
                      <button className="btn sm" disabled={busy} onClick={() => void exportUser(user.id)}><Copy size={13} />导出</button>
                      <button className="btn sm" disabled={busy} onClick={() => openQuotaDialog(user, false)}>加量</button>
                      <button className="btn sm" disabled={busy} onClick={() => void action(() => api.resetSingBoxUser(user.id), "已清零重置")}><RotateCcw size={13} />清零</button>
                      <button className="btn sm danger" disabled={busy} onClick={() => void action(() => api.deleteSingBoxUser(user.id), "已删除")}><Trash2 size={13} /></button>
                    </>}
                  </div>
                </div>
                <div className="singbox-user-meta">
                  <div><span>入口</span><strong>{user.node_name ?? user.node_id}:{user.port}</strong></div>
                  <div><span>已用流量</span><strong>{usageLabel(user, used, total)}</strong></div>
                  <div><span>连接有效期</span><strong>{user.source === "external" ? "—" : user.expires_at ? formatDateTime(user.expires_at) : "永久"}</strong></div>
                  <div><span>流量周期结束</span><strong>{user.source === "external" ? "—" : formatDateTime(user.period_ends_at)}</strong></div>
                </div>
              </article>
            );
          })}</div>
    );
  }

  return (
    <>
      <PageHeader title="sing-box 用户节点" desc="每个用户独立服务、独立端口和独立流量额度。" />
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}
      <Card>
        <div className="spread"><h2 style={{ margin: 0 }}>创建用户节点</h2><span className="muted">自签 TLS · 流量每 30 天结算</span></div>
        <div className="singbox-create-grid" style={{ marginTop: 16 }}>
          <label>用户名称<input value={input.name} onChange={(e) => setInput({ ...input, name: e.target.value })} placeholder="例如 Alice" /></label>
          <label>部署 VPS<select value={input.node_id} onChange={(e) => setInput({ ...input, node_id: Number(e.target.value) })}>{nodes.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}</select></label>
          <label>协议<select value={input.protocol} onChange={(e) => setInput({ ...input, protocol: e.target.value as SingBoxProtocol })}>{protocols.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          {input.protocol === "ss2022" && <label>SS2022 加密方法<select value={input.cipher} onChange={(e) => setInput({ ...input, cipher: e.target.value as SingBoxCipher })}>{ciphers.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>}
          {input.protocol === "anytls" && <label>SNI<input value="developer.apple.com" readOnly /></label>}
          <label>端口（留空=自动）<input type="number" min={0} max={65535} placeholder="0=自动" value={input.port || ""} onChange={(e) => setInput({ ...input, port: e.target.value === "" ? 0 : Number(e.target.value) })} /></label>
          <label>基础额度（GB，留空=无限）<input type="number" min={0} placeholder="无限制" value={input.quota_bytes ? Math.round(input.quota_bytes / 1024 ** 3) : ""} onChange={(e) => setInput({ ...input, quota_bytes: e.target.value === "" ? 0 : Math.max(0, Number(e.target.value)) * 1024 ** 3 })} /></label>
          <label>连接过期时间（留空=永久）<input type="datetime-local" value={input.expires_at ?? ""} onChange={(e) => setInput({ ...input, expires_at: e.target.value })} /></label>
        </div>
        <div className="row" style={{ marginTop: 16 }}><button className="btn primary" disabled={busy || !input.name || !input.node_id} onClick={() => void create()}><Plus size={14} />创建并部署</button><button className="btn" disabled={busy || !input.node_id} onClick={() => void scanExternal()}><RefreshCw size={14} />扫描已有节点</button></div>
      </Card>
      {discovered.length > 0 && <Card>
        <div className="spread"><button className="btn sm" onClick={() => setDiscoveredOpen((open) => !open)}>{discoveredOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}扫描结果（{discovered.length}）</button></div>
        {discoveredOpen && <><p className="muted">只读发现，不会修改 `/etc/sing-box/conf.d/`。接管前请先确认端口和客户端配置。</p><div className="table-wrap"><table><thead><tr><th>标签</th><th>协议</th><th>端口</th><th>服务</th><th>配置片段</th></tr></thead><tbody>{discovered.map((d) => <tr key={`${d.path}-${d.port}`}><td>{d.tag}</td><td>{d.protocol}</td><td>{d.port}</td><td>{d.running ? "运行中" : "停止"}</td><td><code>{d.path}</code></td></tr>)}</tbody></table></div></>}
      </Card>}
      <Card>
        <div className="spread"><button className="btn sm" onClick={() => setManagedOpen((open) => !open)}>{managedOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}面板创建节点（{managedUsers.length}）</button><button className="btn sm" onClick={() => void load()}><RefreshCw size={13} />刷新</button></div>
        {managedOpen && userTable(managedUsers)}
      </Card>
      <Card>
        <div className="spread"><button className="btn sm" onClick={() => setExternalOpen((open) => !open)}>{externalOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}外部扫描节点（{externalUsers.length}）</button><span className="muted">移除记录不会删除远程配置</span></div>
        {externalOpen && userTable(externalUsers)}
      </Card>
      {exported && <Card><div className="spread"><h2 style={{ margin: 0 }}>节点配置</h2><button className="btn sm" onClick={() => setExported(null)}>关闭</button></div>{exported.qr && <img src={exported.qr} width={192} height={192} alt="节点二维码" style={{ marginTop: 16, borderRadius: 12 }} />}<p className="muted">链接</p><div className="row"><code style={{ wordBreak: "break-all" }}>{exported.link}</code><CopyButton text={exported.link} /></div><p className="muted">sing-box JSON</p><pre className="code-block">{exported.config}</pre></Card>}
      {quotaDialog && <Modal title={quotaDialog.adopt ? "接管外部节点" : "增加本周期额度"} onClose={() => setQuotaDialog(null)}><p className="muted">{quotaDialog.adopt ? "面板会备份原片段并迁移到独立服务。" : "周期结束时间和已用流量保持不变。"}</p><label>额度（GB）<input autoFocus type="number" min={1} value={quotaGB} onChange={(e) => setQuotaGB(e.target.value)} /></label><div className="row" style={{ justifyContent: "flex-end", marginTop: 16 }}><button className="btn" onClick={() => setQuotaDialog(null)}>取消</button><button className="btn primary" disabled={busy} onClick={() => void confirmQuotaDialog()}>确定</button></div></Modal>}
      {expiryDialog && <Modal title="编辑连接过期时间" onClose={() => setExpiryDialog(null)}><p className="muted">直接选择新的日期和时间；清空表示永久有效，流量 30 天周期不变。</p><label>过期时间<input autoFocus type="datetime-local" value={expiryAt} onChange={(e) => setExpiryAt(e.target.value)} /></label><div className="row" style={{ justifyContent: "flex-end", marginTop: 16 }}><button className="btn" onClick={() => setExpiryDialog(null)}>取消</button><button className="btn primary" disabled={busy} onClick={() => void confirmExpiryDialog()}>确定</button></div></Modal>}
    </>
  );
}
