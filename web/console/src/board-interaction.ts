import { type KeyboardEvent, useSyncExternalStore } from "react";

function boardMedia(query: string) {
  return {
    subscribe: (listener: () => void) => {
      const list = matchMedia(query);
      list.addEventListener("change", listener);
      return () => list.removeEventListener("change", listener);
    },
    matches: () => matchMedia(query).matches,
  };
}

// The shell uses the tablet board from 820px, with its sidebar folded below 1024px.
export const phoneWidth = boardMedia("(max-width: 819px)");
export const wideWidth = boardMedia("(min-width: 1024px)");

export function useBoardMedia(query: ReturnType<typeof boardMedia>) {
  return useSyncExternalStore(query.subscribe, query.matches);
}

export function pressBoardButton(event: KeyboardEvent<Element>) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    (event.currentTarget as HTMLElement).click();
  }
}
