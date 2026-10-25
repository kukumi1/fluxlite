import type { ReactNode } from "react";
import { metricTone } from "./MetricBar";

interface Props {
  label: string;
  /** 已经算好的占用百分比。null 表示没量到，环不画进度。 */
  percent: number | null;
  detail?: ReactNode;
  /** 读数存疑时的说明，非空则在环下标出。 */
  caveat?: string;
}

const RADIUS = 26;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

/** MetricBar 的环形版本，阈值和「量不到不画」的规矩与它相同。 */
export function MetricRing({ label, percent: pct, detail, caveat }: Props) {
  const tone = metricTone(pct);
  return (
    <div className={`metric-ring${tone ? ` ${tone}` : ""}`} title={pct === null ? caveat ?? "未能读到这项读数" : caveat}>
      <div className="metric-ring-dial">
        <svg viewBox="0 0 64 64" aria-hidden>
          <circle className="metric-ring-track" cx="32" cy="32" r={RADIUS} />
          {pct !== null && (
            <circle
              className="metric-ring-value"
              cx="32"
              cy="32"
              r={RADIUS}
              strokeDasharray={`${(CIRCUMFERENCE * Math.max(pct, 1)) / 100} ${CIRCUMFERENCE}`}
            />
          )}
        </svg>
        <span className="metric-ring-figure">
          {pct === null ? "—" : <>{pct.toFixed(0)}<small>%</small></>}
        </span>
      </div>
      <span className="metric-ring-label">
        {label}
        {caveat && pct !== null && <span className="tag warn">存疑</span>}
      </span>
      {detail && pct !== null && <span className="metric-ring-detail">{detail}</span>}
    </div>
  );
}
