import { useState } from "react";
import type { DailyTotal } from "../api";
import { formatBytes } from "../lib/format";

const KIB = 1024;
const UNITS = [1, KIB, KIB ** 2, KIB ** 3, KIB ** 4];

// niceCeil rounds a byte count up to 1, 2 or 5 of its binary unit, so the axis
// reads "10 GB" rather than "8.37 GB". The top gridline has to be a number a
// person would say out loud, or it stops working as a reference.
function niceCeil(bytes: number): number {
  if (bytes <= 0) return KIB;
  const unit = UNITS.filter((u) => bytes >= u).pop() ?? 1;
  const n = bytes / unit;
  const pow = 10 ** Math.floor(Math.log10(n));
  const f = n / pow;
  const step = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return step * pow * unit;
}

function shortDay(day: string): string {
  return day.slice(5);
}

interface Props {
  days: DailyTotal[];
}

/**
 * 每天一根堆叠柱：底部入向、上面出向。
 *
 * 堆叠而不是并排，是因为服务商按两个方向相加计费，额度也按合计算 —— 柱子的
 * 总高度就是那一天实际吃掉的额度。两段各自是多少放在悬停提示里。
 */
export function TrafficTrend({ days }: Props) {
  const [hover, setHover] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);

  const counted = days.filter((d) => d.counted);
  const peak = Math.max(0, ...counted.map((d) => d.bytes_in + d.bytes_out));
  const top = niceCeil(peak);
  const windowIn = counted.reduce((sum, d) => sum + d.bytes_in, 0);
  const windowOut = counted.reduce((sum, d) => sum + d.bytes_out, 0);
  const today = days.length - 1;

  // 14 个日期挤在一行会互相压住。隔一个标一个，且永远标出今天。
  const labelEvery = days.length > 10 ? 2 : 1;

  return (
    <>
      <div className="spread">
        <div>
          <h2 style={{ margin: 0 }}>近 {days.length} 天流量</h2>
          <div className="muted" style={{ fontSize: 12, marginTop: 2 }}>
            {counted.length === 0
              ? "这段时间还没有任何计数"
              : `合计 ${formatBytes(windowIn + windowOut)}（入 ${formatBytes(windowIn)} · 出 ${formatBytes(windowOut)}）`}
          </div>
        </div>
        <div className="row" style={{ gap: 12 }}>
          <div className="legend" aria-hidden>
            <span className="legend-item">
              <span className="legend-swatch" style={{ background: "var(--series-1)" }} />
              入向
            </span>
            <span className="legend-item">
              <span className="legend-swatch" style={{ background: "var(--series-2)" }} />
              出向
            </span>
          </div>
          <button className="btn sm" onClick={() => setShowTable((v) => !v)}>
            {showTable ? "看图" : "看数字"}
          </button>
        </div>
      </div>

      {showTable ? (
        <table className="chart-table">
          <thead>
            <tr>
              <th>日期</th>
              <th>入向</th>
              <th>出向</th>
              <th>合计</th>
            </tr>
          </thead>
          <tbody>
            {[...days].reverse().map((d) => (
              <tr key={d.day}>
                <td>{d.day}</td>
                {d.counted ? (
                  <>
                    <td>{formatBytes(d.bytes_in)}</td>
                    <td>{formatBytes(d.bytes_out)}</td>
                    <td>{formatBytes(d.bytes_in + d.bytes_out)}</td>
                  </>
                ) : (
                  <td colSpan={3} className="muted">
                    没有计数
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <div className="trend" role="img" aria-label={`近 ${days.length} 天每日流量，入向与出向堆叠`}>
          <div className="trend-axis">
            <span style={{ bottom: "100%" }}>{formatBytes(top)}</span>
            <span style={{ bottom: "50%" }}>{formatBytes(top / 2)}</span>
            <span style={{ bottom: "0%" }}>0</span>
          </div>

          <div className="trend-plot">
            <div className="trend-grid" style={{ top: 0 }} />
            <div className="trend-grid" style={{ top: "50%" }} />
            <div className="trend-grid" style={{ bottom: 0 }} />

            <div className="trend-bars">
              {days.map((d, i) => {
                const total = d.bytes_in + d.bytes_out;
                return (
                  <div
                    key={d.day}
                    className="trend-col"
                    onMouseEnter={() => setHover(i)}
                    onMouseLeave={() => setHover(null)}
                  >
                    {d.counted ? (
                      <div className="trend-stack" style={{ height: `${(total / top) * 100}%` }}>
                        {/* 某一段为 0 就不画。最小高度会让它仍露出一条 2px 的色带，
                            等于声称那个方向有流量。 */}
                        {d.bytes_in > 0 && <div className="trend-seg in" style={{ flex: d.bytes_in }} />}
                        {d.bytes_out > 0 && <div className="trend-seg out" style={{ flex: d.bytes_out }} />}
                      </div>
                    ) : (
                      <div className="trend-missing" />
                    )}

                    {hover === i && (
                      <div className={`chart-tip${i >= days.length / 2 ? " flip" : ""}`}>
                        <div style={{ fontWeight: 600, marginBottom: 4 }}>
                          {d.day}
                          {i === today && <span className="muted">（今天，仍在累计）</span>}
                        </div>
                        {d.counted ? (
                          <>
                            <div className="chart-tip-row">
                              <span className="legend-item">
                                <span className="legend-swatch" style={{ background: "var(--series-1)" }} />
                                入向
                              </span>
                              <span>{formatBytes(d.bytes_in)}</span>
                            </div>
                            <div className="chart-tip-row">
                              <span className="legend-item">
                                <span className="legend-swatch" style={{ background: "var(--series-2)" }} />
                                出向
                              </span>
                              <span>{formatBytes(d.bytes_out)}</span>
                            </div>
                            <div className="chart-tip-row" style={{ marginTop: 4 }}>
                              <span className="muted">合计</span>
                              <strong>{formatBytes(total)}</strong>
                            </div>
                          </>
                        ) : (
                          <div className="muted">
                            这天没有任何链路记到数。
                            <br />
                            不等于流量为 0。
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </div>

          <div className="trend-labels">
            {days.map((d, i) => (
              <span key={d.day}>
                {i === today ? "今天" : (today - i) % labelEvery === 0 ? shortDay(d.day) : ""}
              </span>
            ))}
          </div>
        </div>
      )}
    </>
  );
}
