import type { Sea } from "@axiom/fx/sea";
import type { Whale } from "@axiom/fx/whale";
import { type RefObject, useCallback, useEffect, useRef, useState } from "react";
import { turns } from "./script";

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

const strip = ".hs > .main .fam [data-egg=deepseek]";

// The calls on purpose: the word typed anywhere, which is the keyboard's way to the whale, or the first screen's strip
// pressed (the strip is decoration, out of what is read); the returned function stops listening.
function calls(frame: HTMLElement, call: () => void) {
  const onKey = typing(call);
  const onClick = (e: MouseEvent) => {
    if (e.target instanceof Element && e.target.closest(strip)) call();
  };
  document.addEventListener("keydown", onKey);
  frame.addEventListener("click", onClick);
  return () => {
    document.removeEventListener("keydown", onKey);
    frame.removeEventListener("click", onClick);
  };
}

// What ends a call or a visit: the page going out of view drops a call whose code is on its way, and the page put away
// takes the visit down too, its canvas cleared, as does the end (the returned function).
function drops(calling: ReturnType<typeof turns>, takeDown: () => void) {
  const onHide = () => document.visibilityState === "hidden" && calling.end();
  const onLeave = () => {
    calling.end();
    takeDown();
  };
  document.addEventListener("visibilitychange", onHide);
  window.addEventListener("pagehide", onLeave);
  return () => {
    calling.end();
    document.removeEventListener("visibilitychange", onHide);
    window.removeEventListener("pagehide", onLeave);
  };
}

// useWhale returns what calls the whale of the easter egg (packages/fx whale), which is loaded the first time it is
// called: typing deepseek anywhere on the page, or pressing the word in the first screen's strip, calls it on
// purpose; meet, for the screens, is a chance meeting, and call, for the sea-floor terminal, is a call on purpose. The whale swims on the running sea and is
// taken down when it has left. It never comes before there is a sea to swim in. A call whose code is still on its way
// is the call's: the page going out of view or away, or the frame ending drops it; a visit under way takes the theme as it changes.
export function useWhale(
  root: RefObject<HTMLElement | null>,
  sea: RefObject<Sea | null>,
  met: RefObject<boolean>,
  { narrow, dark }: { narrow: boolean; dark: boolean },
) {
  const visit = useRef<Whale | null>(null);
  const loading = useRef(false);
  const [calling] = useState(turns);
  const latest = useRef({ narrow, dark });
  latest.current = { narrow, dark };
  useEffect(() => visit.current?.setTheme(dark), [dark]);

  const play = useCallback(
    async (frame: HTMLElement, engine: Sea["engine"], mine: () => boolean) => {
      const { startWhale } = await loadWhale();
      // the reader may have left while the code was on its way
      if (!mine()) return;
      const { narrow, dark } = latest.current;
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
    },
    [met],
  );

  const summon = useCallback(
    (force: boolean) => {
      const frame = root.current;
      const engine = sea.current?.engine;
      if (!frame || !engine || visit.current || loading.current) return;
      if (!force && (met.current || performance.now() < settle)) return;
      loading.current = true;
      // a module that fails to load leaves the page as it is drawn: the whale is only decoration
      play(frame, engine, calling.start())
        .catch(() => {})
        .finally(() => {
          loading.current = false;
        });
    },
    [root, sea, met, play, calling],
  );

  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    const takeDown = () => {
      visit.current?.destroy();
      visit.current = null;
    };
    const stopCalls = calls(frame, () => summon(true));
    const stopDrops = drops(calling, takeDown);
    return () => {
      stopCalls();
      stopDrops();
      takeDown();
    };
  }, [root, summon, calling]);

  return { meet: useCallback(() => summon(false), [summon]), call: useCallback(() => summon(true), [summon]) };
}
