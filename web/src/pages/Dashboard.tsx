import { useEffect, useState } from "react";
import { ArrowRight, ArrowUpRight, Crown, Waypoints } from "lucide-react";
import {
  api,
  type DailyTotal,
  type Node,
  type QuotaState,
  type Route,
  type RouteStatus,
  type Traffic,
} from "../api";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { Mascot, type MascotMood } from "../components/Mascot";
import { PageHeader } from "../components/PageHeader";
import { TrafficTrend } from "../components/TrafficTrend";
import { Banner } from "../components/Modal";
import { describeAge, formatBytes, isStale, ageOf } from "../lib/format";
import { regionOf, TONE_CYCLE, toneOf } from "../lib/region";
import "./dashboard.css";

const RANK_LIMIT = 6;

const QUOTA_NEAR_RATIO = 0.9;
type Tone = "err" | "warn";

interface Issue {
  tone: Tone;
  text: string;
  hint?: string;
}

/**
 * 把五个接口的现状归并成一张「需要注意」清单。
 *
 * 只列已经确知不对，或者确知测不出来的事。测得出来且正常的东西不占位置 ——
 * 一屏全是绿勾等于没有信息。
 */
function collectIssues(
  routes: Route[],
  nodes: Node[],
  statuses: RouteStatus[],
  traffic: Record<string, Traffic>,
  quotas: QuotaState[],
): Issue[] {
  const issues: Issue[] = [];
  const quotaOf = new Map(quotas.map((q) => [q.route_id, q]));

  for (const node of nodes) {
    if (node.status === "offline") {
      issues.push({
        tone: "err",
        text: `机器 ${node.name} 离线`,
        hint: node.last_seen ? `最后一次连通：${node.last_seen}` : "从未连通过",
      });
    }
  }

  for (const route of routes) {
    if (route.quota_paused_at) {
      issues.push({
        tone: "err",
        text: `链路 ${route.name} 已达流量额度，面板已自动停止`,
        hint: "下个周期开始后自动恢复；想立刻恢复就调高额度",
      });
      continue;
    }

    const quota = route.quota_bytes;
    const state = quotaOf.get(route.id);
    if (quota && state?.measured && state.used_bytes >= quota * QUOTA_NEAR_RATIO) {
      issues.push({
        tone: "warn",
        text: `链路 ${route.name} 额度快满：${formatBytes(state.used_bytes)} / ${formatBytes(quota)}`,
      });
    }
    if (quota && (!state || !state.measured)) {
      issues.push({
        tone: "warn",
        text: `链路 ${route.name} 设了额度但本周期没有任何计数`,
        hint: "无法判断用量，额度不会被执行",
      });
    }
  }

  for (const status of statuses) {
    for (const hop of status.hops) {
      if (hop.running === false) {
        issues.push({
          tone: "err",
          text: `链路 ${status.name} 的 ${hop.node_name} 上转发进程未在运行`,
        });
        continue;
      }
      if (hop.running !== null && isStale(hop.checked_at)) {
        const age = ageOf(hop.checked_at);
        issues.push({
          tone: "warn",
          text: `链路 ${status.name} 的 ${hop.node_name} 采样已过期`,
          hint: age === null ? undefined : `已 ${describeAge(age)}未能采样，当前状态未知`,
        });
      }
    }
  }

  for (const route of routes) {
    const t = traffic[String(route.id)];
    if (t && !t.from_entry) {
      issues.push({
        tone: "warn",
        text: `链路 ${route.name} 的流量数字取自非入口跳`,
        hint: "入口跳数不到，这个数字会偏小",
      });
    }
  }

  return issues;
}

interface Props {
  onNavigate: (tab: "routes" | "nodes") => void;
}

const STATUS_LABEL: Record<Node["status"], string> = { online: "在线", offline: "离线", unknown: "状态未知" };

function moodAt(hour: number): MascotMood {
  return hour < 6 || hour >= 23 ? "sleepy" : "wave";
}

const RING_RADIUS = 34;

