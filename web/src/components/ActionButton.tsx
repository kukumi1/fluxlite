import type { LucideIcon } from "lucide-react";

interface Props {
  label: string;
  icon: LucideIcon;
  /** 列表视图一行放不下文字按钮，只画图标，文字进 title 和 aria-label。 */
  iconOnly: boolean;
  title?: string;
  className?: string;
  disabled?: boolean;
  onClick: () => void;
}

/** A row action that is a text button in card views and an icon in list views. */
export function ActionButton({ label, icon: Icon, iconOnly, title, className = "", disabled, onClick }: Props) {
  return (
    <button
      className={`btn sm${iconOnly ? " icon-action" : ""}${className ? ` ${className}` : ""}`}
      title={title ?? label}
      aria-label={iconOnly ? label : undefined}
      disabled={disabled}
      onClick={onClick}
    >
      {iconOnly ? <Icon size={15} /> : label}
    </button>
  );
}
