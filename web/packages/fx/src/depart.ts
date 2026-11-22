// Bidirectional tiles transition between the website and the console's sign-in pages (#747, port design §9,
// trans.js.txt STYLES.tiles) for the two-document case (direct sign-in visit returning home):
// - depart: on the website (.tr), breaks the pressed button (and any visible sea-floor wordmark tiles via
//   frame.__axCast.wordTiles()) into sea tiles that stream into the form panel, reversible midway.
// - departLogin: on a console Login-board page (.lg), plays the mirror in reverse — Morph releases its solid back into
//   sharks, the form panel breaks into tiles streaming back to the trigger, reversible midway.
// - arriveHome: on the website (.tr) after returning from sign-in, plays the short closing settle of tiles returning
//   into the header button so the landing does not replay the cold tide entrance.
import type { OriginRect } from "./arrival";
import { own } from "./loop";
import { type Box, drawButton, drawCells, drive, E, gridOf, inkOf, panel, planTiles, toBox } from "./panel";
import type { OceanEngine } from "./sea";
import { clamp, drawWord } from "./word-tiles";

// overlay is the canvas the run draws on, fixed over the whole window and above the frame's own layers.
function overlay(frame: HTMLElement, W: number, H: number) {
  const canvas = document.createElement("canvas");
  canvas.setAttribute("aria-hidden", "true");
  Object.assign(canvas.style, {
    position: "fixed",
    inset: "0",
    width: "100%",
    height: "100%",
    zIndex: "60",
    pointerEvents: "none",
  });
  frame.append(canvas);
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.round(W * dpr);
  canvas.height = Math.round(H * dpr);
  return { canvas, x: canvas.getContext("2d"), dpr };
}

export type Departure = {
  readonly dir: 1 | -1;
  // how many ms remain on the wall clock in the current direction
  remaining: () => number;
  // turns the transition round midway from where it currently stands
  reverse: (onDone?: (end: 1 | -1) => void) => void;
  destroy: () => void;
};

// what fades as the homepage leaves (trans.js's tiles frame), each over [from, from + fade] ms of the engine's clock;
// the header's links (all but the mark, which travels) go too, each later by its distance from the press (barLeaving)
const leaving = [
  [".hs > main h1, .hs > main .lede", 120, 360],
  [".cta, .fam", 200, 200],
  [".hs > .under, .isl", 60, 360],
  [".glow", 0, 460],
] as const;

type Fade = { el: HTMLElement; from: number; fade: number };

// The header's links un-write nearest the press first (trans.js: 200 ms over 1800 px, then 140 ms each).
function barLeaving(frame: HTMLElement, b: Box): Fade[] {
  const cx = b.x + b.w / 2;
  const cy = b.y + b.h / 2;
  return [...frame.querySelectorAll<HTMLElement>(".bar .nav > *")].map((el) => {
    const r = el.getBoundingClientRect();
    const d = Math.hypot(r.x + r.width / 2 - cx, r.y + r.height / 2 - cy);
    return { el, from: 40 + (d / 1800) * 200, fade: 140 };
  });
}

function leavingParts(frame: HTMLElement, b: Box): Fade[] {
  const page = leaving.flatMap(([selector, from, fade]) =>
    [...frame.querySelectorAll<HTMLElement>(selector)].map((el) => ({ el, from, fade })),
  );
  return [...page, ...barLeaving(frame, b)];
}

// Shared scene setup and frame paint for depart, departLogin, and arriveHome.
function prepare(frame: HTMLElement, b: Box, P: Box, sea?: OceanEngine, button?: HTMLElement, withWord = false) {
  const W = window.innerWidth;
  const H = window.innerHeight;
  const grid = gridOf(sea);
  const run = planTiles(b, P, grid, withWord ? frame : undefined);
  const ink = inkOf(frame, button);
  const { canvas, x, dpr } = overlay(frame, W, H);
  return { W, H, grid, run, ink, canvas, x, dpr, b };
}
type Scene = ReturnType<typeof prepare>;

function paintScene(scene: Scene, t: number, withWord = false, lands = true) {
  const { x, dpr, W, H, run, b, ink, grid } = scene;
  if (!x) return;
  x.setTransform(dpr, 0, 0, dpr, 0, 0);
  x.clearRect(0, 0, W, H);
  drawCells(x, run.cells, run.gh, t, ink);
  drawButton(x, run.bits, b, t, ink, grid, lands);
  if (withWord) drawWord(x, run.word, run.wm, t, ink.tile, E.emph);
  x.globalAlpha = 1;
}

function prepareSite(frame: HTMLElement, button: HTMLElement, sea: OceanEngine | undefined, withWord: boolean) {
  return prepare(
    frame,
    toBox(button.getBoundingClientRect()),
    panel(window.innerWidth, window.innerHeight),
    sea,
    button,
    withWord,
  );
}

function runDeparture(
  canvas: HTMLCanvasElement,
  span: number,
  start: number,
  paint: (elapsed: number, dir: 1 | -1) => void,
  cleanup: () => void,
): Departure {
  let cleaned = false;
  const cleanOnce = () => {
    if (cleaned) return;
    cleaned = true;
    cleanup();
  };
  const clock = drive({
    canvas,
    span,
    start,
    holdAtEnd: true,
    paint,
    onEnd: (end) => {
      if (end === -1) cleanOnce();
    },
  });
  return {
    get dir() {
      return clock.dir;
    },
    remaining: clock.remaining,
    reverse: clock.reverse,
    destroy: () => {
      clock.stop();
      cleanOnce();
    },
  };
}

