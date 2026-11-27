import { useCallback, useEffect, useState } from "react";
import { Pencil, Plus, Radio, Trash2 } from "lucide-react";
import { api, ApiError, type ProbeMachine, type ProbeOverview, type ProbeReport, type ProbeSite, type ProbeTask } from "../api";
import { Banner } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { PageHeader } from "../components/PageHeader";
import { formatBytes, formatRate, formatUptime } from "../lib/format";
import { regionOf } from "../lib/region";
import { EnrollCard, flagCode, MachineDialog, TaskDialog, ThemeUpload } from "./ProbeAdmin";
import "./line-quality.css";
import "./probe.css";

// Agents report every few seconds; the page follows closely enough to read
// as live without asking for every report.
const REFRESH_MS = 5000;

const TASK_TYPES: Record<string, string> = { icmp: "ICMP", tcp: "TCP", http: "HTTP" };

function percent(used: number, total: number) {
  return total > 0 ? Math.min(100, (used / total) * 100) : 0;
}

function Meter({ value, label }: { value: number; label: string }) {
  const tone = value >= 90 ? "bad" : value >= 70 ? "warn" : "ok";
  return (
    <span className="probe-meter" title={label}>
      <span className="probe-meter-bar"><span className={tone} style={{ width: `${value}%` }} /></span>
      <span className="probe-meter-text">{value.toFixed(0)}%</span>
    </span>
  );
}

function Usage({ report }: { report: ProbeReport }) {
  return (
    <>
      <td><Meter value={report.cpu.usage} label={`CPU ${report.cpu.usage.toFixed(1)}%`} /></td>
      <td><Meter value={percent(report.ram.used, report.ram.total)} label={`内存 ${formatBytes(report.ram.used)} / ${formatBytes(report.ram.total)}`} /></td>
      <td><Meter value={percent(report.disk.used, report.disk.total)} label={`磁盘 ${formatBytes(report.disk.used)} / ${formatBytes(report.disk.total)}`} /></td>
      <td className="num">
        <span className="probe-rate">↓ {formatRate(report.network.down)}</span>
        <span className="probe-rate">↑ {formatRate(report.network.up)}</span>
      </td>
      <td className="num">{report.load.load1.toFixed(2)}</td>
      <td className="num muted">{formatUptime(report.uptime)}</td>
    </>
  );
}

interface RowProps {
  m: ProbeMachine;
  onNotify: (notify: boolean) => void;
  onEdit: () => void;
  onDelete: () => void;
}

function MachineRow({ m, onNotify, onEdit, onDelete }: RowProps) {
  const code = flagCode(m.display.region) ?? regionOf(m.name);
  const system = [m.info.os, m.info.arch].filter(Boolean).join(" · ");
  return (
    <tr className={m.online ? "" : "probe-offline"}>
      <td>
        <span className="quality-node">
          {code && <Flag code={code} />}
          <strong>{m.name}</strong>
          {m.hidden && <span className="tag">隐藏</span>}
        </span>
        {system && <span className="probe-system muted">{system}</span>}
      </td>
      <td>
        <span className={`probe-state ${m.online ? "on" : m.at ? "off" : "wait"}`}>{m.online ? "在线" : m.at ? "离线" : "等待上报"}</span>
      </td>
      {m.report ? <Usage report={m.report} /> : <td colSpan={6} className="muted">还没有收到这台机器的上报</td>}
      <td className="muted mono">{m.info.version || "-"}</td>
      <td>
        <button type="button" role="switch" aria-checked={m.notify} aria-label={`${m.name} 掉线时通知`}
          className={`toggle-switch${m.notify ? " on" : ""}`} onClick={() => onNotify(!m.notify)}><span /></button>
      </td>
      <td className="probe-actions">
        <button type="button" className="icon-btn" title="编辑" aria-label={`编辑 ${m.name}`} onClick={onEdit}><Pencil size={15} /></button>
        <button type="button" className="icon-btn" title="删除" aria-label={`删除 ${m.name}`} onClick={onDelete}><Trash2 size={15} /></button>
      </td>
    </tr>
  );
}

