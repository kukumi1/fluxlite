import { useEffect } from "react";
import type { ReactNode } from "react";

interface ModalProps {
  title: string;
  onClose: () => void;
  children: ReactNode;
}

export function Modal({ title, onClose, children }: ModalProps) {
  // 点背景能关，Esc 却不能关，是两套不一致的直觉。确认弹窗已经认 Esc，
  // 这里跟上。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

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

// Banner keeps its content in one flex item on purpose. The stripe itself is a
// flex row, so passing children straight through turned every inline tag into
// its own column — a sentence with two <b> in it came out as five narrow
// columns of text instead of a paragraph.
export function Banner({ kind, children }: { kind: "err" | "ok" | "warn"; children: ReactNode }) {
  return (
    <div className={`banner ${kind}`}>
      <div className="banner-body">{children}</div>
    </div>
  );
}
