import test from "node:test";
import assert from "node:assert/strict";
import { api, setTelegramToken } from "../src/api.ts";
import { telegramTab, safeBindingURL, singleUseExchange, telegramDraftNonce, telegramMachineDraftInput } from "../src/lib/telegram.ts";

test("Mini App only accepts the four operational tabs", () => {
  for (const value of ["dashboard", "routes", "nodes", "singbox"]) assert.equal(telegramTab(value), value);
  for (const value of ["console", "account", "audit", "https://example.com", undefined]) assert.equal(telegramTab(value), "dashboard");
});

test("binding link only navigates to HTTPS Telegram", () => {
  assert.equal(safeBindingURL("https://t.me/example_bot?start=one-time"), "https://t.me/example_bot?start=one-time");
  for (const value of ["javascript:alert(1)", "https://t.me.evil.example/bot", "http://t.me/bot", "https://user:password@t.me/bot", "invalid"]) assert.equal(safeBindingURL(value), null);
});

test("Mini App has no cookie fallback and keeps authorization only in memory", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const calls = [];
  globalThis.window = { location: { pathname: "/telegram" } };
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return new Response(JSON.stringify({ token: "test-session", expires_at: "2030-01-01T00:00:00Z" }), { status: 200 });
  };
  try {
    setTelegramToken("");
    await assert.rejects(api.listNodes(), /重新打开/);
    assert.equal(calls.length, 0);
    await api.telegramAuth("raw-signed-data");
    assert.equal(calls[0].init.credentials, "omit");
    assert.equal(calls[0].init.headers.has("Authorization"), false);
    assert.deepEqual(JSON.parse(calls[0].init.body), { init_data: "raw-signed-data" });
    setTelegramToken("test-session");
    await api.listNodes();
    assert.equal(calls[1].init.credentials, "omit");
    assert.equal(calls[1].init.headers.get("Authorization"), "Bearer test-session");
    globalThis.window.location.pathname = "/";
    await api.me();
    assert.equal(calls[2].init.credentials, "same-origin");
    assert.equal(calls[2].init.headers.has("Authorization"), false);
    setTelegramToken("");
    globalThis.window.location.pathname = "/telegram/";
    await assert.rejects(api.listRoutes(), /重新打开/);
  } finally {
    setTelegramToken("");
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});

test("StrictMode shares single-use authentication and does not retry a spent payload", async () => {
  let calls = 0;
  const exchange = singleUseExchange(async (data) => { calls++; return data; });
  const first = exchange("signed-data");
  const replay = exchange("signed-data");
  assert.equal(first, replay);
  assert.equal(await replay, "signed-data");
  assert.equal(calls, 1);
  assert.equal(await exchange("fresh-signed-data"), "fresh-signed-data");
  assert.equal(calls, 2);
  let failures = 0;
  const rejected = singleUseExchange(async () => { failures++; throw new Error("expired"); });
  await assert.rejects(rejected("spent"), /expired/);
  await assert.rejects(rejected("spent"), /expired/);
  assert.equal(failures, 1);
});


test("machine draft links accept only bounded opaque nonces", () => {
  const nonce = "abcdef0123456789_-";
  assert.equal(telegramDraftNonce(nonce), nonce);
  for (const value of [null, "", "short", "a".repeat(257), "https://example.com/secret", "a".repeat(16) + "&next=evil", "a".repeat(16) + "\n"]) {
    assert.equal(telegramDraftNonce(value), null);
  }
});

test("machine drafts expire and can never populate SSH secrets", () => {
  const input = { name: "Bot machine", host: "192.0.2.10", ssh_port: 22, ssh_user: "root", auth_type: "password", secret: "server-must-not-echo", via_node_id: null, port_start: 10000, port_end: 20000, skip_udp_probe: false, ipv6_entry: "" };
  const result = telegramMachineDraftInput({ input, expires_at: "2030-01-01T00:00:00Z" }, 0);
  assert.deepEqual(result, { ...input, secret: "" });
  assert.equal(input.secret, "server-must-not-echo");
  const { secret, ...withoutSecret } = input;
  assert.deepEqual(telegramMachineDraftInput({ input: withoutSecret, expires_at: "2030-01-01T00:00:00Z" }, 0), result);
  for (const expires_at of ["invalid", "1970-01-01T00:00:00Z"]) assert.throws(() => telegramMachineDraftInput({ input, expires_at }, 1), /请回 Bot 重新添加机器/);
  for (const bad of [{ ssh_port: 0 }, { port_start: 30000 }, { auth_type: "unknown" }, { via_node_id: -1 }, { name: null }]) {
    assert.throws(() => telegramMachineDraftInput({ input: { ...input, ...bad }, expires_at: "2030-01-01T00:00:00Z" }, 0), /请回 Bot 重新添加机器/);
  }
});

test("machine draft retrieval requires the Mini App bearer and omits cookies", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const calls = [];
  globalThis.window = { location: { pathname: "/telegram" } };
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return new Response(JSON.stringify({ input: {}, expires_at: "2030-01-01T00:00:00Z" }), { status: 200 });
  };
  try {
    setTelegramToken("");
    await assert.rejects(api.telegramMachineDraft("opaque-draft-1234"), /重新打开/);
    assert.equal(calls.length, 0);
    setTelegramToken("draft-session");
    await api.telegramMachineDraft("opaque-draft-1234");
    assert.equal(calls[0].url, "/api/telegram/machine-draft?draft=opaque-draft-1234");
    assert.equal(calls[0].init.credentials, "omit");
    assert.equal(calls[0].init.headers.get("Authorization"), "Bearer draft-session");
  } finally {
    setTelegramToken("");
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});
