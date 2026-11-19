// The board's sea engine (ocean-orig.js as @axiom/boards generates it), driven by the shared loop instead of its own
// requestAnimationFrame: the website's first screen (sea.ts) and the console's sign-in pages (login.ts) build theirs
// here.
import "@axiom/boards/engines/ocean-orig.js";

// What the page and the preference effects use of the engine; its own members are the engine's (ocean-orig.js).
export type OceanEngine = {
  readonly canvas: HTMLCanvasElement;
  readonly renderer: { readonly cw: number; readonly ch: number };
  held: boolean;
  schedule(): void;
  tick(dt: number): void;
  still(): void;
  setLight(light: boolean): void;
  destroy(): void;
  // the engine's sharks: the whale's ghost joins them, so the fish steer around it
  world?: { sharks: object[] };
};

declare global {
  interface Window {
    AxiomOceanOrig: { Engine: new (canvas: HTMLCanvasElement, roster: object[], options: object) => OceanEngine };
  }
}

const stillSeed = 0x5ea;

// withSeed runs make with Math.random replaced by a fixed-seed generator (mulberry32): the engine draws its sea from
// Math.random while it is built and warmed up, so the still frame is the same on every visit.
function withSeed<T>(seed: number, make: () => T): T {
  const random = Math.random;
  let state = seed;
  Math.random = () => {
    state = (state + 0x6d2b79f5) | 0;
    let t = Math.imul(state ^ (state >>> 15), 1 | state);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  try {
    return make();
  } finally {
    Math.random = random;
  }
}

export type Sea = { readonly engine: OceanEngine; destroy: () => void };

// buildSea makes an engine whose frames come from the shared loop: the engine's own scheduling (hold, its observer)
// must never start one, so it is built held and its scheduler is replaced. Under reduced motion it is built from a
// fixed seed, so its still frame is the same on every visit.
export function buildSea(canvas: HTMLCanvasElement, specs: object[], opts: object, still: boolean): OceanEngine {
  const make = () => new window.AxiomOceanOrig.Engine(canvas, specs, { ...opts, hold: true });
  const engine = still ? withSeed(stillSeed, make) : make();
  engine.schedule = () => {};
  return engine;
}

// stillSea draws engine once and keeps that frame up. A resized canvas is blank, and a new scheme only repaints the
// engine's ground, until the sea is drawn again; no frame will come to draw it. Whoever switches the scheme (the page,
// the theme switch) goes through setLight.
export function stillSea(engine: OceanEngine): Sea {
  engine.still();
  const setLight = engine.setLight.bind(engine);
  engine.setLight = (light) => {
    setLight(light);
    engine.still();
  };
  const resized = new ResizeObserver(() => engine.still());
  resized.observe(engine.canvas);
  return {
    engine,
    destroy: () => {
      resized.disconnect();
      engine.destroy();
    },
  };
}
