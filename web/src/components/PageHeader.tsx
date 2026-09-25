import type { ReactNode } from "react";

interface Props {
  title: string;
  desc?: ReactNode;
  actions?: ReactNode;
  /** 紧挨标题的内容，用于切换本页看的是哪一批数据。放在这里而不是 actions 里，
   *  是因为它改变的是「这一页在讲什么」，而 actions 里的东西都是对当前视图的操作。 */
  titleAside?: ReactNode;
}

export function PageHeader({ title, desc, actions, titleAside }: Props) {
  return (
    <div className="page-header">
      <div>
        <div className="page-title-row">
          <h1>{title}</h1>
          {titleAside}
        </div>
        {desc && <p className="page-desc">{desc}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}
