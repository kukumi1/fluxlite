import type { ReactNode } from "react";

interface ModalProps {
  title: string;
  onClose: () => void;
  children: ReactNode;
}

export function Modal({ title, onClose, children }: ModalProps) {
  return (
    <div
      className="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="modal">
        <div className="spread">
          <h2 style={{ margin: 0 }}>{title}</h2>
          <button className="btn sm" onClick={onClose}>
            关闭
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

export function Banner({ kind, children }: { kind: "err" | "ok" | "warn"; children: ReactNode }) {
  return <div className={`banner ${kind}`}>{children}</div>;
}
