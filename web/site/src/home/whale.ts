import type { Sea } from "@axiom/fx/sea";
import type { Whale } from "@axiom/fx/whale";
import { type RefObject, useCallback, useEffect, useRef, useState } from "react";
import { turns, typedWord } from "./script";
import { signingIn } from "./sign-in-place";

const loadWhale = () => import("@axiom/fx/whale");

const password = "deepseek";
// A chance meeting (a deepseek model met further down the page) waits until the page is this old, as the board's egg
// does, and happens once per page load; typing the word or pressing the strip calls the whale on purpose.
const settle = 10_000;

const strip = ".hs > .main .fam [data-egg=deepseek]";
// how long a visit takes to dive out of sight when the sign-in pages go up
const diveFor = 420;

// The calls on purpose: the word typed anywhere, which is the keyboard's way to the whale, or the first screen's strip
// pressed (the strip is decoration, out of what is read); the returned function stops listening.
function calls(frame: HTMLElement, call: () => void) {
  const onKey = typedWord(password, call, () => signingIn(frame));
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
      // the sign-in pages up, the sea is theirs: the whale keeps away (leave)
      if (!frame || !engine || visit.current || loading.current || signingIn(frame)) return;
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

  useLeave(root, visit, calling);

  return { meet: useCallback(() => summon(false), [summon]), call: useCallback(() => summon(true), [summon]) };
}

// The sign-in pages going up: a call on its way is dropped, and a visit under way dives out of sight rather than swim on
// under the sign-in page, whose form panel it would show through (its canvas spans the frame) and whose solid gathers the
// sharks alone (login-ui.js leaves whale-like creatures out).
function useLeave(
  root: RefObject<HTMLElement | null>,
  visit: RefObject<Whale | null>,
  calling: ReturnType<typeof turns>,
) {
  useEffect(() => {
    const frame = root.current;
    if (!frame) return;
    const watch = new MutationObserver(() => {
      if (!signingIn(frame)) return;
      calling.end();
      const going = visit.current;
      visit.current = null;
      const canvas = frame.querySelector<HTMLCanvasElement>(":scope > canvas.wh");
      if (going && canvas) dive(going, canvas);
    });
    watch.observe(frame, { attributes: true, attributeFilter: ["data-state"] });
    return () => watch.disconnect();
  }, [root, visit, calling]);
}

// the whale sinks and fades, held at its last frame until it is taken down
function dive(going: Whale, canvas: HTMLCanvasElement) {
  const fall = canvas.animate([{ opacity: 1 }, { opacity: 0, transform: "translateY(32px) scale(0.97)" }], {
    duration: diveFor,
    easing: "cubic-bezier(0.4, 0, 1, 1)",
    fill: "forwards",
  });
  const gone = () => {
    going.destroy();
    fall.cancel();
  };
  fall.finished.then(gone, gone);
}
