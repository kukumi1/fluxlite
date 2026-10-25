import { useState } from "react";
import { Plus, Settings2 } from "lucide-react";
import type { Node, SingBoxCipher, SingBoxProtocol, SingBoxUserInput } from "../api";
import { Flag } from "../components/Flag";
import { Banner, Modal } from "../components/Modal";
import { regionOf } from "../lib/region";

export interface ProtocolMeta {
  id: SingBoxProtocol;
  label: string;
  /** 两三个字母的徽章，卡片和列表里用它认协议。 */
  short: string;
  note: string;
  tone: "lime" | "sky" | "lilac" | "peach" | "sun";
}

export const PROTOCOLS: ProtocolMeta[] = [
  { id: "vless-reality", label: "VLESS + Reality", short: "VR", note: "借大站 TLS 握手，最常用", tone: "lilac" },
  { id: "hysteria2", label: "Hysteria2", short: "HY2", note: "QUIC，弱网抗丢包", tone: "peach" },
  { id: "ss2022", label: "SS2022", short: "SS", note: "轻量，无需证书", tone: "lime" },
  { id: "anytls", label: "AnyTLS", short: "AT", note: "TLS 伪装，抗识别", tone: "sky" },
  { id: "tuic", label: "TUIC", short: "TU", note: "QUIC，低延迟", tone: "sun" },
  { id: "trojan", label: "Trojan", short: "TJ", note: "TLS，客户端兼容好", tone: "sky" },
  { id: "vmess", label: "VMess", short: "VM", note: "老牌，客户端最全", tone: "lilac" },
  { id: "vless-tls", label: "VLESS + TLS", short: "VT", note: "标准 TLS 加密", tone: "peach" },
];

export function protocolMeta(id: SingBoxProtocol): ProtocolMeta {
  return PROTOCOLS.find((p) => p.id === id) ?? { id, label: id, short: id.slice(0, 2).toUpperCase(), note: "", tone: "lime" };
}

const CIPHERS: Array<[SingBoxCipher, string]> = [
  ["2022-blake3-aes-128-gcm", "AES-128-GCM"],
  ["2022-blake3-aes-256-gcm", "AES-256-GCM"],
  ["2022-blake3-chacha20-poly1305", "ChaCha20-Poly1305"],
];

const GB = 1024 ** 3;
const QUOTA_PRESETS: Array<[number, string]> = [[0, "不限"], [50, "50 GB"], [100, "100 GB"], [200, "200 GB"], [500, "500 GB"], [1024, "1 TB"]];
const EXPIRY_PRESETS: Array<[number, string]> = [[0, "永久"], [30, "30 天"], [90, "90 天"], [365, "1 年"]];

export function toDateTimeLocal(date: Date): string {
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
  return local.toISOString().slice(0, 16);
}

interface Props {
  nodes: Node[];
  input: SingBoxUserInput;
  onChange: (input: SingBoxUserInput) => void;
  busy: boolean;
  error: string;
  onCreate: () => void;
  onClose: () => void;
}

