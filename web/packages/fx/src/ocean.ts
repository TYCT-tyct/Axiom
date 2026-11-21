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
  // draw: false steps the water without drawing it; the next drawn frame takes the time it skipped
  tick(dt: number, draw?: boolean): void;
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

export type Sea = {
  readonly engine: OceanEngine;
  // awake keeps the sea going while element is in view too, until the returned function is called (a moment of the
  // eggs plays wherever the reader is)
  awake: (element: Element) => () => void;
  settle?: () => void;
  destroy: () => void;
};

// buildSea makes an engine whose frames come from the shared loop: the engine's own scheduling (hold, its observer)
// must never start one, so it is built held and its scheduler is replaced.
export function buildSea(canvas: HTMLCanvasElement, specs: object[], opts: object): OceanEngine {
  const engine = new window.AxiomOceanOrig.Engine(canvas, specs, { ...opts, hold: true });
  engine.schedule = () => {};
  return engine;
}

// A drawing of the sea that costs more than this (ms; a slow machine, a big window) leaves too little of a 60 Hz frame
// to the motion over it: a departure's tiles, the sign-in page's solid, the screens.
const drawBudget = 6;

// pacedTick is a frame of the sea: the water steps every frame, and is drawn every frame while a drawing costs little,
// else every other frame, the skipped frame's time passed on to the drawn one (the engine's tick(dt, draw)). The sea is
// drawn in cells whose marks change a few times a second, so every other frame does not read as slower, and a slow
// machine keeps whole frames for what moves over it. The cost is measured as it goes, so a machine fast enough draws
// every frame.
export function pacedTick(engine: OceanEngine): (dt: number) => void {
  let cost = 0;
  let skipped = false;
  return (dt) => {
    if (cost > drawBudget && !skipped) {
      skipped = true;
      engine.tick(dt, false);
      return;
    }
    skipped = false;
    const start = performance.now();
    engine.tick(dt);
    const took = performance.now() - start;
    cost = cost ? cost * 0.8 + took * 0.2 : took;
  };
}
