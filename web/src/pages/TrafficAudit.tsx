import { Fragment, useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, Globe, Network, Pause, Play, Radar, Server, type LucideIcon } from "lucide-react";
import { api, ApiError, type TrafficAuditPage, type TrafficAuditRecord } from "../api";
import { Banner } from "../components/Modal";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { ListToolbar } from "../components/ListToolbar";
import { PageHeader } from "../components/PageHeader";
import { regionOf, TONE_CYCLE } from "../lib/region";
import "./management-redesign.css";

const REFRESH_MS = 3000;

/** "host:port" → host 与 :port 分开画；IPv6 的方括号留在 host 里。 */
function splitTarget(target: string): [string, string] {
  const cut = target.lastIndexOf(":");
  if (cut <= 0 || target.slice(cut + 1).includes("]")) return [target, ""];
  return [target.slice(0, cut), target.slice(cut)];
}

const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;

// 头像取站点主域的首字母，颜色由主域决定：同一个网站在整页里永远是同一个圆。
function siteOf(host: string): { letter: string; tone: string } {
  if (IPV4.test(host) || host.startsWith("[")) return { letter: "#", tone: "muted" };
  const labels = host.split(".");
  const main = labels.length >= 2 ? labels[labels.length - 2] : host;
  let hash = 0;
  for (const char of main) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return { letter: main.charAt(0).toUpperCase(), tone: TONE_CYCLE[hash % TONE_CYCLE.length] };
}

function minuteOf(at: Date): string {
  return at.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
}

