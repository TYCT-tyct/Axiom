// The website and the console are two documents (their sign-in pages a third way in, when opened directly). Going from
// one to the other is never a blank page between them:
// - where the browser can (cross-document view transitions), the page left stays on screen as it was last drawn until
//   the page come to is whole (it says so: arrivedWhole; its sea and question up, its shell drawn), or holdFor ms, then
//   the two cross-fade. Any navigation between them does, a press of the tiles transition's ways and a sign-in that
//   enters the console alike.
// - the tiles transition's own way across (the sign-in page opened directly, gone back home) also takes the part both
//   pages have: the brand mark slides from one page's place to the other's (flyBrand). The page left writes down where
//   it stood (leaveTogether); the page come to plays it from there into its own place (flyParts). Any browser.
import { peekArrival, peekReturn } from "./arrival";

const partsKey = "axiom:parts";
const fresh = 10_000;
// the longest the page left is kept on screen, from this page's first script: a still picture for longer reads as the
// page stuck, so a part slower than this (a cold sea) comes in on the page that is already there, faded in
const holdFor = 600;
// trans.js's E.emph and the board's flight time for the mark (flyBrand, 40 → 600 ms)
const flight = { duration: 560, easing: "cubic-bezier(0.2, 0, 0, 1)" };

export type Part = "brand";
type Rect = { x: number; y: number; w: number; h: number };
type Stood = { at?: number; stood?: Partial<Record<Part, Rect>> };

// the page left, still on screen over this one: released once this one is whole, or after holdFor
let hold: { release: () => void } | null = null;
// this page said it is whole before the browser said whether a transition came: one that comes is not held
let wholeEarly = false;
const whole: (() => void)[] = [];

function readParts(remove: boolean): Partial<Record<Part, Rect>> | null {
  try {
    const raw = sessionStorage.getItem(partsKey);
    if (remove) sessionStorage.removeItem(partsKey);
    const data = raw ? (JSON.parse(raw) as Stood) : null;
    return data && typeof data.at === "number" && Date.now() - data.at < fresh ? (data.stood ?? {}) : null;
  } catch {
    return null;
  }
}

// the transition's own animations (the cross-fade of the pages, the travel of named parts), on its pseudo-elements
const transitionAnimations = () =>
  document
    .getAnimations()
    .filter((a) => String((a.effect as KeyframeEffect | null)?.pseudoElement ?? "").startsWith("::view-transition"));

// A transition the browser aborts (pages of two origins, as in a test) or one skipped rejects its promises: nothing
// waits on them but the hold, so a rejection is no error of the page's.
function quiet(transition: ViewTransition) {
  for (const done of [transition.ready, transition.finished, transition.updateCallbackDone]) done.catch(() => {});
}

// expect holds this page back from the moment it starts: its first effects (the return's settling) run before the
// browser says whether a transition came (pagereveal), and must wait for it like the rest
function expect() {
  if (hold) return;
  let paused: Animation[] = [];
  let on = true;
  const release = () => {
    if (!on) return;
    on = false;
    hold = null;
    for (const a of paused) a.play();
    for (const go of whole.splice(0)) go();
  };
  hold = { release };
  window.setTimeout(release, holdFor);
  return (transition: ViewTransition | null) => {
    if (!transition) return release();
    transition.ready.then(() => {
      if (!on) return;
      paused = transitionAnimations();
      for (const a of paused) a.pause();
    }, release);
  };
}

// crossDocuments listens for the two ends of a cross-document view transition. It runs in the page's first script,
// which blocks the first frame (index.html), so it hears that frame's pagereveal.
export function crossDocuments() {
  const supported = "onpagereveal" in window && typeof CSSViewTransitionRule === "function";
  // the tiles transition's way across is held from this page's first script, as its settling waits on it (whenWhole);
  // any other is held from the transition's coming. A page brought back whole from the back-forward cache said so
  // long ago, and cross-fades at once.
  let attach = supported && readParts(false) ? expect() : undefined;
  window.addEventListener("pageswap", (e) => {
    const transition = (e as PageSwapEvent).viewTransition;
    if (transition) quiet(transition);
  });
  window.addEventListener("pagereveal", (e) => {
    const transition = (e as PageRevealEvent).viewTransition;
    if (transition) quiet(transition);
    if (!attach && transition && !wholeEarly) attach = expect();
    attach?.(transition);
    attach = undefined;
  });
}

// prepareArrival asks, from the page's first script, for the effects a page held on screen waits for (crossDocuments):
// the home page's sea on a return from the sign-in pages, the sign-in page's on an arrival from the website, beside the
// page's first render rather than after it.
export function prepareArrival() {
  if (peekReturn()) void import("./sea").catch(() => {});
  if (peekArrival()) void import("./login").catch(() => {});
}

// held is whether the page left is still on screen over this one, waiting for it to be whole
export const held = () => hold !== null;

// arrivedWhole lets the page left go: this one is whole
export function arrivedWhole() {
  if (hold) hold.release();
  else wholeEarly = true;
}

// whenWhole runs go once the page is whole and on screen: at once if nothing holds it
export function whenWhole(go: () => void) {
  if (hold) whole.push(go);
  else go();
}

const rectOf = (el: Element): Rect => {
  const r = el.getBoundingClientRect();
  return { x: r.x, y: r.y, w: r.width, h: r.height };
};
const onScreen = (r: Rect) => r.w > 0 && r.h > 0 && r.y + r.h > 0 && r.y < window.innerHeight;

// leaveTogether is called as the page goes, by one of the tiles transition's ways: it writes down where the shared
// parts on screen stood.
export function leaveTogether(parts: Partial<Record<Part, Element | null>>) {
  const stood: Partial<Record<Part, Rect>> = {};
  for (const [name, el] of Object.entries(parts) as [Part, Element | null][]) {
    const r = el && rectOf(el);
    if (r && onScreen(r)) stood[name] = r;
  }
  try {
    sessionStorage.setItem(partsKey, JSON.stringify({ at: Date.now(), stood }));
  } catch {
    // storage refused: the parts come in in place
  }
}

// flyParts plays each shared part of this page from where the page left had it into its own place: moved and scaled
// (by height, so it keeps its shape), from its top left corner.
export function flyParts(parts: Partial<Record<Part, HTMLElement | null>>) {
  const stood = readParts(true) ?? {};
  for (const [name, el] of Object.entries(parts) as [Part, HTMLElement | null][]) {
    const from = stood[name];
    const to = el && rectOf(el);
    if (!el || !from || !to || !onScreen(to)) continue;
    el.animate(
      [
        {
          transformOrigin: "0 0",
          transform: `translate(${from.x - to.x}px, ${from.y - to.y}px) scale(${from.h / to.h})`,
        },
        { transformOrigin: "0 0", transform: "none" },
      ],
      flight,
    );
  }
}
