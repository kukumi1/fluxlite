import { useEffect, useState } from "react";
import { ChevronLeft, ChevronRight, Pause, Play, Radar } from "lucide-react";
import { api, ApiError, type TrafficAuditPage, type TrafficAuditRecord } from "../api";
import { Banner } from "../components/Modal";
import { Card } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";

const REFRESH_MS = 3000;

export function TrafficAudit() {
  const [page, setPage] = useState(1);
  const [machine, setMachine] = useState("");
  const [query, setQuery] = useState("");
  const [data, setData] = useState<TrafficAuditPage | null>(null);
  const [error, setError] = useState("");
  // 暂停时记下当时最新一条的 ID：之后翻页、筛选都只看那一刻之前的记录，
  // 新来的连接不会把正在排查的行挤走。0 表示没暂停。
  const [until, setUntil] = useState(0);
  const paused = until !== 0;

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .trafficAudit(page, { machine, q: query, until })
        .then((next) => {
          if (cancelled) return;
          setData(next);
          setError("");
        })
        .catch((err) => {
          if (!cancelled) setError(err instanceof ApiError ? err.message : "请求失败");
        });
    load();
    const timer = paused ? undefined : window.setInterval(load, REFRESH_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [page, machine, query, until, paused]);

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const pageSize = data?.page_size ?? 100;
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const first = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const last = Math.min(page * pageSize, total);

  const machines = data?.machines ?? [];

  const togglePause = () => {
    if (paused) {
      setUntil(0);
      setPage(1);
      return;
    }
    // 第一页第一条就是最新的；不在第一页时冻结在页面里见过的最大 ID。
    setUntil(Math.max(1, ...items.map((r) => r.id)));
  };

  return (
    <div className="management-page">
      <PageHeader
        title="流量审计"
        desc={`每台机器上的每个节点访问了什么。只保留最近 ${data?.capacity ?? 1000} 条，面板重启后从头开始。`}
      />

      {error && <Banner kind="err">{error}</Banner>}

      <Card className="traffic-audit-filters">
        <label>
          机器
          <select
            value={machine}
            onChange={(event) => {
              setMachine(event.target.value);
              setPage(1);
            }}
          >
            <option value="">全部机器</option>
            {machines.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label className="traffic-audit-query">
          搜索
          <input
            placeholder="网址或节点"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setPage(1);
            }}
          />
        </label>
        <button
          className="btn ghost"
          onClick={() => {
            setMachine("");
            setQuery("");
            setPage(1);
          }}
        >
          重置
        </button>
      </Card>

      <div className="traffic-audit-bar">
        <button className={`btn ${paused ? "" : "ghost"}`} onClick={togglePause}>
          {paused ? <Play size={15} /> : <Pause size={15} />}
          {paused ? "继续刷新" : "暂停"}
        </button>
        <span className="muted">{paused ? "已暂停，新记录不会显示" : "每 3 秒自动刷新"}</span>
        <span className="traffic-audit-bar-spacer" />
        <span className="muted">
          {first}-{last} / {total}
        </span>
        <Pager page={page} pages={pages} onPage={setPage} />
      </div>

      <Card className="flush traffic-audit">
        {items.length === 0 ? (
          <EmptyState
            icon={<Radar size={22} />}
            title="还没有记录"
            desc="节点访问网站后，几秒内就会出现在这里。"
          />
        ) : (
          <table>
            <thead>
              <tr>
                <th>时间</th>
                <th>访问目标</th>
                <th>节点</th>
              </tr>
            </thead>
            <tbody>
              {items.map((r) => (
                <AuditRow key={r.id} record={r} />
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {pages > 1 && (
        <div className="traffic-audit-pager">
          <Pager page={page} pages={pages} onPage={setPage} />
        </div>
      )}
    </div>
  );
}

function Pager({ page, pages, onPage }: { page: number; pages: number; onPage: (page: number) => void }) {
  const [draft, setDraft] = useState("");
  const jump = () => {
    const target = Number.parseInt(draft, 10);
    if (Number.isFinite(target)) onPage(Math.min(Math.max(target, 1), pages));
    setDraft("");
  };
  return (
    <span className="traffic-audit-pages">
      <button className="btn ghost" disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label="上一页">
        <ChevronLeft size={15} />
      </button>
      <span className="muted">
        第 {page} / {pages} 页
      </span>
      <button className="btn ghost" disabled={page >= pages} onClick={() => onPage(page + 1)} aria-label="下一页">
        <ChevronRight size={15} />
      </button>
      <input
        className="traffic-audit-jump"
        inputMode="numeric"
        placeholder="页码"
        aria-label="跳转到第几页"
        value={draft}
        onChange={(event) => setDraft(event.target.value.replace(/\D/g, ""))}
        onKeyDown={(event) => {
          if (event.key === "Enter") jump();
        }}
      />
      <button className="btn ghost" disabled={draft === ""} onClick={jump}>
        跳转
      </button>
    </span>
  );
}

function AuditRow({ record: r }: { record: TrafficAuditRecord }) {
  const at = new Date(r.at);
  return (
    <tr>
      <td className="mono nowrap" title={at.toLocaleString("zh-CN")}>
        {at.toLocaleTimeString("zh-CN", { hour12: false })}
      </td>
      <td>
        <span className="mono traffic-audit-target">{r.target}</span>
        {r.count > 1 && <span className="tag">×{r.count}</span>}
      </td>
      <td>
        {r.name}
        <div className="muted traffic-audit-note">
          {r.port ? `${r.machine} · 端口 ${r.port}` : r.machine}
          {r.protocol && ` · ${r.protocol}`}
        </div>
      </td>
    </tr>
  );
}
