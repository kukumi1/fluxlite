import { KeyRound, Pencil, Trash2 } from "lucide-react";
import { api, type ProbeMachine, type ProbeReport } from "../api";
import { Flag } from "../components/Flag";
import { SortHandle, useManualOrder } from "../components/ManualOrder";
import { formatBytes, formatRate, formatUptime } from "../lib/format";
import { regionOf } from "../lib/region";
import { flagCode } from "./ProbeAdmin";
import "./line-quality.css";
import "./probe.css";

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

type Row = ProbeMachine & { id: string };

const asRow = (m: ProbeMachine): Row => ({ ...m, id: m.uuid });

interface Props {
  machines: ProbeMachine[];
  setMachines: (machines: ProbeMachine[]) => void;
  reload: () => Promise<ProbeMachine[] | null>;
  onError: (message: string) => void;
  onAddSSH: (m: ProbeMachine) => void;
  onEdit: (m: ProbeMachine) => void;
  onDelete: (m: ProbeMachine) => void;
  disabled: boolean;
}

// AgentOnlyMachines lists the machines that report through an agent but are
// not reached over SSH: live readings, and a way to give them SSH so they
// can carry routes and run speed tests like the rest.
export function AgentOnlyMachines({ machines, setMachines, reload, onError, onAddSSH, onEdit, onDelete, disabled }: Props) {
  const rows: Row[] = machines.map(asRow);
  const order = useManualOrder<string, Row>({
    items: rows,
    setItems: setMachines,
    save: api.reorderProbeMachines,
    reload: async () => (await reload())?.map(asRow) ?? null,
    onError,
    disabled,
  });
  const visible = rows.map((m) => m.id);

  return (
    <section className="card flush probe-table-wrap agent-only">
      <div className="probe-card-head agent-only-head">
        <h2>只有 agent 的机器</h2>
        <span className="muted">有实时数据，但面板连不上 SSH，所以不能转发、测速、开终端。补上 SSH 后会自动并入上面的列表。</span>
      </div>
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
            <th aria-label="操作" />
          </tr>
        </thead>
        <tbody>
          {rows.map((m) => {
            const code = flagCode(m.display.region) ?? regionOf(m.name);
            const system = [m.info.os, m.info.arch].filter(Boolean).join(" · ");
            const target = order.targetProps(m.id, visible);
            return (
              <tr key={m.uuid} {...target} className={`${m.online ? "" : "probe-offline"} ${target.className}`}>
                <td>
                  <span className="quality-node">
                    <SortHandle {...order.handleProps(m.id, m.name, visible)} />
                    {code && <Flag code={code} />}
                    <strong>{m.name}</strong>
                    {m.hidden && <span className="tag">公开页隐藏</span>}
                  </span>
                  {system && <span className="probe-system muted">{system}</span>}
                </td>
                <td>
                  <span className={`probe-state ${m.online ? "on" : m.at ? "off" : "wait"}`}>{m.online ? "在线" : m.at ? "离线" : "等待上报"}</span>
                </td>
                {m.report ? <Usage report={m.report} /> : <td colSpan={6} className="muted">还没有收到这台机器的上报</td>}
                <td className="probe-actions">
                  <button type="button" className="btn sm" title="填上 SSH 信息，让面板能在这台机器上转发、测速" disabled={disabled} onClick={() => onAddSSH(m)}>
                    <KeyRound size={14} />补 SSH
                  </button>
                  <button type="button" className="icon-btn" title="探针与公开页" aria-label={`编辑 ${m.name} 的探针与公开页`} onClick={() => onEdit(m)}><Pencil size={15} /></button>
                  <button type="button" className="icon-btn" title="删除" aria-label={`删除 ${m.name}`} onClick={() => onDelete(m)}><Trash2 size={15} /></button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
