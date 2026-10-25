import { useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";
import type { ReactNode } from "react";
import { X } from "lucide-react";

interface ModalProps {
  title: string;
  /** 标题下一行灰字，说清这个弹窗要做成什么。 */
  subtitle?: ReactNode;
  size?: "wide";
  onClose: () => void;
  children: ReactNode;
}

export function Modal({ title, subtitle, size, onClose, children }: ModalProps) {
  const titleId = useId();
  const backdropRef = useRef<HTMLDivElement>(null);

  // 点背景能关，Esc 却不能关，是两套不一致的直觉。确认弹窗已经认 Esc，
  // 这里跟上。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && Array.from(document.querySelectorAll("body > .modal-backdrop")).pop() === backdropRef.current) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Portal out of animated/transformed page containers: fixed must use the viewport.
  return createPortal(
    <div
      ref={backdropRef}
      className="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className={`modal${size ? ` ${size}` : ""}`} role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <header className="modal-head">
          <div>
            <h2 id={titleId}>{title}</h2>
            {subtitle && <p className="modal-subtitle">{subtitle}</p>}
          </div>
          <button className="modal-close" onClick={onClose} aria-label="关闭" title="关闭">
            <X size={16} />
          </button>
        </header>
        {children}
      </div>
    </div>,
    document.body,
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