function SummaryFigure({ icon: Icon, tone, label, value, note }: { icon: LucideIcon; tone: string; label: string; value: number; note?: string }) {
  return (
    <div className={`tone-${tone}`}>
      <i><Icon size={16} /></i>
      <dt>{label}</dt>
      <dd>{value}{note && <small>{note}</small>}</dd>
    </div>
  );
}

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
  // 上一次刷新时见过的最大 ID，比它新的行闪一下，一眼看出刚进来的是哪些。
  const seenUpTo = useRef(0);
  const [freshAbove, setFreshAbove] = useState(Number.POSITIVE_INFINITY);

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .trafficAudit(page, { machine, q: query, until })
        .then((next) => {
          if (cancelled) return;
          const newest = Math.max(0, ...(next.items ?? []).map((r) => r.id));
          setFreshAbove(seenUpTo.current === 0 ? Number.POSITIVE_INFINITY : seenUpTo.current);
          seenUpTo.current = Math.max(seenUpTo.current, newest);
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
  const distinctTargets = new Set(items.map((r) => splitTarget(r.target)[0])).size;
  const distinctNodes = new Set(items.map((r) => r.node)).size;

  const togglePause = () => {
    if (paused) {
      setUntil(0);
      setPage(1);
      return;
    }
    // 第一页第一条就是最新的；不在第一页时冻结在页面里见过的最大 ID。
    setUntil(Math.max(1, ...items.map((r) => r.id)));
  };

  let lastMinute = "";

  return (
    <div className="management-page audit-management">
      <PageHeader
        title="流量审计"
        desc={`每台机器上的每个节点访问了什么。只保留最近 ${data?.capacity ?? 1000} 条，面板重启后从头开始。`}
        actions={
          <>
            <span className={`live-pill${paused ? " paused" : ""}`} role="status">
              <i />
              {paused ? "已暂停" : `实时 · 每 ${REFRESH_MS / 1000} 秒`}
            </span>
            <button className={`btn ${paused ? "primary" : ""}`} onClick={togglePause}>
              {paused ? <Play size={15} /> : <Pause size={15} />}
              {paused ? "继续刷新" : "暂停"}
            </button>
          </>
        }
      />

      {error && <Banner kind="err">{error}</Banner>}

      <dl className="stat-strip">
        <SummaryFigure icon={Radar} tone="lime" label="记录" value={total} note={`/ ${data?.capacity ?? 1000} 条上限`} />
        <SummaryFigure icon={Server} tone="sky" label="机器" value={machines.length} />
        <SummaryFigure icon={Globe} tone="lilac" label="本页网站" value={distinctTargets} />
        <SummaryFigure icon={Network} tone="peach" label="本页节点" value={distinctNodes} />
      </dl>

      <ListToolbar
        search={query}
        onSearch={(value) => {
          setQuery(value);
          setPage(1);
        }}
        searchLabel="搜索网址或节点"
        placeholder="搜索网址或节点…"
        filterLabel="按机器筛选"
        filter={machine}
        onFilter={(value) => {
          setMachine(value);
          setPage(1);
        }}
        filters={[
          { value: "", label: "全部机器" },
          ...machines.map((m) => {
            const code = regionOf(m);
            return { value: m, label: m, icon: code ? <Flag code={code} className="chip-flag" /> : undefined };
          }),
        ]}
      />

      <div className="audit-bar">
        <span className="audit-range">
          {paused && <b className="audit-frozen">已冻结</b>}
          第 <strong>{first}–{last}</strong> 条，共 {total} 条
        </span>
        <Pager page={page} pages={pages} onPage={setPage} />
      </div>

      <div className="audit-feed">
        {items.length === 0 ? (
          <EmptyState icon={<Radar size={22} />} title="还没有记录" desc="节点访问网站后，几秒内就会出现在这里。" />
        ) : (
          items.map((r) => {
            const at = new Date(r.at);
            const minute = minuteOf(at);
            const showMinute = minute !== lastMinute;
            lastMinute = minute;
            return (
              <Fragment key={r.id}>
                {showMinute && <div className="audit-minute">{minute}</div>}
                <AuditRow record={r} at={at} fresh={!paused && r.id > freshAbove} />
              </Fragment>
            );
          })
        )}
      </div>

      {pages > 1 && (
        <div className="audit-bar bottom">
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
    <span className="audit-pager">
      <button className="audit-pager-step" disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label="上一页">
        <ChevronLeft size={16} />
      </button>
      <span className="audit-pager-label">
        <b>{page}</b> / {pages}
      </span>
      <button className="audit-pager-step" disabled={page >= pages} onClick={() => onPage(page + 1)} aria-label="下一页">
        <ChevronRight size={16} />
      </button>
      <input
        inputMode="numeric"
        placeholder="页码"
        aria-label="跳转到第几页"
        value={draft}
        onChange={(event) => setDraft(event.target.value.replace(/\D/g, ""))}
        onKeyDown={(event) => {
          if (event.key === "Enter") jump();
        }}
      />
      <button className="audit-pager-go" disabled={draft === ""} onClick={jump}>
        跳转
      </button>
    </span>
  );
}

function AuditRow({ record: r, at, fresh }: { record: TrafficAuditRecord; at: Date; fresh: boolean }) {
  const [host, port] = splitTarget(r.target);
  const site = siteOf(host);
  const code = regionOf(r.machine);
  return (
    <div className={`audit-row${fresh ? " is-fresh" : ""}`}>
      <time className="audit-time" dateTime={r.at} title={at.toLocaleString("zh-CN")}>
        :{String(at.getSeconds()).padStart(2, "0")}
      </time>
      <span className={`audit-site tone-${site.tone}`} aria-hidden>{site.letter}</span>
      <span className="audit-target">
        <strong>{host}</strong>
        {port && <span className="audit-port">{port}</span>}
        {r.count > 1 && <span className="audit-count" title={`同一节点连续访问了 ${r.count} 次`}>×{r.count}</span>}
      </span>
      <span className="audit-node">
        <span className="audit-node-name">
          {code && <Flag code={code} className="chip-flag" />}
          {r.name}
        </span>
        <span className="audit-node-meta">
          {r.port ? `${r.machine} · :${r.port}` : r.machine}
          {r.protocol && ` · ${r.protocol}`}
        </span>
      </span>
    </div>
  );
}
