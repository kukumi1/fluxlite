import { useEffect, useState } from "react";
import { Radio } from "lucide-react";
import { api, ApiError, type ProbeMachine, type ProbeOverview, type ProbeReport } from "../api";
import { Banner } from "../components/Modal";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { PageHeader } from "../components/PageHeader";
import { formatBytes, formatRate, formatUptime } from "../lib/format";
import { regionOf } from "../lib/region";
import "./line-quality.css";
import "./probe.css";

// Agents report every few seconds; the page follows closely enough to read
// as live without asking for every report.
const REFRESH_MS = 5000;

const TASK_TYPES: Record<string, string> = { icmp: "ICMP", tcp: "TCP", http: "HTTP" };

// Komari keeps a machine's region as a flag emoji, which Windows shows as
// two letters; read the letters back out so the panel's own flag is drawn.
function flagCode(emoji: string | undefined): string | null {
  const points = [...(emoji ?? "")].map((c) => c.codePointAt(0) ?? 0);
  if (points.length !== 2 || points.some((p) => p < 0x1f1e6 || p > 0x1f1ff)) return null;
  return String.fromCharCode(...points.map((p) => p - 0x1f1e6 + 65));
}

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

function MachineRow({ m }: { m: ProbeMachine }) {
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
    </tr>
  );
}

export function Probe() {
  const [overview, setOverview] = useState<ProbeOverview | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const o = await api.probeOverview();
        if (!cancelled) {
          setOverview(o);
          setError("");
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof ApiError ? err.message : "读取探针失败");
      }
    }
    void load();
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  const machines = overview?.machines ?? [];
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

      {overview && machines.length === 0 ? (
        <EmptyState icon={<Radio size={22} />} title="还没有探针机器" desc="从 Komari 导入后，机器上的 agent 会直接报到这里。" />
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
              </tr>
            </thead>
            <tbody>
              {machines.map((m) => <MachineRow key={m.uuid} m={m} />)}
            </tbody>
          </table>
        </div>
      )}

      {overview && overview.tasks.length > 0 && (
        <div className="card">
          <h2>延迟任务</h2>
          <p className="muted probe-note">由面板按间隔下发给各机器的 agent 执行，结果画在公开状态页的延迟图里。</p>
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>方式</th>
                <th>目标</th>
                <th className="num">间隔</th>
                <th>机器</th>
              </tr>
            </thead>
            <tbody>
              {overview.tasks.map((t) => (
                <tr key={t.id}>
                  <td><strong>{t.name}</strong></td>
                  <td>{TASK_TYPES[t.type] ?? t.type}</td>
                  <td className="mono">{t.target}</td>
                  <td className="num">{t.interval} 秒</td>
                  <td className="muted" title={t.clients.map((c) => names.get(c) ?? c).join("、")}>{t.clients.length} 台</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
