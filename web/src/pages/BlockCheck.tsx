import { Fragment, useState } from "react";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import type { BlockFamily, BlockKind, BlockResult, IPFamily } from "../api";
import { Banner, Modal } from "../components/Modal";
import { Flag } from "../components/Flag";
import { regionOf } from "../lib/region";

const TONE: Record<BlockKind, string> = {
  ok: "q-good",
  partial: "q-fair",
  tcp_blocked: "q-fair",
  tcp_down: "q-fair",
  no_tool: "q-none",
  ip_blocked: "q-bad",
};

function Verdict({ family }: { family: BlockFamily | undefined }) {
  if (!family) return <span className="muted">—</span>;
  return <span className={`quality-pill ${TONE[family.kind]}`} title={family.note}>{family.label}</span>;
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

export function BlockCheckDialog({ results, running, error, onRun, onClose }: {
  results: BlockResult[] | null;
  running: boolean;
  error: string;
  onRun: () => void;
  onClose: () => void;
}) {
  const [open, setOpen] = useState<number | null>(null);
  const familyOf = (r: BlockResult, f: IPFamily) => r.families.find((x) => x.family === f);
  const blocked = (results ?? []).filter((r) => r.families.some((f) => f.kind !== "ok")).length;

  return (
    <Modal
      title="封锁检测"
      subtitle="每台机器同时往国内三网发 ping 和 TCP：ping 通但 TCP 不通是 TCP 阻断，两样都不通是 IP 被墙"
      size="wide"
      onClose={onClose}
    >
      {error && <Banner kind="err">{error}</Banner>}
      {running && <p className="muted"><RefreshCw size={14} className="spin" /> 正在检测所有在线机器，大约需要半分钟到一分钟…</p>}
      {results && !running && (
        <p className="muted">{blocked === 0 ? "所有机器到国内都正常。" : `${blocked} 台机器有异常，点开看每个测试点的结果。`}</p>
      )}
      {results && (
        <table className="quality-summary block-table">
          <thead><tr><th>机器</th><th>IPv4</th><th>IPv6</th><th aria-label="展开" /></tr></thead>
          <tbody>
            {results.map((r) => {
              const code = regionOf(r.name);
              const expanded = open === r.node_id;
              const v6 = familyOf(r, 6);
              return (
                <Fragment key={r.node_id}>
                  <tr className={r.error ? "is-offline" : undefined}>
                    <td><span className="quality-node">{code && <Flag code={code} />}<span>{r.name}</span></span></td>
                    {r.error ? <td colSpan={2} className="muted">{r.error}</td> : (
                      <>
                        <td><Verdict family={familyOf(r, 4)} /></td>
                        <td>{v6 ? <Verdict family={v6} /> : <span className="muted">没有 IPv6</span>}</td>
                      </>
                    )}
                    <td className="quality-actions">
                      {r.families.length > 0 && (
                        <button className="icon-btn" aria-expanded={expanded} aria-label={`${r.name} 的明细`} onClick={() => setOpen(expanded ? null : r.node_id)}>
                          {expanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
                        </button>
                      )}
                    </td>
                  </tr>
                  {expanded && (
                    <tr className="block-expanded">
                      <td colSpan={4}>{r.families.map((f) => <FamilyDetail key={f.family} family={f} />)}</td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      )}
      <p className="muted block-foot">
        这是从机器往国内测的，能看出整个 IP 或整个 TCP 有没有被封。如果只是某个节点端口被封，从这里测不出来，请用 itdog 等国内测试平台对那个端口做 TCPing。
      </p>
      <div className="modal-actions">
        <button className="btn" onClick={onClose}>关闭</button>
        <button className="btn primary" disabled={running} onClick={onRun}>{running ? "检测中…" : "重新检测"}</button>
      </div>
    </Modal>
  );
}
