import type { CSSProperties, ReactNode } from "react";

interface CardProps {
  children: ReactNode;
  className?: string;
  style?: CSSProperties;
  /** 列表里的序号，用来让卡片依次浮起而不是整片同时出现。 */
  index?: number;
}

const STAGGER_STEP_MS = 45;
const STAGGER_MAX_ITEMS = 12;

export function Card({ children, className = "", style, index }: CardProps) {
  // 超过十来张之后再逐个延迟，最后一张要等半秒才出现，反而像卡顿。
  const delay =
    index === undefined ? undefined : Math.min(index, STAGGER_MAX_ITEMS) * STAGGER_STEP_MS;

  return (
    <div
      className={`card${index === undefined ? "" : " stagger-card"} ${className}`.trim()}
      style={
        delay === undefined
          ? style
          : ({ ...style, "--card-enter-delay": `${delay}ms` } as CSSProperties)
      }
    >
      {children}
    </div>
  );
}
