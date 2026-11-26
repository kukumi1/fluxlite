import { useEffect, useState } from "react";
import { Gauge, Play, Trash2, X } from "lucide-react";
import {
  api, ApiError,
  type IPFamily, type Node, type SpeedJob, type SpeedMode, type SpeedOverview, type SpeedResult,
} from "../api";
import { Banner } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { PageHeader } from "../components/PageHeader";
import { formatBytes } from "../lib/format";
import { regionOf } from "../lib/region";
import "./line-quality.css";
import "./speedtest.css";

const MAX_POINTS = 9;
const MAX_NODES = 10;
const QUICK = ["北京", "上海", "广东"];
const MODES: ReadonlyArray<readonly [SpeedMode, string]> = [["single", "单线程"], ["multi", "多线程"], ["both", "单线程 + 多线程"]];
const STATUS: Record<SpeedJob["status"], string> = { running: "测速中", done: "已完成", cancelled: "已取消", interrupted: "面板重启，已中断" };

const toggled = <T,>(set: Set<T>, value: T) => {
  const next = new Set(set);
  if (next.has(value)) next.delete(value);
  else next.add(value);
  return next;
};

const when = (unix: number) => new Date(unix * 1000).toLocaleString("zh-CN", { hour12: false });

// The settings line of a job: 协议：IPv4 | 运营商：联通、移动 | 测速点：广东 | 单线程.
function summary(job: SpeedJob, carriers: string[]) {
  const provinces: string[] = [];
  const used = new Set<string>();
  for (const point of job.request.points) {
    const carrier = carriers.find((c) => point.endsWith(c));
    if (!carrier) continue;
    used.add(carrier);
    const province = point.slice(0, -carrier.length);
    if (!provinces.includes(province)) provinces.push(province);
  }
  const mode = MODES.find(([m]) => m === job.request.mode)?.[1] ?? job.request.mode;
  return `协议：IPv${job.request.family} | 运营商：${carriers.filter((c) => used.has(c)).join("、")} | 测速点：${provinces.join("、")} | ${mode}`;
}

