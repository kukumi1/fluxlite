import type { ReactNode } from "react";

interface Props {
  label: string;
  /** 已经算好的占用百分比。null 表示没量到，整条不画。 */
  percent: number | null;
  /** 右侧的文字说明，例如 "2.3 / 4.0 GB" 或 "4 核"。 */
  detail?: ReactNode;
  /** 读数存疑时的说明，非空则百分比旁出现提示。 */
  caveat?: string;
}

const WARN_AT = 75;
const CRITICAL_AT = 90;

/**
 * 一条资源占用条。
 *
 * 量不到时整条不画，只留一个「—」。画一条 0% 的空条等于在说这台机器很闲，
 * 而实际情况是根本没问到。
 */
export function MetricBar({ label, percent: pct, detail, caveat }: Props) {
  const tone = pct === null ? "" : pct >= CRITICAL_AT ? "err" : pct >= WARN_AT ? "warn" : "";

  return (
    <div className="metric">
      <div className="metric-head">
        <span className="metric-label">{label}</span>
        <span className="metric-detail">
          {pct === null ? (
            <span className="muted" title={caveat ?? "未能读到这项读数"}>
              —
            </span>
          ) : (
            <>
              <strong>{pct.toFixed(0)}%</strong>
              {detail && <span className="muted"> · {detail}</span>}
              {caveat && (
                <span className="tag warn" style={{ marginLeft: 6 }} title={caveat}>
                  存疑
                </span>
              )}
            </>
          )}
        </span>
      </div>
      <div className="progress">
        {pct !== null && (
          <div className={`progress-bar ${tone}`} style={{ width: `${Math.max(pct, 1)}%` }} />
        )}
      </div>
    </div>
  );
}
