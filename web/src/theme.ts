import { createContext, useContext, useEffect, useState } from "react";
import { glassPresentation, normalizeAppearance, parseAppearance, readPreference, writePreference } from "./lib/appearance";
import type { Appearance } from "./lib/appearance";

export type Theme = "light" | "dark";
const STORAGE_KEY = "fluxlite-theme";
const APPEARANCE_KEY = "fluxlite-appearance";

function storedTheme(): Theme | null {
  const saved = readPreference(STORAGE_KEY);
  return saved === "light" || saved === "dark" ? saved : null;
}

function storedAppearance(): Appearance {
  return parseAppearance(readPreference(APPEARANCE_KEY));
}

function preferred(): Theme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function paintTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
}

function paintAppearance(appearance: Appearance) {
  const root = document.documentElement;
  const presentation = glassPresentation(appearance.glassStrength);
  root.classList.toggle("appearance-depth", appearance.depth);
  root.classList.toggle("appearance-glass", appearance.glass);
  root.style.setProperty("--glass-blur", `${presentation.blur.toFixed(2)}px`);
  root.style.setProperty("--glass-opacity", `${presentation.opacity.toFixed(2)}%`);
}

/** Paint before React mounts so saved appearance never flashes the default theme. */
export function applyStoredTheme() {
  paintTheme(storedTheme() ?? preferred());
  paintAppearance(storedAppearance());
}

export interface ThemeState {
  theme: Theme;
  toggle: () => void;
  appearance: Appearance;
  updateAppearance: (changes: Partial<Appearance>) => void;
  preferencesSaved: boolean;
}

// One provider owns appearance for all pages; switching tabs never resets it.
const ThemeContext = createContext<ThemeState | null>(null);

export function useThemeState(): ThemeState {
  const [theme, setTheme] = useState<Theme>(() => storedTheme() ?? preferred());
  const [appearance, setAppearance] = useState<Appearance>(storedAppearance);
  const [preferencesSaved, setPreferencesSaved] = useState(true);

  useEffect(() => {
    paintTheme(theme);
    paintAppearance(appearance);
    const themeSaved = writePreference(STORAGE_KEY, theme);
    const appearanceSaved = writePreference(APPEARANCE_KEY, JSON.stringify(appearance));
    setPreferencesSaved(themeSaved && appearanceSaved);
  }, [theme, appearance]);

  return {
    theme,
    toggle: () => setTheme((current) => current === "dark" ? "light" : "dark"),
    appearance,
    updateAppearance: (changes) => setAppearance((current) => normalizeAppearance({ ...current, ...changes })),
    preferencesSaved,
  };
}

export const ThemeProvider = ThemeContext.Provider;

export function useTheme(): ThemeState {
  const state = useContext(ThemeContext);
  if (!state) throw new Error("useTheme 必须在 ThemeProvider 内使用");
  return state;
}
