import { useEffect, useState } from "react";
import { api, ApiError, type Node, type NodeInput, type ProbeResult } from "../api";
import { Banner, Modal } from "../components/Modal";
import { EnrollDialog } from "./EnrollDialog";

const emptyInput: NodeInput = {
  name: "",
  host: "",
  ssh_port: 22,
  ssh_user: "root",
  auth_type: "password",
  secret: "",
  via_node_id: null,
  port_start: 1,
  port_end: 65535,
  skip_udp_probe: false,
};

export function Nodes() {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState<Node | null>(null);
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [probe, setProbe] = useState<{ node: string; result: ProbeResult } | null>(null);
  const [enrolling, setEnrolling] = useState(false);

  const fail = (err: unknown) =>
    setError(err instanceof ApiError ? err.message : "请求失败");

  async function load() {
    try {
      setNodes((await api.listNodes()) ?? []);
    } catch (err) {
      fail(err);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function runProbe(node: Node) {
    setBusyId(node.id);
    setError("");
    setNotice("");
    try {
      const result = await api.probeNode(node.id);
      setProbe({ node: node.name, result });
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusyId(null);
    }
  }

  async function remove(node: Node) {
    if (!confirm(`确认删除节点 ${node.name}？`)) return;
    setBusyId(node.id);
    setError("");
    try {
      await api.deleteNode(node.id);
      setNotice(`节点 ${node.name} 已删除`);
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
          <h1>节点</h1>
          <p className="page-desc">
            转发链路的组成机器。内网节点需指定跳板，NAT 机器需按实际映射填写端口池。
          </p>
        </div>
        <div className="row">
          <button className="btn" onClick={() => setCreating(true)}>
            手动添加
          </button>
          <button className="btn primary" onClick={() => setEnrolling(true)}>
            一键注册
          </button>
        </div>
      </div>

      {error && <Banner kind="err">{error}</Banner>}
      {notice && <Banner kind="ok">{notice}</Banner>}

      <div className="card" style={{ padding: 0 }}>
        {nodes.length === 0 ? (
          <div className="empty">还没有节点。先添加一台，再做能力探测。</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>地址</th>
                <th>状态</th>
                <th>系统</th>
                <th>端口池</th>
                <th>UDP</th>
                <th>跳板</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => (
                <tr key={n.id}>
                  <td>
                    <strong>{n.name}</strong>
                  </td>
                  <td className="mono nowrap">
                    {n.ssh_user}@{n.host}:{n.ssh_port}
                  </td>
                  <td>
                    <span
                      className={`tag ${
                        n.status === "online" ? "ok" : n.status === "offline" ? "err" : ""
                      }`}
                    >
                      {n.status === "online" ? "在线" : n.status === "offline" ? "离线" : "未知"}
                    </span>
                  </td>
                  <td className="nowrap">
                    {n.arch ? (
                      <span className="muted">
                        {n.os_id}/{n.arch}
                        <br />
                        {n.init_system} · realm {n.realm_version || "未装"}
                      </span>
                    ) : (
                      <span className="tag warn">未探测</span>
                    )}
                  </td>
                  <td className="mono nowrap">
                    {n.port_start}-{n.port_end}
                  </td>
                  <td>
                    {n.skip_udp_probe ? (
                      <span className="tag">已跳过</span>
                    ) : n.udp_capable === null ? (
                      <span className="tag" title="尚未测出结果，不等于不通">
                        未知
                      </span>
                    ) : n.udp_capable ? (
                      <span className="tag ok">通</span>
                    ) : (
                      <span className="tag err">不通</span>
                    )}
                  </td>
                  <td className="muted">
                    {n.via_node_id
                      ? nodes.find((x) => x.id === n.via_node_id)?.name ?? n.via_node_id
                      : "直连"}
                  </td>
                  <td>
                    <div className="row nowrap">
                      <button
                        className="btn sm"
                        disabled={busyId === n.id}
                        onClick={() => void runProbe(n)}
                      >
                        {busyId === n.id ? "探测中…" : "探测"}
                      </button>
                      <button className="btn sm" onClick={() => setEditing(n)}>
                        编辑
                      </button>
                      <button
                        className="btn sm danger"
                        disabled={busyId === n.id}
                        onClick={() => void remove(n)}
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {enrolling && (
        <EnrollDialog
          nodes={nodes}
          onClose={() => setEnrolling(false)}
          onEnrolled={() => void load()}
        />
      )}

      {(creating || editing) && (
        <NodeForm
          nodes={nodes}
          node={editing}
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

      {probe && (
        <Modal title={`探测结果 · ${probe.node}`} onClose={() => setProbe(null)}>
          <table>
            <tbody>
              <tr>
                <td className="muted">主机名</td>
                <td className="mono">{probe.result.facts.Hostname}</td>
              </tr>
              <tr>
                <td className="muted">系统 / 架构</td>
                <td className="mono">
                  {probe.result.facts.OSID} / {probe.result.facts.Arch}
                </td>
              </tr>
              <tr>
                <td className="muted">init</td>
                <td className="mono">{probe.result.facts.InitSystem}</td>
              </tr>
              <tr>
                <td className="muted">realm</td>
                <td className="mono">{probe.result.facts.RealmVersion || "未安装"}</td>
              </tr>
            </tbody>
          </table>

          <h2 style={{ marginTop: 18 }}>UDP 穿透</h2>
          {probe.result.udp?.Supported === true && (
            <Banner kind="ok">UDP 可穿透（探测方式：{probe.result.udp.Method}）</Banner>
          )}
          {probe.result.udp?.Supported === false && (
            <Banner kind="err">
              UDP 被丢弃，该节点不能承载 tcp+udp 链路。{probe.result.udp.Detail}
            </Banner>
          )}
          {(!probe.result.udp || probe.result.udp.Supported === null) && (
            <Banner kind="warn">
              未能判定 UDP 能力，已保持原值而非武断标记为不通。
              {probe.result.udp ? `原因：${probe.result.udp.Detail}` : ""}
            </Banner>
          )}
        </Modal>
      )}
    </div>
  );
}

interface NodeFormProps {
  nodes: Node[];
  node: Node | null;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onError: (err: unknown) => void;
}

function NodeForm({ nodes, node, onClose, onSaved, onError }: NodeFormProps) {
  const [form, setForm] = useState<NodeInput>(
    node
      ? {
          name: node.name,
          host: node.host,
          ssh_port: node.ssh_port,
          ssh_user: node.ssh_user,
          auth_type: node.auth_type,
          secret: "",
          via_node_id: node.via_node_id,
          port_start: node.port_start,
          port_end: node.port_end,
          skip_udp_probe: node.skip_udp_probe,
        }
      : emptyInput,
  );
  const [busy, setBusy] = useState(false);

  const set = <K extends keyof NodeInput>(key: K, value: NodeInput[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      if (node) {
        await api.updateNode(node.id, form);
        await onSaved(`节点 ${form.name} 已更新`);
      } else {
        await api.createNode(form);
        await onSaved(`节点 ${form.name} 已添加，请执行探测`);
      }
    } catch (err) {
      onError(err);
      setBusy(false);
    }
  }

  return (
    <Modal title={node ? `编辑节点 · ${node.name}` : "添加节点"} onClose={onClose}>
      <form onSubmit={submit}>
        <label>
          名称
          <input
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="随便起，支持中文"
            required
          />
        </label>

        <div className="grid2">
          <label>
            主机地址
            <input value={form.host} onChange={(e) => set("host", e.target.value)} required />
          </label>
          <label>
            SSH 端口
            <input
              type="number"
              value={form.ssh_port}
              onChange={(e) => set("ssh_port", Number(e.target.value))}
              required
            />
          </label>
          <label>
            登录用户
            <input
              value={form.ssh_user}
              onChange={(e) => set("ssh_user", e.target.value)}
              required
            />
          </label>
          <label>
            认证方式
            <select
              value={form.auth_type}
              onChange={(e) => set("auth_type", e.target.value as NodeInput["auth_type"])}
            >
              <option value="password">密码</option>
              <option value="key">私钥</option>
            </select>
          </label>
        </div>

        <label>
          {form.auth_type === "key" ? "私钥内容（PEM）" : "密码"}
          <input
            type={form.auth_type === "key" ? "text" : "password"}
            value={form.secret}
            onChange={(e) => set("secret", e.target.value)}
            placeholder={node ? "留空表示不修改" : ""}
            required={!node}
          />
        </label>

        <div className="grid2">
          <label>
            端口池起始
            <input
              type="number"
              value={form.port_start}
              onChange={(e) => set("port_start", Number(e.target.value))}
              required
            />
          </label>
          <label>
            端口池结束
            <input
              type="number"
              value={form.port_end}
              onChange={(e) => set("port_end", Number(e.target.value))}
              required
            />
          </label>
        </div>
        <p className="hint">
          默认放开全部端口，分配时会自动跳过节点上已被占用的端口。
          NAT 机器只能填服务商实际映射到本机的端口范围。
        </p>

        <label>
          跳板节点
          <select
            value={form.via_node_id ?? ""}
            onChange={(e) => set("via_node_id", e.target.value ? Number(e.target.value) : null)}
          >
            <option value="">直连（无需跳板）</option>
            {nodes
              .filter((n) => n.id !== node?.id)
              .map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
          </select>
        </label>
        <p className="hint">仅对控制器无法直连的内网节点设置，下发时会自动经跳板。</p>

        <label style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
          <input
            type="checkbox"
            checked={form.skip_udp_probe}
            onChange={(e) => set("skip_udp_probe", e.target.checked)}
            style={{ width: "auto", marginTop: 3 }}
          />
          <span>跳过 UDP 检测</span>
        </label>
        <p className="hint">多数 NAT 机器只映射 TCP，不跑 UDP 链路可勾选，每次探测省十几秒。</p>

        <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
