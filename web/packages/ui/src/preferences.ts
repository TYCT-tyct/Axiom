import { readStored, writeStored } from "./storage";

export const themes = ["system", "light", "dark"] as const;
export type Theme = (typeof themes)[number];

const themeKey = "axiom.theme";

export function storedTheme(): Theme {
  const stored = readStored(() => localStorage, themeKey);
  return themes.find((theme) => theme === stored) ?? "system";
}

// applyTheme sets the page's colour scheme: data-theme forces one, its absence follows the system (styles.css).
export function applyTheme(theme: Theme) {
  if (theme === "system") {
    delete document.documentElement.dataset.theme;
  } else {
    document.documentElement.dataset.theme = theme;
  }
}

export function chooseTheme(theme: Theme) {
  applyTheme(theme);
  writeStored(() => localStorage, themeKey, theme);
}