export function Speedtest() {
  const [overview, setOverview] = useState<SpeedOverview | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [machines, setMachines] = useState<Set<number>>(new Set());
  const [points, setPoints] = useState<Set<string>>(() => new Set(QUICK.flatMap((p) => ["电信", "联通", "移动"].map((c) => p + c))));
  const [mode, setMode] = useState<SpeedMode>("single");
  const [family, setFamily] = useState<IPFamily>(4);
  const [starting, setStarting] = useState(false);
  const [confirm, confirmDialog] = useConfirm();

  async function load() {
    try {
      const [o, n] = await Promise.all([api.speedOverview(), api.listNodes()]);
      setOverview(o);
      setNodes(n ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取测速记录失败");
    }
  }

  const running = overview?.jobs.some((j) => j.status === "running") ?? false;
  useEffect(() => {
    void load();
  }, []);
  // A test takes minutes; while one runs the page follows its progress.
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => void load(), 3000);
    return () => clearInterval(timer);
  }, [running]);

  const busyNodes = new Set((overview?.jobs ?? []).filter((j) => j.status === "running").flatMap((j) => j.request.node_ids));
  const runsPerMachine = points.size * (mode === "both" ? 2 : 1);

  async function start() {
    const ok = await confirm({
      title: `测速 ${machines.size} 台机器？`,
      body: `每台机器跑 ${runsPerMachine} 次测速，每次下载、上传各 5 秒。单线程一次通常用掉几百 MB，多线程可能超过 1 GB，会计入机器的流量。`,
      confirmLabel: "开始测速",
    });
    if (!ok) return;
    setStarting(true);
    setError("");
    setNotice("");
    try {
      const order = nodes.filter((n) => machines.has(n.id)).map((n) => n.id);
      const chosen = (overview?.provinces ?? []).flatMap((p) => (overview?.carriers ?? []).map((c) => p + c)).filter((p) => points.has(p));
      await api.startSpeedtest({ node_ids: order, points: chosen, mode, family });
      setMachines(new Set());
      setNotice("测速已开始，结果会陆续出现在下面");
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "开始测速失败");
    } finally {
      setStarting(false);
    }
  }

  async function cancel(job: SpeedJob) {
    if (!(await confirm({ title: `取消测速 #${job.id}？`, body: "会停掉机器上正在跑的测速，已经测完的点会保留。", confirmLabel: "取消测速", cancelLabel: "继续测", danger: true }))) return;
    try {
      await api.cancelSpeedtest(job.id);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "取消失败");
    }
  }

  async function remove(job: SpeedJob) {
    if (!(await confirm({ title: `删除测速记录 #${job.id}？`, confirmLabel: "删除", danger: true }))) return;
    try {
      await api.deleteSpeedtest(job.id);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "删除失败");
    }
  }

  const canStart = machines.size > 0 && points.size > 0 && points.size <= MAX_POINTS && machines.size <= MAX_NODES && !starting;

  return (
    <div className="management-page speedtest-page">
      <PageHeader
        title="测速"
        desc="测速点是「全球网测」在该省该运营商机房里的测速服务器，不是随便一个公网 IP：下载是服务器发给机器（去程），上传是机器发给服务器（回程）。丢包不是直接测出来的，回程重传率越高说明回程丢包越多，准确的丢包率看「线路质量」。测速会消耗机器流量，只在手动开始时运行；在 Telegram 里发 /speed 也能测。"
      />
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <section className="card speed-setup">
        <div className="speed-field">
          <h3>机器</h3>
          <div className="mute-chips">
            {nodes.map((n) => {
              const on = machines.has(n.id);
              const code = regionOf(n.name);
              const disabled = n.status !== "online" || busyNodes.has(n.id) || (family === 6 && !n.ipv6_address);
              const why = n.status !== "online" ? "离线" : busyNodes.has(n.id) ? "正在测速" : family === 6 && !n.ipv6_address ? "没有 IPv6" : "";
              return (
                <button key={n.id} type="button" aria-pressed={on} disabled={disabled} title={why || undefined}
                  className={`mute-chip${on ? " muted" : ""}`} onClick={() => setMachines((cur) => toggled(cur, n.id))}>
                  {code && <Flag code={code} />}{n.name}{why && <small>{why}</small>}
                </button>
              );
            })}
          </div>
        </div>

        <div className="speed-field">
          <div className="speed-field-head">
            <h3>测速点 <span className={points.size > MAX_POINTS ? "warn-text" : "muted"}>{points.size} / {MAX_POINTS}</span></h3>
            <div className="speed-quick">
              <button type="button" className="btn sm ghost" onClick={() => setPoints(new Set(QUICK.flatMap((p) => (overview?.carriers ?? []).map((c) => p + c))))}>北上广三网</button>
              <button type="button" className="btn sm ghost" onClick={() => setPoints(new Set())}>清空</button>
            </div>
          </div>
          <div className="speed-points">
            {(overview?.provinces ?? []).map((p) => (
              <div key={p} className="speed-province">
                <strong>{p}</strong>
                {(overview?.carriers ?? []).map((c) => {
                  const on = points.has(p + c);
                  return (
                    <button key={c} type="button" aria-pressed={on} className={`mute-chip${on ? " muted" : ""}`}
                      disabled={!on && points.size >= MAX_POINTS} onClick={() => setPoints((cur) => toggled(cur, p + c))}>{c}</button>
                  );
                })}
              </div>
            ))}
          </div>
        </div>

        <div className="speed-options">
          <div className="segmented labels" role="radiogroup" aria-label="模式">
            {MODES.map(([m, label]) => (
              <button key={m} type="button" role="radio" aria-checked={mode === m} className={mode === m ? "active" : ""} onClick={() => setMode(m)}>{label}</button>
            ))}
          </div>
          <div className="segmented labels" role="radiogroup" aria-label="协议">
            {([4, 6] as const).map((f) => (
              <button key={f} type="button" role="radio" aria-checked={family === f} className={family === f ? "active" : ""} onClick={() => { setFamily(f); setMachines(new Set()); }}>IPv{f}</button>
            ))}
          </div>
          <span className="muted speed-estimate">
            {machines.size > 0 && points.size > 0 ? `每台 ${runsPerMachine} 次测速，单线程每次几百 MB、多线程可能上 GB` : "选好机器和测速点后开始"}
          </span>
          <button className="btn primary" disabled={!canStart} onClick={() => void start()}>
            <Play size={15} />{starting ? "正在开始…" : "开始测速"}
          </button>
        </div>
      </section>

      {overview && overview.jobs.length === 0 && (
        <div className="card">
          <EmptyState icon={<Gauge size={18} />} title="还没有测速记录" desc="选好机器和测速点，点「开始测速」。" />
        </div>
      )}
      {overview?.jobs.map((job) => <JobCard key={job.id} job={job} nodes={nodes} carriers={overview.carriers} onCancel={() => void cancel(job)} onDelete={() => void remove(job)} />)}
      {overview?.tool && <p className="muted speed-foot">测速程序 {overview.tool}（taierspeedtest-mod，「全球网测」的命令行客户端），面板校验后传到机器，机器本身不连 GitHub。只保留最近 40 次记录。</p>}
      {confirmDialog}
    </div>
  );
}

function JobCard({ job, nodes, carriers, onCancel, onDelete }: { job: SpeedJob; nodes: Node[]; carriers: string[]; onCancel: () => void; onDelete: () => void }) {
  const name = (id: number) => nodes.find((n) => n.id === id)?.name ?? `机器 #${id}`;
  return (
    <section className="card speed-job">
      <header className="speed-job-head">
        <div>
          <strong>#{job.id}</strong>
          <span className={`speed-status s-${job.status}`}>{STATUS[job.status]}</span>
          <span className="muted">{when(job.created_at)}</span>
          {job.request.origin === "telegram" && <span className="tag">Telegram</span>}
        </div>
        {job.status === "running" ? (
          <button className="btn sm" onClick={onCancel}><X size={14} />取消</button>
        ) : (
          <button className="icon-btn" title="删除记录" aria-label={`删除测速记录 #${job.id}`} onClick={onDelete}><Trash2 size={15} /></button>
        )}
      </header>
      <p className="speed-summary">{summary(job, carriers)}</p>
      {job.request.node_ids.map((id) => (
        <MachineResult key={id} job={job} nodeID={id} name={name(id)} />
      ))}
    </section>
  );
}

