import { reducedMotion } from "@axiom/fx";
import type { Sea } from "@axiom/fx/sea";
import type { Whale } from "@axiom/fx/whale";
import { type RefObject, useCallback, useEffect, useRef } from "react";

const loadWhale = () => import("@axiom/fx/whale");

const password = "deepseek";
// A chance meeting (a deepseek model met further down the page) waits until the page is this old, as the board's egg
// does, and happens once per page load; typing the word or pressing the strip calls the whale on purpose.
const settle = 10_000;

// The word typed anywhere on the page, as the board's egg listens for it; fields that take text are left alone.
function typing(then: () => void) {
  let typed = "";
  return (e: KeyboardEvent) => {
    if (e.key.length !== 1 || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target instanceof Element && e.target.closest("input, textarea, select, [contenteditable]")) return;
    typed = (typed + e.key.toLowerCase()).slice(-password.length);
    if (typed === password) {
      typed = "";
      then();
    }
  };
}

type Options = { engine: Sea["engine"]; narrow: boolean; dark: boolean };

// useWhale returns what calls the whale of the easter egg (packages/fx whale), which is loaded the first time it is
// called: typing deepseek anywhere on the page, or pressing the word in the first screen's strip, calls it on
// purpose; the returned function, for the screens, is a chance meeting. The whale swims on the running sea and is
// taken down when it has left. It never comes under reduced motion, or before there is a sea to swim in.
export function useWhale(
  root: RefObject<HTMLElement | null>,
  sea: RefObject<Sea | null>,
  narrow: boolean,
  dark: boolean,
) {
  const visit = useRef<Whale | null>(null);
  const loading = useRef(false);
  const met = useRef(false);
  const live = useRef(true);
  const latest = useRef({ narrow, dark });
  latest.current = { narrow, dark };

  const play = useCallback(async (frame: HTMLElement, { engine, narrow, dark }: Options) => {
    const { startWhale } = await loadWhale();
    if (!live.current) return;
    const end = (done: Whale) => {
      done.destroy();
      if (visit.current === done) visit.current = null;
    };
    const whale: Whale = startWhale(frame, engine, {
      dark,
      phone: narrow,
      onDone: () => queueMicrotask(() => end(whale)),
    });
    visit.current = whale;
    if (whale.summon(null)) met.current = true;
    else end(whale);
  }, []);

  const summon = useCallback(
    (force: boolean) => {
      const frame = root.current;
      const engine = sea.current?.engine;
      if (!frame || !engine || reducedMotion() || visit.current || loading.current) return;
      if (!force && (met.current || performance.now() < settle)) return;
      loading.current = true;
      // a module that fails to load leaves the page as it is drawn: the whale is only decoration
      play(frame, { engine, ...latest.current })
        .catch(() => {})
        .finally(() => {
          loading.current = false;
        });
    },
    [root, sea, play],
  );

  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    live.current = true;
    const onKey = typing(() => summon(true));
    const onClick = (e: MouseEvent) => {
      if (e.target instanceof Element && e.target.closest(".hs > .main .fam [data-egg=deepseek]")) summon(true);
    };
    document.addEventListener("keydown", onKey);
    frame.addEventListener("click", onClick);
    return () => {
      live.current = false;
      document.removeEventListener("keydown", onKey);
      frame.removeEventListener("click", onClick);
      visit.current?.destroy();
      visit.current = null;
    };
  }, [root, summon]);

  return useCallback(() => summon(false), [summon]);
}
