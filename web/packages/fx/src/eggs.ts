// The sea's easter eggs on the home page (#747, the design's eggs.js, docs/frontend.md「海的彩蛋」), loaded after the first
// content once the sea is up. The director (director.ts) keeps the rules; this module watches what the reader does and
// the visitor's clock, and plays the moments on the running sea (moments.ts):
//   - earned: the pointer resting 4 s on open water brings one shark over; copying an integration command feeds the
//     fish near its card; resting 3 s on the sea floor offers the way back up; two loops drawn on open water make a
//     whirlpool there
//   - states: the night (22:00 to 06:00: the deep's glow and a slightly darker sea) and 1024 (24 October: the fish are
//     0 and 1 for 24 s)
//   - auto: the hourly tide at :59
//   - hidden: "/" opens the sea-floor terminal (the page's), typing axiom shows the depth gauge
// A moment keeps the sea going while it plays, wherever the reader is (the sea otherwise rests below the first screen);
// the night's glow plays when the sea does.
import { director } from "./director";
import { own } from "./loop";
import { type Engine, glyphs, tend } from "./moments";
import type { Sea } from "./ocean";

// where a press is a control's (eggs.js QUIET), what the page's content is (whale-egg.js), and where keys are words
const quiet = "a,button,input,textarea,select,label,[role=button],[contenteditable]";
const content = "[data-avoid], .lens, .rcard, .pnl, .pick, .bar:not(.away), .isl.on";
const typing = "input, textarea, [contenteditable]";

export type EggsOptions = {
  // the class of the moments' canvas, which the page styles
  glow: string;
  // the sea-floor terminal: whether it is open, and opening it
  terminal: { open: () => boolean; show: () => void };
  // the depth gauge, shown for a while
  gauge: () => void;
  // the way back up, offered or taken away
  floor: (shown: boolean) => void;
};

export type Eggs = {
  // feed drops food somewhere low in the open water
  feed: () => void;
  // milestone sends one ring across the open water
  milestone: () => void;
  dive: () => void;
  surface: () => void;
  destroy: () => void;
};

type Point = { x: number; y: number; t: number };

// loops says where a pointer's trail (the last 2.4 s) went round twice, about a centre 24 to 220 px away (eggs.js)
function loops(trail: Point[]): { x: number; y: number } | null {
  if (trail.length < 24) return null;
  const x = trail.reduce((s, q) => s + q.x, 0) / trail.length;
  const y = trail.reduce((s, q) => s + q.y, 0) / trail.length;
  let turn = 0;
  let radius = 0;
  for (let i = 1; i < trail.length; i++) {
    const a = trail[i - 1] as Point;
    const b = trail[i] as Point;
    let d = Math.atan2(b.y - y, b.x - x) - Math.atan2(a.y - y, a.x - x);
    if (d > Math.PI) d -= 2 * Math.PI;
    if (d < -Math.PI) d += 2 * Math.PI;
    turn += d;
    radius += Math.hypot(b.x - x, b.y - y);
  }
  radius /= trail.length;
  return Math.abs(turn) > Math.PI * 3.4 && radius > 24 && radius < 220 ? { x, y } : null;
}

// openness is how far a point (in the viewport) is from the page's content: 0 at it, 1 from 80 px out (respond.js)
function openness(ho: HTMLElement, x: number, y: number) {
  let d = Number.POSITIVE_INFINITY;
  for (const el of ho.querySelectorAll(content)) {
    const r = el.getBoundingClientRect();
    if (!r.width) continue;
    d = Math.min(
      d,
      Math.hypot(Math.max(r.left - 10 - x, 0, x - r.right - 10), Math.max(r.top - 10 - y, 0, y - r.bottom - 10)),
    );
  }
  return Math.min(1, Math.max(0, (d - 8) / 72));
}

// keep is the sea kept going, and the moments' layer drawn, for as long as the latest moment asks
function keeper(ho: HTMLElement, sea: Sea, draw: (dt: number) => void, busy: () => boolean) {
  let lease: (() => void) | undefined;
  let until = 0;
  let drawn = -1;
  // the layer's frame, which the lease and the night's glow may both ask for: it draws once
  const frame = (dt: number, now: number) => {
    if (now === drawn) return;
    drawn = now;
    draw(dt);
    if (lease && now > until && !busy()) {
      lease();
      lease = undefined;
    }
  };
  return {
    frame,
    keep: (ms: number) => {
      until = Math.max(until, performance.now() + ms);
      if (lease) return;
      const stopLayer = own(ho, frame);
      const stopSea = sea.awake(ho);
      lease = () => {
        stopLayer();
        stopSea();
      };
    },
    end: () => lease?.(),
  };
}

