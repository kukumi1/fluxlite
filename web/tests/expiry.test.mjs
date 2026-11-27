import test from "node:test";
import assert from "node:assert/strict";
import { expiryDate, expiryLeft, expiryOf, expiryTitle } from "../src/lib/expiry.ts";

const now = new Date(2026, 10, 27, 10, 0, 0);

test("reads both ways Komari and the panel write a date", () => {
  const a = expiryOf({ expired_at: "2026-12-02 00:00:00+00:00" }, now);
  const b = expiryOf({ expired_at: "2027-04-22T00:00:00.000Z" }, now);
  assert.ok(a && b);
  assert.equal(a.date.getUTCDate(), 2);
  assert.equal(b.date.getUTCMonth(), 3);
});

test("counts calendar days and colours by how soon", () => {
  const at = (y, m, d) => expiryOf({ expired_at: new Date(y, m, d, 23, 0).toISOString() }, now);
  assert.equal(at(2026, 10, 27).days, 0);
  assert.equal(expiryLeft(at(2026, 10, 27)), "今天到期");
  assert.equal(at(2026, 11, 2).days, 5);
  assert.equal(at(2026, 11, 2).tone, "err");
  assert.equal(at(2026, 11, 20).tone, "warn");
  assert.equal(at(2027, 2, 1).tone, "");
  assert.equal(expiryLeft(at(2026, 10, 24)), "已过期 3 天");
});

test("no date, a broken one, or Komari's zero time is no expiry", () => {
  assert.equal(expiryOf({}, now), null);
  assert.equal(expiryOf({ expired_at: null }, now), null);
  assert.equal(expiryOf({ expired_at: "soon" }, now), null);
  assert.equal(expiryOf({ expired_at: "0001-01-01T00:00:00Z" }, now), null);
});

test("says price, period and renewal", () => {
  const e = expiryOf({ expired_at: new Date(2026, 11, 2, 12).toISOString(), price: 10, currency: "¥", billing_cycle: 30, auto_renewal: 1 }, now);
  assert.equal(expiryTitle(e), "2026-12-02 到期（余 5 天） · ¥10/月 · 自动续费");
  assert.equal(expiryDate(e, now), "12-02");
  const far = expiryOf({ expired_at: new Date(2028, 4, 13, 12).toISOString() }, now);
  assert.equal(expiryDate(far, now), "2028-05-13");
});
