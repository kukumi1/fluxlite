import { useState } from "react";
import { RefreshCw, Route as RouteIcon, Settings2, Waypoints } from "lucide-react";
import { api, ApiError, type Node, type ReturnOverview, type ReturnRoute, type ReturnTarget, type RouteVerdict } from "../api";
import { Banner, Modal } from "../components/Modal";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { regionOf } from "../lib/region";
import { lastChecked } from "../lib/format";

const key = (nodeID: number, target: string) => `${nodeID}:${target}`;

const TIER_CLASS = ["r-unknown", "r-ordinary", "r-quality", "r-premium"] as const;
const TIER_LABEL = ["未识别", "普通线路", "优质线路", "精品线路"] as const;
const CONFIDENCE_LABEL = { confirmed: "确认", inferred: "推测", insufficient: "证据不足" } as const;

function routeClass(v: RouteVerdict): string {
  const tier = v.confidence === "insufficient" ? "r-unknown" : TIER_CLASS[v.tier] ?? "r-unknown";
  return `route-pill ${tier}${v.confidence === "inferred" ? " is-inferred" : ""}`;
}

function routeTitle(r: ReturnRoute): string {
  const v = r.verdict;
  const parts = [`${TIER_LABEL[v.tier] ?? ""} · ${CONFIDENCE_LABEL[v.confidence]}`, v.evidence];
  if (v.handoff) parts.push(`从${v.entry}入境，国内转给${v.handoff}`);
  if (v.chain.length) parts.push(v.chain.join(" → "));
  return parts.filter(Boolean).join("\n");
}

// The line and, when the route is handed to another carrier inside China,
// where it goes next — on one line, so every cell in a row stays level.
function LineLabel({ verdict }: { verdict: RouteVerdict }) {
  return (
    <>
      {verdict.line}
      {verdict.handoff && <small>→{verdict.handoff}</small>}
    </>
  );
}

// Backbone tags take the colour of the line they make up: premium green,
// quality blue, ordinary grey — the same scale as the table's pills.
const NET_TIER: Record<string, "premium" | "quality" | "ordinary"> = {
  CN2: "premium", CTGNet: "premium", "9929": "premium", CMIN2: "premium",
  CUG: "quality",
};

// Private and military space (RFC1918, DoD) is the machine's own network
// before the route leaves for anywhere; it is shown faded.
function isLocalHop(h: ReturnRoute["hops"][number]): boolean {
  return !h.asn && (h.whois === "RFC1918" || h.whois === "DOD");
}

function groupByCity(targets: ReturnTarget[]): Array<[string, ReturnTarget[]]> {
  const out: Array<[string, ReturnTarget[]]> = [];
  for (const t of targets) {
    const last = out[out.length - 1];
    if (last && last[0] === t.city) last[1].push(t);
    else out.push([t.city, [t]]);
  }
  return out;
}

