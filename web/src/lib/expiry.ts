/** A machine's paid time, as the probe page holds it, read for display. */
export interface Expiry {
  date: Date;
  /** Whole days left by the calendar; 0 is today, negative is past. */
  days: number;
  autoRenew: boolean;
  /** "err" when past or within a week, "warn" within a month, "" otherwise. */
  tone: "err" | "warn" | "";
  /** Price per period, such as "¥10/月", when one is set. */
  price: string;
}

interface Billing {
  expired_at?: string | null;
  auto_renewal?: boolean | number;
  price?: number;
  currency?: string;
  billing_cycle?: number;
}

const DAY_MS = 24 * 60 * 60 * 1000;

export function cycleLabel(days: number | undefined): string {
  if (!days || days <= 0) return "";
  if (days >= 28 && days <= 31) return "月";
  if (days >= 89 && days <= 93) return "季";
  if (days >= 180 && days <= 186) return "半年";
  if (days >= 365 && days <= 366) return "年";
  if (days >= 730 && days <= 731) return "两年";
  return `${days} 天`;
}

// Komari wrote "2027-09-24 00:00:00+00:00", which not every browser reads;
// with a T it is plain ISO 8601 everywhere.
function parse(value: string): Date | null {
  const d = new Date(value.trim().replace(" ", "T"));
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? null : d;
}

export function expiryOf(display: Billing | undefined, now: Date = new Date()): Expiry | null {
  if (!display?.expired_at) return null;
  const date = parse(display.expired_at);
  if (!date) return null;
  const day = (d: Date) => Date.UTC(d.getFullYear(), d.getMonth(), d.getDate());
  const days = Math.round((day(date) - day(now)) / DAY_MS);
  const tone = days <= 7 ? "err" : days <= 30 ? "warn" : "";
  const cycle = cycleLabel(display.billing_cycle);
  const price = display.price && display.price > 0 ? `${display.currency ?? ""}${display.price}${cycle ? `/${cycle}` : ""}` : "";
  return { date, days, autoRenew: !!display.auto_renewal, tone, price };
}

const pad = (n: number) => String(n).padStart(2, "0");

/** "12-02" this year and next, with the year otherwise. */
export function expiryDate(e: Expiry, now: Date = new Date()): string {
  const md = `${pad(e.date.getMonth() + 1)}-${pad(e.date.getDate())}`;
  return Math.abs(e.days) < 300 && e.date.getFullYear() <= now.getFullYear() + 1 ? md : `${e.date.getFullYear()}-${md}`;
}

/** "余 5 天", "今天到期", "已过期 3 天". */
export function expiryLeft(e: Expiry): string {
  if (e.days > 0) return `余 ${e.days} 天`;
  if (e.days === 0) return "今天到期";
  return `已过期 ${-e.days} 天`;
}

/** The whole story, for a tooltip. */
export function expiryTitle(e: Expiry): string {
  const full = `${e.date.getFullYear()}-${pad(e.date.getMonth() + 1)}-${pad(e.date.getDate())}`;
  return [`${full} 到期（${expiryLeft(e)}）`, e.price, e.autoRenew ? "自动续费" : "手动续费"].filter(Boolean).join(" · ");
}
