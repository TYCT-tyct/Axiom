// Clawd, the website's second easter egg (#747): the board's own clawd-cast.js as @axiom/boards generates it, run
// unedited on the sea's engine and the shared frame loop. Its two places: the dive ("claude" pressed in the first
// screen's strip, or typed, brings a jellyfish with a team of Clawds that follow the reading depth down the right edge
// and jump into the sand at the sea floor) and the wordmark (Clawds living in the sand behind the letters, which turn
// into Clawds). The dive draws on the frame's canvas.cw; the wordmark part is drawn by the sea bed's dune through the
// hooks the cast puts on its canvas (canvas.art.__fx, bed-script.ts).
import "@axiom/boards/engines/clawd-cast.js";
import { own } from "./loop";
import type { OceanEngine } from "./ocean";
import { water } from "./water";

type BedInfo = {
  cols: number;
  rows: number;
  ox: number;
  CW: number;
  CH: number;
  t: number;
  mask: Float32Array;
  colL: Int16Array;
  letters: { a: number; b: number }[];
  topAt: (x: number, t: number) => number;
};

type Actor = { q: unknown[]; cur: unknown; y: number; rev: number; busy: boolean; away: boolean };

type CastFx = {
  skip: Set<number>;
  dotDy: number;
  fast: boolean;
  reset?: () => void;
};

type Cast = {
  dead?: boolean;
  H: number;
  raf: number;
  art: HTMLCanvasElement | null;
  fx: CastFx;
  F: { st: string } | null;
  pend: object | null;
  drew: boolean;
  bed: {
    acts: Actor[];
    morphs: Record<string, unknown>;
    timers: { at: number; fn: () => void }[];
    info: BedInfo | null;
    ripple: number;
  };
  frame(dt: number): void;
  artRect(): { top: number; left: number; w: number; h: number } | null;
  floorInView(): boolean;
  whaleOut(): boolean;
  home(actor: Actor): number;
  drop(anchor: Element | null): boolean;
  morph(li: number, life: number): void;
  ripple(): void;
  peek(li: number, color: boolean): void;
  hop(): void;
  call(): boolean;
  busy(): boolean;
  destroy(): void;
};

declare global {
  interface Window {
    AxiomClawdCast: new (root: HTMLElement, engine: object, options: object) => Cast;
  }
}

export type Clawd = {
  // call is "claude" typed: the team comes (or one more joins it), or, at the sea floor, the letters answer
  call: () => void;
  // floor hands it the sea bed's canvas, once the bed's screen is there (null when it goes)
  floor: (art: HTMLCanvasElement | null) => void;
  destroy: () => void;
};

export type ClawdOptions = {
  // the sea's engine, read each time it is used: the sea may come up after Clawd
  engine: () => OceanEngine | undefined;
  dark: () => boolean;
  // the narrow layout: two Clawds in the team instead of three
  phone: boolean;
};

// The sea's engine as the cast reads it, member by member, each time: the sea may come up after Clawd.
const seaOf = (engine: () => OceanEngine | undefined) => ({
  get world() {
    return engine()?.world;
  },
  get canvas() {
    return engine()?.canvas;
  },
  get vs() {
    return (engine() as { vs?: number } | undefined)?.vs;
  },
});

// The whale's egg as the cast asks after it: whether it is out, and its canvas, which the cast cuts off along the sea
// floor's letters while it passes behind them.
const eggOf = (root: HTMLElement) => ({
  cv: root.querySelector(":scope > canvas.wh"),
  get wh() {
    return water.whale?.wh;
  },
  active: () => !!water.whale?.active(),
});

// hasTiles checks the exact condition clawd-cast's morph uses to collect source tiles for letter li. White-label site
// names such as "Acme Relay" have a space at index 4 with no tiles; morphing it would put undefined into parts[].s.
function hasTiles(info: BedInfo, li: number) {
  for (let c = 0; c < info.cols; c++) {
    if (info.colL[c] !== li) continue;
    const x = info.ox + (c + 0.5) * info.CW;
    const top = info.topAt(x, info.t);
    for (let r = 0; r < info.rows; r++) {
      const y = (r + 0.5) * info.CH;
      if (y >= top) break;
      if ((info.mask[r * info.cols + c] ?? 0) > 0.3) return true;
    }
  }
  return false;
}

