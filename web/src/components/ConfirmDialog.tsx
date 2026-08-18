import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

export interface ConfirmRequest {
  title: string;
  body?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** 破坏性操作：确认按钮转为实心红，焦点停在取消上。 */
  danger?: boolean;
}

interface Pending extends ConfirmRequest {
  resolve: (confirmed: boolean) => void;
}

/**
 * 面板自己的确认弹窗，替代浏览器的 window.confirm。
 *
 * 原生 confirm 由浏览器绘制：样式不可改、固定贴在窗口顶端、还会带上域名前缀，
 * 跟面板其余部分毫无关系。它唯一的好处是同步返回布尔值，所以这里用 Promise
 * 还原那个调用形态，让调用处仍然是一行 if 判断。
 *
 * 返回 [询问函数, 要渲染的弹窗]。弹窗必须挂进组件树，否则询问永远不会有答案。
 */
export function useConfirm(): [(request: ConfirmRequest) => Promise<boolean>, ReactNode] {
  const [pending, setPending] = useState<Pending | null>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);

  const ask = useCallback(
    (request: ConfirmRequest) =>
      new Promise<boolean>((resolve) => setPending({ ...request, resolve })),
    [],
  );

  const settle = useCallback(
    (confirmed: boolean) => {
      if (!pending) return;
      pending.resolve(confirmed);
      setPending(null);
    },
    [pending],
  );

  // 打开时焦点落在「取消」而不是「删除」：这些弹窗几乎都是拦破坏性操作的，
  // 一个回车不该就把东西删了。
  useEffect(() => {
    if (pending) cancelRef.current?.focus();
  }, [pending]);

  useEffect(() => {
    if (!pending) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") settle(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [pending, settle]);

  const dialog = pending ? (
    <div
      className="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) settle(false);
      }}
    >
      <div className="modal confirm" role="alertdialog" aria-modal="true">
        <h2 style={{ marginBottom: pending.body ? 10 : 18 }}>{pending.title}</h2>
        {pending.body && <div className="modal-body">{pending.body}</div>}
        <div className="modal-actions">
          <button className="btn" ref={cancelRef} onClick={() => settle(false)}>
            {pending.cancelLabel ?? "取消"}
          </button>
          <button
            className={`btn ${pending.danger ? "destructive" : "primary"}`}
            onClick={() => settle(true)}
          >
            {pending.confirmLabel ?? "确定"}
          </button>
        </div>
      </div>
    </div>
  ) : null;

  return [ask, dialog];
}
