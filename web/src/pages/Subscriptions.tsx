import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Check, Info, KeyRound, Layers, Link2, Pencil, Plus, RefreshCw, Rss, Trash2, Unplug, type LucideIcon } from "lucide-react";
import { api, ApiError } from "../api";
import type { Subscription, SubscriptionCandidate, SubscriptionFormat, SubscriptionInput } from "../api";
import { useConfirm } from "../components/ConfirmDialog";
import { CopyButton } from "../components/CopyButton";
import { EmptyState } from "../components/EmptyState";
import { Flag } from "../components/Flag";
import { Banner, Modal } from "../components/Modal";
import { PageHeader } from "../components/PageHeader";
import { regionOf, TONE_CYCLE } from "../lib/region";
import { subscriptionFormats, subscriptionInput, subscriptionMemberDetails, subscriptionName, subscriptionURL } from "../lib/subscriptions";
import "./management-redesign.css";
import { protocolMeta } from "./SingBoxCreate";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);

/** 订阅卡上最多直接列出几个节点，其余折成「还有 N 个」。 */
const MEMBER_PREVIEW = 4;

function MemberRow({ name, machine, protocol, port }: { name: string; machine: string; protocol: SubscriptionCandidate["protocol"]; port: number | null }) {
  const meta = protocolMeta(protocol);
  const code = regionOf(machine);
  return (
    <div className="sub-member">
      <span className={`proto-badge tone-${meta.tone}`} title={meta.label}>{meta.short}</span>
      <strong>{name}</strong>
      <span className="sub-member-where">
        {code && <Flag code={code} className="sb-where-flag" />}
        {machine}
        {port !== null && <code>:{port}</code>}
      </span>
    </div>
  );
}

function SummaryFigure({ icon: Icon, tone, label, value, note }: { icon: LucideIcon; tone: string; label: string; value: number; note?: string }) {
  return (
    <div className={`tone-${tone}`}>
      <i><Icon size={16} /></i>
      <dt>{label}</dt>
      <dd>{value}{note && <small>{note}</small>}</dd>
    </div>
  );
}

