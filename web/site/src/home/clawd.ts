import { useEffectModule } from "@axiom/fx";
import type { Sea } from "@axiom/fx/sea";
import { createContext, type RefObject, useCallback, useEffect, useRef } from "react";
import { typedWord } from "./script";
import { signingIn } from "./sign-in-place";

const loadClawd = () => import("@axiom/fx/clawd");

// What the sea bed's screen calls with its dune's canvas once it is drawn, and with null when it goes: the frame
// provides it, and hands the canvas to Clawd, who lives behind its letters.
export const FloorContext = createContext<(art: HTMLCanvasElement | null) => void>(() => {});

const password = "claude";

// useClawd runs Clawd, the easter egg of the board's clawd-cast.js (packages/fx clawd), on the frame root points at. It
// comes after the first content as the sea does, since its quiet hint (the first screen's strip winking) plays at the
// top before anyone calls; the cast listens on the frame itself (the strip's word, "claude" typed while the focus is in
// the frame, the letters at the sea floor), and the word typed anywhere else on the page is handed to it from here.
// The returned function is the sea bed's (FloorContext).
export function useClawd(
  root: RefObject<HTMLElement | null>,
  sea: RefObject<Sea | null>,
  { narrow, dark }: { narrow: boolean; dark: boolean },
) {
  const latest = useRef(dark);
  latest.current = dark;
  const floor = useRef<HTMLCanvasElement | null>(null);
  const clawd = useEffectModule(root, loadClawd, ({ startClawd }, frame) => {
    const started = startClawd(frame, { engine: () => sea.current?.engine, dark: () => latest.current, phone: narrow });
    started.floor(floor.current);
    return started;
  });

  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    const onKey = typedWord(
      password,
      () => clawd.current?.call(),
      (target) => !(target instanceof Node) || frame.contains(target) || signingIn(frame),
    );
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [root, clawd]);

  return useCallback(
    (art: HTMLCanvasElement | null) => {
      floor.current = art;
      clawd.current?.floor(art);
    },
    [clawd],
  );
}
