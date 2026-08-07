import { useEffect, useState } from "react";

export type Theme = "dark" | "light" | "unfold" | "fluent";
const STORAGE_KEY = "acs_theme";
const THEMES: Theme[] = ["dark", "light", "unfold", "fluent"];

function isTheme(value: string | null): value is Theme {
  return value !== null && (THEMES as string[]).includes(value);
}

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    return isTheme(stored) ? stored : "dark";
  });

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(STORAGE_KEY, theme);
  }, [theme]);

  return { theme, setTheme, themes: THEMES };
}
