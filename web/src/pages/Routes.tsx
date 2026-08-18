import { useEffect, useState } from "react";
import {
  api,
  ApiError,
  type ApplyResult,
  type Node,
  type DailyTraffic,
  type Route,
  type RouteHop,
  type RouteInput,
  type QuotaState,
  type RouteStatus,
  type Traffic,
  type VerifyReport,
} from "../api";
import { ArrowDown, ArrowUp, LayoutGrid, List, Plus, Rows3, Waypoints } from "lucide-react";
import { Banner, Modal } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { Card } from "../components/Card";
import { CopyIconButton } from "../components/CopyButton";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { ageOf, describeAge, formatBytes, staleAfterMs } from "../lib/format";

type ViewMode = "card" | "compact" | "list";

const VIEW_KEY = "fluxlite-routes-view";

const VIEW_OPTIONS = [
  { id: "card", label: "宽松卡片", icon: LayoutGrid },
  { id: "compact", label: "紧凑卡片", icon: Rows3 },
  { id: "list", label: "横向列表", icon: List },
] as const;

function storedView(): ViewMode {
  const saved = localStorage.getItem(VIEW_KEY);
  return saved === "card" || saved === "compact" || saved === "list" ? saved : "card";
}

// entryAddress is what a client actually points at: the entry node's reachable
// host paired with the route's entry port. For a NAT box the reachable host is
// the mapped address the provider gave out, not anything the machine knows
// about itself, which is why it comes from the node record rather than a probe.
function entryAddress(route: Route, nodes: Node[]): string {
  const host = nodes.find((n) => n.id === route.hops[0]?.node_id)?.host ?? "?";
  return `${host}:${route.entry_port}`;
}

// onNodeIdentity spells out what a route is called on the machines. A display
// name of "腾讯-IX-TW-SC" gives no hint that the unit to inspect is tw-b, and
// that is exactly what an operator needs at the moment they are logged in.
function onNodeIdentity(slug: string): string {
  return (
    `节点上的标识：${slug}\n` +
    `服务：fluxlite-relay@${slug}（OpenRC 为 fluxlite-${slug}）\n` +
    `配置：/etc/fluxlite/realm/${slug}.toml`
  );
}

// HopState separates "nobody has looked yet" from "looked, and it is down"
// from "the answer we have has stopped being refreshed". Collapsing any two of
// them would show an outage as healthy or a healthy relay as broken.
type HopState =
  | { kind: "unknown" }
  | { kind: "up" }
  | { kind: "down" }
  | { kind: "stale"; since: number; wasRunning: boolean };

function hopStateHint(state: HopState): string | undefined {
  switch (state.kind) {
    case "unknown":
      return "尚未采样，运行状态未知";
    case "down":
      return "转发进程未在运行";
    case "stale":
      return `已 ${describeAge(state.since)}未能采样，最后一次为${
        state.wasRunning ? "运行中" : "已停止"
      }，当前状态未知`;
    default:
      return undefined;
  }
}

// Link is the arrow between a hop and whatever it forwards to, carrying that
// leg's measured latency. An unmeasured leg shows no number rather than a
// zero, which would read as "instant".
function Link({ ms, at }: { ms: number | null; at: string | null }) {
  if (ms === null) return <span className="arrow">→</span>;
  const age = ageOf(at);
  const stale = age !== null && age > staleAfterMs;
  return (
    <span
      className="arrow"
      title={
        age === null
          ? undefined
          : stale
            ? `测于 ${describeAge(age)}，此后未能再测到，该数字已不代表现状`
            : `测于 ${describeAge(age)}`
      }
    >
      →
      <span className={`lat ${stale ? "stale" : ""}`}>
        {ms}ms{stale && " ⚠"}
      </span>
    </span>
  );
}

// ChainTotal sums the legs. It is deliberately absent unless every leg has
// been measured: a partial sum understates the chain and invites the wrong
// conclusion about which link is slow.
function ChainTotal({ hops }: { hops: RouteHop[] }) {
  if (hops.length === 0 || hops.some((h) => h.latency_ms === null)) return null;
  const total = hops.reduce((sum, h) => sum + (h.latency_ms ?? 0), 0);
  const stale = hops.some((h) => {
    const age = ageOf(h.latency_at);
    return age !== null && age > staleAfterMs;
  });
  return (
    <span
      className={`lat total ${stale ? "stale" : ""}`}
      title={
        stale
          ? "其中有链段已停止更新，合计不代表现状"
          : "各段建连耗时之和，不是端到端实测往返"
      }
    >
      合计 {total}ms{stale && " ⚠"}
    </span>
  );
}

