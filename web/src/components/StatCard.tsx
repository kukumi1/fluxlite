import type { ReactNode } from "react";
import { Card } from "./Card";

interface Props {
  label: string;
  /** null 表示还没有可信读数。会显示为「—」，绝不显示成 0。 */
  value: string | null;
  sub?: ReactNode;
  icon: ReactNode;
  tone?: "default" | "warm" | "cool";
  index?: number;
}

export function StatCard({ label, value, sub, icon, tone = "default", index }: Props) {
  return (
    <Card index={index}>
      <div className="stat-head">
        <div style={{ minWidth: 0 }}>
          <div className="stat-label">{label}</div>
          <div className="stat-value">{value ?? "—"}</div>
          {sub && <div className="stat-sub">{sub}</div>}
        </div>
        <div className={`stat-icon${tone === "default" ? "" : ` ${tone}`}`}>{icon}</div>
      </div>
    </Card>
  );
}