const hasAnyTiles = (info: BedInfo) => [0, 1, 2, 3, 4].some((li) => hasTiles(info, li));

function artOutOfView(cast: Cast) {
  const rect = cast.artRect();
  return !rect?.h || rect.top > cast.H || rect.top + rect.h < 0;
}

function floorReady(cast: Cast) {
  const info = cast.bed.info;
  return Boolean(cast.art?.isConnected && info && info.letters.length >= 5 && !artOutOfView(cast));
}

function hasFloorWork(cast: Cast) {
  const b = cast.bed;
  return Boolean(b.timers.length || Object.keys(b.morphs).length || b.ripple > performance.now() || cast.fx.skip.size);
}

// settleFloor clears queued or in-flight wordmark actions when the floor leaves the viewport or stops running, so
// paused dune draws never leave busy() true and hold the whale's turn forever.
function settleFloor(cast: Cast) {
  const b = cast.bed;
  b.timers = [];
  b.morphs = {};
  b.ripple = 0;
  cast.fx.skip.clear();
  cast.fx.dotDy = 0;
  cast.fx.fast = false;
  for (const a of b.acts) {
    a.q = [];
    a.cur = null;
    if (b.info) a.y = cast.home(a);
    a.rev = 0;
    a.busy = false;
    a.away = true;
  }
}

type Pending = { at: number; floor: boolean; run: () => void };
const waitLimit = 15_000;
type Gate = (floor: boolean, run: () => void) => boolean;

function wrapFloorActions(cast: Cast, runOrWait: Gate) {
  const origMorph = cast.morph.bind(cast);
  cast.morph = (li, life) => {
    const info = cast.bed.info;
    if (!floorReady(cast) || !info || !hasTiles(info, li)) return;
    runOrWait(true, () => {
      const cur = cast.bed.info;
      if (floorReady(cast) && cur && hasTiles(cur, li)) origMorph(li, life);
    });
  };
  const origRipple = cast.ripple.bind(cast);
  cast.ripple = () => {
    const info = cast.bed.info;
    if (!floorReady(cast) || !info || !hasAnyTiles(info)) return;
    runOrWait(true, () => {
      const cur = cast.bed.info;
      if (floorReady(cast) && cur && hasAnyTiles(cur)) origRipple();
    });
  };
  const origPeek = cast.peek.bind(cast);
  cast.peek = (li, color) => {
    if (!cast.whaleOut() && floorReady(cast)) origPeek(li, color);
  };
  const origHop = cast.hop.bind(cast);
  cast.hop = () => {
    if (!cast.whaleOut() && floorReady(cast)) origHop();
  };
}

function wrapDropAction(cast: Cast, runOrWait: Gate, ride: () => void) {
  const origDrop = cast.drop.bind(cast);
  cast.drop = (anchor) => {
    if (cast.F && cast.F.st !== "gone") {
      const joined = origDrop(anchor);
      ride();
      return joined;
    }
    let dropped = false;
    runOrWait(false, () => {
      if (cast.floorInView()) {
        cast.ripple();
        dropped = true;
      } else {
        dropped = origDrop(anchor);
      }
    });
    return dropped;
  };
}

