// The sea behind the website's first screen and its entrance (#747, port design §8.2): the board's own engines,
// ocean-orig.js and hero-enter.js as @axiom/boards generates them, loaded after the first content (useEffectModule).
// They run unedited; this module only changes who drives them. The sea's own requestAnimationFrame is replaced by the
// shared loop (loop.ts), so it rests while the tab is hidden or its owner is out of view; the entrance is stepped by
// the same frames through its seek, the board's test hook. Under reduced motion nothing is scheduled: the sea is one
// frame drawn from a fixed seed, and the entrance is at its end.
import "@axiom/boards/engines/ocean-orig.js";
import "@axiom/boards/engines/hero-enter.js";
import { own, reducedMotion } from "./loop";

// What the page and the preference effects use of the engine; its own members are the engine's (ocean-orig.js).
export type OceanEngine = {
  readonly canvas: HTMLCanvasElement;
  held: boolean;
  schedule(): void;
  tick(dt: number): void;
  still(): void;
  setLight(light: boolean): void;
  destroy(): void;
};

type Entrance = { play(variant?: string): void; seek(ms: number): void; rise(): void; destroy(): void };

declare global {
  interface Window {
    AxiomOceanOrig: { Engine: new (canvas: HTMLCanvasElement, roster: object[], options: object) => OceanEngine };
    AxiomEnter: new (root: HTMLElement, engine: OceanEngine, options: object) => Entrance;
  }
}

// The board's sea (Site.dc.html ROSTER and the options its boot passes the engine).
const roster = [
  { kind: "hammerhead", size: 1 },
  { kind: "classic", size: 0.72 },
  { kind: "classic", size: 0.58 },
  { kind: "classic", size: 0.46 },
];
const options = {
  font: '"Geist Mono", ui-monospace, Menlo, monospace',
  whale: null,
  glowFish: true,
  bgLight: "#030302",
  bgDark: "#111113",
  sizeK: 0.64,
  maxLen: 640,
  warm: 2,
  virtualScale: 1,
  hold: true,
};
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

// startSea starts the sea on root, the frame the board's markup draws (.tr: its canvas.sea, its cover and its .ho),
// in the light page's picture when light is set. owner is the part of the page the sea is for: the sea is fixed
// behind the page and always in view, so it rests while its owner is out of view.
export function startSea(root: HTMLElement, owner: Element, light: boolean): Sea {
  const canvas = root.querySelector<HTMLCanvasElement>("canvas.sea:not(.cover)");
  if (!canvas) {
    throw new Error("startSea: the root has no canvas.sea");
  }
  const still = reducedMotion();
  const make = () => new window.AxiomOceanOrig.Engine(canvas, roster, { ...options, light });
  const engine = still ? withSeed(stillSeed, make) : make();
  // its frames come from the shared loop: the engine's own scheduling (hold, its observer) must never start one
  engine.schedule = () => {};
  if (still) {
    root.dataset.enter = "done";
    engine.still();
    // A resized canvas is blank, and a new scheme only repaints the engine's ground, until the sea is drawn again;
    // no frame will come to draw it. Whoever switches the scheme (the page, the theme switch) goes through setLight.
    const setLight = engine.setLight.bind(engine);
    engine.setLight = (light) => {
      setLight(light);
      engine.still();
    };
    const resized = new ResizeObserver(() => engine.still());
    resized.observe(canvas);
    return {
      engine,
      destroy: () => {
        resized.disconnect();
        engine.destroy();
      },
    };
  }
  // The tide: the sea rises behind a crest of ~ and the sharks swim in. The words are on the page from its first
  // frame, so they do not rise with it (port design R11); the logo plays it again, as on the board.
  const entrance = new window.AxiomEnter(root, engine, {
    scope: root.querySelector(".ho"),
    pick: () => "tide",
    manual: true,
  });
  entrance.rise = () => {};
  let elapsed = 0;
  const play = entrance.play.bind(entrance);
  entrance.play = (variant) => {
    elapsed = 0;
    play(variant);
  };
  entrance.play("tide");
  const release = own(owner, (dt) => {
    if (root.dataset.enter !== "done") {
      elapsed += dt * 1000;
      entrance.seek(elapsed);
    }
    if (!engine.held) {
      engine.tick(dt);
    }
  });
  return {
    engine,
    destroy: () => {
      release();
      entrance.destroy();
      engine.destroy();
    },
  };
}
