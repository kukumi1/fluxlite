import { useEffect, useState } from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "fluxlite-theme";

function stored(): Theme | null {
  const saved = localStorage.getItem(STORAGE_KEY);
  return saved === "light" || saved === "dark" ? saved : null;
}

function preferred(): Theme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function paint(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
}

/**
 * 在 React 挂载之前先把主题涂上。
 *
 * 交给 effect 去做会先按亮色渲染一帧，暗色用户每次刷新都要闪一下白屏。
 */
export function applyStoredTheme() {
  paint(stored() ?? preferred());
}

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(() => stored() ?? preferred());

  useEffect(() => {
    paint(theme);
    localStorage.setItem(STORAGE_KEY, theme);
  }, [theme]);

  return {
    theme,
    toggle: () => setTheme((current) => (current === "dark" ? "light" : "dark")),
  };
}
