// formatBytes renders a counter at three significant figures. Byte totals are
// read to answer "am I going to blow the monthly allowance", a question no one
// asks to the nearest kilobyte.
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(v >= 10 ? 1 : 2)} ${units[i]}`;
}

// staleAfterMs is how long a sample stays trustworthy. Sampling runs every 30
// seconds, so anything several rounds old means the node stopped answering and
// the number on screen is frozen, not live.
export const staleAfterMs = 150000;

export function ageOf(at: string | null): number | null {
  if (!at) return null;
  const ms = Date.now() - new Date(at).getTime();
  return Number.isFinite(ms) ? ms : null;
}

export function isStale(at: string | null): boolean {
  const age = ageOf(at);
  return age !== null && age > staleAfterMs;
}

export function describeAge(ms: number): string {
  if (ms < 60000) return `${Math.max(1, Math.round(ms / 1000))} 秒前`;
  if (ms < 3600000) return `${Math.round(ms / 60000)} 分钟前`;
  return `${Math.round(ms / 3600000)} 小时前`;
}