// guardCast puts one shared whaleOut() exclusion and floor-readiness check in front of every Clawd entry point (the
// strip, typing "claude", clicking a floor letter, and dive landings) without editing the generated engine file.
function guardCast(cast: Cast, ride: () => void) {
  let pending: Pending | null = null;
  const clearExpired = () => {
    if (pending && performance.now() - pending.at >= waitLimit) pending = null;
  };
  const settle = () => {
    settleFloor(cast);
    if (pending?.floor) pending = null;
  };
  const runOrWait: Gate = (floor, run) => {
    if (cast.dead) return false;
    if (cast.whaleOut()) {
      pending = { at: performance.now(), floor, run };
      ride();
      return false;
    }
    run();
    ride();
    return true;
  };
  cast.fx.reset = () => {
    settle();
    cast.bed.info = null;
    ride();
  };
  wrapFloorActions(cast, runOrWait);
  wrapDropAction(cast, runOrWait, ride);
  const origBusy = cast.busy.bind(cast);
  cast.busy = () => {
    clearExpired();
    if ((hasFloorWork(cast) || pending?.floor) && !floorReady(cast)) settle();
    return Boolean(pending || origBusy());
  };
  return {
    waiting: () => Boolean(pending),
    settle,
    clearPending: () => {
      pending = null;
    },
    flush: () => {
      clearExpired();
      if ((hasFloorWork(cast) || pending?.floor) && !floorReady(cast)) settle();
      if (pending && !cast.whaleOut()) {
        const next = pending;
        pending = null;
        next.run();
      }
    },
  };
}

type Guard = ReturnType<typeof guardCast>;

function bindFloor(cast: Cast, guard: Guard, tick: (dt: number, now: number) => void, ride: () => void) {
  let bed = () => {};
  let watchBed: IntersectionObserver | undefined;
  let boundArt: HTMLCanvasElement | null = null;
  const set = (art: HTMLCanvasElement | null) => {
    if (art && art === boundArt) return;
    bed();
    bed = () => {};
    watchBed?.disconnect();
    watchBed = undefined;
    const held = cast.art as (HTMLCanvasElement & { __fx?: CastFx }) | null;
    if (held && held.__fx === cast.fx) delete held.__fx;
    if (held !== art) {
      guard.settle();
      cast.bed.info = null;
    }
    cast.art = art;
    boundArt = art;
    if (!art) {
      ride();
      return;
    }
    (art as HTMLCanvasElement & { __fx?: CastFx }).__fx = cast.fx;
    bed = own(art, tick);
    watchBed = new IntersectionObserver((entries) => {
      if (entries.some((entry) => !entry.isIntersecting)) {
        guard.settle();
        ride();
      }
    });
    watchBed.observe(art);
  };
  return {
    set,
    stop: () => {
      bed();
      watchBed?.disconnect();
    },
  };
}

// startClawd builds the cast on root, the frame the board's markup draws (.tr: its canvas.cw, canvas.wh, .hs and .ho).
export function startClawd(root: HTMLElement, { engine, dark, phone }: ClawdOptions): Clawd {
  const options = { dark, phone, scope: root.querySelector(".ho"), egg: eggOf(root) };
  const cast = new window.AxiomClawdCast(root, seaOf(engine), options);
  // its own requestAnimationFrame is replaced by the shared loop, before its first frame
  cancelAnimationFrame(cast.raf);
  water.clawd = cast;

  let last = -1;
  let team: (() => void) | null = null;
  const layer = root.querySelector(":scope > canvas.cw") ?? root;
  const tick = (dt: number, now: number) => {
    if (now === last) return;
    last = now;
    guard.flush();
    cast.frame(Math.min(0.05, dt));
    ride();
  };
  const ride = () => {
    const out = Boolean(guard.waiting() || cast.pend || cast.drew || (cast.F && cast.F.st !== "gone"));
    if (out && !team) team = own(layer, tick);
    else if (!out && team) {
      team();
      team = null;
    }
  };
  const guard = guardCast(cast, ride);
  const hero = own(root.querySelector(".hs > main") ?? root, tick);
  const floor = bindFloor(cast, guard, tick, ride);
  const initial = root.querySelector<HTMLCanvasElement>("canvas.art");
  if (initial) floor.set(initial);
  return {
    call: () => cast.call(),
    floor: floor.set,
    destroy: () => {
      guard.clearPending();
      guard.settle();
      hero();
      floor.stop();
      team?.();
      cast.destroy();
      if (water.clawd === cast) water.clawd = null;
    },
  };
}
