import { useState } from "react";
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

  if (ticket) {
    return (
      <Modal title="一键注册命令" onClose={onClose}>
        <Banner kind="ok">
          在目标机器上以 root 执行下面这条命令即可完成注册，无需再填地址和密码。
        </Banner>

        <pre className="cmd">{ticket.command}</pre>

        <div className="row" style={{ marginBottom: 16 }}>
          <CopyButton text={ticket.command} />
          <span className="muted" style={{ fontSize: 13 }}>
            有效期至 {new Date(ticket.expires_at).toLocaleString("zh-CN")}，仅可使用一次
          </span>
        </div>

        <h2>脚本会做什么</h2>
        <ul className="hop-list" style={{ marginBottom: 16 }}>
          <li>识别系统、架构与 init（支持 systemd 与 OpenRC，amd64 / arm64）</li>
          <li>把面板公钥写入 authorized_keys —— 私钥始终留在面板，不经过网络</li>
          <li>从面板下载 realm 并安装（节点无需访问 GitHub）</li>
          <li>回报系统信息，面板随即回连验证并当场告诉你结果</li>
        </ul>

        <Banner kind="warn">
          若这台是 NAT 机器，上面填的连接地址必须是服务商映射后的公网地址与端口，
          不是机器自己看到的地址。填错时脚本会明确提示回连失败。
        </Banner>

        <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
          <button className="btn" onClick={onClose}>
            关闭
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
    <Modal title="生成一键注册命令" onClose={onClose}>
      <form onSubmit={generate}>
        {error && <Banner kind="err">{error}</Banner>}

        <label>
          节点名称
          <input
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="随便起，支持中文"
            required
          />
        </label>

        <div className="grid2">
          <label>
            SSH 连接地址
            <input
              value={form.host}
              onChange={(e) => set("host", e.target.value)}
              placeholder="面板用来连它的地址"
              required
            />
          </label>
          <label>
            SSH 端口
            <NumberField
              value={form.ssh_port}
              onChange={(v) => set("ssh_port", v)}
              required
            />
          </label>
        </div>
        <p className="hint">
          NAT 机器填服务商映射后的公网地址和端口（例如 1.2.3.4:10022 映射到内网 22）。
          这一项无法由脚本自动探测，因为 NAT 机器的出口地址与入口地址通常不同。
        </p>

        <label>
          登录用户
          <input value={form.ssh_user} onChange={(e) => set("ssh_user", e.target.value)} required />
        </label>

        <div className="grid2">
          <label>
            端口池起始
            <NumberField
              value={form.port_start}
              onChange={(v) => set("port_start", v)}
              required
            />
          </label>
          <label>
            端口池结束
            <NumberField
              value={form.port_end}
              onChange={(v) => set("port_end", v)}
              required
            />
          </label>
        </div>
        <p className="hint">
          分配时会从起始端口向上取第一个空闲端口，并自动跳过节点上已被其他服务占用的
          端口（sshd、网页服务等）。起始端口默认避开 1-9999，那一段既有特权端口也常被
          系统服务占用。NAT 机器请改成服务商实际映射到本机的范围，否则会分配到映射之
          外、外部根本连不进来的端口。
        </p>

        <label>
          跳板节点
          <select
            value={form.via_node_id ?? ""}
            onChange={(e) => set("via_node_id", e.target.value ? Number(e.target.value) : null)}
          >
            <option value="">直连（无需跳板）</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </select>
        </label>
        <p className="hint">仅对面板无法直连的内网节点设置。</p>

        <label style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
          <input
            type="checkbox"
            checked={form.skip_udp_probe}
            onChange={(e) => set("skip_udp_probe", e.target.checked)}
            style={{ width: "auto", marginTop: 3 }}
          />
          <span>跳过 UDP 检测</span>
        </label>
        <p className="hint">
          多数 NAT 机器只映射 TCP，不打算跑 UDP 链路的话可以勾选，每次探测省十几秒。
          勾选后该节点的 UDP 能力标记为「已跳过」，建 tcp+udp 链路时不会拦你。
        </p>

        <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "生成中…" : "生成命令"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
