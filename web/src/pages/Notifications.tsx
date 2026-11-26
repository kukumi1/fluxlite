import { useEffect, useState } from "react";
import { BellRing, Clock, Gauge, Send, VolumeX } from "lucide-react";
import { api, ApiError, type AlertSettings, type Node } from "../api";
import { Banner } from "../components/Modal";
import { FormBlock, SwitchField } from "../components/DrawerForm";
import { PageHeader } from "../components/PageHeader";
import { Flag } from "../components/Flag";
import { regionOf } from "../lib/region";
import "./line-quality.css";

type EventKey = keyof AlertSettings["events"];

const EVENTS: Array<{ key: EventKey; label: string; note: string }> = [
  { key: "node_offline", label: "机器掉线 / 恢复", note: "连续约 30 秒连不上才算掉线，重新连上再发一条恢复" },
  { key: "route_down", label: "链路失效 / 恢复", note: "转发进程没在运行，或某一跳连不上下一跳或落地，持续 30 秒" },
  { key: "quota", label: "流量快用完 / 用完", note: "节点和链路的流量用到 90%、用完各提醒一次，下个周期重新计" },
  { key: "expiry", label: "节点即将到期 / 已到期", note: "到期前 3 天内提醒一次，到期后再提醒一次" },
  { key: "latency", label: "线路质量异常 / 恢复", note: "到某个测试点连续 3 轮（约 9 分钟）超过下面的阈值" },
  { key: "route_change", label: "回程线路变化", note: "每天检测回程时和上次对比，比如 CN2 GIA 变成 163" },
  { key: "blocked", label: "IP 被封锁", note: "每 6 小时检测一次，机器到国内变成 TCP 阻断或疑似 IP 被墙时提醒一次，恢复正常不发" },
  { key: "daily", label: "每日汇总", note: "每天在设定时间发一条：机器、链路、昨日流量、需要处理的事" },
];

export function Notifications() {
  const [settings, setSettings] = useState<AlertSettings | null>(null);
  const [bound, setBound] = useState(true);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    Promise.all([api.alertSettings(), api.listNodes()])
      .then(([a, n]) => {
        setSettings(a.settings);
        setBound(a.telegram_bound);
        setNodes(n ?? []);
      })
      .catch((err) => setError(err instanceof ApiError ? err.message : "读取通知设置失败"));
  }, []);

  if (!settings) {
    return (
      <div className="management-page notifications-page">
        <PageHeader title="通知" />
        {error ? <Banner kind="err">{error}</Banner> : <p className="muted">读取中…</p>}
      </div>
    );
  }

  const update = (patch: Partial<AlertSettings>) => setSettings({ ...settings, ...patch });
  const setEvent = (key: EventKey, on: boolean) => update({ events: { ...settings.events, [key]: on } });
  const toggleMute = (id: number) =>
    update({
      muted_nodes: settings.muted_nodes.includes(id)
        ? settings.muted_nodes.filter((n) => n !== id)
        : [...settings.muted_nodes, id],
    });

  async function run(fn: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await fn();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "操作失败");
    } finally {
      setBusy(false);
    }
  }

  const save = () =>
    run(async () => {
      setSettings(await api.saveAlertSettings(settings));
      setNotice("通知设置已保存");
    });
  const test = () =>
    run(async () => {
      await api.testAlert();
      setNotice("测试消息已发出，去 Telegram 看看");
    });

  return (
    <div className="management-page notifications-page">
      <PageHeader
        title="通知"
        desc="机器、链路和线路质量出问题时推送到个人中心里绑定的 Telegram 账号，恢复时再推一条。同一个问题只提醒一次。"
        actions={
          <>
            <button className="btn" disabled={busy || !bound} onClick={() => void test()}><Send size={15} />发送测试</button>
            <button className="btn primary" disabled={busy} onClick={() => void save()}>{busy ? "保存中…" : "保存"}</button>
          </>
        }
      />
      {!bound && <Banner kind="warn">还没有启用 Telegram 机器人或绑定账号，提醒发不出去。到「个人中心 → Telegram」完成绑定。</Banner>}
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="notify-grid">
        <FormBlock title="提醒哪些事" icon={BellRing} note="关掉的只是不推送，状态照常跟踪，重新打开不会补发之前的事">
          {EVENTS.map((e) => (
            <SwitchField key={e.key} label={e.label} note={e.note} checked={settings.events[e.key]} onChange={(on) => setEvent(e.key, on)} />
          ))}
        </FormBlock>

        <div className="notify-side">
          <FormBlock title="线路质量阈值" icon={Gauge} note="每轮每个测试点连 4 次，所以丢包以 25% 为一档">
            <label>
              丢包超过（%）
              <input type="number" min={1} max={100} value={settings.loss_percent} onChange={(e) => update({ loss_percent: Number(e.target.value) })} />
            </label>
            <label>
              平均延迟超过（ms）
              <input type="number" min={0} max={10000} value={settings.latency_ms} onChange={(e) => update({ latency_ms: Number(e.target.value) })} />
            </label>
            <p className="hint">延迟填 0 表示不按延迟提醒，只看丢包。</p>
          </FormBlock>

          <FormBlock title="每日汇总" icon={Clock}>
            <label>
              发送时间（北京时间）
              <select value={settings.daily_hour} onChange={(e) => update({ daily_hour: Number(e.target.value) })} disabled={!settings.events.daily}>
                {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
              </select>
            </label>
          </FormBlock>

          <FormBlock title="静音的机器" icon={VolumeX} note="不推送这些机器的掉线、线路质量和回程变化，经过它们的链路照常提醒">
            {nodes.length === 0 ? (
              <p className="muted">还没有机器。</p>
            ) : (
              <div className="mute-chips">
                {nodes.map((n) => {
                  const muted = settings.muted_nodes.includes(n.id);
                  const code = regionOf(n.name);
                  return (
                    <button key={n.id} type="button" aria-pressed={muted} className={`mute-chip${muted ? " muted" : ""}`} onClick={() => toggleMute(n.id)}>
                      {code && <Flag code={code} />}
                      {n.name}
                      {muted && <VolumeX size={13} aria-label="已静音" />}
                    </button>
                  );
                })}
              </div>
            )}
          </FormBlock>
        </div>
      </div>
    </div>
  );
}
