export interface Appearance {
  depth: boolean;
  glass: boolean;
  glassStrength: number;
}

export const DEFAULT_APPEARANCE: Readonly<Appearance> = {
  depth: true,
  glass: false,
  glassStrength: 45,
};

export function normalizeGlassStrength(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.min(100, Math.max(1, Math.round(value)))
    : DEFAULT_APPEARANCE.glassStrength;
}

export function normalizeAppearance(value: unknown): Appearance {
  const candidate = typeof value === "object" && value !== null ? value : {};
  return {
    depth: "depth" in candidate && typeof candidate.depth === "boolean" ? candidate.depth : DEFAULT_APPEARANCE.depth,
    glass: "glass" in candidate && typeof candidate.glass === "boolean" ? candidate.glass : DEFAULT_APPEARANCE.glass,
    glassStrength: normalizeGlassStrength("glassStrength" in candidate ? candidate.glassStrength : undefined),
  };
}

/** Keep blur bounded even when storage or external callers contain invalid values. */
export function glassPresentation(strength: unknown) {
  const ratio = (normalizeGlassStrength(strength) - 1) / 99;
  return { blur: 1 + 23 * ratio, opacity: 94 - 20 * ratio };
}

/** Storage may throw in privacy-restricted browsers; do not prevent rendering. */
export function readPreference(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writePreference(key: string, value: string): boolean {
  try {
    localStorage.setItem(key, value);
    return true;
  } catch {
    return false;
  }
}

export function parseAppearance(saved: string | null): Appearance {
  try {
    return normalizeAppearance(JSON.parse(saved ?? "null"));
  } catch {
    return normalizeAppearance(null);
  }
}
