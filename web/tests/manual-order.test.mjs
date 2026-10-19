import test from "node:test";
import assert from "node:assert/strict";
import { reorderVisible } from "../src/lib/manual-order.ts";
import { api } from "../src/api.ts";

test("full-list moves work in both directions without mutating input", () => {
  const ids = [1, 2, 3, 4];
  assert.deepEqual(reorderVisible(ids, ids, 1, 3), [2, 3, 1, 4]);
  assert.deepEqual(reorderVisible(ids, ids, 4, 2), [1, 4, 2, 3]);
  assert.deepEqual(ids, [1, 2, 3, 4]);
  assert.equal(reorderVisible(ids, ids, 2, 2), ids);
});
test("search and family filters retain all hidden IDs in their original slots", () => {
  assert.deepEqual(reorderVisible([1, 2, 3, 4, 5, 6], [1, 3, 5], 5, 1), [5, 2, 1, 4, 3, 6]);
  assert.deepEqual(reorderVisible([1, 2, 3, 4, 5], [2, 4], 2, 4), [1, 4, 3, 2, 5]);
});
test("stale, duplicate and missing IDs fail before persistence", () => {
  for (const args of [ [[1,2], [1,3], 1,3], [[1,2], [1,1],1,1], [[1,1],[1],1,1], [[1,2],[1],1,2] ]) {
    assert.throws(() => reorderVisible(...args), /列表已变化/);
  }
});
test("order APIs PUT the complete ID array and surface rejected saves", async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({url, init});
    return new Response(JSON.stringify({ ok: true }), {status: 200});
  };
  try {
    await api.reorderNodes([3,1,2]);
    await api.reorderRoutes([2,5,4]);
    assert.deepEqual(calls.map(call => call.url), ["/api/nodes/order", "/api/routes/order"]);
    assert.ok(calls.every(call => call.init.method === "PUT"));
    assert.deepEqual(JSON.parse(calls[0].init.body), {ids: [3,1,2]});
    assert.deepEqual(JSON.parse(calls[1].init.body), {ids: [2,5,4]});
    globalThis.fetch = async () => new Response(JSON.stringify({error: "列表已变化"}), {status: 400});
    await assert.rejects(api.reorderRoutes([2,5,4]), /列表已变化/);
  } finally { globalThis.fetch = originalFetch; }
});
