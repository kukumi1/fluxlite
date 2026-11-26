import { useEffect, useMemo, useState } from "react";
import { Activity, LineChart, Plus, RefreshCw, Settings2, Trash2 } from "lucide-react";
import {
  api, ApiError,
  type LatencyOverview, type LatencySample, type LatencyTarget, type Node,
} from "../api";
import { Banner, Modal } from "../components/Modal";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { PageHeader } from "../components/PageHeader";
import { regionOf } from "../lib/region";
import { ReturnRoutesSection } from "./ReturnRoutes";
import "./line-quality.css";

type Quality = "good" | "fair" | "bad" | "none";

// The thresholds a person reading the table cares about: under 100 ms with no
// loss is fine for anything, past 300 ms or half the probes lost is not.
function qualityOf(sample: LatencySample | undefined): Quality {
  if (!sample) return "none";
  if (sample.loss >= 50) return "bad";
  if (sample.avg_ms !== null && sample.avg_ms > 300) return "bad";
  if (sample.loss > 0 || (sample.avg_ms !== null && sample.avg_ms >= 100)) return "fair";
  return "good";
}

function sampleText(sample: LatencySample | undefined): string {
  if (!sample) return "—";
  if (sample.loss >= 100) return "不通";
  return sample.avg_ms === null ? "可连接" : `${sample.avg_ms}ms`;
}

const key = (nodeID: number, target: string) => `${nodeID}:${target}`;