function OnlineRing({ online, total }: { online: number; total: number }) {
  const circumference = 2 * Math.PI * RING_RADIUS;
  const share = total > 0 ? online / total : 0;
  return (
    <div className="online-ring">
      <svg viewBox="0 0 84 84" width="84" height="84" aria-hidden>
        <circle className="ring-track" cx="42" cy="42" r={RING_RADIUS} />
        <circle className="ring-value" cx="42" cy="42" r={RING_RADIUS} strokeDasharray={`${circumference * share} ${circumference}`} />
      </svg>
      <span><b>{total ? online : "—"}</b><small>/ {total}</small></span>
    </div>
  );
}

const GAUGE_RADIUS = 40;

// A half ring: how much of the whole is measured. Half a circle keeps it short
// enough to sit beside a headline figure.
function HalfGauge({ part, whole, label }: { part: number; whole: number; label: string }) {
  const arc = Math.PI * GAUGE_RADIUS;
  const share = whole > 0 ? part / whole : 0;
  const d = `M ${50 - GAUGE_RADIUS} 50 A ${GAUGE_RADIUS} ${GAUGE_RADIUS} 0 0 1 ${50 + GAUGE_RADIUS} 50`;
  return (
    <div className="half-gauge">
      <svg viewBox="0 0 100 56" width="112" height="63" aria-hidden>
        <path className="gauge-track" d={d} />
        <path className="gauge-value" d={d} strokeDasharray={`${arc * share} ${arc}`} />
      </svg>
      <span><b>{whole > 0 ? Math.round(share * 100) : "—"}<small>%</small></b>{label}</span>
    </div>
  );
}

function regionCounts(nodes: Node[]): [string, number][] {
  const counts = new Map<string, number>();
  for (const node of nodes) {
    const region = regionOf(node.name) ?? "其他";
    counts.set(region, (counts.get(region) ?? 0) + 1);
  }
  return [...counts].sort((a, b) => b[1] - a[1]);
}

function greeting(hour: number): string {
  if (hour < 6) return "夜深了";
  if (hour < 12) return "早上好";
  if (hour < 18) return "下午好";
  return "晚上好";
}

/** formatBytes 给的是「540 GB」，主卡上数字和单位字号不同，要拆开。 */
function splitBytes(bytes: number): [string, string] {
  const [value, unit = ""] = formatBytes(bytes).split(" ");
  return [value, unit];
}

function percentOf(part: number, whole: number): string {
  return whole > 0 ? `${Math.round((part / whole) * 100)}%` : "—";
}

