import { createContext, useContext, useEffect, useState } from "react";
import { readPreference, writePreference } from "./lib/preferences";

export type Theme = "light" | "dark";
const STORAGE_KEY = "fluxlite-theme";

function storedTheme(): Theme | null {
  const saved = readPreference(STORAGE_KEY);
  return saved === "light" || saved === "dark" ? saved : null;
}

function preferred(): Theme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function paintTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
}

/** Paint before React mounts so the saved theme never flashes the default one. */
export function applyStoredTheme() {
  paintTheme(storedTheme() ?? preferred());
}

export interface ThemeState {
  theme: Theme;
  toggle: () => void;
}

// One provider owns the theme for all pages; switching tabs never resets it.
const ThemeContext = createContext<ThemeState | null>(null);

export function useThemeState(): ThemeState {
  const [theme, setTheme] = useState<Theme>(() => storedTheme() ?? preferred());

  useEffect(() => {
    paintTheme(theme);
    writePreference(STORAGE_KEY, theme);
  }, [theme]);

  return {
    theme,
    toggle: () => setTheme((current) => current === "dark" ? "light" : "dark"),
  };
}

export const ThemeProvider = ThemeContext.Provider;

export function useTheme(): ThemeState {
  const state = useContext(ThemeContext);
  if (!state) throw new Error("useTheme 必须在 ThemeProvider 内使用");
  return state;
}
