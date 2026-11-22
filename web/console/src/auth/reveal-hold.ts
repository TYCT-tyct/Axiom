import { createContext, use, useLayoutEffect } from "react";

// What keeps a sign-in arrival's reveal back (LoginLayout): a part of the page that is not on show yet, its terms on
// the way. Its own module, so a page's parts hold it without loading the frame.
export const RevealHold = createContext<((by: 1 | -1) => void) | null>(null);

// useHoldReveal keeps the reveal back while pending. A layout effect, so the hold is in place before the frame looks.
export function useHoldReveal(pending: boolean) {
  const hold = use(RevealHold);
  useLayoutEffect(() => {
    if (!pending || !hold) return;
    hold(1);
    return () => hold(-1);
  }, [pending, hold]);
}
