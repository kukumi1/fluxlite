import test from "node:test";
import assert from "node:assert/strict";
import { describeAction } from "../src/lib/audit-actions.ts";

test("groups actions by their prefix and names them in Chinese", () => {
  assert.deepEqual(describeAction("auth.login"), { category: "account", label: "登录", failed: false });
  assert.deepEqual(describeAction("nodes.reorder"), { category: "machine", label: "调整机器顺序", failed: false });
  assert.equal(describeAction("enroll.complete").category, "machine");
  assert.equal(describeAction("singbox.top_up").category, "node");
});

test("flags failed and incomplete actions", () => {
  assert.equal(describeAction("auth.login_failed").failed, true);
  assert.equal(describeAction("console.unlock_failed").failed, true);
  assert.equal(describeAction("subscription.delete_incomplete").failed, true);
  assert.equal(describeAction("route.apply").failed, false);
});

test("shows an unknown action as its raw code", () => {
  assert.deepEqual(describeAction("backup.export"), { category: "other", label: "backup.export", failed: false });
});
