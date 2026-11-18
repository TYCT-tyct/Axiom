import { reducedMotion } from "@axiom/fx";
import { type RefCallback, useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

// A screen's script, as the board writes each screen below the first (its Component): it keeps the screen's state and
// says what to write in the region's holes from it, in the words and formats input gives. It calls changed when its
// state moves; destroy stops it for good.
export type Script<Input> = { values: (input: Input) => Record<string, unknown>; destroy: () => void };

// firstSeen resolves the first time threshold of element is in view, as the board's screens start their scripts. An
// entry can intersect with less than the threshold showing (the edge just in), so the ratio decides.
export function firstSeen(element: Element, threshold: number) {
  let observer: IntersectionObserver | undefined;
  const promise = new Promise<void>((seen) => {
    observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting && entry.intersectionRatio >= threshold)) {
          observer?.disconnect();
          seen();
        }
      },
      { threshold },
    );
    observer.observe(element);
  });
  return { promise, cancel: () => observer?.disconnect() };
}

// The palette the board's script gives each screen (themeOf's vars), as the tokens it stands for; extra are the
// screen's own state colours.
export const screenVars = (...extra: string[]) =>
  [
    "--bg: var(--n1)",
    "--panel: var(--n2)",
    "--hover: var(--n3)",
    "--press: var(--n4)",
    "--hair1x: var(--n5)",
    "--line1x: var(--n6)",
    "--lineStrong1x: var(--n7)",
    "--hair2x: var(--n6)",
    "--line2x: var(--n7)",
    "--lineStrong2x: var(--n8)",
    "--selection: var(--accentSoft)",
    "--text1: var(--n12)",
    "--text2: var(--n11)",
    "--accent: var(--accentT)",
    "--veil: light-dark(color-mix(in srgb, var(--n1) 50%, transparent), color-mix(in srgb, var(--n1) 42%, transparent))",
    "--veilStrong: light-dark(color-mix(in srgb, var(--n1) 78%, transparent), color-mix(in srgb, var(--n1) 72%, transparent))",
    "--popShadow: 0 12px 32px light-dark(rgba(0, 0, 0, 0.08), rgba(0, 0, 0, 0.5))",
    ...extra,
  ].join("; ");

// useScript starts a screen's script on its stage (the element setStage is given) once the screen is drawn, and the
// screen draws itself again whenever the script's state moves. The screen's code is loaded near the view (screens.tsx),
// so the script starts a view's height before it is seen; until then values is undefined and the holes are empty.
export function useScript<S extends Script<never>>(
  start: (stage: HTMLElement, changed: () => void) => S,
  input: NoInfer<Parameters<S["values"]>[0]>,
): { setStage: RefCallback<Element>; script: S | null; values: Record<string, unknown> | undefined; entered: boolean } {
  const [stage, setStageElement] = useState<HTMLElement | null>(null);
  const [script, setScript] = useState<S | null>(null);
  const [, setVersion] = useState(0);
  const [entered, setEntered] = useState(false);
  const latest = useRef(start);
  latest.current = start;
  const setStage = useCallback((element: Element | null) => {
    setStageElement(element instanceof HTMLElement ? element : null);
  }, []);
  useEffect(() => {
    if (!stage) return;
    const running = latest.current(stage, () => setVersion((n) => n + 1));
    setScript(running);
    // the screen fades in the first time an eighth of it is seen (the board's .hfm.in), at once under reduced motion
    const seen = firstSeen(stage, reducedMotion() ? 0 : 0.12);
    seen.promise.then(() => setEntered(true));
    stage.addEventListener("keydown", press);
    return () => {
      running.destroy();
      seen.cancel();
      stage.removeEventListener("keydown", press);
      setScript(null);
    };
  }, [stage]);
  return { setStage, script, values: script?.values(input), entered };
}

// A row the board makes clickable is a button to the keyboard too (the regions' role="button"): Enter and Space press it.
function press(e: KeyboardEvent) {
  const control = e.target instanceof HTMLElement && e.target.matches("[role=button]") ? e.target : null;
  if (control && (e.key === "Enter" || e.key === " ")) {
    e.preventDefault();
    control.click();
  }
}

// useBoardWords gives a screen its region's own words in the page's language: they come with the screen's code, not
// with the page's messages, which the first screen loads. after adds the page's words to some of them, as the sample
// data notes that say what a demonstration is.
export function useBoardWords<K extends string>(
  zh: Record<K, string>,
  en: Record<K, string>,
  after: Partial<Record<K, string>> = {},
) {
  const { i18n } = useTranslation();
  const words = i18n.language === "en" ? en : zh;
  return (key: K) => (after[key] ? `${words[key]}${after[key]}` : words[key]);
}
