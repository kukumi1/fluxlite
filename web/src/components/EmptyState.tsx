import type { ReactNode } from "react";
import { Mascot } from "./Mascot";

interface Props {
  icon: ReactNode;
  title: string;
  desc?: string;
  action?: ReactNode;
}

export function EmptyState({ icon, title, desc, action }: Props) {
  return (
    <div className="empty-state">
      <div className="empty-state-art">
        <Mascot size={92} />
        <span className="empty-state-icon">{icon}</span>
      </div>
      <div className="empty-state-title">{title}</div>
      {desc && <div className="empty-state-desc">{desc}</div>}
      {action && <div style={{ marginTop: 10 }}>{action}</div>}
    </div>
  );
}
