import { useEffect, useState } from "react";
import {
  api,
  ApiError,
  type ApplyResult,
  type Node,
  type Route,
  type RouteHop,
  type RouteInput,
  type RouteStatus,
  type VerifyReport,
} from "../api";
import { Banner, Modal } from "../components/Modal";

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

// staleAfterMs is how long a sample stays trustworthy. Sampling runs every 30
// seconds, so anything several rounds old means the node stopped answering and
// the number on screen is frozen, not live.
const staleAfterMs = 150000;

function ageOf(at: string | null): number | null {
  if (!at) return null;
  const ms = Date.now() - new Date(at).getTime();
  return Number.isFinite(ms) ? ms : null;
}

function describeAge(ms: number): string {
  if (ms < 60000) return `${Math.max(1, Math.round(ms / 1000))} 秒前`;
  if (ms < 3600000) return `${Math.round(ms / 60000)} 分钟前`;
  return `${Math.round(ms / 3600000)} 小时前`;
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

export function Routes() {
  const [routes, setRoutes] = useState<Route[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [statuses, setStatuses] = useState<RouteStatus[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busyId, setBusyId] = useState<number | null>(null);
  const [editing, setEditing] = useState<Route | null>(null);
  const [creating, setCreating] = useState(false);
  const [applyResult, setApplyResult] = useState<ApplyResult | null>(null);
  const [verifyReport, setVerifyReport] = useState<VerifyReport | null>(null);

  const fail = (err: unknown) => setError(err instanceof ApiError ? err.message : "请求失败");
  const nodeName = (id: number) => nodes.find((n) => n.id === id)?.name ?? `#${id}`;

  async function load() {
    try {
      const [r, n, s] = await Promise.all([api.listRoutes(), api.listNodes(), api.status()]);
      setRoutes(r ?? []);
      setNodes(n ?? []);
      setStatuses(s ?? []);
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
      Promise.all([api.listRoutes(), api.status()])
        .then(([r, s]) => {
          setRoutes(r ?? []);
          setStatuses(s ?? []);
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
    try {
      await fn();
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusyId(null);
    }
  }

  return (
    <div>
      <div className="spread">
        <div>
          <h1>链路</h1>
          <p className="page-desc">
            多跳转发链。流量从入口节点进入，逐跳中继，最后一跳拨向落地地址。
          </p>
        </div>
        <button className="btn primary" onClick={() => setCreating(true)}>
          新建链路
        </button>
      </div>

      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      {routes.length === 0 ? (
        <div className="card empty">还没有链路。先在节点页添加并探测节点，再来建链路。</div>
      ) : (
        routes.map((route) => (
          <div className="card" key={route.id}>
            <div className="spread">
              <div>
                <h2 style={{ marginBottom: 6 }}>
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
                <div className="muted" style={{ marginTop: 8, fontSize: 13 }}>
                  客户端连接：
                  <code>
                    {nodes.find((n) => n.id === route.hops[0]?.node_id)?.host ?? "?"}:
                    {route.entry_port}
                  </code>
                </div>
              </div>
            </div>

            <div className="row" style={{ marginTop: 14 }}>
              <button
                className="btn primary"
                disabled={busyId === route.id}
                onClick={() =>
                  void act(route.id, async () => {
                    const result = await api.applyRoute(route.id);
                    setApplyResult(result);
                  })
                }
              >
                {busyId === route.id ? "处理中…" : "下发"}
              </button>
              <button
                className="btn"
                disabled={busyId === route.id}
                onClick={() =>
                  void act(route.id, async () => {
                    const report = await api.verifyRoute(route.id);
                    setVerifyReport(report);
                  })
                }
              >
                验证
              </button>
              <button className="btn" onClick={() => setEditing(route)}>
                编辑
              </button>
              <button
                className="btn"
                disabled={busyId === route.id}
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
                className="btn danger"
                disabled={busyId === route.id}
                onClick={() => {
                  if (!confirm(`确认删除链路 ${route.name}？会同时清理各节点上的配置。`)) return;
                  void act(route.id, async () => {
                    await api.deleteRoute(route.id);
                    setNotice(`链路 ${route.name} 已删除`);
                  });
                }}
              >
                删除
              </button>
            </div>
          </div>
        ))
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
    </div>
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
