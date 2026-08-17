import type { ReactNode } from "react";

interface Props {
  title: string;
  desc?: ReactNode;
  actions?: ReactNode;
}

export function PageHeader({ title, desc, actions }: Props) {
  return (
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {desc && <p className="page-desc">{desc}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}