export function Subscriptions() {
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([]);
  const [candidates, setCandidates] = useState<SubscriptionCandidate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState(false);
  const [renaming, setRenaming] = useState<Subscription | null>(null);
  const [viewingMembers, setViewingMembers] = useState<Subscription | null>(null);
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

  const memberCount = subscriptions.reduce((sum, subscription) => sum + (subscription.members?.length ?? 0), 0);
  const freeCount = candidates.filter((node) => node.subscription_id === null).length;

  return <div className="management-page subscriptions-management">
    <PageHeader title="订阅"
      actions={<>
        <button className="icon-btn" aria-label="刷新" title="刷新" disabled={loading || busy} onClick={() => { setError(""); void load(); }}><RefreshCw size={16} /></button>
        <button className="btn primary" disabled={loading || busy} onClick={() => setEditor(true)}><Plus size={15} />创建订阅</button>
      </>} />
    {error && !editor && <Banner kind="err">{error}</Banner>}
    {loading && !subscriptions.length && <p className="muted" role="status">正在加载订阅…</p>}

    {subscriptions.length > 0 && (
      <dl className="stat-strip">
        <SummaryFigure icon={Rss} tone="lime" label="订阅" value={subscriptions.length} />
        <SummaryFigure icon={Layers} tone="sky" label="已收录节点" value={memberCount} />
        <SummaryFigure icon={Unplug} tone="peach" label="未收录节点" value={freeCount} note={freeCount ? "可加入新订阅" : undefined} />
      </dl>
    )}

    {!loading && !subscriptions.length && !error && (
      <div className="card">
        <EmptyState icon={<Rss size={18} />} title="还没有订阅" desc="把几个节点打包成一条链接，客户端导入一次就能全部用上。"
          action={<button className="btn primary" onClick={() => setEditor(true)}><Plus size={15} />创建订阅</button>} />
      </div>
    )}

    <div className="sub-grid">
      {subscriptions.map((subscription, index) => {
        const members = subscriptionMemberDetails(subscription.members, candidates);
        const legacy = subscription.mode !== "collection";
        return (
          <article key={subscription.id} className={`sub-card tone-${TONE_CYCLE[(index + 1) % TONE_CYCLE.length]}`}>
            <header className="sub-card-head">
              <span className="sub-icon"><Rss size={18} /></span>
              <div className="sub-card-title">
                <h3>{subscription.name}</h3>
                <span>创建于 {new Date(subscription.created_at).toLocaleDateString("zh-CN")}</span>
              </div>
              <button type="button" className="sub-count" aria-label={`查看「${subscription.name}」的 ${members.length} 个节点`} onClick={() => setViewingMembers(subscription)}>
                <b>{members.length}</b>个节点
              </button>
            </header>

            {legacy && <Banner kind="warn">旧独立额度订阅需要先迁移</Banner>}
            {subscription.link_available === false && <Banner kind="warn">旧订阅需手动重置一次以启用查看；现有地址仍有效</Banner>}

            <div className="sub-members">
              {members.length === 0 && <p className="sub-members-empty">此订阅暂无节点。</p>}
              {members.slice(0, MEMBER_PREVIEW).map((member) => <MemberRow key={member.user_id} {...member} />)}
              {members.length > MEMBER_PREVIEW && (
                <button type="button" className="sub-more" onClick={() => setViewingMembers(subscription)}>还有 {members.length - MEMBER_PREVIEW} 个节点</button>
              )}
            </div>

            <div className="sb-actions">
              <div className="sb-actions-main">
                <button className="btn sm export-btn" disabled={busy || legacy} onClick={() => void viewLink(subscription)}><Link2 size={14} />订阅链接</button>
              </div>
              <div className="sb-actions-side">
                <button className="btn sm ghost icon-action" aria-label="修改名称" title="修改名称" disabled={busy || loading || legacy} onClick={() => setRenaming(subscription)}><Pencil size={15} /></button>
                <button className="btn sm ghost icon-action" aria-label="重置订阅链接" title="重置订阅链接（旧链接失效）" disabled={busy || legacy} onClick={() => void resetToken(subscription)}><KeyRound size={15} /></button>
                <button className="btn sm ghost icon-action danger" aria-label="删除订阅" title="删除订阅" disabled={busy || legacy} onClick={() => void remove(subscription)}><Trash2 size={15} /></button>
              </div>
            </div>
          </article>
        );
      })}
    </div>

    {editor && <SubscriptionEditor candidates={candidates} onSave={save} onClose={() => setEditor(false)} />}
    {viewingMembers && <SubscriptionMembers subscription={viewingMembers} candidates={candidates} onClose={() => setViewingMembers(null)} />}
    {renaming && <SubscriptionNameEditor key={renaming.id} name={renaming.name} onSave={rename} onClose={() => setRenaming(null)} />}
    {issued && <SubscriptionLink key={issued.id} name={issued.name} token={issued.token} onClose={() => setIssued(null)} />}
    {confirmDialog}
  </div>;
}

// One client at a time: the formats differ only in the query string, so five
// stacked inputs were mostly the same text. Picking the client first also
// makes it obvious which link goes where.
function SubscriptionLink({ name, token, onClose }: { name: string; token: string; onClose: () => void }) {
  const [format, setFormat] = useState<SubscriptionFormat>("clash");
  const url = subscriptionURL(window.location.origin, token, format);
  return <Modal title="订阅链接" subtitle={`${name} · 选好客户端，复制链接导入`} size="wide" onClose={onClose}>
    <div className="segmented labels sub-formats" role="radiogroup" aria-label="客户端">
      {subscriptionFormats.map(([value, label]) => (
        <button key={value} type="button" role="radio" aria-checked={format === value} className={format === value ? "active" : ""} onClick={() => setFormat(value)}>{label}</button>
      ))}
    </div>
    <div className="term-block sb-link">
      <div className="terminal-bar">
        <span className="terminal-dots" aria-hidden><i /><i /><i /></span>
        <span className="terminal-title">{subscriptionFormats.find(([value]) => value === format)?.[1]}</span>
        <CopyButton text={url} label="复制链接" />
      </div>
      <code className="terminal-body">{url}</code>
    </div>
    <p className="dialog-note"><Info size={14} />链接含访问凭据，请勿公开。各客户端仅导出兼容节点；Clash 链接面向 Mihomo，旧版 Clash 不保证支持。</p>
  </Modal>;
}