export function Dashboard({ onNavigate }: Props) {
  const [routes, setRoutes] = useState<Route[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [statuses, setStatuses] = useState<RouteStatus[]>([]);
  const [traffic, setTraffic] = useState<Record<string, Traffic>>({});
  const [quotas, setQuotas] = useState<QuotaState[]>([]);
  const [daily, setDaily] = useState<DailyTotal[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  async function loadDashboard() {
    try {
      const [r, n, s, t, q, d] = await Promise.all([
        api.listRoutes(),
        api.listNodes(),
        api.status(),
        api.traffic(),
        api.quotas(),
        api.dailyTotals(14),
      ]);
      setRoutes(r ?? []);
      setNodes(n ?? []);
      setStatuses(s ?? []);
      setTraffic(t ?? {});
      setQuotas(q ?? []);
      setDaily(d ?? []);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadDashboard();
    const timer = window.setInterval(() => void loadDashboard(), 15000);
    return () => window.clearInterval(timer);
  }, []);

  const entries = Object.values(traffic);
  const totalIn = entries.reduce((sum, t) => sum + t.bytes_in, 0);
  const totalOut = entries.reduce((sum, t) => sum + t.bytes_out, 0);
  const enabled = routes.filter((r) => r.enabled).length;
  const online = nodes.filter((n) => n.status === "online").length;
  const offlineNodes = nodes.filter((n) => n.status === "offline");
  const unknownNodes = nodes.length - online - offlineNodes.length;
  const issues = collectIssues(routes, nodes, statuses, traffic, quotas);
  const errors = issues.filter((issue) => issue.tone === "err").length;
  const relayDown = statuses.filter((s) => s.hops.some((h) => h.running === false)).length;

  // 只排有计数的链路。没计数的那几条不是「用得少」，是不知道用了多少 ——
  // 把它们以 0 排在末尾，会让人以为它们闲着。
  const ranked = routes
    .map((route) => ({ route, t: traffic[String(route.id)] }))
    .filter((x): x is { route: Route; t: Traffic } => !!x.t)
    .map((x) => ({ ...x, total: x.t.bytes_in + x.t.bytes_out }))
    .sort((a, b) => b.total - a.total);
  const rankTop = ranked.slice(0, RANK_LIMIT);
  const rankMax = rankTop[0]?.total ?? 0;
  const uncounted = routes.length - ranked.length;

  if (loading) {
    return (
      <>
        <PageHeader title="仪表盘" />
        <p className="muted">读取中…</p>
      </>
    );
  }

  const [totalValue, totalUnit] = splitBytes(totalIn + totalOut);
  const countedDays = daily.filter((d) => d.counted);
  const windowTotal = countedDays.reduce((sum, d) => sum + d.bytes_in + d.bytes_out, 0);
  const today = daily.at(-1);
  const hour = new Date().getHours();
  const bubble = nodes.length === 0
    ? "先去添加一台机器吧"
    : today?.counted ? `今天已转发 ${formatBytes(today.bytes_in + today.bytes_out)}` : "今天还没有计数";

  return (
    <div className="dashboard">
      <PageHeader
        title={greeting(hour)}
        titleAside={
          <div className="greeting-buddy">
            <Mascot mood={moodAt(hour)} size={58} />
            <span className="greeting-bubble">{bubble}</span>
          </div>
        }
        desc={nodes.length === 0 ? "还没有添加机器。" : `${online} / ${nodes.length} 台机器在线，${enabled} 条链路在转发。`}
        actions={<button className="btn" onClick={() => onNavigate("routes")}>管理链路 <ArrowRight size={15} /></button>}
      />
      {error && <Banner kind="err">{error}</Banner>}

      <div className="bento">
        <section className="bento-hero" aria-label="转发流量">
          <div className="hero-top">
            <div className="bento-eyebrow">累计转发流量</div>
            {countedDays.length > 0 && (
              <dl className="hero-meta">
                <div><dt>今天</dt><dd>{today?.counted ? formatBytes(today.bytes_in + today.bytes_out) : "—"}</dd></div>
                <div><dt>日均</dt><dd>{formatBytes(windowTotal / countedDays.length)}</dd></div>
              </dl>
            )}
          </div>
          {entries.length === 0 ? (
            <p className="hero-empty">等待首次流量计数</p>
          ) : (
            <>
              <div className="hero-figure">
                <span className="hero-value">{totalValue}</span>
                <span className="hero-unit">{totalUnit}</span>
              </div>
              <div className="hero-ratio">
                <div className="in" style={{ flexGrow: totalIn }}>
                  <span>入 <em>{formatBytes(totalIn)}</em></span>
                  <b>{percentOf(totalIn, totalIn + totalOut)}</b>
                </div>
                <div className="out" style={{ flexGrow: totalOut }}>
                  <span>出 <em>{formatBytes(totalOut)}</em></span>
                  <b>{percentOf(totalOut, totalIn + totalOut)}</b>
                </div>
              </div>
            </>
          )}
          {routes.length > 0 && <div className="hero-trend"><TrafficTrend days={daily} /></div>}
        </section>

        <section className="bento-card bento-machines tone-sky" aria-label="机器">
          <header>
            <span className="bento-eyebrow">机器</span>
            <button className="bento-link" title="查看机器" aria-label="查看机器" onClick={() => onNavigate("nodes")}><ArrowUpRight size={16} /></button>
          </header>
          <div className="machine-summary">
            <OnlineRing online={online} total={nodes.length} />
            <div className="machine-pills">
              <span className="pill ok"><i />{online} 台在线</span>
              {offlineNodes.length > 0 && <span className="pill err"><i />{offlineNodes.length} 台离线</span>}
              {unknownNodes > 0 && <span className="pill"><i />{unknownNodes} 台未知</span>}
            </div>
          </div>
          <ul className="region-chips">
            {regionCounts(nodes).map(([region, count]) => (
              <li key={region} className={`tone-${toneOf(region)}`}><Flag code={region} />{region}<b>{count}</b></li>
            ))}
          </ul>
          <ul className="machine-cells">
            {nodes.map((node) => (
              <li key={node.id} className={node.status} title={`${node.name} · ${STATUS_LABEL[node.status]}`}>
                <i />
                <span>{node.name}</span>
              </li>
            ))}
          </ul>
          <p className="bento-foot">
            {offlineNodes.length > 0
              ? `${offlineNodes.map((n) => n.name).join("、")} 离线`
              : unknownNodes > 0 ? `${unknownNodes} 台状态未知` : "全部机器连接正常"}
          </p>
        </section>

        <section className="bento-card bento-routes tone-lilac" aria-label="链路">
          <header>
            <span className="bento-eyebrow">链路</span>
            <button className="bento-link" title="查看链路" aria-label="查看链路" onClick={() => onNavigate("routes")}><ArrowUpRight size={16} /></button>
          </header>
          <div className="routes-figure">
            <div className="bento-figure"><b>{enabled}</b><span><em>/ {routes.length}</em> 已启用</span></div>
            <HalfGauge part={ranked.length} whole={routes.length} label="已计量" />
          </div>
          <dl className="route-counts">
            <div className={relayDown > 0 ? "err" : ""}><dt>转发异常</dt><dd>{relayDown}</dd></div>
            <div><dt>已计量</dt><dd>{ranked.length}</dd></div>
            <div><dt>未计量</dt><dd>{uncounted}</dd></div>
          </dl>
        </section>

        <section className="bento-card bento-rank" aria-label="流量排行">
          <header>
            <span className="bento-eyebrow">流量排行</span>
            <span className="bento-caption">累计 · 入 + 出</span>
          </header>
          {routes.length === 0 ? (
            <EmptyState icon={<Waypoints size={22} />} title="还没有链路"
              desc="添加并探测机器后，即可创建链路。"
              action={<button className="btn primary" onClick={() => onNavigate("nodes")}>去添加机器</button>} />
          ) : rankTop.length === 0 ? <p className="muted">还没有任何链路记到数。</p> : (
            <ol className="rank-rows">{rankTop.map(({ route, t, total }, index) => (
              <li key={route.id} className={`tone-${TONE_CYCLE[index % TONE_CYCLE.length]}`}>
                <button title={`入 ${formatBytes(t.bytes_in)} · 出 ${formatBytes(t.bytes_out)}`} onClick={() => onNavigate("routes")}>
                  {index === 0
                    ? <span className="rank-no first" aria-label="第 1"><Crown size={13} /></span>
                    : <span className="rank-no">{index + 1}</span>}
                  <span className="rank-title">{route.name}{route.listen_ipv6 && <span className="tag">IPv6</span>}</span>
                  <span className="rank-bar"><i style={{ width: `${rankMax > 0 ? (total / rankMax) * 100 : 0}%` }} /></span>
                  <span className="rank-total">{formatBytes(total)}</span>
                </button>
              </li>
            ))}</ol>
          )}
          {(ranked.length > RANK_LIMIT || uncounted > 0) && <p className="bento-foot">{ranked.length > RANK_LIMIT && `另有 ${ranked.length - RANK_LIMIT} 条未列出。`}{uncounted > 0 && `${uncounted} 条没有计数，不参与排行。`}</p>}
        </section>

        <section className={`bento-card bento-issues tone-${errors > 0 ? "peach" : issues.length > 0 ? "sun" : "lime"}${errors > 0 ? " has-errors" : ""}`} aria-label="需要注意">
          <header>
            <span className="bento-eyebrow">需要注意</span>
            <span className="issue-count">{issues.length}</span>
          </header>
          {issues.length === 0 ? (
            <div className="issues-clear"><Mascot mood="happy" size={84} /><strong>一切正常</strong><p>仅覆盖面板能采集到的数据。</p></div>
          ) : (
            <>
              <ul className="issue-list">{issues.map((issue, i) => (
                <li key={i} className={issue.tone}><i /><div><strong>{issue.text}</strong>{issue.hint && <p>{issue.hint}</p>}</div></li>
              ))}</ul>
              <Mascot mood="worried" size={72} className="issues-buddy" />
            </>
          )}
        </section>
      </div>
    </div>
  );
}
