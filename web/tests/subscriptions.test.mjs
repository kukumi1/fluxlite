import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { api } from "../src/api.ts";
import { subscriptionFormats, subscriptionInput, subscriptionMemberDetails, subscriptionName, subscriptionURL } from "../src/lib/subscriptions.ts";
import { telegramTab } from "../src/lib/telegram.ts";

const candidates = [
  { user_id: 1, node_id: 4, name: "A", node_name: "server", protocol: "trojan", port: 24443, subscription_id: null, subscription_name: "" },
  { user_id: 2, node_id: 4, name: "B", node_name: "server", protocol: "trojan", port: 24444, subscription_id: 7, subscription_name: "朋友" },
];

test("member details resolve a single node by user ID without adding unrelated candidates", () => {
  assert.deepEqual(subscriptionMemberDetails([{ user_id: 2, node_id: 4, protocol: "trojan" }], candidates), [
    { user_id: 2, name: "B", machine: "server", protocol: "trojan", port: 24444 },
  ]);
});

test("member details preserve membership order and authoritative protocol and port", () => {
  const members = [{ user_id: 2, node_id: 4, protocol: "vmess", port: 12345 }, { user_id: 1, node_id: 4, protocol: "trojan" }];
  const details = subscriptionMemberDetails(members, candidates);
  assert.deepEqual(details.map(({ user_id, name, protocol, port }) => ({ user_id, name, protocol, port })), [
    { user_id: 2, name: "B", protocol: "vmess", port: 12345 },
    { user_id: 1, name: "A", protocol: "trojan", port: 24443 },
  ]);
  assert.equal(members[1].port, undefined);
});

test("missing candidate metadata retains member with node ID and no invented machine or port", () => {
  assert.deepEqual(subscriptionMemberDetails([{ user_id: 99, node_id: 4, protocol: "tuic" }], candidates), [
    { user_id: 99, name: "节点 ID 99", machine: "—", protocol: "tuic", port: null },
  ]);
});

test("empty or absent membership does not infer membership from candidates", () => {
  for (const members of [[], null, undefined]) assert.deepEqual(subscriptionMemberDetails(members, candidates), []);
});

