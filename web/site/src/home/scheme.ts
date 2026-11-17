import { currentTheme, subscribeTheme } from "@axiom/ui";
import { useSyncExternalStore } from "react";

const dark = () => matchMedia("(prefers-color-scheme: dark)");

function subscribe(listener: () => void) {
  const media = dark();
  media.addEventListener("change", listener);
  const stop = subscribeTheme(listener);
  return () => {
    media.removeEventListener("change", listener);
    stop();
  };
}

// The scheme the page shows: the one chosen, or the system's while none is.
const scheme = (): "light" | "dark" => {
  const theme = currentTheme();
  return theme === "system" ? (dark().matches ? "dark" : "light") : theme;
};

export const systemScheme = (): "light" | "dark" => (dark().matches ? "dark" : "light");

export function useScheme() {
  return useSyncExternalStore(subscribe, scheme);
}
