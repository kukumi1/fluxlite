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
import { Banner } from "../components/Modal";
import { describeAge, formatBytes, isStale, ageOf } from "../lib/format";

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
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  async function load() {
    try {
      const [r, n, s, t, q] = await Promise.all([
        api.listRoutes(),
        api.listNodes(),
        api.status(),
        api.traffic(),
        api.quotas(),
      ]);
      setRoutes(r ?? []);
      setNodes(n ?? []);
      setStatuses(s ?? []);
      setTraffic(t ?? {});
      setQuotas(q ?? []);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 15000);
    return () => clearInterval(timer);
  }, []);

  const entries = Object.values(traffic);
  const totalIn = entries.reduce((sum, t) => sum + t.bytes_in, 0);
  const totalOut = entries.reduce((sum, t) => sum + t.bytes_out, 0);
  const enabled = routes.filter((r) => r.enabled).length;
  const online = nodes.filter((n) => n.status === "online").length;
  const offline = nodes.filter((n) => n.status === "offline").length;
  const issues = collectIssues(routes, nodes, statuses, traffic, quotas);

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
    </>
  );
}
