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

// openness is how far a point (in the viewport) is from the page's content: 0 at it, 1 from 80 px out (respond.js
// quiet); a small block ([data-avoid=s]) is padded 14 px, the rest 10
function openness(ho: HTMLElement, x: number, y: number) {
  let d = Number.POSITIVE_INFINITY;
  for (const el of ho.querySelectorAll(content)) {
    const r = el.getBoundingClientRect();
    if (!r.width) continue;
    const pad = el.matches("[data-avoid=s]") ? 14 : 10;
    d = Math.min(
      d,
      Math.hypot(Math.max(r.left - pad - x, 0, x - r.right - pad), Math.max(r.top - pad - y, 0, y - r.bottom - pad)),
    );
  }
  return Math.min(1, Math.max(0, (d - 8) / 72));
}

// eased is the pointer's strength easing toward where it should be, as respond.js eases it every frame
// (k += (target - k) * (1 - e^(-10 dt))): read at any time, it is where those frames would have taken it
function eased() {
  let k = 0;
  let target = 0;
  let at = 0;
  const now = (t: number) => {
    k = target + (k - target) * Math.exp(-Math.max(0, t - at) / 100);
    at = t;
    return k;
  };
  return {
    now,
    aim: (t: number, value: number) => {
      now(t);
      target = value;
    },
  };
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

// the reader taking over the page: what ends a scroll the page started (behaviour.ts takeOver)
const takeOver = ["wheel", "touchstart", "pointerdown", "keydown"] as const;
// eggs.js surface: cubic in and out
const inOut = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - (-2 * t + 2) ** 3 / 2);

