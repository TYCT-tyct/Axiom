import {
  type KeyboardEvent as KeyPress,
  type RefCallback,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
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

// turns numbers the interactions of one kind (a copy, a tool picked, a question patched to): start returns whether that
// interaction is still the latest, so what it left in flight (a clipboard's answer, a frame step, a timer) does nothing
// once another has started, or end has been called for the screen or the setting it ran under.
export function turns() {
  let latest = 0;
  return {
    start: () => {
      const own = ++latest;
      return () => own === latest;
    },
    end: () => {
      latest++;
    },
  };
}

// tabKey moves the choice along a row of tabs from the keyboard (the ARIA tabs pattern): the arrows to the next or the
// previous, round the row, Home and End to its ends; the chosen tab takes the focus, as the row has one stop, the chosen.
export function tabKey(count: number, current: () => number, choose: (i: number) => void) {
  return (e: KeyPress<Element>) => {
    const now = current();
    const to = {
      ArrowRight: now + 1,
      ArrowDown: now + 1,
      ArrowLeft: now - 1,
      ArrowUp: now - 1,
      Home: 0,
      End: count - 1,
    }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    const i = (to + count) % count;
    choose(i);
    e.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]')[i]?.focus();
  };
}
// the roving stop: only the chosen tab is in the keyboard's order
export const tabStop = (i: number, chosen: number) => (i === chosen ? 0 : -1);

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
): {
  setStage: RefCallback<Element>;
  stage: HTMLElement | null;
  script: S | null;
  values: Record<string, unknown> | undefined;
  entered: boolean;
} {
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
    // the screen fades in the first time an eighth of it is seen (the board's .hfm.in)
    const seen = firstSeen(stage, 0.12);
    seen.promise.then(() => setEntered(true));
    stage.addEventListener("keydown", press);
    return () => {
      running.destroy();
      seen.cancel();
      stage.removeEventListener("keydown", press);
      setScript(null);
    };
  }, [stage]);
  return { setStage, stage, script, values: script?.values(input), entered };
}

// On the narrow layout (phone and tablet), the steps and trust frames take the height their words need
// (SitePhone.logic.html fw): measure the section at its natural height and size the frame to h + 96 px.
export function useFitFrame(stage: HTMLElement | null, narrow: boolean) {
  const { i18n } = useTranslation();
  const language = i18n.language;
  useLayoutEffect(() => {
    if (!stage || !narrow) return;
    let live = true;
    const fit = () => {
      if (!live) return;
      const sec = stage.querySelector<HTMLElement>(":scope > .sec");
      if (!sec) return;
      sec.style.position = "relative";
      sec.style.height = "auto";
      const height = sec.offsetHeight;
      sec.style.position = "";
      sec.style.height = "";
      if (height <= 200) return;
      const next = `${Math.ceil(height + 96)}px`;
      if (stage.style.height !== next) stage.style.height = next;
    };
    fit();
    const resized = new ResizeObserver(fit);
    resized.observe(stage);
    const ho = stage.closest(".ho");
    const tabbed = new MutationObserver(fit);
    if (ho) tabbed.observe(ho, { attributes: true, attributeFilter: ["data-tab"] });
    document.fonts?.ready.then(fit);
    return () => {
      live = false;
      resized.disconnect();
      tabbed.disconnect();
    };
  }, [stage, narrow, language]);
}

// A row the board makes clickable is a button or an option to the keyboard too (the regions' roles): Enter and Space
// press it.
function press(e: KeyboardEvent) {
  const control = e.target instanceof HTMLElement && e.target.matches("[role=button], [role=option]") ? e.target : null;
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
