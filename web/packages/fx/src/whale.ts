// The humpback of the website's easter egg (#747, port design §8.2): the board's own whale.js and whale-egg.js as
// @axiom/boards generates them, loaded the first time the egg is called (typing deepseek, the first screen's
// deepseek, a deepseek model met further down). They run unedited on the sea's engine: the whale draws on the frame's
// canvas.wh and hands its body to the sea as a ghost the fish and sharks steer around. The caller decides when a visit
// may happen (never under reduced motion, a chance meeting once per page load); this module plays one and is torn down
// when it ends.
import "@axiom/boards/engines/whale.js";
import "@axiom/boards/engines/whale-egg.js";
import type { OceanEngine } from "./sea";

type Egg = {
  t0: number;
  cv: HTMLCanvasElement;
  done: () => void;
  summon: (anchor: Element | null, force: boolean) => boolean;
  setTheme: (dark: boolean) => void;
  destroy: () => void;
};

declare global {
  interface Window {
    AxiomWhaleEgg: new (root: HTMLElement, engine: OceanEngine, options: object) => Egg;
  }
}

export type Whale = {
  // summon starts a visit that heads for anchor (an element it passes by) and reports whether one began.
  summon: (anchor: Element | null) => boolean;
  // setTheme recolours the whale for the page's scheme, mid-visit too.
  setTheme: (dark: boolean) => void;
  // destroy ends the visit wherever it is: the canvas is cleared and what the visit lit or thinned is put back.
  destroy: () => void;
};

export type WhaleOptions = {
  dark: boolean;
  // the narrow layout, where the whale passes behind the glass instead of the open water
  phone: boolean;
  // the visit is over: the whale has left and its canvas is clear
  onDone: () => void;
};

// startWhale builds the egg on root, the frame the board's markup draws (.tr: its canvas.wh and .ho), over the sea's
// engine. The egg's own triggers (the keys, the strip, the connect card) stay on too; a second call while the whale is
// out is answered false by it.
export function startWhale(root: HTMLElement, engine: OceanEngine, { dark, phone, onDone }: WhaleOptions): Whale {
  const egg = new window.AxiomWhaleEgg(root, engine, {
    dark: () => dark,
    phone,
    scope: root.querySelector(".ho"),
    rowSummon: false,
    fam: ".hs > .main .fam",
  });
  // the egg is built on the first call, but its clock is the page's (a chance meeting waits for the page to be a few
  // seconds old, and the caller checks that against the same clock)
  egg.t0 = 0;
  const done = egg.done.bind(egg);
  egg.done = () => {
    done();
    onDone();
  };
  return {
    summon: (anchor) => egg.summon(anchor, true),
    setTheme: (dark) => egg.setTheme(dark),
    // The egg's own destroy only stops its frames and the timer that would put its sweep's light out: what is on the
    // canvas, and the light and the thinned glass of a visit cut short, would stay.
    destroy: () => {
      egg.destroy();
      done();
      const canvas = egg.cv.getContext("2d");
      canvas?.setTransform(1, 0, 0, 1, 0, 0);
      canvas?.clearRect(0, 0, egg.cv.width, egg.cv.height);
      for (const lit of root.querySelectorAll(".wh-lit")) lit.classList.remove("wh-lit");
      for (const lit of root.querySelectorAll(".ho .mr.lit, .ho .mr .mn.lit, .ho .lens.lit"))
        lit.classList.remove("lit");
    },
  };
}