test("member dialog is read-only, accessible, scrollable, and uses shared dismiss controls", () => {
  const source = readFileSync(new URL("../src/pages/Subscriptions.tsx", import.meta.url), "utf8");
  const dialog = source.slice(source.indexOf("function SubscriptionMembers("), source.indexOf("function SubscriptionNameEditor("));
  const modal = readFileSync(new URL("../src/components/Modal.tsx", import.meta.url), "utf8");
  assert.match(source, /aria-label=\{`查看「\$\{subscription.name\}」/);
  assert.match(source, /onClick=\{\(\) => setViewingMembers\(subscription\)\}/);
  assert.match(source, /onClose=\{\(\) => setViewingMembers\(null\)\}/);
  assert.match(dialog, /<Modal title=\{`订阅节点 — \$\{subscription.name\}`\} onClose=\{onClose\}/);
  assert.match(dialog, /此订阅暂无节点/);
  assert.match(dialog, /className="table-scroll"/);
  for (const heading of ["节点名称", "所属机器", "协议", "端口"]) assert.ok(dialog.includes(heading));
  assert.doesNotMatch(dialog, /api\.|token|onSave|<input|<form/);
  assert.match(modal, /e.key === "Escape"/);
  assert.match(modal, /onClick=\{onClose\}/);
});

test("collection selection accepts only existing unoccupied nodes and trimmed names", () => {
  assert.deepEqual(subscriptionInput("  自用  ", [1], candidates), { name: "自用", user_ids: [1] });
  assert.throws(() => subscriptionInput(" ", [1], candidates));
  assert.throws(() => subscriptionInput("a".repeat(65), [1], candidates));
  assert.equal(subscriptionInput("中".repeat(64), [1], candidates).name.length, 64);
  assert.equal(Array.from(subscriptionInput("😀".repeat(64), [1], candidates).name).length, 64);
  assert.throws(() => subscriptionInput("自用", [], candidates));
  assert.throws(() => subscriptionInput("自用", [1, 1], candidates));
  assert.throws(() => subscriptionInput("自用", [99], candidates));
  assert.throws(() => subscriptionInput("自用", [2], candidates), /朋友/);
  assert.throws(() => subscriptionInput("自用", [NaN], candidates));
  const many = Array.from({ length: 101 }, (_, i) => ({ ...candidates[0], user_id: i + 1 }));
  assert.equal(subscriptionInput("自用", many.slice(0, 100).map(n => n.user_id), many).user_ids.length, 100);
  assert.throws(() => subscriptionInput("自用", many.map(n => n.user_id), many));
  assert.equal(subscriptionFormats.find(([format]) => format === "shadowrocket")[1], "Shadowrocket");
});
test("client URLs use individually encoded bearer token and explicit format", () => {
  assert.equal(subscriptionFormats.length, 5);
  for (const [format] of subscriptionFormats) assert.equal(subscriptionURL("https://panel.example", "a/b?", format), `https://panel.example/sub/a%2Fb%3F?format=${format}`);
  assert.equal(telegramTab("subscriptions"), "dashboard");
});

test("collection APIs send existing user IDs and retain authenticated node exports", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const calls = [];
  globalThis.window = { location: { pathname: "/" } };
  globalThis.fetch = async (url, init) => { calls.push({ url, init }); return new Response("{}", { status: 200 }); };
  try {
    await api.listSubscriptionCandidates();
    await api.resetSubscriptionToken(7);
    await api.previewSubscription(7, "qx");
    await api.createSubscription({ name: "existing", user_ids: [1] });
    await api.exportSingBoxUser(19, "ipv6");
    assert.equal(calls[0].url, "/api/subscriptions/candidates");
    assert.equal(calls[0].init.method ?? "GET", "GET");
    assert.equal(calls[0].init.body, undefined);
    assert.equal(calls[0].init.credentials, "same-origin");
    assert.equal(calls[1].url, "/api/subscriptions/7/reset-token");
    assert.equal(calls[2].url, "/api/subscriptions/7/preview?format=qx");
    assert.deepEqual(JSON.parse(calls[3].init.body), { name: "existing", user_ids: [1] });
    assert.equal(calls[4].url, "/api/singbox/users/19/export?address_family=ipv6");
    assert.equal(calls[4].init.credentials, "same-origin");
  } finally { globalThis.fetch = originalFetch; globalThis.window = originalWindow; }
});

test("subscription page has no persistent bearer-token storage", () => {
  const source = readFileSync(new URL("../src/pages/Subscriptions.tsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /localStorage|sessionStorage|writePreference|document\.cookie|indexedDB/);
  assert.match(source, /不轮换节点凭据/);
  assert.match(source, /不删除机器/);
});

test("viewing subscription links always retrieves the saved token with authenticated GET, never reset", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const calls = [];
  globalThis.window = { location: { pathname: "/" } };
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return new Response(JSON.stringify({ token: "saved-token" }), { status: 200 });
  };
  try {
    for (let index = 0; index < 2; index++) assert.deepEqual(await api.getSubscriptionLink(7), { token: "saved-token" });
    assert.equal(calls.length, 2);
    for (const { url, init } of calls) {
      assert.equal(url, "/api/subscriptions/7/link");
      assert.equal(init.method, "GET");
      assert.equal(init.credentials, "same-origin");
      assert.equal(init.cache, "no-store");
      assert.equal(init.body, undefined);
    }
  } finally { globalThis.fetch = originalFetch; globalThis.window = originalWindow; }
});

test("legacy subscription link failure preserves the server warning and never resets automatically", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const calls = [];
  const warning = "旧订阅需手动重置一次以启用查看；现有地址仍有效";
  globalThis.window = { location: { pathname: "/" } };
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return new Response(JSON.stringify({ error: warning, code: "subscription_link_unavailable" }), { status: 409 });
  };
  try {
    await assert.rejects(api.getSubscriptionLink(7), (error) => error.status === 409 && error.message === warning);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].init.method, "GET");
  } finally { globalThis.fetch = originalFetch; globalThis.window = originalWindow; }
});

test("view and reset have separate controls and failed views cannot leave a stale token modal", () => {
  const source = readFileSync(new URL("../src/pages/Subscriptions.tsx", import.meta.url), "utf8");
  const view = source.slice(source.indexOf("async function viewLink("), source.indexOf("async function resetToken("));
  assert.match(view, /setIssued\(null\)[\s\S]*await api\.getSubscriptionLink/);
  assert.doesNotMatch(view, /resetSubscriptionToken|resetToken\(/);
  assert.match(view, /catch \(cause\)[\s\S]*setError\(message\(cause\)\)/);
  assert.match(source, /viewLink\(subscription\)\}>查看订阅链接/);
  assert.match(source, /resetToken\(subscription\)\}>重置订阅链接/);
  assert.match(source, /subscription\.link_available === false/);
  assert.match(source, /旧订阅需手动重置一次以启用查看；现有地址仍有效/);
  assert.match(source, /链接含访问凭据，请勿公开/);
  assert.match(source, /各客户端仅导出兼容节点/);
  assert.doesNotMatch(source, /仅本次显示|找回时必须重置|重置并获取链接/);
});


