import { readStored, writeStored } from "./storage";

export const themes = ["system", "light", "dark"] as const;
export type Theme = (typeof themes)[number];

// web/theme.js reads the same key to apply the saved choice before the first paint.
const themeKey = "axiom.theme";

// The theme is one value for the whole page, so every switch that shows it agrees: the console has one in the sidebar
// and one in the drawer, the website one in the header and one in the footer.
let current: Theme | undefined;
const listeners = new Set<() => void>();

export function currentTheme(): Theme {
  if (current === undefined) {
    const stored = readStored(() => localStorage, themeKey);
    current = themes.find((theme) => theme === stored) ?? "system";
  }
  return current;
}

export function subscribeTheme(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// chooseTheme sets the page's colour scheme and keeps the choice: data-theme forces one, its absence follows the system
// (styles.css).
export function chooseTheme(theme: Theme) {
  current = theme;
  if (theme === "system") {
    delete document.documentElement.dataset.theme;
  } else {
    document.documentElement.dataset.theme = theme;
  }
  writeStored(() => localStorage, themeKey, theme);
  for (const listener of listeners) {
    listener();
  }
}