export function Probe() {
  const [overview, setOverview] = useState<ProbeOverview | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState<ProbeMachine | null>(null);
  const [task, setTask] = useState<ProbeTask | "new" | null>(null);
  const [confirm, confirmDialog] = useConfirm();

  const load = useCallback(async () => {
    try {
      setOverview(await api.probeOverview());
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取探针失败");
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [load]);

  async function setNotify(uuid: string, notify: boolean) {
    setOverview((o) => o && { ...o, machines: o.machines.map((m) => (m.uuid === uuid ? { ...m, notify } : m)) });
    try {
      await api.setProbeNotify(uuid, notify);
    } catch (err) {
      setOverview((o) => o && { ...o, machines: o.machines.map((m) => (m.uuid === uuid ? { ...m, notify: !notify } : m)) });
      setError(err instanceof ApiError ? err.message : "保存掉线通知设置失败");
    }
  }

  async function act(fn: () => Promise<unknown>, done: string) {
    setNotice("");
    try {
      await fn();
      setNotice(done);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "操作失败");
    }
  }

  async function deleteMachine(m: ProbeMachine) {
    const ok = await confirm({
      title: `删除 ${m.name}？`,
      body: "这台机器的历史数据会一起删掉，它的 agent 也会因为 token 失效而连不上。要重新接入得在机器上重新装 agent。",
      confirmLabel: "删除",
      danger: true,
    });
    if (ok) await act(() => api.deleteProbeMachine(m.uuid), `已删除 ${m.name}`);
  }

  async function deleteTask(t: ProbeTask) {
    const ok = await confirm({ title: `删除延迟任务「${t.name}」？`, body: "它的历史数据会一起删掉。", confirmLabel: "删除", danger: true });
    if (ok) await act(() => api.deleteProbeTask(t.id), `已删除延迟任务「${t.name}」`);
  }

  const machines = overview?.machines ?? [];
  const tasks = overview?.tasks ?? [];
  const online = machines.filter((m) => m.online).length;
  const names = new Map(machines.map((m) => [m.uuid, m.name]));

  return (
    <div className="page">
      <PageHeader
        title="探针"
        desc={
          <>
            机器上的 Komari agent 每几秒上报一次 CPU、内存、网速，不需要 SSH。
            {overview?.enabled && machines.length > 0 && ` 在线 ${online} / ${machines.length} 台。`}
          </>
        }
      />
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      {overview && machines.length === 0 ? (
        <EmptyState icon={<Radio size={22} />} title="还没有探针机器" desc="用下面的命令在机器上装 agent，或从 Komari 导入后，机器会直接报到这里。" />
      ) : (
        <div className="card flush probe-table-wrap">
          <table className="probe-table">
            <thead>
              <tr>
                <th>机器</th>
                <th>状态</th>
                <th>CPU</th>
                <th>内存</th>
                <th>磁盘</th>
                <th className="num">网速</th>
                <th className="num">负载</th>
                <th className="num">运行</th>
                <th>agent</th>
                <th title="机器掉线超过 3 分钟、恢复时发 Telegram 通知；总开关在「通知」页">通知</th>
                <th aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {machines.map((m) => (
                <MachineRow key={m.uuid} m={m} onNotify={(on) => void setNotify(m.uuid, on)} onEdit={() => setEditing(m)} onDelete={() => void deleteMachine(m)} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {overview?.enabled && (
        <div className="card">
          <div className="probe-card-head">
            <h2>延迟任务</h2>
            <button type="button" className="btn sm" onClick={() => setTask("new")}><Plus size={14} />新增</button>
          </div>
          <p className="muted probe-note">面板按间隔把检测发给选中机器的 agent 执行，结果画在公开页的延迟图里。</p>
          {tasks.length === 0 ? (
            <p className="muted">还没有延迟任务。</p>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>名称</th>
                  <th>方式</th>
                  <th>目标</th>
                  <th className="num">间隔</th>
                  <th>机器</th>
                  <th aria-label="操作" />
                </tr>
              </thead>
              <tbody>
                {tasks.map((t) => (
                  <tr key={t.id}>
                    <td><strong>{t.name}</strong></td>
                    <td>{TASK_TYPES[t.type] ?? t.type}</td>
                    <td className="mono">{t.target}</td>
                    <td className="num">{t.interval} 秒</td>
                    <td className="muted" title={t.clients.map((c) => names.get(c) ?? c).join("、")}>{t.clients.length} 台</td>
                    <td className="probe-actions">
                      <button type="button" className="icon-btn" title="编辑" aria-label={`编辑 ${t.name}`} onClick={() => setTask(t)}><Pencil size={15} /></button>
                      <button type="button" className="icon-btn" title="删除" aria-label={`删除 ${t.name}`} onClick={() => void deleteTask(t)}><Trash2 size={15} /></button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {overview?.enabled && <EnrollCard />}
      {overview?.enabled && <ProbeSiteCard />}

      {editing && (
        <MachineDialog machine={editing} onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); void act(async () => undefined, "已保存"); }} />
      )}
      {task && (
        <TaskDialog task={task === "new" ? null : task} machines={machines} onClose={() => setTask(null)}
          onSaved={() => { setTask(null); void act(async () => undefined, "延迟任务已保存，下一轮开始按新设置检测"); }} />
      )}
      {confirmDialog}
    </div>
  );
}

// ProbeSiteCard sets the public status page: the domain it answers on,
// what it is called, and a way into the theme's own settings.
function ProbeSiteCard() {
  const [site, setSite] = useState<ProbeSite | null>(null);
  const [form, setForm] = useState<Omit<ProbeSite, "theme"> | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    api.probeSite().then(
      (s) => {
        setSite(s);
        setForm({ public_host: s.public_host, sitename: s.sitename, description: s.description, custom_head: s.custom_head, custom_body: s.custom_body });
      },
      (err) => setError(err instanceof ApiError ? err.message : "读取公开页设置失败"),
    );
  }, []);

  async function save() {
    if (!form) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      setSite(await api.saveProbeSite(form));
      setNotice("已保存");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  async function editTheme() {
    // The tab opens before the link arrives, so a popup blocker lets it through.
    const tab = window.open("", "_blank");
    try {
      const { url } = await api.probeThemeLogin();
      if (tab) tab.location.href = url;
      else window.location.href = url;
    } catch (err) {
      tab?.close();
      setError(err instanceof ApiError ? err.message : "打开主题设置失败");
    }
  }

  if (!form) return error ? <Banner kind="err">{error}</Banner> : null;
  const set = (key: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [key]: e.target.value });
  return (
    <div className="card">
      <h2>公开页</h2>
      <p className="muted probe-note">
        访客看到的状态页，用 {site?.theme ?? "LuminaPlus"} 主题，只在下面这个域名上打开；面板自己的功能在那个域名上一律访问不到。
      </p>
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}
      <form className="dialog-form setting-form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <div className="grid2">
          <label>公开页域名<input value={form.public_host} onChange={set("public_host")} placeholder="status.example.com" /></label>
          <label>站点名称<input value={form.sitename} onChange={set("sitename")} /></label>
        </div>
        <label>描述<input value={form.description} onChange={set("description")} /></label>
        <label>自定义 head<textarea className="mono probe-code" rows={3} value={form.custom_head} onChange={set("custom_head")} /></label>
        <label>自定义 body<textarea className="mono probe-code" rows={3} value={form.custom_body} onChange={set("custom_body")} /></label>
        <p className="field-hint flush">自定义代码会原样插进公开页，只放你自己信得过的内容。</p>
        <div className="setting-actions">
          <button className="btn primary" disabled={busy}>{busy ? "保存中…" : "保存"}</button>
          <button type="button" className="btn" disabled={busy || !site?.public_host} onClick={() => void editTheme()}>
            编辑主题设置
          </button>
          {site?.public_host && <a className="btn ghost" href={`https://${site.public_host}/`} target="_blank" rel="noopener noreferrer">打开公开页</a>}
        </div>
      </form>
      {site && <ThemeUpload theme={site.theme} />}
    </div>
  );
}
