import { useEffect, useId, useRef } from "react";
import type { ReactNode } from "react";
import { X } from "lucide-react";

interface DrawerProps {
  title: string;
  onClose: () => void;
  children: ReactNode;
}

/** Native modal dialogs keep keyboard focus inside the drawer and restore it on close. */
export function Drawer({ title, onClose, children }: DrawerProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleID = useId();

  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    const previousOverflow = document.body.style.overflow;
    element.showModal();
    document.body.style.overflow = "hidden";
    return () => {
      element.close();
      document.body.style.overflow = previousOverflow;
    };
  }, []);

  return (
    <dialog
      ref={dialog}
      className="drawer"
      aria-labelledby={titleID}
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) onClose();
      }}
    >
      <div className="drawer-heading">
        <div><span className="drawer-eyebrow">FLUXLITE / 配置</span><h2 id={titleID}>{title}</h2></div>
        <button type="button" className="icon-btn" aria-label="关闭" onClick={onClose}><X size={20} /></button>
      </div>
      <div className="drawer-content">{children}</div>
    </dialog>
  );
}
