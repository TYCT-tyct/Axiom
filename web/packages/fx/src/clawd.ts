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

type Cast = {
  raf: number;
  art: HTMLCanvasElement | null;
  fx: object;
  F: { st: string } | null;
  pend: object | null;
  drew: boolean;
  frame(dt: number): void;
  drop(anchor: Element | null): boolean;
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

// startClawd builds the cast on root, the frame the board's markup draws (.tr: its canvas.cw, canvas.wh, .hs and .ho).
export function startClawd(root: HTMLElement, { engine, dark, phone }: ClawdOptions): Clawd {
  const options = { dark, phone, scope: root.querySelector(".ho"), egg: eggOf(root) };
  const cast = new window.AxiomClawdCast(root, seaOf(engine), options);
  // its own requestAnimationFrame is replaced by the shared loop, before its first frame
  cancelAnimationFrame(cast.raf);
  water.clawd = cast;

  // The cast's frames come from whichever of its places is in view: the first screen (the strip's words wink there),
  // the sea floor, and, while the team is out, waiting for the whale or its layer still to be cleared, the layer
  // itself, which spans the frame. Two of them in view at once still make one frame.
  let last = -1;
  let team: (() => void) | null = null;
  const tick = (dt: number, now: number) => {
    if (now === last) return;
    last = now;
    cast.frame(Math.min(0.05, dt));
    ride();
  };
  const layer = root.querySelector(":scope > canvas.cw") ?? root;
  const ride = () => {
    const out = !!(cast.pend || cast.drew || (cast.F && cast.F.st !== "gone"));
    if (out && !team) team = own(layer, tick);
    else if (!out && team) {
      team();
      team = null;
    }
  };
  // every way the team sets out goes through drop (the strip's word, the word typed, the wait for the whale)
  const drop = cast.drop.bind(cast);
  cast.drop = (anchor) => {
    const dropped = drop(anchor);
    ride();
    return dropped;
  };
  const hero = own(root.querySelector(".hs > main") ?? root, tick);
  let bed = () => {};
  const floor = (art: HTMLCanvasElement | null) => {
    bed();
    bed = () => {};
    const held = cast.art as (HTMLCanvasElement & { __fx?: object }) | null;
    if (held && held.__fx === cast.fx) delete held.__fx;
    cast.art = art;
    if (!art) return;
    (art as HTMLCanvasElement & { __fx?: object }).__fx = cast.fx;
    bed = own(art, tick);
  };
  floor(root.querySelector<HTMLCanvasElement>("canvas.art"));
  return {
    call: () => cast.call(),
    floor,
    destroy: () => {
      hero();
      bed();
      team?.();
      cast.destroy();
      if (water.clawd === cast) water.clawd = null;
    },
  };
}
