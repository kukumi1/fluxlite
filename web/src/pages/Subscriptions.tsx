import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, ApiError } from "../api";
import type { Subscription, SubscriptionCandidate, SubscriptionInput } from "../api";
import { Card } from "../components/Card";
import { useConfirm } from "../components/ConfirmDialog";
import { CopyButton } from "../components/CopyButton";
import { Banner, Modal } from "../components/Modal";
import { PageHeader } from "../components/PageHeader";
import { subscriptionFormats, subscriptionInput, subscriptionName, subscriptionURL } from "../lib/subscriptions";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);

export function Subscriptions() {
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([]);
  const [candidates, setCandidates] = useState<SubscriptionCandidate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState(false);
  const [renaming, setRenaming] = useState<Subscription | null>(null);
  // Tokens remain in page memory only; the server stores them encrypted.
  const [issued, setIssued] = useState<{ id: number; name: string; token: string } | null>(null);
  const [confirm, confirmDialog] = useConfirm();
  const loadSequence = useRef(0);
  const linkSequence = useRef(0);

  async function load() {
    const sequence = ++loadSequence.current;
    setLoading(true);
    try {
      const [result, available] = await Promise.all([api.listSubscriptions(), api.listSubscriptionCandidates()]);
      if (sequence !== loadSequence.current) return;
      setSubscriptions(result.subscriptions ?? []);
      setCandidates(available.nodes ?? []);
    } catch (cause) {
      if (sequence === loadSequence.current) setError(message(cause));
    } finally {
      if (sequence === loadSequence.current) setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    return () => { ++loadSequence.current; ++linkSequence.current; };
  }, []);

  async function mutate(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    try { await action(); await load(); }
    catch (cause) { setError(message(cause)); }
    finally { setBusy(false); }
  }

  async function remove(subscription: Subscription) {
    if (subscription.mode !== "collection") return;
    if (!await confirm({ title: `删除「${subscription.name}」？`, danger: true, confirmLabel: "删除订阅",
      body: "旧链接将失效，节点关联将解除。不删除机器，节点、服务和密钥均保留；已导入节点仍可连接。" })) return;
    await mutate(async () => {
      await api.deleteSubscription(subscription.id);
      if (issued?.id === subscription.id) setIssued(null);
    });
  }

  async function viewLink(subscription: Subscription) {
    const sequence = ++linkSequence.current;
    setIssued(null);
    setBusy(true);
    setError("");
    try {
      const { token } = await api.getSubscriptionLink(subscription.id);
      if (sequence === linkSequence.current) setIssued({ id: subscription.id, name: subscription.name, token });
    } catch (cause) {
      if (sequence === linkSequence.current) setError(message(cause));
    } finally {
      if (sequence === linkSequence.current) setBusy(false);
    }
  }

  async function resetToken(subscription: Subscription) {
    if (!await confirm({ title: "重置订阅下载链接？", danger: true, confirmLabel: "使旧链接失效",
      body: "旧链接立即失效。不轮换节点凭据，已导入节点仍可连接。新链接可随时查看。" })) return;
    setIssued(null);
    await mutate(async () => {
      const { token } = await api.resetSubscriptionToken(subscription.id);
      setIssued({ id: subscription.id, name: subscription.name, token });
    });
  }

  async function save(input: SubscriptionInput) {
    try {
      const result = await api.createSubscription(input);
      setIssued({ id: result.subscription.id, name: result.subscription.name, token: result.token });
      setEditor(false);
      await load();
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        await load();
        throw new Error(`${message(cause)}。节点归属已刷新，请重新选择未占用的节点。`);
      }
      throw cause;
    }
  }

  async function rename(name: string) {
    if (!renaming) return;
    const updated = await api.updateSubscription(renaming.id, { name });
    setSubscriptions((current) => current.map((subscription) => subscription.id === updated.id ? { ...subscription, name: updated.name } : subscription));
    setIssued((current) => current?.id === updated.id ? { ...current, name: updated.name } : current);
    setRenaming(null);
    await load();
  }

  return <>
    <PageHeader title="订阅"
      actions={<><button className="btn" disabled={loading || busy} onClick={() => { setError(""); void load(); }}>刷新</button><button className="btn primary" disabled={loading || busy} onClick={() => setEditor(true)}>创建订阅</button></>} />
    {error && <Banner kind="err">{error}</Banner>}
    {loading && <p className="muted" role="status">正在加载订阅…</p>}
    {!loading && !subscriptions.length && !error && <Card><h2>还没有订阅</h2><p className="muted">选择已有节点创建订阅。</p></Card>}
    {subscriptions.map((subscription) => <Card key={subscription.id}>
      <div className="spread"><h2>{subscription.name}</h2><span className="tag">{subscription.members?.length ?? 0} 个节点</span></div>
      {subscription.mode !== "collection" && <Banner kind="warn">旧独立额度订阅需要先迁移</Banner>}
      {subscription.link_available === false && <Banner kind="warn">旧订阅需手动重置一次以启用查看；现有地址仍有效</Banner>}
      <div className="row">
        <button className="btn sm" disabled={busy || loading || subscription.mode !== "collection"} onClick={() => setRenaming(subscription)}>修改名称</button>
        <button className="btn sm" disabled={busy || subscription.mode !== "collection"} onClick={() => void viewLink(subscription)}>查看订阅链接</button>
        <button className="btn sm" disabled={busy || subscription.mode !== "collection"} onClick={() => void resetToken(subscription)}>重置订阅链接</button>
        <button className="btn sm destructive" disabled={busy || subscription.mode !== "collection"} onClick={() => void remove(subscription)}>删除</button>
      </div>
    </Card>)}
    {editor && <SubscriptionEditor candidates={candidates} onSave={save} onClose={() => setEditor(false)} />}
    {renaming && <SubscriptionNameEditor key={renaming.id} name={renaming.name} onSave={rename} onClose={() => setRenaming(null)} />}
    {issued && <Modal title={`订阅链接 — ${issued.name}`} onClose={() => setIssued(null)}>
      <Banner kind="warn">链接含访问凭据，请勿公开。各客户端仅导出兼容节点。</Banner>
      {subscriptionFormats.map(([format, label]) => {
        const url = subscriptionURL(window.location.origin, issued.token, format);
        return <section key={format}><h3>{label}</h3><label>订阅地址<input readOnly value={url} onFocus={(event) => event.target.select()} /></label><CopyButton text={url} label={`复制 ${label} 链接`} /></section>;
      })}
      <p className="muted">Clash 链接面向 Mihomo；旧版 Clash 不保证支持。</p>
    </Modal>}
    {confirmDialog}
  </>;
}

