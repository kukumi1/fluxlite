import { useEffect, useState } from "react";
import {
  CalendarClock, CircleDot, Globe, KeyRound, Network, ScrollText, Server, ShieldAlert, TerminalSquare, TriangleAlert, Waypoints, type LucideIcon,
} from "lucide-react";
import { api, ApiError, type AuditEntry } from "../api";
import { Banner } from "../components/Modal";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { ListToolbar } from "../components/ListToolbar";
import { PageHeader } from "../components/PageHeader";
import { AUDIT_CATEGORIES, describeAction, type AuditAction, type AuditCategory } from "../lib/audit-actions";
import { regionOf } from "../lib/region";
import "./management-redesign.css";

const LIMIT = 200;
const FAILED = "failed";

const CATEGORY_ICON: Record<AuditCategory, LucideIcon> = {
  account: KeyRound,
  machine: Server,
  route: Waypoints,
  node: Network,
  subscription: ScrollText,
  console: TerminalSquare,
  other: CircleDot,
};

// 这些类别的对象是机器名或链路名，带地区的就配国旗；账号类的对象是用户名，不猜。
const FLAGGED: ReadonlySet<AuditCategory> = new Set(["machine", "route", "node", "console"]);

interface Described extends AuditAction {
  entry: AuditEntry;
  at: Date;
}

function dayKey(at: Date): string {
  return `${at.getFullYear()}-${at.getMonth()}-${at.getDate()}`;
}

function dayLabel(at: Date, today: Date): string {
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (dayKey(at) === dayKey(today)) return "今天";
  if (dayKey(at) === dayKey(yesterday)) return "昨天";
  return at.toLocaleDateString("zh-CN", { month: "long", day: "numeric", weekday: "short" });
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

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("");

  useEffect(() => {
    api
      .audit(LIMIT)
      .then((e) => setEntries(e ?? []))
      .catch((err) => setError(err instanceof ApiError ? err.message : "请求失败"));
  }, []);

  const today = new Date();
  const described: Described[] = entries.map((entry) => ({ entry, at: new Date(entry.ts), ...describeAction(entry.action) }));
  const failedCount = described.filter((d) => d.failed).length;
  const todayCount = described.filter((d) => dayKey(d.at) === dayKey(today)).length;
  const sourceCount = new Set(entries.map((e) => e.ip).filter(Boolean)).size;

  const needle = query.trim().toLowerCase();
  const visible = described.filter((d) => {
    if (filter === FAILED ? !d.failed : filter !== "" && d.category !== filter) return false;
    if (!needle) return true;
    const { actor, action, target, detail, ip } = d.entry;
    return [d.label, actor, action, target, detail, ip].some((field) => field.toLowerCase().includes(needle));
  });

  const days: { key: string; label: string; items: Described[] }[] = [];
  for (const d of visible) {
    const key = dayKey(d.at);
    const last = days[days.length - 1];
    if (last?.key === key) last.items.push(d);
    else days.push({ key, label: dayLabel(d.at, today), items: [d] });
  }

  const present = AUDIT_CATEGORIES.filter((c) => described.some((d) => d.category === c.id));

  return (
    <div className="management-page log-management">
      <PageHeader title="审计日志" desc={`登录、机器、链路、节点和订阅的每一次变更，显示最近 ${LIMIT} 条。`} />

      {error && <Banner kind="err">{error}</Banner>}

      <dl className="stat-strip">
        <SummaryFigure icon={ScrollText} tone="lilac" label="记录" value={entries.length} note={`/ 最近 ${LIMIT} 条`} />
        <SummaryFigure icon={CalendarClock} tone="lime" label="今天" value={todayCount} />
        <SummaryFigure icon={ShieldAlert} tone="peach" label="失败" value={failedCount} />
        <SummaryFigure icon={Globe} tone="sky" label="来源 IP" value={sourceCount} />
      </dl>

      <ListToolbar
        search={query}
        onSearch={setQuery}
        searchLabel="搜索操作、对象或 IP"
        placeholder="搜索操作、对象或 IP…"
        filterLabel="按类别筛选"
        filter={filter}
        onFilter={setFilter}
        filters={[
          { value: "", label: "全部", count: described.length },
          ...present.map((c) => ({ value: c.id, label: c.label, count: described.filter((d) => d.category === c.id).length })),
          ...(failedCount > 0 ? [{ value: FAILED, label: "失败", count: failedCount, tone: "err" as const }] : []),
        ]}
      />

      {days.length === 0 ? (
        <div className="card">
          <EmptyState
            icon={<ScrollText size={22} />}
            title={entries.length === 0 ? "暂无记录" : "没有符合条件的记录"}
            desc={entries.length === 0 ? "登录或修改配置后，这里会留下记录。" : "换个关键词或类别试试。"}
          />
        </div>
      ) : (
        days.map((day) => (
          <section className="log-day" key={day.key}>
            <h2 className="log-day-label">
              {day.label}
              <small>{day.items.length} 条</small>
            </h2>
            <ol className="log-list">
              {day.items.map((d) => (
                <LogRow key={d.entry.id} item={d} />
              ))}
            </ol>
          </section>
        ))
      )}
    </div>
  );
}

function LogRow({ item }: { item: Described }) {
  const { entry, at, category, label, failed } = item;
  const tone = AUDIT_CATEGORIES.find((c) => c.id === category)?.tone ?? "muted";
  const Icon = failed ? TriangleAlert : CATEGORY_ICON[category];
  const code = FLAGGED.has(category) ? regionOf(entry.target) : null;
  return (
    <li className={`log-row tone-${tone}${failed ? " failed" : ""}`}>
      <time className="log-time" dateTime={entry.ts} title={at.toLocaleString("zh-CN")}>
        {at.toLocaleTimeString("zh-CN", { hour12: false })}
      </time>
      <span className="log-icon" aria-hidden><Icon size={15} /></span>
      <div className="log-main">
        <div className="log-title">
          <strong title={entry.action}>{label}</strong>
          {entry.target && (
            <span className="log-target">
              {code && <Flag code={code} className="chip-flag" />}
              {entry.target}
            </span>
          )}
        </div>
        {entry.detail && <p className="log-detail">{entry.detail}</p>}
      </div>
      <div className="log-who">
        <span>{entry.actor || "系统"}</span>
        {entry.ip && <code>{entry.ip}</code>}
      </div>
    </li>
  );
}
