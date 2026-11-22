import { type KeyboardEvent, useSyncExternalStore } from "react";

export function boardMedia(query: string) {
  return {
    subscribe: (listener: () => void) => {
      const list = matchMedia(query);
      list.addEventListener("change", listener);
      return () => list.removeEventListener("change", listener);
    },
    matches: () => matchMedia(query).matches,
  };
}

export function useBoardMedia(query: ReturnType<typeof boardMedia>) {
  return useSyncExternalStore(query.subscribe, query.matches);
}

export function pressBoardButton(event: KeyboardEvent<Element>) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    (event.currentTarget as HTMLElement).click();
  }
}
