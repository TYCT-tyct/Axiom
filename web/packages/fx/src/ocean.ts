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
