import { useEffect, useState } from "react";
import { api, ApiError, type AuditEntry } from "../api";
import { Banner } from "../components/Modal";

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .audit(200)
      .then((e) => setEntries(e ?? []))
      .catch((err) => setError(err instanceof ApiError ? err.message : "请求失败"));
  }, []);

  return (
    <div>
      <h1>审计日志</h1>
      <p className="page-desc">登录、节点变更、链路下发等操作的完整记录。</p>

      {error && <Banner kind="err">{error}</Banner>}

      <div className="card" style={{ padding: 0 }}>
        {entries.length === 0 ? (
          <div className="empty">暂无记录。</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>时间</th>
                <th>操作者</th>
                <th>动作</th>
                <th>对象</th>
                <th>详情</th>
                <th>来源 IP</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id}>
                  <td className="mono nowrap">{new Date(e.ts).toLocaleString("zh-CN")}</td>
                  <td>{e.actor}</td>
                  <td>
                    <span className={`tag ${e.action.includes("failed") ? "err" : ""}`}>
                      {e.action}
                    </span>
                  </td>
                  <td className="mono">{e.target}</td>
                  <td className="muted">{e.detail}</td>
                  <td className="mono muted">{e.ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
