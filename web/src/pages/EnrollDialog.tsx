import { useState } from "react";
import { ArrowLeft, ArrowRight, Cpu, Download, Info, KeyRound, RadioTower, Settings2, Timer } from "lucide-react";
import {
  api,
  ApiError,
  DEFAULT_PORT_END,
  DEFAULT_PORT_START,
  type EnrollRequest,
  type EnrollTicket,
  type Node,
} from "../api";
import { CopyButton } from "../components/CopyButton";
import { NumberField } from "../components/NumberField";
import { Banner, Modal } from "../components/Modal";

interface Props {
  nodes: Node[];
  onClose: () => void;
  onEnrolled: () => void;
}

const SCRIPT_STEPS = [
  { icon: Cpu, tone: "sky", title: "识别环境", text: "系统、架构与 init，支持 systemd / OpenRC、amd64 / arm64" },
  { icon: KeyRound, tone: "lilac", title: "写入公钥", text: "面板公钥进 authorized_keys，私钥始终留在面板" },
  { icon: Download, tone: "peach", title: "安装 realm", text: "从面板下载，机器不需要能访问 GitHub" },
  { icon: RadioTower, tone: "lime", title: "回连验证", text: "回报系统信息，面板当场回连并告诉你结果" },
] as const;

function expiryText(expiresAt: string): string {
  const at = new Date(expiresAt);
  const minutes = Math.max(0, Math.round((at.getTime() - Date.now()) / 60000));
  return `${at.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })} 前有效（约 ${minutes} 分钟）· 仅可使用一次`;
}

export function EnrollDialog({ nodes, onClose, onEnrolled }: Props) {
  const [form, setForm] = useState<EnrollRequest>({
    name: "",
    host: "",
    ssh_port: 22,
    ssh_user: "root",
    port_start: DEFAULT_PORT_START,
    port_end: DEFAULT_PORT_END,
    via_node_id: null,
    skip_udp_probe: false,
  });
  const [ticket, setTicket] = useState<EnrollTicket | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const set = <K extends keyof EnrollRequest>(key: K, value: EnrollRequest[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  async function generate(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      setTicket(await api.enrollTicket(form));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "生成失败");
    } finally {
      setBusy(false);
    }
  }

  const viaName = form.via_node_id ? nodes.find((n) => n.id === form.via_node_id)?.name ?? "跳板" : "直连";
  const steps = (
    <ol className="dialog-steps" aria-label="步骤">
      <li className={ticket ? "done" : "current"}><b>1</b>填写连接</li>
      <li className={ticket ? "current" : ""}><b>2</b>在机器上执行</li>
    </ol>
  );

  if (ticket) {
    return (
      <Modal title="一键注册机器" subtitle={`把命令复制到「${form.name}」上，以 root 执行。`} size="wide" onClose={onClose}>
        {steps}

        <div className="term-block">
          <div className="terminal-bar">
            <span className="terminal-dots" aria-hidden><i /><i /><i /></span>
            <span className="terminal-title">root@{form.name || "目标机器"}</span>
            <CopyButton text={ticket.command} />
          </div>
          <pre className="terminal-body"><span className="terminal-prompt" aria-hidden>$ </span>{ticket.command}</pre>
        </div>
        <p className="enroll-expiry"><Timer size={14} />{expiryText(ticket.expires_at)}</p>

        <h3 className="dialog-section-title">脚本会做什么</h3>
        <ol className="script-steps">
          {SCRIPT_STEPS.map(({ icon: Icon, tone, title, text }) => (
            <li key={title} className={`tone-${tone}`}>
              <i><Icon size={16} /></i>
              <strong>{title}</strong>
              <span>{text}</span>
            </li>
          ))}
        </ol>

        <p className="dialog-note">
          <Info size={14} />
          NAT 机器的连接地址必须是服务商映射后的公网地址与端口；填错时脚本会明确提示回连失败。
        </p>

        <div className="modal-actions spread-actions">
          <button className="btn ghost" onClick={() => setTicket(null)}>
            <ArrowLeft size={15} />
            返回修改
          </button>
          <button
            className="btn primary"
            onClick={() => {
              onEnrolled();
              onClose();
            }}
          >
            已执行，刷新列表
          </button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="一键注册机器" subtitle="告诉面板怎么连上它，再拿一条命令去机器上跑。" size="wide" onClose={onClose}>
      {steps}
      <form onSubmit={generate} className="dialog-form">
        {error && <Banner kind="err">{error}</Banner>}

        <label>
          机器名称
          <input value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="例如 DMIT-JP，写上地区会自动配国旗" required autoFocus />
        </label>

        <div className="field">
          <span className="field-label">SSH 连接地址</span>
          <div className="host-port">
            <input aria-label="SSH 连接地址" value={form.host} onChange={(e) => set("host", e.target.value)} placeholder="面板用来连它的 IP 或域名" required />
            <span aria-hidden>:</span>
            <NumberField aria-label="SSH 端口" value={form.ssh_port} onChange={(v) => set("ssh_port", v)} required />
          </div>
          <p className="field-hint" title="NAT 机器的出口地址与入口地址通常不同，脚本无法自动探测，只能由你填写。">
            NAT 机器填服务商映射后的公网地址和端口，例如 1.2.3.4 : 10022。
          </p>
        </div>

        <label>
          登录用户
          <input value={form.ssh_user} onChange={(e) => set("ssh_user", e.target.value)} required />
        </label>

        <details className="advanced">
          <summary>
            <Settings2 size={15} />
            高级设置
            <span className="advanced-summary">
              端口池 {form.port_start}–{form.port_end} · {viaName} · {form.skip_udp_probe ? "跳过 UDP" : "检测 UDP"}
            </span>
          </summary>

          <div className="field">
            <span className="field-label">端口池</span>
            <div className="range-row">
              <NumberField aria-label="端口池起始" value={form.port_start} onChange={(v) => set("port_start", v)} required />
              <span aria-hidden>–</span>
              <NumberField aria-label="端口池结束" value={form.port_end} onChange={(v) => set("port_end", v)} required />
            </div>
            <p
              className="field-hint"
              title="分配时从起始端口向上取第一个空闲端口，并跳过已被 sshd、网页服务等占用的端口。默认避开 1-9999，那一段既有特权端口也常被系统服务占用。"
            >
              链路端口从这里分配。NAT 机器改成服务商实际映射到本机的范围。
            </p>
          </div>

          <label>
            跳板机器
            <select value={form.via_node_id ?? ""} onChange={(e) => set("via_node_id", e.target.value ? Number(e.target.value) : null)}>
              <option value="">直连（无需跳板）</option>
              {nodes.map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </label>
          <p className="field-hint">仅面板无法直连的内网机器需要。</p>

          <div className="switch-row">
            <div>
              <strong>跳过 UDP 检测</strong>
              <span>多数 NAT 机器只映射 TCP；勾选后 UDP 标为「已跳过」，每次探测省十几秒。</span>
            </div>
            <button
              type="button"
              className={`toggle-switch ${form.skip_udp_probe ? "on" : ""}`}
              role="switch"
              aria-checked={form.skip_udp_probe}
              aria-label="跳过 UDP 检测"
              onClick={() => set("skip_udp_probe", !form.skip_udp_probe)}
            >
              <span />
            </button>
          </div>
        </details>

        <div className="modal-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "生成中…" : "生成命令"}
            {!busy && <ArrowRight size={15} />}
          </button>
        </div>
      </form>
    </Modal>
  );
}
