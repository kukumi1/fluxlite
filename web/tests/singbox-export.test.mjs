import test from "node:test";
import assert from "node:assert/strict";
import { normalizeSingBoxExport } from "../src/lib/singbox-export.ts";

const config = { outbounds: [{ type: "anytls", server: "example.com", server_port: 443, password: "test-only" }] };
const valid = { link: "anytls://test-only@example.com:443", config };

test("normalizes address families without advertising invalid or duplicate choices", () => {
  const result = normalizeSingBoxExport({ ...valid, address_family: "ipv6", available_families: ["ipv6", "invalid", null, "ipv4", "ipv6"] });
  assert.equal(result.addressFamily, "ipv6");
  assert.deepEqual(result.availableFamilies, ["ipv4", "ipv6"]);
  assert.deepEqual(normalizeSingBoxExport({ ...valid, address_family: "ipv6", available_families: [] }).availableFamilies, ["ipv6"]);
});

test("legacy responses expose only the exported address family", () => {
  for (const server of ["example.com", "192.0.2.1", "2001:db8::1", "[2001:db8::1]"]) {
    const result = normalizeSingBoxExport({ ...valid, config: { outbounds: [{ ...config.outbounds[0], server }] } });
    const family = server.includes(":") ? "ipv6" : "ipv4";
    assert.equal(result.addressFamily, family);
    assert.deepEqual(result.availableFamilies, [family]);
  }
  const result = normalizeSingBoxExport({ ...valid, address_family: "invalid", available_families: "ipv6" });
  assert.equal(result.addressFamily, "ipv4");
  assert.deepEqual(result.availableFamilies, ["ipv4"]);
});

test("rejects missing or blank links before showing export controls", () => {
  for (const link of [undefined, null, 42, "", "   "]) {
    assert.throws(() => normalizeSingBoxExport({ ...valid, link }), /未生成有效的节点链接/);
  }
});

test("validates supported share-link formats", () => {
  for (const scheme of ["ss", "anytls", "vless", "hysteria2", "hy2", "tuic", "trojan", "vmess"]) {
    assert.equal(normalizeSingBoxExport({ ...valid, link: `${scheme}://payload` }).link, `${scheme}://payload`);
  }
  for (const link of ["https://example.com", "anytls://", "anytls://bad link"]) {
    assert.throws(() => normalizeSingBoxExport({ ...valid, link }), /节点链接格式无效/);
  }
});

test("refuses missing client outbounds and server inbound configuration", () => {
  for (const candidate of [undefined, null, "{}", [], {}, { outbounds: [] }, { outbounds: [null] }, { inbounds: [{ type: "anytls" }], outbounds: config.outbounds }]) {
    assert.throws(() => normalizeSingBoxExport({ ...valid, config: candidate }), /未生成有效的客户端配置/);
  }
});

test("serializes client JSON and trims link without mutating the response", () => {
  const result = normalizeSingBoxExport({ ...valid, link: ` ${valid.link} ` });
  assert.equal(result.link, valid.link);
  assert.deepEqual(JSON.parse(result.config), config);
  assert.deepEqual(result.warnings, []);
  assert.equal(valid.link, "anytls://test-only@example.com:443");
});

test("missing or malformed QR retains usable links and explicit feedback", () => {
  for (const qr_data_url of [undefined, "", "https://external.example/qr.png", "data:image/png;base64,invalid!"]) {
    const result = normalizeSingBoxExport({ ...valid, qr_data_url });
    assert.equal(result.qr, undefined);
    assert.match(result.qrError, /仍可复制节点链接或客户端配置/);
    assert.equal(result.link, valid.link);
  }
  assert.match(normalizeSingBoxExport({ ...valid, qr_error: "二维码超出容量" }).qrError, /二维码超出容量.*仍可复制节点链接或客户端配置/);
});

test("accepts PNG QR and preserves text warnings without accepting nontext data", () => {
  const result = normalizeSingBoxExport({ ...valid, qr_data_url: "data:image/png;base64,dGVzdA==", qr_error: "stale", warnings: ["证书验证已跳过", null, 42, " "] });
  assert.equal(result.qr, "data:image/png;base64,dGVzdA==");
  assert.equal(result.qrError, undefined);
  assert.deepEqual(result.warnings, ["证书验证已跳过"]);
});
