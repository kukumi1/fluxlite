import { useEffect, useMemo, useState } from "react";
import { Plus, Settings2, ShieldAlert, Trash2 } from "lucide-react";
import { api, ApiError, type BlockState, type LatencyOverview, type LatencyTarget, type Node } from "../api";
import { Banner, Modal } from "../components/Modal";
import { Flag } from "../components/Flag";
import { PageHeader } from "../components/PageHeader";
import { regionOf } from "../lib/region";
import { lastChecked } from "../lib/format";
import { BlockCell, BlockDetailDialog, isBlocked } from "./BlockCheck";
import { ReturnRoutesSection } from "./ReturnRoutes";
import "./line-quality.css";

// The page watches what can only be seen from the machine itself: whether
// China can still reach it, and which way its traffic comes back. Latency
// and loss are the probe agents' to measure, on the probe page.
export function LineQuality() {
  const [overview, setOverview] = useState<LatencyOverview | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [editingTargets, setEditingTargets] = useState(false);
  const [blockFor, setBlockFor] = useState<Node | null>(null);

  async function load() {
    try {
      const [o, n] = await Promise.all([api.latencyOverview(), api.listNodes()]);
      setOverview(o);
      setNodes(n ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取线路质量失败");
    }
  }

  useEffect(() => {
    void load();
    // Checks run every six hours, or when asked for; a minute is soon enough
    // to show one that finished.
    const timer = setInterval(() => void load(), 60_000);
    return () => clearInterval(timer);
  }, []);

  async function act<T>(id: string, fn: () => Promise<T>, done: string | ((result: T) => string)) {
    setBusy(id);
    setError("");
    setNotice("");
    try {
      const result = await fn();
      await load();
      setNotice(typeof done === "function" ? done(result) : done);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "操作失败");
    } finally {
      setBusy(null);
    }
  }

  const blocks = useMemo(() => {
    const byNode = new Map<number, BlockState[]>();
    for (const b of overview?.blocks ?? []) byNode.set(b.node_id, [...(byNode.get(b.node_id) ?? []), b]);
    return byNode;
  }, [overview]);

  async function runBlockCheck() {
    await act("block", async () => {
      const results = await api.blockCheck();
      return results.filter((r) => r.families.some((f) => isBlocked(f.kind))).length;
    }, (blocked) => (blocked === 0 ? "封锁检测完成：所有机器到国内都正常" : `封锁检测完成：${blocked} 台机器被封锁`));
  }

  return (
    <div className="management-page line-quality-page">
      <PageHeader
        title="线路质量"
        desc="IP 封锁和三网回程每 6 小时自动检测一次，也可以随时手动检测。延迟和丢包由探针的 agent 实时测量，在「探针」页设置延迟任务，在公开页查看图表。"
      />
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="quality-section-head">
        <div>
          <h2>IP 封锁</h2>
          <p>
            从每台机器对国内各运营商的测试点分别 ping 和连 TCP：ping 通但 TCP 不通是 TCP 阻断，两样都不通是疑似 IP 被墙。变成封锁、以及恢复时都发 Telegram。
            <span className="quality-checked"> {lastChecked((overview?.blocks ?? []).map((b) => b.checked_at))} · 每 6 小时自动</span>
          </p>
        </div>
        <div className="quality-section-actions">
          <button className="btn" disabled={busy !== null} onClick={() => void runBlockCheck()}><ShieldAlert size={15} className={busy === "block" ? "spin" : undefined} />{busy === "block" ? "检测中…" : "立即检测"}</button>
          <button className="btn" onClick={() => setEditingTargets(true)}><Settings2 size={15} />测试点</button>
        </div>
      </div>

      <div className="card flush quality-table-wrap">
        <table className="quality-table">
          <thead>
            <tr>
              <th>机器</th>
              <th>封锁</th>
            </tr>
          </thead>
          <tbody>
            {nodes.map((n) => {
              const code = regionOf(n.name);
              const offline = n.status !== "online";
              return (
                <tr key={n.id} className={offline ? "is-offline" : undefined}>
                  <th scope="row">
                    <span className="quality-node">
                      {code && <Flag code={code} />}
                      <span>{n.name}</span>
                      {offline && <span className="tag">离线</span>}
                    </span>
                  </th>
                  <td className="quality-block">
                    <BlockCell node={n} states={blocks.get(n.id) ?? []} onOpen={() => setBlockFor(n)} />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {blockFor && (blocks.get(blockFor.id)?.length ?? 0) > 0 && (
        <BlockDetailDialog node={blockFor} states={blocks.get(blockFor.id) ?? []} onClose={() => setBlockFor(null)} />
      )}
      {overview && (
        <ReturnRoutesSection
          overview={overview.return}
          nodes={nodes}
          busy={busy}
          onTrace={(n) => void act(`t${n.id}`, () => api.traceReturnRoutes(n.id), `${n.name} 的回程已重新检测`)}
          onSaved={async () => {
            setNotice("回程设置已保存，下一轮检测开始生效");
            await load();
          }}
        />
      )}
      {editingTargets && overview && (
        <TargetsDialog
          targets={overview.targets}
          presets={overview.presets}
          onClose={() => setEditingTargets(false)}
          onSaved={async () => {
            setEditingTargets(false);
            setNotice("测试点已保存，下一次封锁检测开始生效");
            await load();
          }}
        />
      )}
    </div>
  );
}

// A family switches itself on when its first address is typed and off when
// the address is cleared; in between it is the operator's to choose.
function switchFor(before: string | undefined, after: string, on: boolean): boolean {
  if (!after.trim()) return false;
  return before?.trim() ? on : true;
}

function FamilyToggle({ label, name, on, disabled, onToggle }: {
  label: string;
  name: string;
  on: boolean;
  disabled?: boolean;
  onToggle: () => void;
}) {
  return (
    <label className={`family-toggle${disabled ? " is-disabled" : ""}`} title={disabled ? `这个测试点没有 ${label} 地址` : undefined}>
      <button type="button" role="switch" aria-checked={on} aria-label={`${name || "测试点"} 测 ${label}`} disabled={disabled}
        className={`toggle-switch${on ? " on" : ""}`} onClick={onToggle}><span /></button>
      {label}
    </label>
  );
}

function TargetsDialog({ targets, presets, onClose, onSaved }: {
  targets: LatencyTarget[];
  presets: LatencyTarget[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  // Presets keep their place even when switched off, so they can be turned
  // back on; anything else is a custom point the operator added.
  const [list, setList] = useState<LatencyTarget[]>(() => {
    const byName = new Map(targets.map((t) => [t.name, t]));
    const merged = presets.map((p) => byName.get(p.name) ?? { ...p, enabled: false, enabled_v6: false });
    const custom = targets.filter((t) => !presets.some((p) => p.name === t.name));
    return [...merged, ...custom];
  });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const presetNames = new Set(presets.map((p) => p.name));
  const enabled = list.filter((t) => t.enabled).length;
  const enabledV6 = list.filter((t) => t.enabled_v6).length;

  const patch = (i: number, change: Partial<LatencyTarget>) => setList(list.map((t, j) => (j === i ? { ...t, ...change } : t)));

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.saveLatencyTargets(list);
      await onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "保存失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="封锁检测测试点" subtitle="放在各地运营商机房里的地址，封锁检测从每台机器对它们 ping 和连 TCP；IPv4 和 IPv6 分别选择，各自最多启用 12 个" size="wide" onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="target-list">
        {list.map((t, i) => {
          const preset = presetNames.has(t.name);
          return (
            <div key={i} className={`target-row${t.enabled || t.enabled_v6 ? " on" : ""}`}>
              {preset ? (
                <>
                  <strong>{t.name}</strong>
                  <span className="mono muted target-hosts">
                    {t.host && <span>{t.host}:{t.port}</span>}
                    {t.host_v6 && <span>{t.host_v6.includes(":") ? `[${t.host_v6}]` : t.host_v6}:{t.port}</span>}
                  </span>
                </>
              ) : (
                <>
                  <input value={t.name} onChange={(e) => patch(i, { name: e.target.value })} placeholder="名称" aria-label="名称" />
                  <input className="mono" value={t.host ?? ""} placeholder="IPv4（可选）" aria-label="IPv4 地址"
                    onChange={(e) => patch(i, { host: e.target.value, enabled: switchFor(t.host, e.target.value, t.enabled) })} />
                  <input className="mono" value={t.host_v6 ?? ""} placeholder="IPv6（可选）" aria-label="IPv6 地址"
                    onChange={(e) => patch(i, { host_v6: e.target.value, enabled_v6: switchFor(t.host_v6, e.target.value, t.enabled_v6) })} />
                  <input className="mono port" type="number" value={t.port} onChange={(e) => patch(i, { port: Number(e.target.value) })} aria-label="端口" />
                  <button type="button" className="icon-btn" aria-label="删除测试点" onClick={() => setList(list.filter((_, j) => j !== i))}><Trash2 size={15} /></button>
                </>
              )}
              <span className="target-families">
                <FamilyToggle label="IPv4" name={t.name} on={t.enabled} disabled={!t.host?.trim()} onToggle={() => patch(i, { enabled: !t.enabled })} />
                <FamilyToggle label="IPv6" name={t.name} on={t.enabled_v6} disabled={!t.host_v6?.trim()}
                  onToggle={() => patch(i, { enabled_v6: !t.enabled_v6 })} />
              </span>
            </div>
          );
        })}
      </div>
      <button type="button" className="hop-add" onClick={() => setList([...list, { name: "", host: "", port: 80, enabled: false, enabled_v6: false }])}>
        <Plus size={15} />自定义测试点
      </button>
      <div className="modal-actions">
        <span className={enabled > 12 || enabledV6 > 12 ? "warn-text" : "muted"}>IPv4 {enabled} 个 · IPv6 {enabledV6} 个</span>
        <button className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy || enabled > 12 || enabledV6 > 12} onClick={() => void save()}>{busy ? "保存中…" : "保存"}</button>
      </div>
    </Modal>
  );
}
