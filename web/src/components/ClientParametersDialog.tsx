import { useEffect, useRef, useState } from "react";
import { api, type SingBoxClientParameters, type SingBoxClientParametersInput, type SingBoxUser } from "../api";
import { Banner, Modal } from "./Modal";

const fingerprints = ["chrome", "firefox", "safari", "edge", "360", "qq", "ios", "android", "random", "randomized"];
const emptyParameters: SingBoxClientParameters = { sni: "", insecure: false, fingerprint: "" };

interface Props {
  user: SingBoxUser;
  onClose: () => void;
  onChanged: () => void;
}

export function ClientParametersDialog({ user, onClose, onChanged }: Props) {
  const [parameters, setParameters] = useState<SingBoxClientParameters>(emptyParameters);
  const [inherited, setInherited] = useState(true);
  const [originalLink, setOriginalLink] = useState("");
  const [loading, setLoading] = useState(true);
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const pending = useRef(false);
  const mounted = useRef(false);

  useEffect(() => {
    let cancelled = false;
    mounted.current = true;
    void api.getSingBoxClientParameters(user.id).then((result) => {
      if (cancelled) return;
      setParameters(result.parameters ?? result.effective_parameters);
      setInherited(result.parameters === null);
      setReady(true);
    }).catch((failure: unknown) => {
      if (!cancelled) setError(failure instanceof Error ? failure.message : String(failure));
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; mounted.current = false; };
  }, [user.id]);

  async function save(input: SingBoxClientParametersInput | null) {
    if (loading || pending.current) return;
    pending.current = true;
    setBusy(true); setError(""); setNotice("");
    setOriginalLink("");
    let saved = false;
    try {
      if (input === null) await api.resetSingBoxClientParameters(user.id);
      else await api.setSingBoxClientParameters(user.id, input);
      saved = true;
      if (!mounted.current) return;
      setReady(false);
      setOriginalLink("");
      onChanged();
      setNotice(input === null ? "已恢复原配置默认参数，请重新导出节点。" : "客户端参数已保存，请重新导出节点链接或二维码。");
      const result = await api.getSingBoxClientParameters(user.id);
      if (!mounted.current) return;
      setParameters(result.parameters ?? result.effective_parameters);
      setInherited(result.parameters === null);
      setReady(true);
    } catch (failure) {
      if (mounted.current) setError(`${saved ? "保存已成功，但重新读取参数失败，请关闭并重新打开确认：" : ""}${failure instanceof Error ? failure.message : String(failure)}`);
    } finally {
      pending.current = false;
      if (mounted.current) setBusy(false);
    }
  }

  const disabled = loading || busy || !ready;
  const supportsFingerprint = user.protocol !== "hysteria2" && user.protocol !== "tuic";
  return <Modal title={`客户端参数 — ${user.name}`} onClose={() => { if (!pending.current) { setOriginalLink(""); onClose(); } }}>
    <div className="singbox-client-parameters" aria-busy={loading || busy}>
      <p className="muted">仅影响节点链接、二维码和客户端配置，不修改机器上的服务、密码或端口。</p>
      {error && <Banner kind="err">{error}</Banner>}
      {notice && <div role="status"><Banner kind="ok">{notice}</Banner></div>}
      {loading ? <p role="status">正在读取客户端参数…</p> : ready && <>
        <p className="muted">{inherited ? "当前使用原配置默认参数；下方显示实际生效值，只有点击保存后才会覆盖。" : "已保存自定义客户端参数。"}</p>
        <form className="singbox-client-parameter-fields" onSubmit={(event) => { event.preventDefault(); void save({ parameters: { ...parameters, sni: parameters.sni.trim() } }); }}>
          <label>TLS 服务器名称（SNI）<input value={parameters.sni} maxLength={253} disabled={disabled} onChange={(event) => setParameters({ ...parameters, sni: event.target.value })} placeholder="留空沿用原配置或入口地址" autoComplete="off" spellCheck={false} /></label>
          <label>TLS 指纹<select value={parameters.fingerprint} disabled={disabled || !supportsFingerprint} onChange={(event) => setParameters({ ...parameters, fingerprint: event.target.value })}><option value="">默认（不指定）</option>{fingerprints.map((fingerprint) => <option key={fingerprint} value={fingerprint}>{fingerprint}</option>)}</select>{!supportsFingerprint && <small className="muted">Hysteria2 / TUIC 使用 QUIC，不支持 uTLS 指纹；仍可设置 SNI 和证书校验。</small>}</label>
          <label className="singbox-check"><input type="checkbox" checked={parameters.insecure} disabled={disabled} onChange={(event) => setParameters({ ...parameters, insecure: event.target.checked })} />跳过证书校验</label>
          {parameters.insecure && <Banner kind="warn">已开启跳过证书校验（insecure=1）。客户端将不验证服务器证书，请仅在确认原节点需要此设置时使用。</Banner>}
          <div className="singbox-client-parameter-actions"><button className="btn" type="button" disabled={disabled || inherited} onClick={() => void save(null)}>恢复原配置默认参数</button><button className="btn primary" type="submit" disabled={disabled}>保存客户端参数</button></div>
        </form>
        {user.protocol === "anytls" && <section className="singbox-client-link-import" aria-label="从原节点链接恢复参数">
          <label>原 AnyTLS 节点链接<input type="password" value={originalLink} disabled={disabled} onChange={(event) => setOriginalLink(event.target.value)} placeholder="粘贴 anytls:// 原节点链接" autoComplete="off" spellCheck={false} /></label>
          <p className="muted">链接包含节点密码，请勿分享。仅提取 SNI、证书校验和指纹参数，密码、地址和端口必须与此节点匹配；不会保存完整链接。导入会替换已保存的客户端参数，不会合并上方未保存的修改。</p>
          <button className="btn" disabled={disabled || !originalLink.trim()} onClick={() => void save({ original_link: originalLink.trim() })}>从原链接恢复参数</button>
        </section>}
      </>}
    </div>
  </Modal>;
}