// depart draws the departure from the button pressed, on the website's frame (.tr), over its sea: the run started at
// start (performance.now()) and lands its stream span ms later, on the wall clock. Calling reverse() midway turns every
// flying tile and faded layer back from where it stands; destroy() puts the page (and seabed wordmark) back.
export function depart(
  frame: HTMLElement,
  button: HTMLElement,
  start: number,
  span: number,
  sea?: OceanEngine,
): Departure {
  const scene = prepareSite(frame, button, sea, true);
  const speed = scene.run.landed / span;
  const faded = leavingParts(frame, scene.b);
  return runDeparture(
    scene.canvas,
    span,
    start,
    (elapsed) => {
      const t = elapsed * speed;
      button.style.visibility = t >= 60 ? "hidden" : "";
      for (const { el, from, fade } of faded) el.style.opacity = String(1 - E.out(clamp((t - from) / fade, 0, 1)));
      paintScene(scene, t, true);
    },
    () => {
      scene.canvas.remove();
      button.style.visibility = "";
      for (const { el } of faded) el.style.opacity = "";
      scene.run.wm?.show();
    },
  );
}

// Where return tiles aim when the sign-in page was visited directly (no saved homepage button rect): the header's
// sign-in button on wide screens, or the top-left brand mark on phones.
function fallbackTarget(W: number): Box {
  if (W < 768) return { x: 20, y: 15, w: 88, h: 32 };
  return { x: Math.max(24, W - 180), y: 18, w: 68, h: 36 };
}

function clampBox(r: OriginRect, W: number, H: number): Box {
  const w = clamp(r.w, 24, W);
  const h = clamp(r.h, 20, H);
  return { x: clamp(r.x, 0, Math.max(0, W - w)), y: clamp(r.y, 0, Math.max(0, H - h)), w, h };
}

function syncLoginFrame(
  side: HTMLElement | null,
  seaBox: HTMLElement | null,
  faded: HTMLElement[],
  morphOn: boolean,
  elapsed: number,
  span: number,
  dir: 1 | -1,
) {
  if (seaBox && morphOn) seaBox.dataset.state = dir === 1 ? "run" : "login";
  const fade = E.out(clamp(elapsed / (span * 0.42), 0, 1));
  for (const el of faded) el.style.opacity = String(1 - fade);
  if (side) side.style.background = elapsed > 0 ? "transparent" : "";
  if (seaBox) seaBox.style.boxShadow = elapsed > 0 ? "none" : "";
}

// departLogin plays the return half of the tiles transition on a console Login-board page (.lg): the solid disperses
// back into sharks (by clearing data-state="login" on .lg-sea), the form fades, and the form panel dissolves ring by
// ring into tiles streaming back toward the homepage button. Calling reverse() midway turns it cleanly back onto the
// sign-in page and calls the sharks back in.
export function departLogin(
  frame: HTMLElement,
  origin: OriginRect | null,
  start: number,
  span: number,
  sea?: OceanEngine,
): Departure {
  const W = window.innerWidth;
  const H = window.innerHeight;
  const side = frame.querySelector<HTMLElement>(".lg-side");
  const seaBox = frame.querySelector<HTMLElement>(".lg-sea");
  const P = side ? toBox(side.getBoundingClientRect()) : panel(W, H);
  const b = origin ? clampBox(origin, W, H) : fallbackTarget(W);
  const scene = prepare(frame, b, P, sea);
  const morphOn = seaBox?.dataset.state === "login";
  const stage = morphOn ? null : seaBox?.querySelector<HTMLElement>(".lg-stg");
  const faded = [...frame.querySelectorAll<HTMLElement>(".lg-form, .lg-top, .lg-pitch")];
  if (stage) faded.push(stage);
  return runDeparture(
    scene.canvas,
    span,
    start,
    (elapsed, dir) => {
      syncLoginFrame(side, seaBox, faded, morphOn, elapsed, span, dir);
      paintScene(scene, (1 - elapsed / span) * scene.run.landed, false, false);
    },
    () => {
      scene.canvas.remove();
      if (side) side.style.background = "";
      if (seaBox) seaBox.style.boxShadow = "";
      if (seaBox && morphOn) seaBox.dataset.state = "login";
      for (const el of faded) el.style.opacity = "";
    },
  );
}

// arriveHome plays the closing tail of a return transition when landing back on the website (/): the remaining tiles
// finish landing into the trigger link while the hero fades in, then the overlay removes itself.
export function arriveHome(
  frame: HTMLElement,
  button: HTMLElement,
  start: number,
  span: number,
  sea?: OceanEngine,
): Departure {
  const scene = prepareSite(frame, button, sea, false);
  const tail = scene.run.landed * 0.38;
  const faded = leavingParts(frame, scene.b).map(({ el }) => el);
  const cleanup = () => {
    scene.canvas.remove();
    for (const el of faded) el.style.opacity = "";
  };
  let release = () => {};
  let dead = false;
  const tick = (_dt: number, now: number) => {
    if (dead) return;
    const u = clamp((now - start) / span, 0, 1);
    const v = E.out(u);
    for (const el of faded) el.style.opacity = String(0.35 + 0.65 * v);
    paintScene(scene, (1 - u) * tail);
    if (u >= 1) {
      dead = true;
      release();
      cleanup();
    }
  };
  release = own(scene.canvas, tick);
  tick(0, performance.now());
  return {
    dir: -1,
    remaining: () => clamp(span - (performance.now() - start), 0, span),
    reverse: () => {},
    destroy: () => {
      if (dead) return;
      dead = true;
      release();
      cleanup();
    },
  };
}