function SubscriptionNameEditor({ name: initialName, onSave, onClose }: {
  name: string; onSave: (name: string) => Promise<void>; onClose: () => void;
}) {
  const [name, setName] = useState(initialName);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    try { await onSave(subscriptionName(name)); }
    catch (cause) { setError(message(cause)); }
    finally { pending.current = false; setBusy(false); }
  }

  return <Modal title="修改订阅名称" onClose={() => { if (!pending.current) onClose(); }}>
    <form onSubmit={(event) => void submit(event)}>
      {error && <Banner kind="err">{error}</Banner>}
      <fieldset disabled={busy}>
        <label>名称<input required autoFocus value={name} onChange={(event) => setName(event.target.value)} /></label>
        <div className="modal-actions"><button className="btn" type="button" onClick={onClose}>取消</button><button className="btn primary" type="submit">{busy ? "保存中…" : "保存"}</button></div>
      </fieldset>
    </form>
  </Modal>;
}

function SubscriptionEditor({ candidates, onSave, onClose }: {
  candidates: SubscriptionCandidate[]; onSave: (input: SubscriptionInput) => Promise<void>; onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef(false);
  const availableSelection = selected.filter((id) => candidates.some((node) => node.user_id === id && node.subscription_id === null));

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    try { await onSave(subscriptionInput(name, availableSelection, candidates)); }
    catch (cause) { setError(message(cause)); }
    finally { pending.current = false; setBusy(false); }
  }

  return <Modal title="创建订阅" onClose={() => { if (!busy) onClose(); }}>
    <form onSubmit={(event) => void submit(event)}>
      {error && <Banner kind="err">{error}</Banner>}
      <fieldset disabled={busy}>
        <label>名称<input required maxLength={64} value={name} onChange={(event) => setName(event.target.value)} placeholder="自用 / 分享给朋友" /></label>
        <p className="muted">已选 {availableSelection.length} / 100；每个节点只能属于一个订阅。</p>
        {!candidates.length && <p className="muted">暂无可管理节点，请先前往节点页面创建。</p>}
        {candidates.map((node) => {
          const occupied = node.subscription_id !== null;
          const checked = availableSelection.includes(node.user_id);
          return <label key={node.user_id} className={`subscription-node-choice${occupied ? " is-occupied" : ""}`}>
            <input type="checkbox" checked={occupied || checked} disabled={occupied || (!checked && availableSelection.length >= 100)} onChange={(event) => {
              setSelected(event.target.checked ? [...availableSelection, node.user_id] : availableSelection.filter((id) => id !== node.user_id));
            }} />
            <span className="subscription-node-choice-text">{node.name} · {node.node_name} · {node.protocol} · {node.port}
            {occupied && <span className="muted">（已属于「{node.subscription_name}」）</span>}</span>
          </label>;
        })}
        <div className="modal-actions"><button className="btn" type="button" onClick={onClose}>取消</button><button className="btn primary" type="submit" disabled={!availableSelection.length}>{busy ? "处理中…" : "创建订阅"}</button></div>
      </fieldset>
    </form>
  </Modal>;
}
