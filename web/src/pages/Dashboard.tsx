import { useEffect, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  CircleCheck,
  ArrowRight,
  Server,
  TriangleAlert,
  Waypoints,
} from "lucide-react";
import {
  api,
  type DailyTotal,
  type Node,
  type QuotaState,
  type Route,
  type RouteStatus,
  type Traffic,
} from "../api";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { TrafficTrend } from "../components/TrafficTrend";
import { Banner } from "../components/Modal";
import { describeAge, formatBytes, isStale, ageOf } from "../lib/format";
import "./dashboard-redesign.css";

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
  const offline = nodes.filter((n) => n.status === "offline").length;
  const issues = collectIssues(routes, nodes, statuses, traffic, quotas);

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

  return (
    <div className="dashboard-redesign">
      <PageHeader
        title="仪表盘"
        actions={<button className="btn" onClick={() => onNavigate("routes")}>管理链路 <ArrowRight size={15} /></button>}
      />
      {error && <Banner kind="err">{error}</Banner>}

      <div className="dashboard-kpis">
        <StatCard index={0} label="累计转发流量" value={entries.length === 0 ? null : formatBytes(totalIn + totalOut)}
          sub={entries.length === 0 ? "等待首次流量计数" : <span className="dashboard-directions"><span><ArrowDown size={12} /> {formatBytes(totalIn)}</span><span><ArrowUp size={12} /> {formatBytes(totalOut)}</span></span>}
          icon={<ArrowDown size={19} />} />
        <StatCard index={1} label="机器在线" value={nodes.length === 0 ? null : `${online} / ${nodes.length}`}
          sub={nodes.length === 0 ? "尚未添加机器" : `${offline} 台离线 · ${nodes.length - online - offline} 台状态未知`}
          icon={<Server size={19} />} />
        <StatCard index={2} label="已启用链路" value={`${enabled} / ${routes.length}`}
          sub="启用状态不代表实际连接状态" icon={<Waypoints size={19} />} />
        <StatCard index={3} label="待关注事项" value={String(issues.length)}
          sub={issues.length ? "检查离线、流量额度与采样异常" : "当前已采集数据未发现异常"}
          icon={<TriangleAlert size={19} />} />
      </div>

      <div className="dashboard-columns">
        <div className="dashboard-main-column">
          {routes.length === 0 ? (
            <Card>
              <EmptyState icon={<Waypoints size={22} />} title="还没有链路"
                desc="添加并探测机器后，即可创建链路。"
                action={<button className="btn primary" onClick={() => onNavigate("nodes")}>去添加机器</button>} />
            </Card>
          ) : (
            <Card className="dashboard-traffic"><TrafficTrend days={daily} /></Card>
          )}

          {routes.length > 0 && <Card className="dashboard-ranking">
            <div className="spread"><h2>流量排行</h2><span className="dashboard-caption">累计 · 入站 + 出站</span></div>
            {rankTop.length === 0 ? <p className="muted">还没有任何链路记到数。</p> : (
              <ul className="rank-list">{rankTop.map(({ route, t, total }, index) => (
                <li key={route.id}><button className="rank-item" title={`入 ${formatBytes(t.bytes_in)} · 出 ${formatBytes(t.bytes_out)}`} onClick={() => onNavigate("routes")}>
                  <div className="rank-head"><span className="rank-name"><span className="dashboard-rank-number">{String(index + 1).padStart(2, "0")}</span><span className="dashboard-rank-title">{route.name}</span>{route.listen_ipv6 && <span className="tag">IPv6</span>}</span><span className="rank-value">{formatBytes(total)}</span></div>
                  <div className="rank-track"><div className="rank-fill" style={{ width: `${rankMax > 0 ? (total / rankMax) * 100 : 0}%` }} /></div>
                </button></li>
              ))}</ul>
            )}
            {(ranked.length > RANK_LIMIT || uncounted > 0) && <p className="dashboard-caption">{ranked.length > RANK_LIMIT && `另有 ${ranked.length - RANK_LIMIT} 条未列出。`}{uncounted > 0 && `${uncounted} 条没有计数，不参与排行。`}</p>}
          </Card>}
        </div>

        <aside className="dashboard-side-column" aria-label="运行概况、待关注事项与流量排行">
          <Card className="dashboard-health">
            <div className="spread"><h2>运行概况</h2><Server size={17} /></div>
            <div className="dashboard-health-gauge">
              <svg viewBox="0 0 160 140" aria-hidden="true"><path d="M 24 118 A 66 66 0 1 1 136 118" fill="none" stroke="var(--border)" strokeWidth="8" strokeLinecap="round" />{nodes.length > 0 && online > 0 && <path d="M 24 118 A 66 66 0 1 1 136 118" fill="none" stroke="var(--ok)" strokeWidth="8" strokeLinecap="round" pathLength="100" strokeDasharray={`${online / nodes.length * 100} 100`} />}</svg>
              <div><strong>{nodes.length ? online : "—"}<small> / {nodes.length}</small></strong><span>机器连接正常</span></div>
            </div>
            <div className="dashboard-health-counts"><div><strong>{enabled}</strong><span>启用链路</span></div><div><strong>{offline}</strong><span>离线机器</span></div><div><strong>{nodes.length - online - offline}</strong><span>状态未知</span></div></div>
            <p className="dashboard-health-note">机器连通与节点客户端在线是不同状态；此处不代表活跃客户端数量。</p>
          </Card>

          <Card className="dashboard-attention">
            <div className="spread"><h2>需要注意</h2><span className="tag">{issues.length}</span></div>
            {issues.length === 0 ? <div className="dashboard-clear"><CircleCheck size={23} /><strong>暂未发现异常</strong><p>仅覆盖当前面板能够采集的数据，不代表未采集部分正常。</p></div> : (
              <><ul className="dashboard-issues">{issues.map((issue, i) => <li key={i} className={issue.tone}><TriangleAlert size={15} /><div><strong>{issue.text}</strong>{issue.hint && <p>{issue.hint}</p>}</div></li>)}</ul><button className="btn sm" onClick={() => onNavigate("routes")}>去链路页处理 <ArrowRight size={14} /></button></>
            )}
          </Card>
        </aside>
      </div>
    </div>
  );
}
