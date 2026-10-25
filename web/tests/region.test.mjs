import test from "node:test";
import assert from "node:assert/strict";
import { regionOf } from "../src/lib/region.ts";

test("reads the region code from conventional machine names", () => {
  assert.equal(regionOf("DMIT-JP"), "JP");
  assert.equal(regionOf("DATA-TW"), "TW");
  assert.equal(regionOf("Legend-SG-Lite"), "SG");
  assert.equal(regionOf("DMIT-FAC-TW-1区"), "TW");
});

test("prefers a common hosting region over a code that is also a word", () => {
  assert.equal(regionOf("DE-CN2"), "DE");
  assert.equal(regionOf("NO-JP"), "JP");
});

test("understands aliases and Chinese place names", () => {
  assert.equal(regionOf("bwg-uk"), "GB");
  assert.equal(regionOf("Vultr Tokyo"), "JP");
  assert.equal(regionOf("香港-阿里云"), "HK");
});

test("returns null when the name names no place", () => {
  assert.equal(regionOf("relay"), null);
  assert.equal(regionOf(undefined), null);
});