// QuotaBar shows how much of a route's allowance is gone.
//
// An unmeasured period is not drawn as 0% used. The panel does not enforce a
// quota it cannot measure, and a bar sitting reassuringly at zero would imply
// the opposite of that.
function QuotaBar({ route, state }: { route: Route; state: QuotaState | undefined }) {
  const quota = route.quota_bytes;
  if (!quota) return null;

  if (!state || !state.measured) {
    return (
      <div className="muted" style={{ marginTop: 4, fontSize: 13 }}>
        额度：{formatBytes(quota)} / 周期{" "}
        <span className="tag warn" title="本周期还没有任何计数，无法判断用了多少，额度暂不执行">
          未计量
        </span>
      </div>
    );
  }

  const ratio = state.used_bytes / quota;
  const pct = Math.min(100, Math.round(ratio * 100));
  const tone = ratio >= 1 ? "err" : ratio >= 0.8 ? "warn" : "ok";

  return (
    <div style={{ marginTop: 10 }}>
      <div className="spread muted" style={{ marginBottom: 0, fontSize: 13 }}>
        <span>
          额度 {formatBytes(state.used_bytes)} / {formatBytes(quota)}
        </span>
        <span
          className={ratio >= 0.8 ? `tag ${tone}` : undefined}
          title={`本周期自 ${state.period_start} 起，每月 ${route.quota_reset_day} 号重置`}
        >
          {pct}%
        </span>
      </div>
      <div className="progress">
        <div className={`progress-bar ${tone === "ok" ? "" : tone}`} style={{ width: `${pct}%` }} />
      </div>
      {route.quota_paused_at && (
        <div
          className="tag err"
          style={{ marginTop: 8 }}
          title="下个周期开始后会自动恢复；想立刻恢复就调高额度"
        >
          已达额度，面板已自动停止
        </div>
      )}
    </div>
  );
}