function speedText(v: number | null) {
  if (v === null) return "—";
  if (v === 0) return <span className="speed-fail">失败</span>;
  return v >= 100 ? v.toFixed(0) : v.toFixed(1);
}

function retransTone(v: number) {
  if (v < 10) return "q-good";
  if (v < 20) return "q-fair";
  return "q-bad";
}

function MachineResult({ job, nodeID, name }: { job: SpeedJob; nodeID: number; name: string }) {
  const run = job.runs.find((r) => r.node_id === nodeID);
  const progress = job.progress.find((p) => p.node_id === nodeID);
  const results = job.results.filter((r) => r.node_id === nodeID);
  const code = regionOf(name);
  const showSingle = job.request.mode !== "multi";
  const showMulti = job.request.mode !== "single";
  const byPoint = job.request.points.map((point) => ({
    point,
    single: results.find((r) => r.point === point && r.mode === "single"),
    multi: results.find((r) => r.point === point && r.mode === "multi"),
  })).filter((row) => row.single || row.multi);

  return (
    <div className="speed-machine">
      <div className="speed-machine-head">
        <span className="quality-node">{code && <Flag code={code} />}<strong>{name}</strong></span>
        {run?.egress && <span className="muted">出口：{run.egress}</span>}
        {run && run.started_at > 0 && run.finished_at >= run.started_at && <span className="muted">耗时 {run.finished_at - run.started_at} 秒</span>}
        {progress && (
          <span className="speed-progress">
            <span className="speed-bar"><span style={{ width: `${(progress.done / Math.max(progress.total, 1)) * 100}%` }} /></span>
            {progress.done}/{progress.total}{progress.current && ` · 正在测 ${progress.current}`}
          </span>
        )}
        {run?.error && <span className="warn-text">{run.error}</span>}
      </div>
      {run && run.traffic_bytes > 0 && (
        <p className="speed-traffic">📦 消耗流量：共 {formatBytes(run.traffic_bytes)}（下载 {formatBytes(run.rx_bytes)} / 上传 {formatBytes(run.tx_bytes)}）</p>
      )}
      {byPoint.length > 0 && (
        <>
          <table className="quality-summary speed-table">
            <thead>
              <tr>
                <th>测速点</th>
                <th className="num">延迟 (ms)</th>
                {showSingle && <><th className="num">单线程 ↓ (Mbps)</th><th className="num">单线程 ↑ (Mbps)</th></>}
                {showMulti && <><th className="num">多线程 ↓ (Mbps)</th><th className="num">多线程 ↑ (Mbps)</th></>}
                <th />
              </tr>
            </thead>
            <tbody>
              {byPoint.map(({ point, single, multi }) => {
                const any = (single ?? multi) as SpeedResult;
                const notes = [single, multi].flatMap((r) => (r?.error ? [r.error] : []));
                const retried = (single?.retried ?? 0) + (multi?.retried ?? 0);
                return (
                  <tr key={point}>
                    <td>{point}</td>
                    <td className="mono num">{any.rtt ? any.rtt.replace(/ms$/, "") : "—"}</td>
                    {showSingle && <><td className="mono num">{speedText(single?.down ?? null)}</td><td className="mono num">{speedText(single?.up ?? null)}</td></>}
                    {showMulti && <><td className="mono num">{speedText(multi?.down ?? null)}</td><td className="mono num">{speedText(multi?.up ?? null)}</td></>}
                    <td className="muted speed-note">{[...notes, retried > 0 ? `自动重测 ${retried} 次` : ""].filter(Boolean).join("；")}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <p className="muted speed-legend">↓ 去程：中国 → 机器　↑ 回程：机器 → 中国</p>
          {byPoint.some(({ single, multi }) => (single?.retrans ?? multi?.retrans ?? null) !== null) && (
            <>
              <h4 className="speed-subhead">回程 TCP 重传率（测速期间，机器 → 中国）</h4>
              <table className="quality-summary speed-retrans">
                <thead><tr><th>测速点</th><th className="num">回程重传</th></tr></thead>
                <tbody>
                  {byPoint.map(({ point, single, multi }) => {
                    const retrans = single?.retrans ?? multi?.retrans ?? null;
                    return retrans === null ? null : (
                      <tr key={point}>
                        <td>{point} <span className="muted">v{job.request.family}</span></td>
                        <td className="num"><span className={`quality-pill ${retransTone(retrans)}`}>{retrans.toFixed(2)}%</span></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </>
          )}
        </>
      )}
    </div>
  );
}