function scrollers(frame: HTMLElement) {
  const scroller = frame.querySelector<HTMLElement>(".hs") ?? frame;
  return {
    deep: () => {
      const max = scroller.scrollHeight - scroller.clientHeight;
      return max > 0 ? scroller.scrollTop / max : 0;
    },
    dive: () => scroller.scrollTo({ top: scroller.scrollHeight, behavior: "smooth" }),
    rise: () => scroller.scrollTo({ top: 0, behavior: "smooth" }),
  };
}

declare global {
  interface Window {
    axiomEggs?: { readonly state: ReturnType<ReturnType<typeof director>["state"]> };
  }
}

// the hidden keys: "/" opens the terminal, axiom typed shows the depth gauge; never while typing into a field
function hiddenKeys({ terminal, gauge }: EggsOptions) {
  let typed = "";
  return (e: KeyboardEvent) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target instanceof Element && e.target.closest(typing)) return;
    if (e.key === "/" && !terminal.open()) {
      e.preventDefault();
      terminal.show();
      return;
    }
    if (e.key.length !== 1) return;
    typed = (typed + e.key.toLowerCase()).slice(-5);
    if (typed === "axiom") {
      typed = "";
      gauge();
    }
  };
}

// controls listens to the frame's controls and the hidden keys: a control used holds the auto moments off for a while,
// and a copied integration command feeds the fish just outside its card (once a visit); the returned function stops it
function controls(
  frame: HTMLElement,
  moments: ReturnType<typeof director>,
  options: EggsOptions,
  feedBy: (card: DOMRect) => void,
) {
  const onKey = hiddenKeys(options);
  const onControl = (e: Event) => {
    if (e.target instanceof Element && e.target.closest(quiet)) moments.touched();
  };
  const onCopy = (e: MouseEvent) => {
    const button = e.target instanceof Element ? e.target.closest<HTMLButtonElement>(".under .cpy") : null;
    if (!button || button.disabled) return;
    const card = (button.closest(".code") ?? button).getBoundingClientRect();
    moments.earn("feed", () => feedBy(card));
  };
  frame.addEventListener("pointerdown", onControl, true);
  frame.addEventListener("keydown", onControl, true);
  frame.addEventListener("click", onCopy);
  window.addEventListener("keydown", onKey);
  return () => {
    window.removeEventListener("keydown", onKey);
    frame.removeEventListener("pointerdown", onControl, true);
    frame.removeEventListener("keydown", onControl, true);
    frame.removeEventListener("click", onCopy);
  };
}

function timeouts() {
  const pending = new Set<number>();
  return {
    later: (ms: number, then: () => void) => {
      const id = window.setTimeout(() => {
        pending.delete(id);
        then();
      }, ms);
      pending.add(id);
    },
    clear: () => {
      for (const id of pending) window.clearTimeout(id);
    },
  };
}

type Parts = {
  layer: ReturnType<typeof glyphs>;
  water: ReturnType<typeof tend>;
  later: (ms: number, then: () => void) => void;
  // the night's glow drawn while the sea plays, until the returned function is called
  glow: () => () => void;
  depth: HTMLElement | null;
};

// states colours the visit by the visitor's clock: the night's glow and darker sea, 1024's binary fish; it returns
// what ends the glow
function states({ layer, water, later, glow, depth }: Parts) {
  const at = new Date();
  const hour = at.getHours();
  if (at.getMonth() === 9 && at.getDate() === 24) {
    water.binary(true);
    later(24_000, () => water.binary(false));
  }
  if (hour < 22 && hour >= 6) return undefined;
  layer.night(0.55);
  if (depth) {
    // the sea is never lighter than this tonight (behaviour.ts reads the floor as the page scrolls)
    depth.dataset.floor = "0.12";
    depth.style.opacity = String(Math.max(Number(depth.style.opacity) || 0, 0.12));
  }
  return glow();
}

// plays are the moments on the sea, each keeping it going for as long as it plays
function plays(engine: Engine, { layer, water, later }: Omit<Parts, "glow" | "depth">, keep: (ms: number) => void) {
  // food in the world's own coordinates, kept off the edges
  const feedAt = (x: number, y: number) => {
    const { width, height } = engine.world;
    water.feed(Math.min(width - 40, Math.max(40, x)), Math.min(height - 40, Math.max(40, y)));
    keep(4000);
  };
  return {
    feedAt,
    feed: () => feedAt(engine.world.width * (0.2 + Math.random() * 0.6), engine.world.height * 0.78),
    whirl: (x: number, y: number) => {
      water.whirl(x, y, 7);
      keep(7000);
    },
    tide: () => {
      layer.tide();
      keep(7000);
    },
    // one shark comes over to where the pointer rests, for 9 s
    visit: (x: number, y: number) => {
      later(9000, water.lure(x, y));
      keep(9000);
    },
    milestone: () => {
      layer.milestone();
      keep(2600);
    },
  };
}

