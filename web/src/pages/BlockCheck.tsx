import type { BlockFamily, BlockKind, BlockState, Node } from "../api";
import { Modal } from "../components/Modal";

const TONE: Record<BlockKind, string> = {
  ok: "q-good",
  partial: "q-fair",
  tcp_blocked: "q-fair",
  tcp_down: "q-fair",
  no_tool: "q-none",
  ip_blocked: "q-bad",
};

export const isBlocked = (kind: BlockKind) => kind === "tcp_blocked" || kind === "ip_blocked" || kind === "tcp_down";

const when = (unix: number) => new Date(unix * 1000).toLocaleString("zh-CN", { hour12: false });

// The latest check of one machine, both families side by side whichever one
// the table above is showing: a block on either is what the column is for.
export function BlockCell({ node, states, onOpen }: { node: Node; states: BlockState[]; onOpen: () => void }) {
  if (states.length === 0) return <span className="muted">—</span>;
  return (
    <button type="button" className="block-cell" aria-label={`${node.name} 的封锁检测明细`} onClick={onOpen}>
      {states.map((s) => (
        <span key={s.family} className={`quality-pill ${TONE[s.kind]}`} title={`${s.note || s.label}\n检测于 ${when(s.checked_at)}`}>
          <small>v{s.family}</small>{s.label}
        </span>
      ))}
    </button>
  );
}

function FamilyDetail({ family }: { family: BlockFamily }) {
  return (
    <div className="block-detail">
      <p><strong>IPv{family.family} · {family.label}</strong>{family.note && <span className="muted">　{family.note}</span>}</p>
      <table className="quality-summary">
        <thead><tr><th>测试点</th><th>地址</th><th>ping 丢包</th><th>TCP 连上</th></tr></thead>
        <tbody>
          {family.targets.map((t) => (
            <tr key={t.name}>
              <td>{t.name}</td>
              <td className="mono">{family.family === 6 ? `[${t.ip}]` : t.ip}:{t.port}</td>
              <td className="mono">{t.icmp_loss === null ? "没有 ping" : `${t.icmp_loss}%`}</td>
              <td className="mono">{t.tcp_total === 0 ? "—" : `${t.tcp_ok}/${t.tcp_total}`}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function BlockDetailDialog({ node, states, onClose }: { node: Node; states: BlockState[]; onClose: () => void }) {
  return (
    <Modal
      title={`封锁检测 · ${node.name}`}
      subtitle={`检测于 ${when(Math.max(...states.map((s) => s.checked_at)))}。ping 通但 TCP 不通是 TCP 阻断，两样都不通是 IP 被墙`}
      size="wide"
      onClose={onClose}
    >
      {states.map((s) => <FamilyDetail key={s.family} family={s} />)}
      <p className="muted block-foot">
        这是从机器往国内测的，能看出整个 IP 或整个 TCP 有没有被封。如果只是某个节点端口被封，从这里测不出来，请用 itdog 等国内测试平台对那个端口做 TCPing。
      </p>
    </Modal>
  );
}