export function ReturnRoutesSection({ overview, nodes, busy, onTrace, onSaved }: {
  overview: ReturnOverview;
  nodes: Node[];
  busy: string | null;
  onTrace: (node: Node) => void;
  onSaved: () => Promise<void>;
}) {
  const [detailFor, setDetailFor] = useState<Node | null>(null);
  const [editing, setEditing] = useState(false);
  const routes = new Map(overview.routes.map((r) => [key(r.node_id, r.target), r]));
  const targets = overview.presets.filter((p) => overview.settings.targets.includes(p.name));
  const cities = groupByCity(targets);
  const skipped = new Set(overview.settings.skip_nodes);

  return (
    <section className="quality-section">
      <div className="quality-section-head">
        <div>
          <h2>三网回程</h2>
          <p>
            每 6 小时从每台机器追踪一次回国内各地三网的路径，按入境的骨干网判断线路；结果也显示在公开页的机器卡片上。
            <span className="quality-checked"> {lastChecked(overview.routes.map((r) => r.checked_at))} · 每 6 小时自动</span>
          </p>
        </div>
        <button className="btn" onClick={() => setEditing(true)}><Settings2 size={15} />回程设置</button>
      </div>

      {targets.length === 0 ? (
        <div className="card">
          <EmptyState icon={<Waypoints size={18} />} title="没有选择回程测试点" desc="选几个城市和运营商，机器就会开始检测回程。"
            action={<button className="btn primary" onClick={() => setEditing(true)}>选择测试点</button>} />
        </div>
      ) : (
        <div className="card flush quality-table-wrap">
          <table className="quality-table return-table">
            <thead>
              <tr>
                <th rowSpan={2}>机器</th>
                {cities.map(([city, list]) => <th key={city} colSpan={list.length} className="city-head">{city}</th>)}
                <th rowSpan={2} aria-label="操作" />
              </tr>
              <tr>
                {targets.map((t) => <th key={t.name} className="carrier-head">{t.carrier}</th>)}
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => {
                const code = regionOf(n.name);
                const offline = n.status !== "online";
                const skip = skipped.has(n.id);
                return (
                  <tr key={n.id} className={offline || skip ? "is-offline" : undefined}>
                    <th scope="row">
                      <span className="quality-node">
                        {code && <Flag code={code} />}
                        <span>{n.name}</span>
                        {offline && <span className="tag">离线</span>}
                      </span>
                    </th>
                    {skip ? (
                      <td colSpan={targets.length} className="muted return-skipped">已跳过，不检测这台机器（可在回程设置中开启）</td>
                    ) : targets.map((t) => {
                      const r = routes.get(key(n.id, t.name));
                      return (
                        <td key={t.name}>
                          {!r ? <span className="route-pill r-none">待检测</span>
                            : r.error ? <span className="route-pill r-error" title={r.error}>失败</span>
                            : (
                              <span className={routeClass(r.verdict)} title={routeTitle(r)}><LineLabel verdict={r.verdict} /></span>
                            )}
                        </td>
                      );
                    })}
                    <td className="quality-actions">
                      <button className="icon-btn" title="重新检测回程" aria-label={`重新检测 ${n.name} 的回程`} disabled={offline || busy !== null}
                        onClick={() => onTrace(n)}>
                        <RefreshCw size={15} className={busy === `t${n.id}` ? "spin" : undefined} />
                      </button>
                      <button className="icon-btn" title="回程详情" aria-label={`${n.name} 的回程详情`} onClick={() => setDetailFor(n)}>
                        <RouteIcon size={15} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <p className="return-footnote">
            由 {overview.tool || "NextTrace"} 对每个测试点各做一遍 ICMP 和 TCP 80 追踪再合并，按 RIPEstat 宣告前缀（{overview.prefix_date}）判断每一跳属于哪张网；详情里的地点和运营商来自 NextTrace 的 IP 库。
            虚线框是推测：入境处有跳点没回应，或没追到目的地。结果仅供参考。
          </p>
        </div>
      )}

      {detailFor && (
        <RouteDetailDialog
          node={detailFor}
          targets={targets}
          routes={routes}
          busy={busy === `t${detailFor.id}`}
          onRetrace={() => onTrace(detailFor)}
          onClose={() => setDetailFor(null)}
        />
      )}
      {editing && (
        <ReturnSettingsDialog
          overview={overview}
          nodes={nodes}
          onClose={() => setEditing(false)}
          onSaved={async () => {
            setEditing(false);
            await onSaved();
          }}
        />
      )}
    </section>
  );
}

function RouteDetailDialog({ node, targets, routes, busy, onRetrace, onClose }: {
  node: Node;
  targets: ReturnTarget[];
  routes: Map<string, ReturnRoute>;
  busy: boolean;
  onRetrace: () => void;
  onClose: () => void;
}) {
  return (
    <Modal title={`回程详情 · ${node.name}`} subtitle="只按回程最先进入的那家运营商判断线路；之后在国内转给别家的，记为「转某某」" size="wide" onClose={onClose}>
      <div className="route-trace-actions">
        <button className="btn sm" disabled={busy || node.status !== "online"} onClick={onRetrace}>
          <RefreshCw size={14} className={busy ? "spin" : undefined} />{busy ? "检测中…" : "重新检测"}
        </button>
      </div>
      {targets.map((t) => {
        const r = routes.get(key(node.id, t.name));
        const v = r?.verdict;
        return (
          <section key={t.name} className="route-trace">
            <header>
              <strong>{t.name}</strong>
              {!r ? <span className="muted">还没有检测</span>
                : r.error ? <span className="route-pill r-error">{r.error}</span>
                : v && (
                  <>
                    <span className={routeClass(v)}><LineLabel verdict={v} /></span>
                    {v.handoff && <span className="muted">从{v.entry}入境，国内转给{v.handoff}</span>}
                    <span className="muted">{TIER_LABEL[v.tier]} · {CONFIDENCE_LABEL[v.confidence]}</span>
                  </>
                )}
              {r && <small className="muted">{new Date(r.checked_at * 1000).toLocaleString("zh-CN")}</small>}
            </header>
            {v && !r?.error && (
              <p className="route-evidence">
                {v.evidence}
                {v.chain.length > 0 && <span className="mono">{v.chain.join(" → ")}</span>}
              </p>
            )}
            {r && r.hops.length > 0 && (
              <div className="trace-hops" role="table" aria-label={`${t.name} 的逐跳路径`}>
                {r.hops.map((h, i) => (
                  <div role="row" key={`${h.ttl}-${h.ip ?? i}`} className={!h.ip ? "is-silent" : isLocalHop(h) ? "is-local" : undefined}>
                    <span role="cell" className="mono ttl">{i > 0 && r.hops[i - 1].ttl === h.ttl ? "" : h.ttl}</span>
                    <span role="cell" className="hop-addr">
                      <span className="mono">{h.ip ?? "*"}</span>
                      {h.hostname && <small className="mono hop-host" title={h.hostname}>{h.hostname}</small>}
                    </span>
                    <span role="cell" className="hop-net">
                      <span>
                        {h.asn ? <span className="mono hop-asn">AS{h.asn}</span> : null}
                        {h.net && <span className={`tag net-${NET_TIER[h.net] ?? "ordinary"}`}>{h.net}</span>}
                      </span>
                      {h.whois && <small className="mono hop-whois">{h.whois}</small>}
                    </span>
                    <span role="cell" className="hop-where">
                      {h.location && <span>{h.location}</span>}
                      {h.owner && <small>{h.owner}</small>}
                    </span>
                    <span role="cell" className="mono hop-ms">{h.ms !== undefined ? `${h.ms.toFixed(1)} ms` : ""}</span>
                  </div>
                ))}
              </div>
            )}
          </section>
        );
      })}
    </Modal>
  );
}

function ReturnSettingsDialog({ overview, nodes, onClose, onSaved }: {
  overview: ReturnOverview;
  nodes: Node[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [chosen, setChosen] = useState(() => new Set(overview.settings.targets));
  const [skip, setSkip] = useState(() => new Set(overview.settings.skip_nodes));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const cities = groupByCity(overview.presets);

  const toggle = <T,>(set: Set<T>, value: T, on: boolean) => {
    const next = new Set(set);
    if (on) next.add(value);
    else next.delete(value);
    return next;
  };

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.saveReturnSettings({
        targets: overview.presets.filter((p) => chosen.has(p.name)).map((p) => p.name),
        skip_nodes: nodes.filter((n) => skip.has(n.id)).map((n) => n.id),
      });
      await onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "保存失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="回程设置" subtitle="测试点是各地运营商机房里开着 80 端口的地址，追踪一定能到达终点" size="wide" onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="return-grid">
        {cities.map(([city, list]) => {
          const all = list.every((t) => chosen.has(t.name));
          return (
            <div key={city} className="return-grid-row">
              <strong>{city}</strong>
              {list.map((t) => {
                const on = chosen.has(t.name);
                return (
                  <button key={t.name} type="button" aria-pressed={on} className={`mute-chip${on ? " muted" : ""}`}
                    onClick={() => setChosen(toggle(chosen, t.name, !on))}>{t.carrier}</button>
                );
              })}
              <button type="button" className="btn sm ghost"
                onClick={() => setChosen(list.reduce((s, t) => toggle(s, t.name, !all), chosen))}>{all ? "全不选" : "全选"}</button>
            </div>
          );
        })}
      </div>

      <h4 className="return-subhead">跳过的机器</h4>
      <p className="hint">NAT 机通常没有完整的公网出口，追踪结果不能代表线路，默认跳过端口范围不超过 20 个的机器。</p>
      <div className="mute-chips">
        {nodes.map((n) => {
          const skipped = skip.has(n.id);
          const code = regionOf(n.name);
          return (
            <button key={n.id} type="button" aria-pressed={skipped} className={`mute-chip${skipped ? " muted" : ""}`}
              onClick={() => setSkip(toggle(skip, n.id, !skipped))}>
              {code && <Flag code={code} />}
              {n.name}
            </button>
          );
        })}
      </div>

      <div className="modal-actions">
        <span className="muted">已选 {chosen.size} 个测试点</span>
        <button className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy} onClick={() => void save()}>{busy ? "保存中…" : "保存"}</button>
      </div>
    </Modal>
  );
}