function SubscriptionMembers({ subscription, candidates, onClose }: {
  subscription: Subscription; candidates: SubscriptionCandidate[]; onClose: () => void;
}) {
  const members = subscriptionMemberDetails(subscription.members, candidates);
  return <Modal title={subscription.name} subtitle={`收录了 ${members.length} 个节点`} onClose={onClose}>
    {!members.length ? <p className="muted">此订阅暂无节点。</p> : <div className="sub-members in-dialog">
      {members.map((member) => <MemberRow key={member.user_id} {...member} />)}
    </div>}
  </Modal>;
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

  return <Modal title="修改订阅名称" subtitle="只改面板里的显示名，链接不变。" onClose={() => { if (!pending.current) onClose(); }}>
    <form className="dialog-form" onSubmit={(event) => void submit(event)}>
      {error && <Banner kind="err">{error}</Banner>}
      <fieldset disabled={busy}>
        <label>名称<input required autoFocus value={name} onChange={(event) => setName(event.target.value)} /></label>
        <div className="modal-actions"><button className="btn ghost" type="button" onClick={onClose}>取消</button><button className="btn primary" type="submit">{busy ? "保存中…" : "保存"}</button></div>
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

  const free = candidates.filter((node) => node.subscription_id === null);
  const allFreeSelected = free.length > 0 && free.every((node) => availableSelection.includes(node.user_id));
  const toggle = (id: number) =>
    setSelected(availableSelection.includes(id) ? availableSelection.filter((x) => x !== id) : [...availableSelection, id].slice(0, 100));

  return <Modal title="创建订阅" subtitle="挑几个节点打包成一条链接；每个节点只能属于一个订阅。" size="wide" onClose={() => { if (!busy) onClose(); }}>
    <form className="dialog-form" onSubmit={(event) => void submit(event)}>
      {error && <Banner kind="err">{error}</Banner>}
      <fieldset disabled={busy}>
        <label>订阅名称<input required autoFocus maxLength={64} value={name} onChange={(event) => setName(event.target.value)} placeholder="例如 自用、分享给朋友" /></label>

        <div className="field">
          <div className="sub-pick-head">
            <span className="field-label">选择节点</span>
            <span className="sub-pick-count"><b>{availableSelection.length}</b> / {Math.min(100, free.length)} 已选</span>
            {free.length > 0 && (
              <button type="button" className="btn sm ghost" onClick={() => setSelected(allFreeSelected ? [] : free.slice(0, 100).map((node) => node.user_id))}>
                {allFreeSelected ? "全不选" : "全选空闲节点"}
              </button>
            )}
          </div>
          {!candidates.length && <p className="field-hint">暂无可用节点，请先去「节点」页创建。</p>}
          <div className="sub-pick" role="group" aria-label="选择节点">
            {candidates.map((node) => {
              const occupied = node.subscription_id !== null;
              const checked = availableSelection.includes(node.user_id);
              const meta = protocolMeta(node.protocol);
              const code = regionOf(node.node_name);
              return (
                <button
                  key={node.user_id}
                  type="button"
                  role="checkbox"
                  aria-checked={checked}
                  className={`sub-pick-item${checked ? " active" : ""}${occupied ? " is-occupied" : ""}`}
                  disabled={occupied || (!checked && availableSelection.length >= 100)}
                  title={occupied ? `已属于「${node.subscription_name}」` : undefined}
                  onClick={() => toggle(node.user_id)}
                >
                  <span className={`proto-badge tone-${meta.tone}`}>{meta.short}</span>
                  <span className="sub-pick-text">
                    <strong>{node.name}</strong>
                    <small>{code && <Flag code={code} className="sb-where-flag" />}{node.node_name} · {meta.label}</small>
                  </span>
                  {occupied ? <span className="sub-pick-owner">{node.subscription_name}</span> : <span className="sub-pick-check" aria-hidden>{checked && <Check size={13} />}</span>}
                </button>
              );
            })}
          </div>
        </div>

        <div className="modal-actions">
          <button className="btn ghost" type="button" onClick={onClose}>取消</button>
          <button className="btn primary" type="submit" disabled={!availableSelection.length}><Plus size={15} />{busy ? "处理中…" : `创建订阅${availableSelection.length ? `（${availableSelection.length}）` : ""}`}</button>
        </div>
      </fieldset>
    </form>
  </Modal>;
}
