// The one frame loop every animation of the pages runs on (docs/frontend.md「动效运行时」). An animation owns an element
// and gets a callback each frame while that element is in the viewport and the page is visible; with no such owner the
// loop schedules nothing. Under reduced motion no callback is ever scheduled: an owner draws its still frame itself.
//
// Invariants: at most one requestAnimationFrame is pending; it is pending only while some owner's element intersects
// the viewport, the document is visible and motion is allowed; with no owners left the loop holds no listener, no
// observer, and its counter is back at 0.

// A callback gets the seconds since the frame before it (0 on the first frame after a pause, at most maxStep) and the
// frame's timestamp.
export type Frame = (dt: number, now: number) => void;

type Owner = { element: Element; frame: Frame };

// A long pause (a slow device, a debugger) is stepped as one short frame, not as a jump the simulations cannot take.
const maxStep = 0.1;

const owners = new Set<Owner>();
const seen = new Map<Element, boolean>();
let observer: IntersectionObserver | undefined;
let motion: MediaQueryList | undefined;
let pending = 0;
let last = 0;
let frames = 0;

const reduce = "(prefers-reduced-motion: reduce)";
export const reducedMotion = () => matchMedia(reduce).matches;

// onMotion calls then with whether motion is allowed each time the reader changes the setting while the page runs, and
// returns a function that stops listening. Work a script set moving belongs to the setting it started under: asked to
// reduce motion, it settles in its final state at once rather than freeze midway, as the loop's owners would.
export function onMotion(then: (allowed: boolean) => void): () => void {
  const query = matchMedia(reduce);
  const changed = () => then(!query.matches);
  query.addEventListener("change", changed);
  return () => query.removeEventListener("change", changed);
}

const running = () =>
  !document.hidden && !motion?.matches && [...owners].some((owner) => seen.get(owner.element) === true);

function tick(now: number) {
  pending = 0;
  const dt = last ? Math.min(maxStep, (now - last) / 1000) : 0;
  last = now;
  for (const owner of [...owners]) {
    if (seen.get(owner.element) && owners.has(owner)) {
      frames++;
      owner.frame(dt, now);
    }
  }
  changed();
}

// changed starts or stops the loop after anything its condition reads has changed.
function changed() {
  if (running()) {
    pending ||= requestAnimationFrame(tick);
    return;
  }
  cancelAnimationFrame(pending);
  pending = 0;
  last = 0;
}

function start() {
  observer = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      seen.set(entry.target, entry.isIntersecting);
    }
    changed();
  });
  motion = matchMedia(reduce);
  motion.addEventListener("change", changed);
  document.addEventListener("visibilitychange", changed);
}

function stop() {
  changed();
  observer?.disconnect();
  observer = undefined;
  motion?.removeEventListener("change", changed);
  motion = undefined;
  document.removeEventListener("visibilitychange", changed);
  seen.clear();
  frames = 0;
}

// own runs frame on every frame while element is in the viewport and the page is visible, until the returned function
// is called. Under reduced motion it schedules nothing and returns a function that does nothing.
export function own(element: Element, frame: Frame): () => void {
  if (reducedMotion()) {
    return () => {};
  }
  if (owners.size === 0) {
    start();
  }
  const owner = { element, frame };
  owners.add(owner);
  if (!seen.has(element)) {
    seen.set(element, false);
    observer?.observe(element);
  }
  return () => {
    if (!owners.delete(owner)) {
      return;
    }
    if (![...owners].some((other) => other.element === element)) {
      observer?.unobserve(element);
      seen.delete(element);
    }
    if (owners.size === 0) {
      stop();
    } else {
      changed();
    }
  };
}

// What the end-to-end tests read (e2e/fx.spec.ts): the callbacks run since the loop last started, how many owners it
// has, and whether it holds its listeners and its frame. Read-only: nothing on the page can drive the loop through it.
const loopState = () => ({
  frames,
  owners: owners.size,
  listening: motion !== undefined,
  pending: pending !== 0,
});

declare global {
  interface Window {
    axiomFx?: { readonly state: ReturnType<typeof loopState> };
  }
}

if (typeof window !== "undefined") {
  window.axiomFx = Object.freeze({
    get state() {
      return loopState();
    },
  });
}
