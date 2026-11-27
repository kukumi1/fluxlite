import { useCallback, useEffect, useState } from "react";
import { Pencil, Plus, Radio, Trash2 } from "lucide-react";
import { api, ApiError, type ProbeOverview, type ProbeSite, type ProbeTask } from "../api";
import { Banner } from "../components/Modal";
import { useConfirm } from "../components/ConfirmDialog";
import { PageHeader } from "../components/PageHeader";
import { AgentEnrollDialog, MachineTasks, TaskDialog, ThemeUpload } from "./ProbeAdmin";
import "./line-quality.css";
import "./probe.css";

const TASK_TYPES: Record<string, string> = { icmp: "ICMP", tcp: "TCP", http: "HTTP" };

const REFRESH_MS = 5000;

// Probe holds what belongs to the probe as a whole: the latency checks the
// agents run, the public page, and enrolling agents. The machines themselves
// are on the machines page, each with its SSH and its agent together.
export function Probe() {
  const [overview, setOverview] = useState<ProbeOverview | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [task, setTask] = useState<ProbeTask | "new" | null>(null);
  const [enrolling, setEnrolling] = useState(false);
  const [taskView, setTaskView] = useState<"task" | "machine">("task");
  const [confirm, confirmDialog] = useConfirm();

  const load = useCallback(async () => {
    try {
      setOverview(await api.probeOverview());
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取探针失败");
    }
  }, []);

  // Machines come and go on the machines page and agents report all the
  // time, so the counts here follow along.
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [load]);

  async function deleteTask(t: ProbeTask) {
    const ok = await confirm({ title: `删除延迟任务「${t.name}」？`, body: "它的历史数据会一起删掉。", confirmLabel: "删除", danger: true });
    if (!ok) return;
    setNotice("");
    try {
      await api.deleteProbeTask(t.id);
      setNotice(`已删除延迟任务「${t.name}」`);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "删除失败");
    }
  }

  const machines = overview?.machines ?? [];
  const tasks = overview?.tasks ?? [];
  const online = machines.filter((m) => m.online).length;
  const names = new Map(machines.map((m) => [m.uuid, m.name]));

  return (
    <div className="page">
      <PageHeader
        title="探针"
        desc={
          <>
            机器上的 Komari agent 每几秒上报一次 CPU、内存、网速。机器统一在「机器」页管理，agent 和 SSH 是同一台机器的话会自动关联。
            {machines.length > 0 && ` agent 在线 ${online} / ${machines.length} 台。`}
          </>
        }
        actions={
          overview?.enabled && (
            <button type="button" className="btn primary" onClick={() => setEnrolling(true)}>
              <Radio size={15} />
              接入 agent
            </button>
          )
        }
      />
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      {overview?.enabled && (
        <div className="card">
          <div className="probe-card-head">
            <h2>延迟任务</h2>
            <span className="probe-card-tools">
              <div className="segmented labels" role="radiogroup" aria-label="查看方式">
                {([["task", "按任务"], ["machine", "按机器"]] as const).map(([v, label]) => (
                  <button key={v} type="button" role="radio" aria-checked={taskView === v} className={taskView === v ? "active" : ""} onClick={() => setTaskView(v)}>{label}</button>
                ))}
              </div>
              <button type="button" className="btn sm" onClick={() => setTask("new")}><Plus size={14} />新增</button>
            </span>
          </div>
          <p className="muted probe-note">
            面板按间隔把检测发给选中机器的 agent 执行，结果画在<strong>执行它的那台机器</strong>的延迟图里：一个测 FAC-TW 的任务选了 5 台机器，就出现在这 5 台的图里，不在 FAC-TW 自己的图里。
          </p>
          {tasks.length === 0 ? (
            <p className="muted">还没有延迟任务。先「新增」一个目标（IP 或域名加端口），再选哪些机器去测。</p>
          ) : taskView === "machine" ? (
            <MachineTasks machines={machines} tasks={tasks}
              onSaved={(name) => { setNotice(`${name} 的延迟任务已保存，下一轮开始按新设置检测`); void load(); }}
              onError={setError} />
          ) : (
            <table>
              <thead>
                <tr>
                  <th>名称</th>
                  <th>方式</th>
                  <th>目标</th>
                  <th className="num">间隔</th>
                  <th>机器</th>
                  <th aria-label="操作" />
                </tr>
              </thead>
              <tbody>
                {tasks.map((t) => (
                  <tr key={t.id}>
                    <td><strong>{t.name}</strong></td>
                    <td>{TASK_TYPES[t.type] ?? t.type}</td>
                    <td className="mono">{t.target}</td>
                    <td className="num">{t.interval} 秒</td>
                    <td className="muted" title={t.clients.flatMap((c) => names.get(c) ?? []).join("、")}>{t.clients.filter((c) => names.has(c)).length} 台</td>
                    <td className="probe-actions">
                      <button type="button" className="icon-btn" title="编辑" aria-label={`编辑 ${t.name}`} onClick={() => setTask(t)}><Pencil size={15} /></button>
                      <button type="button" className="icon-btn" title="删除" aria-label={`删除 ${t.name}`} onClick={() => void deleteTask(t)}><Trash2 size={15} /></button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {overview?.enabled && <ProbeSiteCard />}

      {task && (
        <TaskDialog task={task === "new" ? null : task} machines={machines} onClose={() => setTask(null)}
          onSaved={() => { setTask(null); setNotice("延迟任务已保存，下一轮开始按新设置检测"); void load(); }} />
      )}
      {enrolling && <AgentEnrollDialog onClose={() => setEnrolling(false)} />}
      {confirmDialog}
    </div>
  );
}

// ProbeSiteCard sets the public status page: the domain it answers on,
// what it is called, and a way into the theme's own settings.
function ProbeSiteCard() {
  const [site, setSite] = useState<ProbeSite | null>(null);
  const [form, setForm] = useState<Omit<ProbeSite, "theme"> | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    api.probeSite().then(
      (s) => {
        setSite(s);
        setForm({ public_host: s.public_host, sitename: s.sitename, description: s.description, custom_head: s.custom_head, custom_body: s.custom_body });
      },
      (err) => setError(err instanceof ApiError ? err.message : "读取公开页设置失败"),
    );
  }, []);

  async function save() {
    if (!form) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      setSite(await api.saveProbeSite(form));
      setNotice("已保存");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  async function editTheme() {
    // The tab opens before the link arrives, so a popup blocker lets it through.
    const tab = window.open("", "_blank");
    try {
      const { url } = await api.probeThemeLogin();
      if (tab) tab.location.href = url;
      else window.location.href = url;
    } catch (err) {
      tab?.close();
      setError(err instanceof ApiError ? err.message : "打开主题设置失败");
    }
  }

  if (!form) return error ? <Banner kind="err">{error}</Banner> : null;
  const set = (key: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [key]: e.target.value });
  return (
    <div className="card">
      <h2>公开页</h2>
      <p className="muted probe-note">
        访客看到的状态页，用 {site?.theme ?? "LuminaPlus"} 主题，只在下面这个域名上打开；面板自己的功能在那个域名上一律访问不到。
      </p>
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}
      <form className="dialog-form setting-form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <div className="grid2">
          <label>公开页域名<input value={form.public_host} onChange={set("public_host")} placeholder="status.example.com" /></label>
          <label>站点名称<input value={form.sitename} onChange={set("sitename")} /></label>
        </div>
        <label>描述<input value={form.description} onChange={set("description")} /></label>
        <label>自定义 head<textarea className="mono probe-code" rows={3} value={form.custom_head} onChange={set("custom_head")} /></label>
        <label>自定义 body<textarea className="mono probe-code" rows={3} value={form.custom_body} onChange={set("custom_body")} /></label>
        <p className="field-hint flush">自定义代码会原样插进公开页，只放你自己信得过的内容。</p>
        <div className="setting-actions">
          <button className="btn primary" disabled={busy}>{busy ? "保存中…" : "保存"}</button>
          <button type="button" className="btn" disabled={busy || !site?.public_host} onClick={() => void editTheme()}>
            编辑主题设置
          </button>
          {site?.public_host && <a className="btn ghost" href={`https://${site.public_host}/`} target="_blank" rel="noopener noreferrer">打开公开页</a>}
        </div>
      </form>
      {site && <ThemeUpload theme={site.theme} />}
    </div>
  );
}
