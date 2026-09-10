import { useState } from "react";
import { api, ApiError, type EnrollTicket, type Node } from "../api";
import { CopyButton } from "../components/CopyButton";
import { Banner, Modal } from "../components/Modal";

interface Props {
  node: Node;
  onClose: () => void;
  onDone: () => void;
}

/**
 * ReinstallDialog re-credentials a node whose machine was rebuilt.
 *
 * The alternative is deleting the node and adding it again, which fails while
 * any route references it — so in practice it means tearing down every route
 * on the machine and rebuilding it from memory. Here the node record is
 * reused, so its id, its port allocations and every hop pointing at it survive.
 */
export function ReinstallDialog({ node, onClose, onDone }: Props) {
  const [ticket, setTicket] = useState<EnrollTicket | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function generate() {
    setBusy(true);
    setError("");
    try {
      setTicket(await api.reenrollTicket(node.id));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "生成失败");
    } finally {
      setBusy(false);
    }
  }

  if (ticket) {
    return (
      <Modal title={`重装命令 · ${node.name}`} onClose={onClose}>
        <Banner kind="ok">在这台机器上以 root 执行下面这条命令，执行完节点就会自己恢复在线。</Banner>

        <pre className="cmd">{ticket.command}</pre>

        <div className="row" style={{ marginBottom: 16 }}>
          <CopyButton text={ticket.command} />
          <span className="muted" style={{ fontSize: 13 }}>
            有效期至 {new Date(ticket.expires_at).toLocaleString("zh-CN")}，仅可使用一次
          </span>
        </div>

        <h2>执行后会发生什么</h2>
        <ul className="hop-list" style={{ marginBottom: 16 }}>
          <li>装上面板的新公钥、重新安装 realm</li>
          <li>节点仍是原来那条记录（id {node.id}），链路不用重建</li>
          <li>原有链路会在下一轮巡检（最多 5 分钟）自动重新下发</li>
          <li>想立刻恢复，可到「链路」页对相关链路点「下发」</li>
        </ul>

        <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
          <button className="btn" onClick={onClose}>
            关闭
          </button>
          <button
            className="btn primary"
            onClick={() => {
              onDone();
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
    <Modal title={`重装节点 · ${node.name}`} onClose={onClose}>
      {error && <Banner kind="err">{error}</Banner>}

      <p className="hint" style={{ marginTop: 0 }}>
        机器重装系统后，面板连不上它有三个原因：主机指纹变了、面板的公钥没了、realm 没了。
        这三样都只能在机器上重新执行安装命令来解决。
      </p>

      <h2>会用回这台节点现在的配置</h2>
      <ul className="hop-list" style={{ marginBottom: 16 }}>
        <li>
          连接地址 <code>{node.ssh_user}@{node.host}:{node.ssh_port}</code>
        </li>
        <li>
          端口池 {node.port_start}–{node.port_end}
        </li>
      </ul>
      <p className="hint">这些都不用重填。地址或端口若也变了，请先「编辑」改好再来重装。</p>

      <Banner kind="err">
        执行完成后，面板会重置这台节点的主机指纹并重新记录。指纹校验是用来防止路上有人
        冒充这台机器、骗取面板 root 登录的，所以只有在确认确实是你自己重装了这台机器时
        才继续。
      </Banner>

      <div className="row" style={{ justifyContent: "flex-end", marginTop: 8 }}>
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button className="btn primary" disabled={busy} onClick={() => void generate()}>
          {busy ? "生成中…" : "生成重装命令"}
        </button>
      </div>
    </Modal>
  );
}
