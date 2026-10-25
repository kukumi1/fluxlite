import { useId, useState, type ReactNode } from "react";
import { ChevronDown, type LucideIcon } from "lucide-react";
import type { Tone } from "../lib/region";

interface Props {
  icon: LucideIcon;
  tone: Tone;
  title: string;
  summary: ReactNode;
  status?: ReactNode;
  defaultOpen?: boolean;
  children: ReactNode;
}

// A collapsed row until it is needed. The body is hidden rather than unmounted:
// a one-time recovery code or binding link shown inside must survive the
// section being folded away.
export function SettingSection({ icon: Icon, tone, title, summary, status, defaultOpen = false, children }: Props) {
  const [open, setOpen] = useState(defaultOpen);
  const bodyId = useId();
  return (
    <section className={`setting-section tone-${tone}${open ? " open" : ""}`}>
      <button type="button" className="setting-head" aria-expanded={open} aria-controls={bodyId} onClick={() => setOpen((value) => !value)}>
        <span className="setting-icon"><Icon size={18} /></span>
        <span className="setting-text">
          <strong>{title}</strong>
          <small>{summary}</small>
        </span>
        {status}
        <ChevronDown className="setting-chevron" size={18} aria-hidden />
      </button>
      <div className="setting-body" id={bodyId} hidden={!open}>
        {children}
      </div>
    </section>
  );
}