export function Routes() {
  const [routes, setRoutes] = useState<Route[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [statuses, setStatuses] = useState<RouteStatus[]>([]);
  const [traffic, setTraffic] = useState<Record<string, Traffic>>({});
  const [quotas, setQuotas] = useState<QuotaState[]>([]);
  const [trafficFor, setTrafficFor] = useState<Route | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [warning, setWarning] = useState("");
  const [busyId, setBusyId] = useState<number | null>(null);
  const [editing, setEditing] = useState<Route | null>(null);
  const [creating, setCreating] = useState(false);
  const [applyResult, setApplyResult] = useState<ApplyResult | null>(null);
  const [verifyReport, setVerifyReport] = useState<VerifyReport | null>(null);
  const [view, setView] = useState<ViewMode>(storedView);
  const [confirm, confirmDialog] = useConfirm();

  function chooseView(next: ViewMode) {
    setView(next);
    localStorage.setItem(VIEW_KEY, next);
  }

  const fail = (err: unknown) => setError(err instanceof ApiError ? err.message : "请求失败");
  const nodeName = (id: number) => nodes.find((n) => n.id === id)?.name ?? `#${id}`;

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
    } catch (err) {
      fail(err);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // Liveness and latency are both resampled in the background and served from
  // the database, so polling them costs two queries rather than an SSH session
  // per hop.
  useEffect(() => {
    const timer = setInterval(() => {
      Promise.all([api.listRoutes(), api.status(), api.traffic(), api.quotas()])
        .then(([r, s, t, q]) => {
          setRoutes(r ?? []);
          setStatuses(s ?? []);
          setTraffic(t ?? {});
          setQuotas(q ?? []);
        })
        .catch(() => {
          // A failed poll leaves the previous state on screen. Raising it as an
          // error banner would bury whatever the operator is actually doing.
        });
    }, 10000);
    return () => clearInterval(timer);
  }, []);

  function hopState(routeId: number, hopOrder: number): HopState {
    const entry = statuses
      .find((s) => s.route_id === routeId)
      ?.hops.find((h) => h.hop_order === hopOrder);
    if (!entry || entry.running === null) return { kind: "unknown" };

    const age = ageOf(entry.checked_at);
    // A sample that stopped refreshing describes a node that stopped
    // answering. Reporting its last "up" as current would hide an outage.
    if (age !== null && age > staleAfterMs) {
      return { kind: "stale", since: age, wasRunning: entry.running };
    }
    return { kind: entry.running ? "up" : "down" };
  }

  async function act(
    id: number,
    fn: () => Promise<void>,
  ) {
    setBusyId(id);
    setError("");
    setNotice("");
    setWarning("");
    try {
      await fn();
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusyId(null);
    }
  }

  const quotaOf = (routeID: number) => quotas.find((q) => q.route_id === routeID);

  function chainOf(route: Route) {
    return (
      <div className="chain">
        {route.hops.map((hop) => {
          const state = hopState(route.id, hop.hop_order);
          return (
            <span key={hop.hop_order} className="chain">
              <span
                className={`hop ${state.kind === "down" ? "down" : ""} ${
                  state.kind === "stale" ? "unsure" : ""
                }`}
                title={hopStateHint(state)}
              >
                {nodeName(hop.node_id)}
                <span className="muted"> :{hop.relay_port}</span>
                {state.kind === "down" && <span className="muted"> ✕</span>}
                {state.kind === "stale" && <span className="muted"> ⚠</span>}
              </span>
              <Link ms={hop.latency_ms} at={hop.latency_at} />
            </span>
          );
        })}
        <span className="hop mono">{route.target}</span>
        <ChainTotal hops={route.hops} />
      </div>
    );
  }

  function trafficOf(route: Route) {
    const t = traffic[route.id];
    if (!t) {
      // 没采到不等于没跑流量，画成 0 会让人以为链路没在用
      return <span className="tag warn">未知</span>;
    }
    return (
      <>
        <span
          title={`统计自内核计数器，更新于 ${new Date(t.updated_at).toLocaleString("zh-CN")}`}
        >
          ↑ {formatBytes(t.bytes_in)} ／ ↓ {formatBytes(t.bytes_out)}
        </span>
        {/* 中间跳看不到链路在它之前丢掉的流量，数字只会偏小，
            不说清楚就等于把一个偏小的数字当成总量 */}
        {!t.from_entry && (
          <span
            className="tag warn"
            style={{ marginLeft: 6 }}
            title={`入口节点没有装 iptables，无法计数，这里退而取第 ${
              t.hop_order + 1
            } 跳。该跳看不到链路在它之前丢掉的流量，数字只会偏小。`}
          >
            非入口跳
          </span>
        )}
      </>
    );
  }

  function actionsOf(route: Route) {
    const busy = busyId === route.id;
    return (
      <div className="row nowrap">
        <button
          className="btn sm primary"
          disabled={busy}
          onClick={() =>
            void act(route.id, async () => {
              const result = await api.applyRoute(route.id);
              setApplyResult(result);
            })
          }
        >
          {busy ? "处理中…" : "下发"}
        </button>
        <button
          className="btn sm"
          disabled={busy}
          onClick={() =>
            void act(route.id, async () => {
              const report = await api.verifyRoute(route.id);
              setVerifyReport(report);
            })
          }
        >
          验证
        </button>
        <button className="btn sm" onClick={() => setTrafficFor(route)}>
          流量
        </button>
        <button className="btn sm" onClick={() => setEditing(route)}>
          编辑
        </button>
        <button
          className="btn sm"
          disabled={busy}
          onClick={() =>
            void act(route.id, async () => {
              await api.stopRoute(route.id);
              setNotice(`链路 ${route.name} 已停止`);
            })
          }
        >
          停止
        </button>
        <button
          className="btn sm danger"
          disabled={busy}
          onClick={() => {
            void (async () => {
              const ok = await confirm({
                title: `删除链路 ${route.name}？`,
                body: "面板会尽量清理各节点上的转发配置。连不上的节点会在删除后单独列出，需要你到那台机器上收尾。",
                confirmLabel: "删除链路",
                danger: true,
              });
              if (!ok) return;
              await act(route.id, async () => {
                const res = await api.deleteRoute(route.id);
                const left = res.leftovers ?? [];
                if (left.length === 0) {
                  setNotice(`链路 ${route.name} 已删除，各节点上的转发已清理`);
                  return;
                }
                // 记录没了但转发还在，是需要人去机器上收尾的状态，不能只报成功
                setWarning(
                  `链路 ${route.name} 已删除，但以下节点上的转发未能清理，` +
                    `机器恢复后需在其上执行卸载脚本：` +
                    left.map((l) => `${l.node_name}（${l.reason}）`).join("；"),
                );
              });
            })();
          }}
        >
          删除
        </button>
      </div>
    );
  }

  const counted = Object.values(traffic);
  const totalIn = counted.reduce((sum, t) => sum + t.bytes_in, 0);
  const totalOut = counted.reduce((sum, t) => sum + t.bytes_out, 0);

  return (
    <div>
      <PageHeader
        title="链路"
        desc="多跳转发链。流量从入口节点进入，逐跳中继，最后一跳拨向落地地址。"
        actions={
          <>
            {routes.length > 0 && (
              <div className="segmented" role="group" aria-label="视图">
                {VIEW_OPTIONS.map((option) => {
                  const Icon = option.icon;
                  return (
                    <button
                      key={option.id}
                      className={view === option.id ? "active" : ""}
                      title={option.label}
                      aria-pressed={view === option.id}
                      onClick={() => chooseView(option.id)}
                    >
                      <Icon size={15} />
                    </button>
                  );
                })}
              </div>
            )}
            <button className="btn primary" onClick={() => setCreating(true)}>
              <Plus size={15} />
              新建链路
            </button>
          </>
        }
      />

      {error && <Banner kind="err">{error}</Banner>}
      {warning && <Banner kind="warn">{warning}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      {routes.length > 0 && (
        <div className="stat-grid">
          <StatCard
            index={0}
            label="累计入向"
            value={counted.length === 0 ? null : formatBytes(totalIn)}
            sub={counted.length === 0 ? "还没有任何计数" : `来自 ${counted.length} 条链路`}
            icon={<ArrowDown size={17} />}
          />
          <StatCard
            index={1}
            label="累计出向"
            value={counted.length === 0 ? null : formatBytes(totalOut)}
            sub={counted.length === 0 ? "还没有任何计数" : `来自 ${counted.length} 条链路`}
            icon={<ArrowUp size={17} />}
            tone="warm"
          />
          <StatCard
            index={2}
            label="链路"
            value={String(routes.length)}
            sub={`${routes.filter((r) => r.enabled).length} 条已启用`}
            icon={<Waypoints size={17} />}
            tone="cool"
          />
        </div>
      )}

      {routes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Waypoints size={22} />}
            title="还没有链路"
            desc="先在节点页添加并探测节点，再来建第一条转发链路。"
            action={
              <button className="btn primary" onClick={() => setCreating(true)}>
                <Plus size={15} />
                新建链路
              </button>
            }
          />
        </Card>
      ) : (
        view === "list" ? (
          <Card className="flush">
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>链路</th>
                    <th>入口</th>
                    <th>落地</th>
                    <th>拓扑与延迟</th>
                    <th>流量</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {routes.map((route) => (
                    <tr key={route.id}>
                      <td>
                        <div className="row" style={{ gap: 6 }}>
                          <strong className="has-detail" title={onNodeIdentity(route.slug)}>
                            {route.name}
                          </strong>
                          <span className={`tag ${route.enabled ? "ok" : ""}`}>
                            {route.enabled ? "已启用" : "已停用"}
                          </span>
                        </div>
                        <div className="muted" style={{ fontSize: 12 }}>{route.protocol}</div>
                      </td>
                      <td className="mono nowrap">
                        <div className="row" style={{ gap: 4, flexWrap: "nowrap" }}>
                          {entryAddress(route, nodes)}
                          <CopyIconButton text={entryAddress(route, nodes)} title="复制入口地址" />
                        </div>
                      </td>
                      <td className="mono nowrap">
                        <div className="row" style={{ gap: 4, flexWrap: "nowrap" }}>
                          {route.target}
                          <CopyIconButton text={route.target} title="复制落地地址" />
                        </div>
                      </td>
                      <td>{chainOf(route)}</td>
                      <td className="nowrap">
                        {trafficOf(route)}
                        <QuotaBar route={route} state={quotaOf(route.id)} />
                      </td>
                      <td>{actionsOf(route)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        ) : (
          <div className={`route-grid${view === "compact" ? " dense" : ""}`}>
            {routes.map((route, cardIndex) => (
              <Card index={cardIndex} key={route.id}>
                <h2 style={{ marginBottom: 8 }}>
                  {/* The slug is only ever needed while logged into a node, so
                      it is one hover away rather than occupying the title. */}
                  <span className="has-detail" title={onNodeIdentity(route.slug)}>
                    {route.name}
                  </span>{" "}
                  <span className={`tag ${route.enabled ? "ok" : ""}`}>
                    {route.enabled ? "已启用" : "已停用"}
                  </span>{" "}
                  <span className="tag">{route.protocol}</span>
                </h2>

                {view === "compact" ? (
                  <div className="route-line">
                    <span>{entryAddress(route, nodes)}</span>
                    <CopyIconButton text={entryAddress(route, nodes)} title="复制入口地址" />
                    <span className="sep">→</span>
                    <span>{route.target}</span>
                    <CopyIconButton text={route.target} title="复制落地地址" />
                  </div>
                ) : (
                  <div className="addr-flow">
                    <div>
                      <div className="addr-label">客户端连接</div>
                      <div className="addr-box">
                        <span>{entryAddress(route, nodes)}</span>
                        <CopyIconButton text={entryAddress(route, nodes)} title="复制入口地址" />
                      </div>
                    </div>
                    <div className="addr-arrow">
                      <ArrowDown size={14} />
                    </div>
                    <div>
                      <div className="addr-label">落地</div>
                      <div className="addr-box">
                        <span>{route.target}</span>
                        <CopyIconButton text={route.target} title="复制落地地址" />
                      </div>
                    </div>
                  </div>
                )}

                {chainOf(route)}

                <div className="muted" style={{ marginTop: 10, fontSize: 13 }}>
                  流量：{trafficOf(route)}
                </div>

                <QuotaBar route={route} state={quotaOf(route.id)} />

                <div style={{ marginTop: 14 }}>{actionsOf(route)}</div>
              </Card>
            ))}
          </div>
        )
      )}

      {(creating || editing) && (
        <RouteForm
          nodes={nodes}
          route={editing}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSaved={async (msg) => {
            setCreating(false);
            setEditing(null);
            setNotice(msg);
            await load();
          }}
          onError={fail}
        />
      )}

      {applyResult && (
        <Modal title={`下发结果 · ${applyResult.route_name}`} onClose={() => setApplyResult(null)}>
          {applyResult.hops.some((h) => h.error) ? (
            <Banner kind="err">部分跳下发失败，链路可能不通。</Banner>
          ) : (
            <Banner kind="ok">全部跳下发成功。建议接着执行验证。</Banner>
          )}
          <table>
            <thead>
              <tr>
                <th>跳</th>
                <th>节点</th>
                <th>监听 → 转发</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {applyResult.hops.map((h) => (
                <tr key={h.hop_order}>
                  <td>{h.hop_order}</td>
                  <td>{h.node_name}</td>
                  <td className="mono">
                    :{h.listen} → {h.remote}
                  </td>
                  <td>
                    {h.error ? (
                      <span className="tag err">{h.error}</span>
                    ) : (
                      <span className={`tag ${h.changed ? "ok" : ""}`}>{h.action}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Modal>
      )}

      {trafficFor && (
        <TrafficDialog route={trafficFor} onClose={() => setTrafficFor(null)} />
      )}

      {verifyReport && (
        <Modal title={`验证结果 · ${verifyReport.route_name}`} onClose={() => setVerifyReport(null)}>
          {verifyReport.proven ? (
            <Banner kind="ok">
              抓包已证实数据真实抵达落地。这是唯一可信的结论。
            </Banner>
          ) : (
            <Banner kind="warn">
              未能用抓包证实端到端投递。注意：即使下面每一跳都能建连，也不代表链路真的在转发。
            </Banner>
          )}
          <ul className="checks">
            {verifyReport.checks.map((c, i) => (
              <li key={i}>
                <div className="row">
                  <span
                    className={`tag ${
                      c.verdict === "pass" ? "ok" : c.verdict === "fail" ? "err" : "warn"
                    }`}
                  >
                    {c.verdict === "pass" ? "通过" : c.verdict === "fail" ? "失败" : "未知"}
                  </span>
                  <strong>{c.name}</strong>
                  {c.latency_ms !== undefined && <span className="muted">{c.latency_ms}ms</span>}
                </div>
                <div className="muted" style={{ fontSize: 13 }}>
                  {c.detail}
                </div>
              </li>
            ))}
          </ul>
        </Modal>
      )}

      {confirmDialog}
    </div>
  );
}

// TrafficDialog shows a route's recent daily totals.
//
// Days with no traffic are absent from the response rather than present as
// zero, so the list is what actually moved bytes, not a padded calendar.
function TrafficDialog({ route, onClose }: { route: Route; onClose: () => void }) {
  const [days, setDays] = useState<DailyTraffic[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .routeTraffic(route.id, 30)
      .then((d) => setDays(d ?? []))
      .catch((err) => setError(err instanceof ApiError ? err.message : "读取失败"));
  }, [route.id]);

  const total = (days ?? []).reduce(
    (acc, d) => ({ in: acc.in + d.bytes_in, out: acc.out + d.bytes_out }),
    { in: 0, out: 0 },
  );

  return (
    <Modal title={`流量 · ${route.name}`} onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}

      {days === null ? (
        <p className="muted">读取中…</p>
      ) : days.length === 0 ? (
        <div className="card empty" style={{ marginBottom: 0 }}>
          还没有按天数据。计数规则在下一次下发时装到入口节点上，之后才开始累计。
        </div>
      ) : (
        <>
          <p className="hint" style={{ marginTop: 0 }}>
            近 {days.length} 天合计 ↑ {formatBytes(total.in)} ／ ↓ {formatBytes(total.out)}
            ，按 UTC+8 切分自然日。
          </p>
          <table>
            <thead>
              <tr>
                <th>日期</th>
                <th className="nowrap">上行</th>
                <th className="nowrap">下行</th>
              </tr>
            </thead>
            <tbody>
              {days.map((d) => (
                <tr key={d.day}>
                  <td className="mono">{d.day}</td>
                  <td className="mono nowrap">{formatBytes(d.bytes_in)}</td>
                  <td className="mono nowrap">{formatBytes(d.bytes_out)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}

      <p className="hint" style={{ marginBottom: 0, marginTop: 12 }}>
        数字取自入口节点的内核计数器，代表这条链路搬运的字节数。机器重启或防火墙被
        清空会让计数器归零，面板已按此累加，不会丢账。
      </p>
    </Modal>
  );
}

interface RouteFormProps {
  nodes: Node[];
  route: Route | null;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onError: (err: unknown) => void;
}

function RouteForm({ nodes, route, onClose, onSaved, onError }: RouteFormProps) {
  const [name, setName] = useState(route?.name ?? "");
  const [target, setTarget] = useState(route?.target ?? "");
  const [protocol, setProtocol] = useState<RouteInput["protocol"]>(route?.protocol ?? "tcp");
  const [hops, setHops] = useState<number[]>(route?.hops.map((h) => h.node_id) ?? []);
  const [entryPort, setEntryPort] = useState<string>(route ? String(route.entry_port) : "");
  // 额度以 GB 输入。空串表示不限额 —— 和 0 不是一回事，0 表示一个字节都不许跑。
  const [quotaGB, setQuotaGB] = useState<string>(
    route?.quota_bytes ? String(route.quota_bytes / 1024 ** 3) : "",
  );
  const [resetDay, setResetDay] = useState<number>(route?.quota_reset_day ?? 1);
  const [busy, setBusy] = useState(false);

  const usable = nodes.filter((n) => n.arch !== "");
  const udpBlockers =
    protocol === "tcp+udp"
      ? hops
          .map((id) => nodes.find((n) => n.id === id))
          .filter((n): n is Node => !!n && n.udp_capable === false)
      : [];

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    const input: RouteInput = {
      name,
      target,
      protocol,
      node_ids: hops,
      entry_port: entryPort ? Number(entryPort) : null,
      enabled: true,
      quota_bytes: quotaGB.trim() === "" ? null : Math.round(Number(quotaGB) * 1024 ** 3),
      quota_reset_day: resetDay,
    };
    try {
      if (route) {
        // Only forwarding behaviour reaches the nodes. Telling the operator to
        // redeploy after a rename would send them to restart relays, dropping
        // live connections, for a change the nodes never see.
        const deployable =
          target !== route.target ||
          protocol !== route.protocol ||
          hops.join() !== route.hops.map((h) => h.node_id).join() ||
          (entryPort !== "" && Number(entryPort) !== route.entry_port);
        await api.updateRoute(route.id, input);
        await onSaved(
          deployable
            ? `链路 ${name} 已更新，记得重新下发`
            : `链路 ${name} 已更新（仅名称变更，节点上无需改动）`,
        );
      } else {
        await api.createRoute(input);
        await onSaved(`链路 ${name} 已创建，请执行下发`);
      }
    } catch (err) {
      onError(err);
      setBusy(false);
    }
  }

  return (
    <Modal title={route ? `编辑链路 · ${route.name}` : "新建链路"} onClose={onClose}>
      <form onSubmit={submit}>
        <label>
          名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="随便起，支持中文"
            required
          />
        </label>
        {!route && (
          <p className="hint">
            节点上的服务名和配置文件名会用自动生成的英文标识，中文名称不影响部署。
          </p>
        )}

        <div className="grid2">
          <label>
            落地地址
            <input
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder="host:port"
              required
            />
          </label>
          <label>
            协议
            <select
              value={protocol}
              onChange={(e) => setProtocol(e.target.value as RouteInput["protocol"])}
            >
              <option value="tcp">仅 TCP</option>
              <option value="tcp+udp">TCP + UDP</option>
            </select>
          </label>
        </div>

        {udpBlockers.length > 0 && (
          <Banner kind="err">
            {udpBlockers.map((n) => n.name).join("、")} 探测确认不通 UDP，这条链路会被拒绝创建。
            改成仅 TCP，或把该节点换掉。
          </Banner>
        )}

        <label>
          入口端口
          <input
            type="number"
            value={entryPort}
            onChange={(e) => setEntryPort(e.target.value)}
            placeholder="留空则自动从端口池分配"
          />
        </label>

        <div className="grid2">
          <label>
            流量额度（GB）
            <input
              type="number"
              step="any"
              min="0"
              value={quotaGB}
              onChange={(e) => setQuotaGB(e.target.value)}
              placeholder="留空表示不限"
            />
          </label>
          <label>
            每月重置日
            <select value={resetDay} onChange={(e) => setResetDay(Number(e.target.value))}>
              {Array.from({ length: 28 }, (_, i) => i + 1).map((d) => (
                <option key={d} value={d}>
                  {d} 号
                </option>
              ))}
            </select>
          </label>
        </div>
        <p className="hint">
          按上行 + 下行合计，对齐服务商的计费口径（客户端下载 1GB → 机器进 1GB 出 1GB =
          账单 2GB）。跑满后面板会自动停掉这条链路，下个周期自动恢复。
          重置日只到 28 号，29 号之后并非每月都有。
        </p>

        <h2 style={{ marginTop: 16 }}>转发链</h2>
        <p className="hint">按顺序选择节点，第一个是入口，最后一个负责拨向落地。</p>

        {hops.map((nodeId, i) => (
          <div className="row" key={i} style={{ marginBottom: 8 }}>
            <span className="muted nowrap" style={{ width: 52 }}>
              第 {i + 1} 跳
            </span>
            <select
              value={nodeId}
              onChange={(e) => {
                const next = [...hops];
                next[i] = Number(e.target.value);
                setHops(next);
              }}
              style={{ flex: 1 }}
            >
              {usable.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name} ({n.host})
                </option>
              ))}
            </select>
            <button
              type="button"
              className="btn sm danger"
              onClick={() => setHops(hops.filter((_, idx) => idx !== i))}
            >
              移除
            </button>
          </div>
        ))}

        <button
          type="button"
          className="btn sm"
          disabled={usable.length === 0}
          onClick={() => setHops([...hops, usable[0]?.id])}
        >
          添加一跳
        </button>
        {usable.length === 0 && (
          <p className="hint">没有已探测的节点可用，请先到节点页完成探测。</p>
        )}

        <div className="row" style={{ justifyContent: "flex-end", marginTop: 18 }}>
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy || hops.length === 0}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
