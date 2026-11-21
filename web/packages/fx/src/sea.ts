// The sea behind the website's first screen and its entrance (#747, port design §8.2): the board's own engines,
// ocean-orig.js and hero-enter.js as @axiom/boards generates them, loaded after the first content (useEffectModule).
// They run unedited; this module only changes who drives them. The sea's own requestAnimationFrame is replaced by the
// shared loop (loop.ts), so it rests while the tab is hidden or its owner is out of view; the entrance is stepped by
// the same frames through its seek, the board's test hook.
import "@axiom/boards/engines/hero-enter.js";
import { own } from "./loop";
import { buildSea, type OceanEngine, pacedTick, type Sea } from "./ocean";

export type { OceanEngine, Sea } from "./ocean";

type Slot = { el: HTMLElement; text: string; chars: string[]; out: string };
type Entrance = {
  play(variant?: string): void;
  seek(ms: number): void;
  finish(): void;
  rise(): void;
  destroy(): void;
  chars: HTMLElement[];
  slots: Slot[];
};

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
  const scope = root.querySelector(".ho") ?? root;
  // Each visit picks between tide and decode at random (hero-enter.js default pick); on tide the words stay in place
  // from the first frame (port design R11), and clicking the logo replays in the current language.
  // the entrance marks the root as waiting when it is made, over a home page settling back from sign-in, which keeps its mark
  const settled = root.dataset.enter === "done";
  const entrance = new window.AxiomEnter(root, engine, {
    scope,
    manual: true,
  });
  // the sea is made held for the entrance to let go of; a settled page has none, so it lets go itself
  if (settled) {
    root.dataset.enter = "done";
    engine.held = false;
  }
  entrance.rise = () => {};
  let elapsed = 0;
  const play = entrance.play.bind(entrance);
  entrance.play = (variant) => {
    if (root.dataset.enter === "done") {
      entrance.chars = [...scope.querySelectorAll<HTMLElement>("h1 .ch")];
      for (const slot of entrance.slots) {
        const text = slot.el.textContent ?? "";
        slot.text = text;
        slot.chars = [...text];
        slot.out = "";
      }
    }
    elapsed = 0;
    play(variant);
  };
  // Skip the cold entrance when the home page is settling back from sign-in (behaviour.ts marks data-enter="done").
  if (root.dataset.enter !== "done") entrance.play();
  // the frame of the owner and of whoever keeps the sea awake: on a frame both are given, the sea steps once
  let stepped = -1;
  const step = pacedTick(engine);
  const frame = (dt: number, now: number) => {
    if (now === stepped) return;
    stepped = now;
    if (root.dataset.enter !== "done") {
      elapsed += dt * 1000;
      entrance.seek(elapsed);
    }
    if (!engine.held) {
      step(dt);
    }
  };
  const release = own(owner, frame);
  return {
    engine,
    awake: (element) => own(element, frame),
    settle: () => {
      if (root.dataset.enter !== "done") entrance.finish();
    },
    destroy: () => {
      release();
      entrance.destroy();
      engine.destroy();
    },
  };
}
