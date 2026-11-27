import { useEffect, useState } from "react";
import { Copy, KeyRound } from "lucide-react";
import { api, ApiError, type ProbeEnroll, type ProbeMachine, type ProbeTask } from "../api";
import { Banner, Modal } from "../components/Modal";

const GB = 1024 ** 3;

// Komari keeps a machine's region as a flag emoji, which Windows shows as
// two letters; the letters are read back out so the panel draws its own flag.
export function flagCode(emoji: string | undefined): string | null {
  const points = [...(emoji ?? "")].map((c) => c.codePointAt(0) ?? 0);
  if (points.length !== 2 || points.some((p) => p < 0x1f1e6 || p > 0x1f1ff)) return null;
  return String.fromCharCode(...points.map((p) => p - 0x1f1e6 + 65));
}

function flagEmoji(code: string): string {
  const letters = code.trim().toUpperCase();
  if (!/^[A-Z]{2}$/.test(letters)) return "";
  return String.fromCodePoint(...[...letters].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

const message = (err: unknown, fallback: string) => (err instanceof ApiError ? err.message : fallback);

const TRAFFIC_TYPES: ReadonlyArray<readonly [string, string]> = [
  ["max", "上下行取大"], ["sum", "上下行相加"], ["up", "只算上行"], ["down", "只算下行"], ["min", "上下行取小"],
];

/** A panel machine a probe machine can be paired with. */
export interface PairableNode {
  id: number;
  name: string;
}

export function MachineDialog({ machine, nodes, onClose, onSaved }: { machine: ProbeMachine; nodes: PairableNode[]; onClose: () => void; onSaved: () => void }) {
  const d = machine.display;
  const [form, setForm] = useState({
    name: machine.name,
    hidden: machine.hidden,
    notify: machine.notify,
    node: machine.node_id === null ? "" : String(machine.node_id),
    region: flagCode(d.region) ?? "",
    group: d.group ?? "",
    tags: d.tags ?? "",
    public_remark: d.public_remark ?? "",
    price: d.price ? String(d.price) : "",
    currency: d.currency || "$",
    billing_cycle: d.billing_cycle ? String(d.billing_cycle) : "",
    auto_renewal: d.auto_renewal ?? false,
    expired_at: d.expired_at ? new Date(d.expired_at).toISOString().slice(0, 10) : "",
    traffic_limit: d.traffic_limit ? String(Math.round((d.traffic_limit / GB) * 100) / 100) : "",
    traffic_limit_type: d.traffic_limit_type || "max",
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = (key: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [key]: e.target.value });

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.updateProbeMachine(machine.uuid, {
        name: form.name,
        hidden: form.hidden,
        node_id: form.node === "" ? null : Number(form.node),
        display: {
          ...d,
          region: flagEmoji(form.region),
          group: form.group.trim(),
          tags: form.tags.trim(),
          public_remark: form.public_remark,
          price: Number(form.price) || 0,
          currency: form.currency.trim() || "$",
          billing_cycle: Number(form.billing_cycle) || 0,
          auto_renewal: form.auto_renewal,
          expired_at: form.expired_at ? new Date(form.expired_at + "T00:00:00Z").toISOString() : null,
          traffic_limit: Math.round((Number(form.traffic_limit) || 0) * GB),
          traffic_limit_type: form.traffic_limit_type,
        },
      });
      if (form.notify !== machine.notify) await api.setProbeNotify(machine.uuid, form.notify);
      onSaved();
    } catch (err) {
      setError(message(err, "保存失败"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title={`探针与公开页 · ${machine.name}`} subtitle="价格、到期这些只显示在公开页上；agent 上报的硬件信息不在这里改。" onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}
      <form className="dialog-form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <div className="grid2">
          <label>名称<input value={form.name} onChange={set("name")} maxLength={100} required /></label>
          <label>地区<input value={form.region} onChange={set("region")} placeholder="两位国家代码，如 JP、SG" maxLength={2} /></label>
          <label>分组<input value={form.group} onChange={set("group")} /></label>
          <label>标签<input value={form.tags} onChange={set("tags")} placeholder="多个用 ; 分开" /></label>
          <label>价格<input inputMode="decimal" value={form.price} onChange={set("price")} placeholder="0 表示免费" /></label>
          <label>币种<input value={form.currency} onChange={set("currency")} maxLength={8} /></label>
          <label>计费周期（天）<input inputMode="numeric" value={form.billing_cycle} onChange={set("billing_cycle")} placeholder="30、365…" /></label>
          <label>到期日<input type="date" value={form.expired_at} onChange={set("expired_at")} /></label>
          <label>流量上限（GB）<input inputMode="decimal" value={form.traffic_limit} onChange={set("traffic_limit")} placeholder="0 表示不限" /></label>
          <label>流量计算
            <select value={form.traffic_limit_type} onChange={set("traffic_limit_type")}>
              {TRAFFIC_TYPES.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
            </select>
          </label>
        </div>
        <label>对应的面板机器
          <select value={form.node} onChange={set("node")}>
            <option value="">无（只有 agent，没有 SSH）</option>
            {nodes.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </select>
        </label>
        <p className="field-hint flush">同一台机器的 agent 和 SSH 关联后，机器页显示 agent 的实时数据，转发、终端、测速照常走 SSH；掉线只由机器页的告警通知一次。</p>
        <label>公开备注<input value={form.public_remark} onChange={set("public_remark")} /></label>
        <div className="switch-row">
          <div><strong>自动续费</strong></div>
          <button type="button" role="switch" aria-checked={form.auto_renewal} aria-label="自动续费" className={`toggle-switch${form.auto_renewal ? " on" : ""}`}
            onClick={() => setForm({ ...form, auto_renewal: !form.auto_renewal })}><span /></button>
        </div>
        <div className="switch-row">
          <div><strong>在公开页隐藏</strong><span>访客看不到这台机器；面板里照常显示</span></div>
          <button type="button" role="switch" aria-checked={form.hidden} aria-label="在公开页隐藏" className={`toggle-switch${form.hidden ? " on" : ""}`}
            onClick={() => setForm({ ...form, hidden: !form.hidden })}><span /></button>
        </div>
        {form.node === "" && (
          <div className="switch-row">
            <div><strong>掉线通知</strong><span>超过 3 分钟没上报、恢复时各发一条 Telegram；总开关在「通知」页</span></div>
            <button type="button" role="switch" aria-checked={form.notify} aria-label="掉线通知" className={`toggle-switch${form.notify ? " on" : ""}`}
              onClick={() => setForm({ ...form, notify: !form.notify })}><span /></button>
          </div>
        )}
        <div className="setting-actions">
          <button className="btn primary" disabled={busy}>{busy ? "保存中…" : "保存"}</button>
          <button type="button" className="btn" onClick={onClose}>取消</button>
        </div>
      </form>
    </Modal>
  );
}

const TASK_TYPES: ReadonlyArray<readonly [ProbeTask["type"], string, string]> = [
  ["tcp", "TCP", "IP 或域名加端口，如 192.0.2.1:443；不写端口按 80"],
  ["icmp", "ICMP", "IP 或域名"],
  ["http", "HTTP", "网址，如 https://example.com"],
];

export function TaskDialog({ task, machines, onClose, onSaved }: { task: ProbeTask | null; machines: ProbeMachine[]; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState({
    name: task?.name ?? "",
    type: task?.type ?? ("tcp" as ProbeTask["type"]),
    target: task?.target ?? "",
    interval: String(task?.interval ?? 60),
    weight: String(task?.weight ?? 0),
  });
  const [chosen, setChosen] = useState<Set<string>>(() => {
    const exists = new Set(machines.map((m) => m.uuid));
    return new Set(task ? task.clients.filter((c) => exists.has(c)) : exists);
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const hint = TASK_TYPES.find(([t]) => t === form.type)?.[2];

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.saveProbeTask({
        id: task?.id, name: form.name, type: form.type, target: form.target,
        interval: Number(form.interval) || 0, weight: Number(form.weight) || 0, clients: [...chosen],
      });
      onSaved();
    } catch (err) {
      setError(message(err, "保存失败"));
    } finally {
      setBusy(false);
    }
  }

  const toggle = (uuid: string) => setChosen((cur) => {
    const next = new Set(cur);
    if (next.has(uuid)) next.delete(uuid);
    else next.add(uuid);
    return next;
  });

  return (
    <Modal title={task ? `编辑延迟任务 · ${task.name}` : "新增延迟任务"} subtitle="面板按间隔把检测发给选中的机器，结果画在公开页的延迟图里。" size="wide" onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}
      <form className="dialog-form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <div className="grid2">
          <label>名称<input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} maxLength={100} required /></label>
          <label>方式
            <select value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value as ProbeTask["type"] })}>
              {TASK_TYPES.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
            </select>
          </label>
          <label>目标<input className="mono" value={form.target} onChange={(e) => setForm({ ...form, target: e.target.value })} required /></label>
          <label>间隔（秒）<input inputMode="numeric" value={form.interval} onChange={(e) => setForm({ ...form, interval: e.target.value })} /></label>
          <label>排序<input inputMode="numeric" value={form.weight} onChange={(e) => setForm({ ...form, weight: e.target.value })} /></label>
        </div>
        {hint && <p className="field-hint flush">{hint}</p>}
        <div className="probe-task-machines">
          <div className="probe-task-machines-head">
            <strong>机器（已选 {chosen.size} / {machines.length}）</strong>
            <span>
              <button type="button" className="btn sm ghost" onClick={() => setChosen(new Set(machines.map((m) => m.uuid)))}>全选</button>
              <button type="button" className="btn sm ghost" onClick={() => setChosen(new Set())}>清空</button>
            </span>
          </div>
          <div className="mute-chips">
            {machines.map((m) => (
              <button key={m.uuid} type="button" aria-pressed={chosen.has(m.uuid)} className={`mute-chip${chosen.has(m.uuid) ? " muted" : ""}`} onClick={() => toggle(m.uuid)}>
                {m.name}
              </button>
            ))}
          </div>
        </div>
        <div className="setting-actions">
          <button className="btn primary" disabled={busy}>{busy ? "保存中…" : "保存"}</button>
          <button type="button" className="btn" onClick={onClose}>取消</button>
        </div>
      </form>
    </Modal>
  );
}

// AgentEnrollDialog gives the command that installs Komari's agent on a
// new machine and registers it here.
export function AgentEnrollDialog({ onClose }: { onClose: () => void }) {
  const [enroll, setEnroll] = useState<ProbeEnroll | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    api.probeEnroll().then(setEnroll, (err) => setError(message(err, "读取接入设置失败")));
  }, []);

  async function newKey() {
    setError("");
    try {
      setEnroll(await api.newProbeEnrollKey());
      setNotice("已生成新的注册密钥；已经接入的机器不受影响");
    } catch (err) {
      setError(message(err, "生成注册密钥失败"));
    }
  }

  return (
    <Modal title="接入 agent" subtitle="在机器上用 root 运行下面的命令，它会装上 Komari 官方 agent 并自动注册到面板。已经在机器页的机器会按 IP 自动关联。" size="wide" onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}
      {enroll && !enroll.host && <Banner kind="warn">先在「探针」页的「公开页」里填好域名，agent 要连这个域名上报。</Banner>}
      {enroll?.command ? (
        <div className="probe-command">
          <code className="mono">{enroll.command}</code>
          <button type="button" className="btn sm" onClick={() => void navigator.clipboard.writeText(enroll.command).then(() => setNotice("已复制"), () => setError("复制失败，请手动选中复制"))}>
            <Copy size={14} />复制
          </button>
        </div>
      ) : (
        enroll && enroll.host && <p className="muted">还没有注册密钥。</p>
      )}
      <div className="setting-actions">
        <button type="button" className="btn" onClick={() => void newKey()}><KeyRound size={14} />{enroll?.key ? "换一个注册密钥" : "生成注册密钥"}</button>
      </div>
      <p className="field-hint flush">注册密钥只用于新机器第一次注册；泄露了就换一个，已接入的机器各自有独立的 token。</p>
    </Modal>
  );
}

// ThemeUpload installs a new version of the public page's theme.
export function ThemeUpload({ theme }: { theme: string }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function upload(file: File | undefined) {
    if (!file) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const { version } = await api.uploadProbeTheme(file);
      setNotice(`${theme} 已更新${version ? `到 ${version}` : ""}，刷新公开页即可看到`);
    } catch (err) {
      setError(message(err, "上传失败"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="probe-theme-upload">
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}
      <label className="btn">
        {busy ? "上传中…" : `更新 ${theme}`}
        <input type="file" accept=".zip,application/zip" hidden disabled={busy} onChange={(e) => { void upload(e.target.files?.[0]); e.target.value = ""; }} />
      </label>
      <span className="field-hint flush">上传主题发布页的 zip 包（里面有 komari-theme.json 和 dist/），会整个替换现在的版本。</span>
    </div>
  );
}
