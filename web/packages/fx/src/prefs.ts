// The language and theme switches of the website's header (#747, port design §8.2): the board's own prefs.js as
// @axiom/boards generates it, loaded the first time either switch is used. It runs unedited on the board's markup: it
// opens and filters the language panel, writes the chosen language's words with the sonar decode, and dives into
// the other theme.
import "@axiom/boards/engines/prefs.js";
import type { OceanEngine } from "./sea";

export type Prefs = {
  open(): void;
  // choose picks a language (the engine's code, "zh-CN" or "en") from a control other than the globe; origin is where
  // the decode's ring starts
  choose(code: string, origin?: Element): void;
  toggleTheme(): void;
  setSystem(system: "light" | "dark"): void;
  destroy(): void;
};

declare global {
  interface Window {
    AxiomPrefs: new (root: HTMLElement, engine: object, options: object) => Prefs;
  }
}

export type PrefsOptions = {
  // the system's scheme, which the page follows until the theme is switched
  system: "light" | "dark";
  // a switch has landed: the theme, and the language (the engine's code, "zh-CN" or "en")
  onTheme: (to: "light" | "dark") => void;
  onLang: (code: string) => void;
};

// startPrefs starts the switches on root, the frame the board's markup draws, with the sea's engine to dive through;
// without a sea (its module failed to load) the theme changes over a blank canvas. The engine reads and keeps the
// choices under its own keys (ax-lang, ax-theme): the caller writes them first from the page's own.
export function startPrefs(root: HTMLElement, sea: OceanEngine | undefined, options: PrefsOptions): Prefs {
  const canvas = root.querySelector<HTMLCanvasElement>("canvas.sea:not(.cover)");
  if (!canvas) {
    throw new Error("startPrefs: the root has no canvas.sea");
  }
  return new window.AxiomPrefs(root, sea ?? { canvas, setLight: () => {}, renderer: {}, vs: 1 }, {
    ...options,
    themed: ":scope > .ho",
  });
}
