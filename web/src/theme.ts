import { createContext, useContext, useEffect, useState } from "react";

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

export interface ThemeState {
  theme: Theme;
  toggle: () => void;
}

/**
 * 主题只有一份，放在 context 里。
 *
 * 早先每个组件各自 useState 持有一份，读初值时它们碰巧一致，所以看不出问题；
 * 但切换只会更新点击方那一份，别的组件永远停在旧值。终端就是这么变成
 * 「面板已经暗了，它还是白的」——组件各持一份「当前主题」，本身就是错的。
 */
const ThemeContext = createContext<ThemeState | null>(null);

export function useThemeState(): ThemeState {
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

export const ThemeProvider = ThemeContext.Provider;

export function useTheme(): ThemeState {
  const state = useContext(ThemeContext);
  if (!state) {
    throw new Error("useTheme 必须在 ThemeProvider 内使用");
  }
  return state;
}