// Machine and protocol are picked from cards because both are "which one of a
// few", and seeing them side by side is the decision. Quota and expiry get
// one-tap presets with a field for anything else.
export function CreateNodeDialog({ nodes, input, onChange, busy, error, onCreate, onClose }: Props) {
  const [expiryDays, setExpiryDays] = useState<number | null>(input.expires_at ? null : 0);
  const set = <K extends keyof SingBoxUserInput>(key: K, value: SingBoxUserInput[K]) => onChange({ ...input, [key]: value });
  const quotaGB = input.quota_bytes / GB;

  function chooseExpiry(days: number) {
    setExpiryDays(days);
    set("expires_at", days === 0 ? "" : toDateTimeLocal(new Date(Date.now() + days * 86_400_000)));
  }

  const limits = [
    input.port ? `端口 ${input.port}` : "自动端口",
    input.upload_limit_mbps || input.download_limit_mbps ? `限速 ↑${input.upload_limit_mbps || "∞"} ↓${input.download_limit_mbps || "∞"} Mbps` : "不限速",
  ].join(" · ");

  return (
    <Modal title="创建节点" subtitle="选好机器和协议，面板会在机器上部署一个独立的 sing-box 服务。" size="wide" onClose={onClose}>
      <form
        className="dialog-form"
        onSubmit={(event) => {
          event.preventDefault();
          onCreate();
        }}
      >
        {error && <Banner kind="err">{error}</Banner>}

        <label>
          节点名称
          <input value={input.name} onChange={(e) => set("name", e.target.value)} placeholder="给谁用，例如 Alice 或 家里电视" required autoFocus />
        </label>

        <div className="field">
          <span className="field-label">部署到哪台机器</span>
          <div className="choice-grid machines" role="radiogroup" aria-label="部署机器">
            {nodes.map((node) => {
              const code = regionOf(node.name);
              return (
                <button
                  key={node.id}
                  type="button"
                  role="radio"
                  aria-checked={input.node_id === node.id}
                  className={input.node_id === node.id ? "active" : ""}
                  onClick={() => set("node_id", node.id)}
                >
                  <span className="choice-flag">{code ? <Flag code={code} /> : null}</span>
                  <span className="choice-name">{node.name}</span>
                  <i className={`choice-dot ${node.status}`} title={node.status === "online" ? "在线" : node.status === "offline" ? "离线" : "状态未知"} />
                </button>
              );
            })}
          </div>
        </div>

        <div className="field">
          <span className="field-label">协议</span>
          <div className="choice-grid protocols" role="radiogroup" aria-label="协议">
            {PROTOCOLS.map((p) => (
              <button
                key={p.id}
                type="button"
                role="radio"
                aria-checked={input.protocol === p.id}
                className={`tone-${p.tone}${input.protocol === p.id ? " active" : ""}`}
                onClick={() => set("protocol", p.id)}
              >
                <span className="proto-badge">{p.short}</span>
                <span>
                  <strong>{p.label}</strong>
                  <small>{p.note}</small>
                </span>
              </button>
            ))}
          </div>
          {input.protocol === "ss2022" && (
            <div className="inline-choice">
              <span>加密方法</span>
              <div className="segmented labels" role="radiogroup" aria-label="SS2022 加密方法">
                {CIPHERS.map(([value, label]) => (
                  <button key={value} type="button" role="radio" aria-checked={input.cipher === value} className={input.cipher === value ? "active" : ""} onClick={() => set("cipher", value)}>
                    {label}
                  </button>
                ))}
              </div>
            </div>
          )}
          {input.protocol === "anytls" && <p className="field-hint">SNI 固定为 developer.apple.com。</p>}
        </div>

        <div className="field">
          <span className="field-label">流量额度 <small>每个周期</small></span>
          <div className="preset-row">
            {QUOTA_PRESETS.map(([gb, label]) => (
              <button key={gb} type="button" className={quotaGB === gb ? "active" : ""} onClick={() => set("quota_bytes", gb * GB)}>
                {label}
              </button>
            ))}
            <label className="preset-custom">
              <input
                type="number"
                min={0}
                step="any"
                aria-label="自定义额度（GB）"
                placeholder="自定义"
                value={QUOTA_PRESETS.some(([gb]) => gb === quotaGB) ? "" : quotaGB}
                onChange={(e) => set("quota_bytes", e.target.value === "" ? 0 : Number(e.target.value) * GB)}
              />
              <span>GB</span>
            </label>
          </div>
        </div>

        <div className="field">
          <span className="field-label">连接有效期</span>
          <div className="preset-row">
            {EXPIRY_PRESETS.map(([days, label]) => (
              <button key={days} type="button" className={expiryDays === days ? "active" : ""} onClick={() => chooseExpiry(days)}>
                {label}
              </button>
            ))}
            <label className="preset-custom date">
              <input
                type="datetime-local"
                aria-label="自定义到期时间"
                value={input.expires_at ?? ""}
                onChange={(e) => {
                  setExpiryDays(e.target.value ? null : 0);
                  set("expires_at", e.target.value);
                }}
              />
            </label>
          </div>
        </div>

        <details className="advanced">
          <summary>
            <Settings2 size={15} />
            端口与限速
            <span className="advanced-summary">{limits}</span>
          </summary>
          <div className="advanced-grid">
            <label>
              端口
              <input type="number" min={0} max={65535} placeholder="自动分配" value={input.port || ""} onChange={(e) => set("port", e.target.value === "" ? 0 : Number(e.target.value))} />
            </label>
            <label>
              上行限速（Mbps）
              <input type="number" min={0} max={100000} placeholder="不限" value={input.upload_limit_mbps || ""} onChange={(e) => set("upload_limit_mbps", e.target.value === "" ? 0 : Number(e.target.value))} />
            </label>
            <label>
              下行限速（Mbps）
              <input type="number" min={0} max={100000} placeholder="不限" value={input.download_limit_mbps || ""} onChange={(e) => set("download_limit_mbps", e.target.value === "" ? 0 : Number(e.target.value))} />
            </label>
          </div>
        </details>

        <div className="modal-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy || !input.name || !input.node_id}>
            <Plus size={15} />
            {busy ? "部署中…" : "创建并部署"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
