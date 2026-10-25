import test from "node:test";
import assert from "node:assert/strict";
import { readPreference, writePreference } from "../src/lib/preferences.ts";

test("denied localStorage read and write do not throw", () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new Error("Access denied"); } });
  try {
    assert.equal(readPreference("fluxlite-theme"), null);
    assert.equal(writePreference("fluxlite-theme", "dark"), false);
  } finally {
    if (descriptor) Object.defineProperty(globalThis, "localStorage", descriptor);
    else delete globalThis.localStorage;
  }
});