export function LineQuality() {
  const [overview, setOverview] = useState<LatencyOverview | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [trendFor, setTrendFor] = useState<Node | null>(null);
  const [editingTargets, setEditingTargets] = useState(false);

  async function load() {
    try {
      const [o, n] = await Promise.all([api.latencyOverview(), api.listNodes()]);
      setOverview(o);
      setNodes(n ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取线路质量失败");
    }
  }

  useEffect(() => {
    void load();
    // Samples land every three minutes; checking twice as often keeps the
    // table at most a round and a half behind.
    const timer = setInterval(() => void load(), 90_000);
    return () => clearInterval(timer);
  }, []);

  const samples = useMemo(() => new Map((overview?.samples ?? []).map((s) => [key(s.node_id, s.target), s])), [overview]);
  const targets = (overview?.targets ?? []).filter((t) => t.enabled);

  async function act(id: string, fn: () => Promise<unknown>, done: string) {
    setBusy(id);
    setError("");
    setNotice("");
    try {
      await fn();
      await load();
      setNotice(done);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "操作失败");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="management-page line-quality-page">
      <PageHeader
        title="线路质量"
        desc="每台机器每 3 分钟测一次到国内各地运营商的延迟和丢包，每天检测一次回程线路。测的是从机器出发的连接，和国内用户连过来的方向相反，但线路好坏基本一致。"
      />
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="quality-section-head">
        <div>
          <h2>延迟与丢包</h2>
          <p>每轮每个测试点连 4 次 TCP，取平均延迟；握手超时或重传都算丢包。</p>
        </div>
        <button className="btn" onClick={() => setEditingTargets(true)}><Settings2 size={15} />延迟测试点</button>
      </div>

      {overview && targets.length === 0 && (
        <div className="card">
          <EmptyState icon={<Activity size={18} />} title="没有启用的测试点" desc="选几个测试点，机器就会开始测延迟。"
            action={<button className="btn primary" onClick={() => setEditingTargets(true)}>设置测试点</button>} />
        </div>
      )}

      {targets.length > 0 && (
        <div className="card flush quality-table-wrap">
          <table className="quality-table">
            <thead>
              <tr>
                <th>机器</th>
                {targets.map((t) => <th key={t.name}>{t.name}</th>)}
                <th aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => {
                const code = regionOf(n.name);
                const offline = n.status !== "online";
                return (
                  <tr key={n.id} className={offline ? "is-offline" : undefined}>
                    <th scope="row">
                      <span className="quality-node">
                        {code && <Flag code={code} />}
                        <span>{n.name}</span>
                        {offline && <span className="tag">离线</span>}
                      </span>
                    </th>
                    {targets.map((t) => {
                      const sample = samples.get(key(n.id, t.name));
                      return (
                        <td key={t.name}>
                          <span className={`quality-pill q-${qualityOf(sample)}`} title={sample ? `丢包 ${sample.loss}%` : "还没有测量结果"}>
                            {sampleText(sample)}
                            {sample && sample.loss > 0 && sample.loss < 100 && <small>丢 {sample.loss}%</small>}
                          </span>
                        </td>
                      );
                    })}
                    <td className="quality-actions">
                      <button className="icon-btn" title="立即重测延迟" aria-label={`重测 ${n.name}`} disabled={offline || busy !== null}
                        onClick={() => void act(`p${n.id}`, () => api.probeLatency(n.id), `${n.name} 已重测`)}>
                        <RefreshCw size={15} className={busy === `p${n.id}` ? "spin" : undefined} />
                      </button>
                      <button className="icon-btn" title="延迟趋势" aria-label={`${n.name} 的延迟趋势`} onClick={() => setTrendFor(n)}>
                        <LineChart size={15} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {trendFor && <TrendDialog node={trendFor} targets={targets} onClose={() => setTrendFor(null)} />}
      {overview && (
        <ReturnRoutesSection
          overview={overview.return}
          nodes={nodes}
          busy={busy}
          onTrace={(n) => void act(`t${n.id}`, () => api.traceReturnRoutes(n.id), `${n.name} 的回程已重新检测`)}
          onSaved={async () => {
            setNotice("回程设置已保存，下一轮检测开始生效");
            await load();
          }}
        />
      )}
      {editingTargets && overview && (
        <TargetsDialog
          targets={overview.targets}
          presets={overview.presets}
          onClose={() => setEditingTargets(false)}
          onSaved={async () => {
            setEditingTargets(false);
            setNotice("测试点已保存，下一轮测量开始生效");
            await load();
          }}
        />
      )}
    </div>
  );
}

const HOUR_CHOICES = [[24, "24 小时"], [72, "3 天"], [168, "7 天"]] as const;

function TrendDialog({ node, targets, onClose }: { node: Node; targets: LatencyTarget[]; onClose: () => void }) {
  const [hours, setHours] = useState<number>(24);
  const [history, setHistory] = useState<LatencySample[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    setHistory(null);
    api.latencyHistory(node.id, hours)
      .then(setHistory)
      .catch((err) => setError(err instanceof ApiError ? err.message : "读取失败"));
  }, [node.id, hours]);

  const series = targets.map((t) => ({ name: t.name, points: (history ?? []).filter((s) => s.target === t.name) }));
  return (
    <Modal title={`延迟趋势 · ${node.name}`} subtitle="每个点是一段时间的平均值；断开处是那段时间一次都没连上" size="wide" onClose={onClose}>
      <div className="segmented labels" role="radiogroup" aria-label="时间范围">
        {HOUR_CHOICES.map(([h, label]) => (
          <button key={h} type="button" role="radio" aria-checked={hours === h} className={hours === h ? "active" : ""} onClick={() => setHours(h)}>{label}</button>
        ))}
      </div>
      {error && <Banner kind="err">{error}</Banner>}
      {history === null ? <p className="muted">读取中…</p> : history.length === 0 ? (
        <p className="muted">这段时间还没有测量数据。</p>
      ) : (
        <>
          <TrendChart series={series} hours={hours} />
          <table className="quality-summary">
            <thead><tr><th>测试点</th><th>平均延迟</th><th>最高延迟</th><th>平均丢包</th></tr></thead>
            <tbody>
              {series.map((s, i) => {
                const timed = s.points.flatMap((p) => (p.avg_ms === null ? [] : [p.avg_ms]));
                const avg = timed.length ? Math.round(timed.reduce((a, b) => a + b, 0) / timed.length) : null;
                const loss = s.points.length ? Math.round(s.points.reduce((a, p) => a + p.loss, 0) / s.points.length) : null;
                return (
                  <tr key={s.name}>
                    <td><span className={`series-dot series-${i % 5}`} />{s.name}</td>
                    <td className="mono">{avg === null ? "—" : `${avg}ms`}</td>
                    <td className="mono">{timed.length ? `${Math.max(...timed)}ms` : "—"}</td>
                    <td className="mono">{loss === null ? "—" : `${loss}%`}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </>
      )}
    </Modal>
  );
}

const CHART_W = 720;
const CHART_H = 220;
const PAD = { left: 44, right: 12, top: 12, bottom: 24 };

// One line per test point over a shared time axis. A bucket nothing connected
// in breaks the line instead of dropping it to zero, which would read as the
// best latency of the day.
function TrendChart({ series, hours }: { series: Array<{ name: string; points: LatencySample[] }>; hours: number }) {
  const end = Date.now() / 1000;
  const start = end - hours * 3600;
  const all = series.flatMap((s) => s.points.flatMap((p) => (p.avg_ms === null ? [] : [p.avg_ms])));
  const top = Math.max(100, Math.ceil(Math.max(0, ...all) / 50) * 50);
  const x = (t: number) => PAD.left + ((t - start) / (end - start)) * (CHART_W - PAD.left - PAD.right);
  const y = (ms: number) => PAD.top + (1 - ms / top) * (CHART_H - PAD.top - PAD.bottom);
  const ticks = [0, top / 2, top];
  const labels = [start, (start + end) / 2, end].map((t) => {
    const d = new Date(t * 1000);
    return hours > 24 ? `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, "0")}时` : `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  });

  return (
    <svg className="trend-chart" viewBox={`0 0 ${CHART_W} ${CHART_H}`} role="img" aria-label="延迟趋势图">
      {ticks.map((ms) => (
        <g key={ms}>
          <line className="grid" x1={PAD.left} x2={CHART_W - PAD.right} y1={y(ms)} y2={y(ms)} />
          <text className="axis" x={PAD.left - 6} y={y(ms) + 4} textAnchor="end">{ms}ms</text>
        </g>
      ))}
      {labels.map((label, i) => (
        <text key={i} className="axis" x={[PAD.left, CHART_W / 2, CHART_W - PAD.right][i]} y={CHART_H - 6} textAnchor={(["start", "middle", "end"] as const)[i]}>{label}</text>
      ))}
      {series.map((s, i) => {
        let d = "";
        let pen = false;
        for (const p of s.points) {
          if (p.avg_ms === null) {
            pen = false;
            continue;
          }
          d += `${pen ? "L" : "M"}${x(p.at).toFixed(1)},${y(p.avg_ms).toFixed(1)}`;
          pen = true;
        }
        return <path key={s.name} className={`line series-${i % 5}`} d={d}><title>{s.name}</title></path>;
      })}
    </svg>
  );
}

function TargetsDialog({ targets, presets, onClose, onSaved }: {
  targets: LatencyTarget[];
  presets: LatencyTarget[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  // Presets keep their place even when switched off, so they can be turned
  // back on; anything else is a custom point the operator added.
  const [list, setList] = useState<LatencyTarget[]>(() => {
    const byName = new Map(targets.map((t) => [t.name, t]));
    const merged = presets.map((p) => byName.get(p.name) ?? { ...p, enabled: false });
    const custom = targets.filter((t) => !presets.some((p) => p.name === t.name));
    return [...merged, ...custom];
  });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const presetNames = new Set(presets.map((p) => p.name));
  const enabled = list.filter((t) => t.enabled).length;

  const patch = (i: number, change: Partial<LatencyTarget>) => setList(list.map((t, j) => (j === i ? { ...t, ...change } : t)));

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.saveLatencyTargets(list);
      await onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "保存失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="延迟测试点" subtitle="测试点是放在各地运营商机房里的地址；最多同时启用 12 个" size="wide" onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="target-list">
        {list.map((t, i) => {
          const preset = presetNames.has(t.name);
          return (
            <div key={i} className={`target-row${t.enabled ? " on" : ""}`}>
              <button type="button" role="switch" aria-checked={t.enabled} aria-label={`启用 ${t.name || "测试点"}`}
                className={`toggle-switch${t.enabled ? " on" : ""}`} onClick={() => patch(i, { enabled: !t.enabled })}><span /></button>
              {preset ? (
                <>
                  <strong>{t.name}</strong>
                  <span className="mono muted">{t.host}:{t.port}</span>
                </>
              ) : (
                <>
                  <input value={t.name} onChange={(e) => patch(i, { name: e.target.value })} placeholder="名称" aria-label="名称" />
                  <input className="mono" value={t.host} onChange={(e) => patch(i, { host: e.target.value })} placeholder="域名或 IPv4" aria-label="地址" />
                  <input className="mono port" type="number" value={t.port} onChange={(e) => patch(i, { port: Number(e.target.value) })} aria-label="端口" />
                  <button type="button" className="icon-btn" aria-label="删除测试点" onClick={() => setList(list.filter((_, j) => j !== i))}><Trash2 size={15} /></button>
                </>
              )}
            </div>
          );
        })}
      </div>
      <button type="button" className="hop-add" onClick={() => setList([...list, { name: "", host: "", port: 80, enabled: true }])}>
        <Plus size={15} />自定义测试点
      </button>
      <div className="modal-actions">
        <span className="muted">已启用 {enabled} 个</span>
        <button className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy || enabled > 12} onClick={() => void save()}>{busy ? "保存中…" : "保存"}</button>
      </div>
    </Modal>
  );
}