function scrollers(frame: HTMLElement, ho: HTMLElement) {
  const scroller = frame.querySelector<HTMLElement>(".hs") ?? frame;
  let rising: (() => void) | undefined;
  const stop = () => rising?.();
  return {
    scroller,
    deep: () => {
      const max = scroller.scrollHeight - scroller.clientHeight;
      return max > 0 ? scroller.scrollTop / max : 0;
    },
    dive: () => {
      stop();
      scroller.scrollTo({ top: scroller.scrollHeight, behavior: "smooth" });
    },
    // the way back up, as the design glides it: 1.6 s to the top on the shared loop, until the reader takes over
    rise: () => {
      stop();
      const from = scroller.scrollTop;
      let t = 0;
      const release = own(ho, (dt) => {
        t = Math.min(1, t + dt / 1.6);
        scroller.scrollTop = from * (1 - inOut(t));
        if (t >= 1) end();
      });
      const end = () => {
        release();
        for (const kind of takeOver) frame.removeEventListener(kind, end);
        rising = undefined;
      };
      for (const kind of takeOver) frame.addEventListener(kind, end, { passive: true });
      rising = end;
    },
    stop,
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
  nightDim: () => void;
};

// nightDepth keeps the dark sea at least 0.12 dim at night (eggs.js baseDim); the light theme's .depth is a white
// veil, so night dimming applies only while .ho is dark and follows theme switches.
function nightDepth(ho: HTMLElement, depth: HTMLElement | null) {
  let active = false;
  const sync = () => {
    if (!depth) return;
    const floor = active && ho.classList.contains("dark") ? 0.12 : 0;
    if (floor) depth.dataset.floor = String(floor);
    else delete depth.dataset.floor;
    const dim = Number(depth.dataset.dim ?? 0);
    depth.style.opacity = Math.max(dim, floor).toFixed(3);
  };
  const observed = depth ? new MutationObserver(() => active && sync()) : undefined;
  observed?.observe(ho, { attributes: true, attributeFilter: ["class"] });
  return {
    enable: () => {
      active = true;
      sync();
    },
    destroy: () => {
      observed?.disconnect();
      if (!active) return;
      active = false;
      sync();
    },
  };
}

// states colours the visit by the visitor's clock: the night's glow and darker sea (dark theme only), 1024's binary
// fish; it returns what ends the glow
function states({ layer, water, later, glow, nightDim }: Parts) {
  const at = new Date();
  const hour = at.getHours();
  if (at.getMonth() === 9 && at.getDate() === 24) {
    water.binary(true);
    later(24_000, () => water.binary(false));
  }
  if (hour < 22 && hour >= 6) return undefined;
  layer.night(0.55);
  nightDim();
  return glow();
}

// plays are the moments on the sea, each keeping it going for as long as it plays
function plays(engine: Engine, { layer, water, later }: Omit<Parts, "glow" | "nightDim">, keep: (ms: number) => void) {
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

// floorWatch offers "back to the surface" after 3 s on the sea floor (the last 1.5% of the page), and hides it
// whenever the reader leaves the floor by any means (wheel, scrollbar, keyboard or surface()).
function floorWatch(
  scroller: HTMLElement,
  deep: () => number,
  moments: ReturnType<typeof director>,
  later: (ms: number, then: () => void) => void,
  onFloor: (shown: boolean) => void,
) {
  let since = 0;
  let shown = false;
  const hide = () => {
    if (!shown) return;
    shown = false;
    onFloor(false);
    // offered again once it has faded (250 ms)
    later(250, () => {
      since = 0;
      moments.again("floor");
    });
  };
  const onScroll = () => {
    if (deep() <= 0.985) hide();
  };
  scroller.addEventListener("scroll", onScroll, { passive: true });
  return {
    check: (now: number) => {
      if (deep() <= 0.985) {
        since = 0;
        hide();
        return;
      }
      if (!since) since = now;
      if (now - since > 3000) {
        moments.earn("floor", () => {
          shown = true;
          onFloor(true);
        });
      }
    },
    hide,
    stop: () => scroller.removeEventListener("scroll", onScroll),
  };
}

// startEggs plays the eggs on frame (.tr, the board's markup) over its running sea, until destroy.
export function startEggs(frame: HTMLElement, sea: Sea, options: EggsOptions): Eggs {
  const engine = sea.engine as unknown as Engine;
  const ho = frame.querySelector<HTMLElement>(".ho") ?? frame;
  const night = nightDepth(ho, ho.querySelector<HTMLElement>(".depth"));
  const owner = frame.querySelector(".hs > main") ?? ho;
  const page = scrollers(frame, ho);
  const canvas = layerCanvas(frame, options.glow);
  const water = tend(engine);
  const layer = glyphs(canvas, ho);
  const run = keeper(ho, sea, layer.draw, layer.busy);
  const { later, clear } = timeouts();
  let glow: (() => void) | undefined;
  const parts = { layer, water, later, nightDim: night.enable, glow: () => own(owner, run.frame) };
  const moments = director({ now: () => performance.now(), hidden: () => document.hidden }, () => {
    glow = states(parts);
  });
  const play = plays(engine, { layer, water, later }, run.keep);
  const pointer = watchPointer(ho, engine, (at) => moments.earn("whirl", () => play.whirl(at.x, at.y)));
  const floor = floorWatch(page.scroller, page.deep, moments, later, options.floor);
  const tick = () => {
    if (document.hidden) return;
    const now = performance.now();
    if (new Date().getMinutes() === 59) moments.auto("tide", play.tide);
    const p = pointer.state;
    if (p.active && pointer.k() > 0.85 && now - p.rest > 4000) moments.earn("visit", () => play.visit(p.x, p.y));
    floor.check(now);
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
      floor.hide();
    },
    destroy: () => {
      window.clearInterval(timer);
      clear();
      floor.stop();
      page.stop();
      pointer.stop();
      stopControls();
      window.axiomEggs = undefined;
      run.end();
      glow?.();
      water.restore();
      layer.destroy();
      canvas.remove();
      night.destroy();
    },
  };
}

// watchPointer follows the pointer over the sea (not touch): where it is, how far from the content (k), since when it
// has rested within 24 px, and the loops it draws on open water, which it hands to whirl.
function watchPointer(ho: HTMLElement, engine: Engine, whirl: (at: { x: number; y: number }) => void) {
  const state = { x: 0, y: 0, active: false, rest: 0 };
  const strength = eased();
  let still = { x: 0, y: 0 };
  let trail: Point[] = [];
  const onMove = (e: PointerEvent) => {
    if (e.pointerType === "touch") return;
    const { x, y, inside } = engine.local(e);
    const now = performance.now();
    state.x = x;
    state.y = y;
    state.active = inside;
    strength.aim(now, e.target instanceof Element && e.target.closest(quiet) ? 0 : openness(ho, e.clientX, e.clientY));
    if (Math.hypot(x - still.x, y - still.y) > 24) {
      still = { x, y };
      state.rest = now;
    }
    if (!state.active || strength.now(now) < 0.8) {
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
    // k is how open the water under the pointer is now (0 at the content, 1 well away from it)
    k: () => strength.now(performance.now()),
    stop: () => {
      ho.removeEventListener("pointermove", onMove);
      ho.removeEventListener("pointerleave", onLeave);
    },
  };
}
