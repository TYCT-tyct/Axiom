// The sea behind the website's first screen and its entrance (#747, port design §8.2): the board's own engines,
// ocean-orig.js and hero-enter.js as @axiom/boards generates them, loaded after the first content (useEffectModule).
// They run unedited; this module only changes who drives them. The sea's own requestAnimationFrame is replaced by the
// shared loop (loop.ts), so it rests while the tab is hidden or its owner is out of view; the entrance is stepped by
// the same frames through its seek, the board's test hook.
import "@axiom/boards/engines/hero-enter.js";
import { own } from "./loop";
import { buildSea, type OceanEngine, type Sea } from "./ocean";

export type { OceanEngine, Sea } from "./ocean";

type Entrance = { play(variant?: string): void; seek(ms: number): void; rise(): void; destroy(): void };

declare global {
  interface Window {
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
};

// startSea starts the sea on root, the frame the board's markup draws (.tr: its canvas.sea, its cover and its .ho),
// in the light page's picture when light is set. owner is the part of the page the sea is for: the sea is fixed
// behind the page and always in view, so it rests while its owner is out of view.
export function startSea(root: HTMLElement, owner: Element, light: boolean): Sea {
  const canvas = root.querySelector<HTMLCanvasElement>("canvas.sea:not(.cover)");
  if (!canvas) {
    throw new Error("startSea: the root has no canvas.sea");
  }
  const engine = buildSea(canvas, roster, { ...options, light });
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
