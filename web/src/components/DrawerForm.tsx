import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

interface BlockProps {
  title: string;
  icon: LucideIcon;
  note?: ReactNode;
  children: ReactNode;
}

/** One white block of a drawer form: a small titled group of related fields. */
export function FormBlock({ title, icon: Icon, note, children }: BlockProps) {
  return (
    <section className="form-block">
      <header className="form-block-head">
        <span className="form-block-icon" aria-hidden><Icon size={15} /></span>
        <div>
          <h3>{title}</h3>
          {note && <p>{note}</p>}
        </div>
      </header>
      {children}
    </section>
  );
}

interface ChoiceProps<T extends string> {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (value: T) => void;
}

/** A two-or-three-way choice shown all at once, instead of hidden in a select. */
export function ChoiceGroup<T extends string>({ label, value, options, onChange }: ChoiceProps<T>) {
  return (
    <div className="field">
      <span className="field-label">{label}</span>
      <div className="choice-group" role="radiogroup" aria-label={label}>
        {options.map((option) => (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={option.value === value}
            className={option.value === value ? "active" : ""}
            onClick={() => onChange(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>
    </div>
  );
}

interface SwitchProps {
  label: string;
  note?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}

export function SwitchField({ label, note, checked, disabled, onChange }: SwitchProps) {
  return (
    <div className="switch-row">
      <div><strong>{label}</strong>{note && <span>{note}</span>}</div>
      <button
        type="button"
        role="switch"
        aria-label={label}
        aria-checked={checked}
        disabled={disabled}
        className={`toggle-switch${checked ? " on" : ""}`}
        onClick={() => onChange(!checked)}
      >
        <span />
      </button>
    </div>
  );
}

/** Cancel and save, pinned to the bottom of the drawer while the form scrolls. */
export function DrawerActions({ busy, disabled, onCancel }: { busy: boolean; disabled?: boolean; onCancel: () => void }) {
  return (
    <div className="drawer-actions">
      <button type="button" className="btn" onClick={onCancel}>取消</button>
      <button className="btn primary" disabled={busy || disabled}>{busy ? "保存中…" : "保存"}</button>
    </div>
  );
}