// what the end-to-end tests read (e2e/fx-eggs.spec.ts): read-only, nothing on the page can play a moment through it
function expose(moments: ReturnType<typeof director>) {
  window.axiomEggs = Object.freeze({
    get state() {
      return moments.state();
    },
  });
}

// the moments' canvas, over the sea and the whale and under the page
function layerCanvas(frame: HTMLElement, className: string) {
  const canvas = document.createElement("canvas");
  canvas.className = className;
  canvas.setAttribute("aria-hidden", "true");
  (frame.querySelector("canvas.wh") ?? frame.querySelector("canvas.sea"))?.after(canvas);
  return canvas;
}

// resting says whether the reader has rested on the sea floor (the last 1.5% of the page) for 3 s
function resting(deep: () => number) {
  let since = 0;
  return {
    rested: (now: number) => {
      if (deep() <= 0.985) since = 0;
      else if (!since) since = now;
      return since > 0 && now - since > 3000;
    },
    reset: () => {
      since = 0;
    },
  };
}

// startEggs plays the eggs on frame (.tr, the board's markup) over its running sea, until destroy.
export function startEggs(frame: HTMLElement, sea: Sea, options: EggsOptions): Eggs {
  const engine = sea.engine as unknown as Engine;
  const ho = frame.querySelector<HTMLElement>(".ho") ?? frame;
  const depth = ho.querySelector<HTMLElement>(".depth");
  const owner = frame.querySelector(".hs > main") ?? ho;
  const page = scrollers(frame);
  const canvas = layerCanvas(frame, options.glow);
  const water = tend(engine);
  const layer = glyphs(canvas, ho);
  const run = keeper(ho, sea, layer.draw, layer.busy);
  const { later, clear } = timeouts();
  let glow: (() => void) | undefined;
  const parts = { layer, water, later, depth, glow: () => own(owner, run.frame) };
  const moments = director({ now: () => performance.now(), hidden: () => document.hidden }, () => {
    glow = states(parts);
  });
  const play = plays(engine, { layer, water, later }, run.keep);
  const pointer = watchPointer(ho, engine, (at) => moments.earn("whirl", () => play.whirl(at.x, at.y)));
  const floor = resting(page.deep);
  const tick = () => {
    if (document.hidden) return;
    const now = performance.now();
    if (new Date().getMinutes() === 59) moments.auto("tide", play.tide);
    const p = pointer.state;
    if (p.active && p.k > 0.85 && now - p.rest > 4000) moments.earn("visit", () => play.visit(p.x, p.y));
    if (floor.rested(now)) moments.earn("floor", () => options.floor(true));
    moments.tick();
  };
  const timer = window.setInterval(tick, 250);
  // just outside the card, on open water
  const stopControls = controls(frame, moments, options, (card) => {
    const at = engine.local({ clientX: card.left - 60, clientY: card.bottom + 40 } as PointerEvent);
    play.feedAt(at.x, at.y);
  });
  expose(moments);

  return {
    feed: play.feed,
    milestone: play.milestone,
    dive: page.dive,
    surface: () => {
      page.rise();
      options.floor(false);
      floor.reset();
      moments.again("floor");
    },
    destroy: () => {
      window.clearInterval(timer);
      clear();
      pointer.stop();
      stopControls();
      window.axiomEggs = undefined;
      run.end();
      glow?.();
      water.restore();
      layer.destroy();
      canvas.remove();
      if (depth) delete depth.dataset.floor;
    },
  };
}

// watchPointer follows the pointer over the sea (not touch): where it is, how far from the content (k), since when it
// has rested within 24 px, and the loops it draws on open water, which it hands to whirl.
function watchPointer(ho: HTMLElement, engine: Engine, whirl: (at: { x: number; y: number }) => void) {
  const state = { x: 0, y: 0, active: false, k: 0, rest: 0 };
  let still = { x: 0, y: 0 };
  let trail: Point[] = [];
  const onMove = (e: PointerEvent) => {
    if (e.pointerType === "touch") return;
    const { x, y, inside } = engine.local(e);
    const now = performance.now();
    state.x = x;
    state.y = y;
    state.active = inside;
    state.k = e.target instanceof Element && e.target.closest(quiet) ? 0 : openness(ho, e.clientX, e.clientY);
    if (Math.hypot(x - still.x, y - still.y) > 24) {
      still = { x, y };
      state.rest = now;
    }
    if (!state.active || state.k < 0.8) {
      trail = [];
      return;
    }
    trail = [...trail.filter((q) => now - q.t < 2400), { x, y, t: now }];
    const at = loops(trail);
    if (at) {
      trail = [];
      whirl(at);
    }
  };
  const onLeave = () => {
    state.active = false;
  };
  ho.addEventListener("pointermove", onMove, { passive: true });
  ho.addEventListener("pointerleave", onLeave);
  return {
    state,
    stop: () => {
      ho.removeEventListener("pointermove", onMove);
      ho.removeEventListener("pointerleave", onLeave);
    },
  };
}