test("subscription UI exposes member details and four collection actions with occupied-node selection", () => {
  const source = readFileSync(new URL("../src/pages/Subscriptions.tsx", import.meta.url), "utf8");
  const cards = source.slice(source.indexOf("{subscriptions.map"), source.indexOf("{editor &&"));
  assert.equal((cards.match(/<button /g) ?? []).length, 5);
  assert.doesNotMatch(source, /quota_bytes|expires_at|subscriptionPort|subscriptionSNI|exportSingBoxUser|previewSubscription/);
  assert.match(source, /useState<number\[\]>\(\[\]\)/);
  assert.match(source, /checked=\{occupied \|\| checked\}/);
  assert.match(source, /disabled=\{occupied/);
  assert.match(source, /已属于/);
  assert.match(source, /cause\.status === 409[\s\S]*await load\(\)/);
  assert.match(source, /旧独立额度订阅需要先迁移/);
  assert.match(source, /subscription\.mode !== "collection"/);
  assert.match(source, /节点、服务和密钥均保留/);
});

test("subscription rename validates trimmed Unicode names independently of membership", () => {
  assert.equal(subscriptionName("  新名称  "), "新名称");
  assert.throws(() => subscriptionName(" \n\t "), /请输入订阅名称/);
  assert.equal(subscriptionName("a"), "a");
  assert.equal(subscriptionName("中".repeat(64)), "中".repeat(64));
  assert.equal(subscriptionName("😀".repeat(64)), "😀".repeat(64));
  assert.throws(() => subscriptionName("😀".repeat(65)), /64/);
});

test("subscription rename PATCH sends only name and preserves server errors", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const calls = [];
  globalThis.window = { location: { pathname: "/" } };
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return new Response(JSON.stringify(calls.length === 1 ? { id: 7, name: "新名称" } : { error: "更新失败" }), { status: calls.length === 1 ? 200 : 400 });
  };
  try {
    assert.deepEqual(await api.updateSubscription(7, { name: "新名称", user_ids: [99], enabled: false }), { id: 7, name: "新名称" });
    await assert.rejects(api.updateSubscription(7, { name: "重试" }), /更新失败/);
    assert.equal(calls.length, 2);
    for (const { url, init } of calls) {
      assert.equal(url, "/api/subscriptions/7");
      assert.equal(init.method, "PATCH");
      assert.equal(init.credentials, "same-origin");
      assert.deepEqual(Object.keys(JSON.parse(init.body)), ["name"]);
    }
  } finally { globalThis.fetch = originalFetch; globalThis.window = originalWindow; }
});

test("rename modal is prefilled, blocks duplicate saves, retains failures, and updates names only", () => {
  const source = readFileSync(new URL("../src/pages/Subscriptions.tsx", import.meta.url), "utf8");
  const rename = source.slice(source.indexOf("async function rename("), source.indexOf("  return <>"));
  const editor = source.slice(source.indexOf("function SubscriptionNameEditor("), source.indexOf("function SubscriptionEditor("));
  assert.match(source, /setRenaming\(subscription\)\}>修改名称/);
  assert.match(source, /name=\{renaming\.name\}/);
  assert.match(editor, /useState\(initialName\)/);
  assert.match(editor, /if \(pending\.current\) return/);
  assert.match(editor, /fieldset disabled=\{busy\}/);
  assert.match(editor, /onSave\(subscriptionName\(name\)\)/);
  assert.match(editor, /catch \(cause\) \{ setError\(message\(cause\)\); \}/);
  assert.match(editor, /if \(!pending\.current\) onClose\(\)/);
  assert.match(rename, /await api\.updateSubscription\(renaming\.id, \{ name \}\)/);
  assert.match(rename, /setSubscriptions/);
  assert.match(rename, /setIssued/);
  assert.match(rename, /await load\(\)/);
  assert.doesNotMatch(rename, /resetToken|resetSubscriptionToken|user_ids|members:/);
});

test("subscription checkbox rows have scoped readable touch-target styles", () => {
  const source = readFileSync(new URL("../src/pages/Subscriptions.tsx", import.meta.url), "utf8");
  const css = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
  assert.match(source, /maxLength=\{64\}/);
  assert.match(source, /subscription-node-choice/);
  assert.match(css, /\.subscription-node-choice \{[^}]*display: flex;[^}]*min-height: 44px;/);
  assert.match(css, /\.subscription-node-choice input\[type="checkbox"\] \{[^}]*width: 18px;[^}]*height: 18px;[^}]*margin: 0;/);
  assert.match(css, /\.subscription-node-choice-text \{[^}]*overflow-wrap: anywhere;/);
});
