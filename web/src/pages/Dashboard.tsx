import { useEffect, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  CircleCheck,
  Server,
  TriangleAlert,
  Waypoints,
} from "lucide-react";
import {
  api,
  type DailyTotal,
  type Node,
  type NodeMetrics,
  type QuotaState,
  type Route,
  type RouteStatus,
  type Traffic,
} from "../api";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { MetricBar } from "../components/MetricBar";
import { TrafficTrend } from "../components/TrafficTrend";
import { Banner } from "../components/Modal";
import { describeAge, formatBytes, isStale, ageOf, percentOf } from "../lib/format";
import { memCaveat } from "../lib/metrics";

const RANK_LIMIT = 6;

const QUOTA_NEAR_RATIO = 0.9;
const METRICS_REFRESH_MS = 5000;

function metricAgeLabel(metric: NodeMetrics | undefined): string {
  if (!metric) return "等待采集";
  const age = ageOf(metric.collected_at);
  return age === null ? "采集时间未知" : `更新于 ${describeAge(age)}`;
}

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
        text: `节点 ${node.name} 离线`,
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
  const [metrics, setMetrics] = useState<Record<string, NodeMetrics>>({});
  const [metricsError, setMetricsError] = useState("");
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

  useEffect(() => {
    let disposed = false;
    let timer: number | undefined;

    const refreshMetrics = async () => {
      try {
        const next = await api.metrics();
        if (disposed) return;
        setMetrics(next ?? {});
        setMetricsError("");
      } catch (e) {
        if (!disposed) {
          setMetricsError(e instanceof Error ? e.message : String(e));
        }
      } finally {
        if (!disposed) {
          timer = window.setTimeout(refreshMetrics, METRICS_REFRESH_MS);
        }
      }
    };

    void refreshMetrics();
    return () => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
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
    <>
      <PageHeader title="仪表盘" desc="全部链路与节点的当前状况。" />

      {error && <Banner kind="err">{error}</Banner>}

      <div className="stat-grid">
        <StatCard
          index={0}
          label="转发链路"
          value={String(routes.length)}
          sub={`${enabled} 条已启用`}
          icon={<Waypoints size={17} />}
        />
        <StatCard
          index={1}
          label="节点"
          value={nodes.length === 0 ? null : `${online} / ${nodes.length}`}
          sub={offline === 0 ? "全部在线" : `${offline} 台离线`}
          icon={<Server size={17} />}
          tone="cool"
        />
        <StatCard
          index={2}
          label="累计流量"
          value={entries.length === 0 ? null : formatBytes(totalIn + totalOut)}
          sub={
            entries.length === 0 ? (
              "还没有任何计数"
            ) : (
              <span className="row" style={{ gap: 10 }}>
                <span>
                  <ArrowDown size={11} /> {formatBytes(totalIn)}
                </span>
                <span>
                  <ArrowUp size={11} /> {formatBytes(totalOut)}
                </span>
              </span>
            )
          }
          icon={<ArrowDown size={17} />}
          tone="warm"
        />
      </div>

      {routes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Waypoints size={22} />}
            title="还没有链路"
            desc="先在节点页添加并探测节点，再回来建第一条转发链路。"
            action={
              <button className="btn primary" onClick={() => onNavigate("nodes")}>
                去添加节点
              </button>
            }
          />
        </Card>
      ) : (
        <Card>
          <div className="spread">
            <h2 style={{ margin: 0 }}>需要注意</h2>
            {issues.length > 0 && (
              <button className="btn sm" onClick={() => onNavigate("routes")}>
                去链路页处理
              </button>
            )}
          </div>
          {issues.length === 0 ? (
            <div className="row muted" style={{ gap: 8 }}>
              <CircleCheck size={16} style={{ color: "var(--ok)" }} />
              没有发现异常。注意：这只覆盖面板测得到的部分。
            </div>
          ) : (
            <ul className="checks">
              {issues.map((issue, i) => (
                <li key={i}>
                  <div className="row" style={{ gap: 8, alignItems: "flex-start" }}>
                    <TriangleAlert
                      size={15}
                      style={{
                        color: issue.tone === "err" ? "var(--err)" : "var(--warn)",
                        flexShrink: 0,
                        marginTop: 3,
                      }}
                    />
                    <div>
                      <div>{issue.text}</div>
                      {issue.hint && (
                        <div className="muted" style={{ fontSize: 12 }}>
                          {issue.hint}
                        </div>
                      )}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}

      {routes.length > 0 && (
        <div className="dash-grid">
          <Card>
            <TrafficTrend days={daily} />
          </Card>

          <Card>
            <div className="spread">
              <h2 style={{ margin: 0 }}>流量排行</h2>
              <span className="muted" style={{ fontSize: 12 }}>累计 · 入+出</span>
            </div>
            {rankTop.length === 0 ? (
              <p className="muted" style={{ marginTop: 12 }}>还没有任何链路记到数。</p>
            ) : (
              <ul className="rank-list">
                {rankTop.map(({ route, t, total }) => (
                  <li key={route.id}>
                    <button
                      className="rank-item"
                      title={`入 ${formatBytes(t.bytes_in)} · 出 ${formatBytes(t.bytes_out)}`}
                      onClick={() => onNavigate("routes")}
                    >
                      <div className="rank-head">
                        <span className="rank-name">
                          {route.name}
                          {route.listen_ipv6 && (
                            <span className="tag" style={{ marginLeft: 6 }}>
                              IPv6
                            </span>
                          )}
                        </span>
                        <span className="rank-value">{formatBytes(total)}</span>
                      </div>
                      <div className="rank-track">
                        <div
                          className="rank-fill"
                          style={{ width: `${rankMax > 0 ? (total / rankMax) * 100 : 0}%` }}
                        />
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {(ranked.length > RANK_LIMIT || uncounted > 0) && (
              <p className="muted" style={{ fontSize: 12, marginTop: 8, marginBottom: 0 }}>
                {ranked.length > RANK_LIMIT && `另有 ${ranked.length - RANK_LIMIT} 条未列出。`}
                {uncounted > 0 && `${uncounted} 条没有计数，不参与排行。`}
              </p>
            )}
          </Card>
        </div>
      )}

      {nodes.length > 0 && (
        <Card>
          <div className="spread">
            <div>
              <h2 style={{ margin: 0 }}>节点负载</h2>
              <p className="muted" style={{ fontSize: 12, margin: "4px 0 0" }}>
                {metricsError ? "指标更新失败，暂显示上次采集结果" : "每 5 秒刷新一次"}
              </p>
            </div>
            <button className="btn sm" onClick={() => onNavigate("nodes")}>
              去节点页
            </button>
          </div>
          <div className="node-load-grid">
            {nodes.map((n) => {
              const m = metrics[n.id];
              const caveat = memCaveat(m);
              return (
                <div className="node-load" key={n.id}>
                  <div className="spread" style={{ marginBottom: 8 }}>
                    <div>
                      <strong>{n.name}</strong>
                      <div className="muted" style={{ fontSize: 12, marginTop: 3 }}>
                        {metricAgeLabel(m)}
                      </div>
                    </div>
                    <span
                      className={`tag ${n.status === "online" ? "ok" : n.status === "offline" ? "err" : ""}`}
                    >
                      {n.status === "online" ? "在线" : n.status === "offline" ? "离线" : "未知"}
                    </span>
                  </div>
                  <MetricBar label="CPU" percent={m?.cpu_percent ?? null} caveat={caveat} />
                  <MetricBar
                    label="内存"
                    percent={percentOf(m?.mem_used ?? null, m?.mem_total ?? null)}
                    detail={m?.mem_total ? formatBytes(m.mem_total) : undefined}
                    caveat={caveat}
                  />
                  <MetricBar
                    label="磁盘"
                    percent={percentOf(m?.disk_used ?? null, m?.disk_total ?? null)}
                    detail={m?.disk_total ? formatBytes(m.disk_total) : undefined}
                  />
                </div>
              );
            })}
          </div>
        </Card>
      )}
    </>
  );
}
