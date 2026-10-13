import test from "node:test";
import assert from "node:assert/strict";
import { normalizeAppearance, normalizeGlassStrength, glassPresentation, DEFAULT_APPEARANCE, parseAppearance, readPreference, writePreference } from "../src/lib/appearance.ts";

test("appearance defaults to subtle depth, no glass and strength 45", () => {
  for (const value of [undefined, null, false, 42, "bad", [], {}]) {
    assert.deepEqual(normalizeAppearance(value), DEFAULT_APPEARANCE);
  }
});

test("flags only accept booleans and ignore unexpected properties", () => {
  assert.deepEqual(normalizeAppearance({ depth: "false", glass: 1, glassStrength: "99" }), DEFAULT_APPEARANCE);
  assert.deepEqual(normalizeAppearance({ depth: false, glass: true, glassStrength: 77, extra: true }), { depth: false, glass: true, glassStrength: 77 });
});

test("strength clamps and rounds finite numbers without coercing invalid values", () => {
  for (const value of [NaN, Infinity, -Infinity, null, undefined, "", "50"]) assert.equal(normalizeGlassStrength(value), 45);
  assert.equal(normalizeGlassStrength(-50), 1);
  assert.equal(normalizeGlassStrength(0), 1);
  assert.equal(normalizeGlassStrength(101), 100);
  assert.equal(normalizeGlassStrength(55.7), 56);
});

test("blur is bounded at 1–24px and backgrounds remain at least 74% opaque", () => {
  assert.deepEqual(glassPresentation(1), { blur: 1, opacity: 94 });
  assert.deepEqual(glassPresentation(100), { blur: 24, opacity: 74 });
  for (const value of [-999, 0, 45, 100, 999, NaN]) {
    const { blur, opacity } = glassPresentation(value);
    assert.ok(blur >= 1 && blur <= 24);
    assert.ok(opacity >= 74 && opacity <= 94);
  }
});

test("serialized saved preferences round trip and normalization does not mutate input", () => {
  const input = Object.freeze({ depth: false, glass: true, glassStrength: 120 });
  const normalized = normalizeAppearance(input);
  assert.equal(input.glassStrength, 120);
  assert.deepEqual(normalizeAppearance(JSON.parse(JSON.stringify(normalized))), normalized);
});

test("corrupted JSON safely restores appearance defaults", () => {
  for (const value of [null, "", "{invalid", "null", "[1,2]", "false"]) assert.deepEqual(parseAppearance(value), DEFAULT_APPEARANCE);
});

test("denied localStorage read and write do not throw", () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new Error("Access denied"); } });
  try {
    assert.equal(readPreference("fluxlite-appearance"), null);
    assert.equal(writePreference("fluxlite-appearance", "{}"), false);
    assert.deepEqual(parseAppearance(readPreference("fluxlite-appearance")), DEFAULT_APPEARANCE);
  } finally {
    if (descriptor) Object.defineProperty(globalThis, "localStorage", descriptor);
    else delete globalThis.localStorage;
  }
});
