// Bidirectional tiles transition between the website and the console's sign-in pages (#747, port design §9,
// trans.js.txt STYLES.tiles):
// - depart: on the website (.tr), breaks the pressed button (and any visible sea-floor wordmark tiles via
//   frame.__axCast.wordTiles()) into sea tiles that stream into the form panel, reversible midway.
// - departLogin: on a console Login-board page (.lg), plays the mirror in reverse — Morph releases its solid back into
//   sharks, the form panel breaks into tiles streaming back to the trigger, reversible midway.
// - arriveHome: on the website (.tr) after returning from sign-in, plays the short closing settle of tiles returning
//   into the header button so the landing does not replay the cold tide entrance.
import type { OriginRect } from "./arrival";
import { colour } from "./colour";
import { hash } from "./hash";
import { own } from "./loop";
import type { OceanEngine } from "./sea";
import { clamp, drawWord, type FlyingWord, GLYPHS, lerp, planWord, type WordCast, type WordCell } from "./word-tiles";

// cubic-bezier(0.2, 0, 0, 1), trans.js's E.emph ("opens fast, settles long"), and its house ease-out E.out
function bezier(x1: number, y1: number, x2: number, y2: number) {
  const cx = 3 * x1;
  const bx = 3 * (x2 - x1) - cx;
  const ax = 1 - cx - bx;
  const cy = 3 * y1;
  const by = 3 * (y2 - y1) - cy;
  const ay = 1 - cy - by;
  return (x: number) => {
    if (x <= 0 || x >= 1) return clamp(x, 0, 1);
    let lo = 0;
    let hi = 1;
    let t = x;
    for (let i = 0; i < 30; i++) {
      if (((ax * t + bx) * t + cx) * t < x) lo = t;
      else hi = t;
      t = (lo + hi) / 2;
    }
    return ((ay * t + by) * t + cy) * t;
  };
}
const emph = bezier(0.2, 0, 0, 1);
const out = bezier(0.16, 1, 0.3, 1);

// The sign-in page's form panel, where the Login board puts it: the right 44% (at least 440px) of a wide window,
// everything under the 208px sea on a phone.
function panel(W: number, H: number): Box {
  if (W < 768) return { x: 0, y: 208, w: W, h: Math.max(1, H - 208) };
  const w = Math.max(440, W * 0.44);
  return { x: W - w, y: 0, w, h: H };
}

type Box = { x: number; y: number; w: number; h: number };
type Bit = { sx: number; sy: number; tx: number; ty: number; d: number; ft: number; arc: number };
type Cell = WordCell & { d: number; g: string };
type Grid = { cw: number; ch: number };
type Ink = { bg: string; ring: string; tile: string; glyph: string; button: string };

const toBox = (r: DOMRect | OriginRect): Box =>
  "width" in r ? { x: r.x, y: r.y, w: r.width, h: r.height } : { x: r.x, y: r.y, w: r.w, h: r.h };

function gridOf(sea?: OceanEngine): Grid {
  const R = (sea as { renderer?: { cw?: number; ch?: number } } | undefined)?.renderer;
  return { cw: R?.cw || 10, ch: R?.ch || R?.cw || 10 };
}

// Where the stream enters the panel: the button itself if it is over the panel (a phone), else the nearest edge; and
// how far a point of the panel is from where it fills from (the button's whole outline when it is over the panel).
function entry(b: Box, P: Box) {
  const bx = b.x + b.w / 2;
  const by = b.y + b.h / 2;
  const ex = clamp(bx, P.x, P.x + P.w);
  const ey = clamp(by, P.y, P.y + P.h);
  const gap = Math.hypot(ex - bx, ey - by);
  const over = gap < 4;
  const far = (x: number, y: number) =>
    over
      ? Math.hypot(Math.max(b.x - x, 0, x - (b.x + b.w)), Math.max(b.y - y, 0, y - (b.y + b.h)))
      : Math.hypot(x - ex, y - ey);
  return { ex, ey, gap, over, far };
}
type Entry = ReturnType<typeof entry>;

// The button as the sea's cells, leaving nearest the panel first, each landing around where the stream comes in.
function buttonBits(b: Box, P: Box, e: Entry, { cw, ch }: Grid): Bit[] {
  if (e.over) return [];
  const found: { sx: number; sy: number; d0: number; h: number }[] = [];
  for (let r = Math.floor(b.y / ch); r < Math.ceil((b.y + b.h) / ch); r++) {
    for (let c = Math.floor(b.x / cw); c < Math.ceil((b.x + b.w) / cw); c++) {
      const sx = c * cw + cw / 2;
      const sy = r * ch + ch / 2;
      if (sx < b.x || sx > b.x + b.w || sy < b.y || sy > b.y + b.h) continue;
      found.push({
        sx: c * cw,
        sy: r * ch,
        d0: Math.hypot(c * cw - e.ex, r * ch - e.ey),
        h: hash(c * 5 + 1, r * 11 + 7),
      });
    }
  }
  found.sort((p, q) => p.d0 - q.d0);
  const ft = clamp(220 + e.gap * 0.18, 260, 420);
  const side = e.ex <= P.x + 1 ? cw * 2 : 0;
  return found.map((p, i) => {
    const lx = clamp(e.ex + (hash(i, 3) - 0.5) * cw * 8 + side * hash(i, 4), P.x, P.x + P.w - cw);
    const ly = clamp(e.ey + (hash(i, 5) - 0.5) * ch * 10, P.y, P.y + P.h - ch);
    return {
      sx: p.sx,
      sy: p.sy,
      tx: Math.floor(lx / cw) * cw,
      ty: Math.floor(ly / ch) * ch,
      d: 60 + (i / Math.max(1, found.length - 1)) * 220 + p.h * 30,
      ft: ft * (0.9 + 0.2 * p.h),
      arc: (hash(i, 6) - 0.35) * Math.min(160, 40 + e.gap * 0.16),
    };
  });
}

// The panel's cells on the sea's grid, each with the moment it comes on (from t0), ring by ring from the entry. On a
// big screen a cell is two or three sea cells to a side, so a frame never has more than a few thousand to draw.
function panelCells(P: Box, e: Entry, t0: number, { cw, ch }: Grid) {
  const corners = [P.x, P.x + P.w].flatMap((x) => [P.y, P.y + P.h].map((y) => e.far(x, y)));
  const maxd = Math.max(1, ...corners);
  const gk = Math.max(1, Math.ceil(Math.sqrt((P.w * P.h) / (cw * ch) / 3200)));
  const gw = cw * gk;
  const gh = ch * gk;
  const cells: Cell[] = [];
  for (let r = Math.floor(P.y / gh); r < Math.ceil((P.y + P.h) / gh); r++) {
    for (let c = Math.floor(P.x / gw); c < Math.ceil((P.x + P.w) / gw); c++) {
      const x = Math.max(c * gw, P.x);
      const y = Math.max(r * gh, P.y);
      const h = hash(c * 7 + 3, r * 13 + 5);
      const d = e.far(c * gw + gw / 2, r * gh + gh / 2) / maxd;
      cells.push({
        x,
        y,
        w: Math.min((c + 1) * gw, P.x + P.w) - x,
        h: Math.min((r + 1) * gh, P.y + P.h) - y,
        at: t0 + d * 560 + h * (e.over && d === 0 ? 30 : 50),
        d,
        g: h < 0.34 ? (GLYPHS[Math.floor(hash(c, r) * GLYPHS.length)] ?? "") : "",
      });
    }
  }
  return { cells, gh };
}

type Run = {
  bits: Bit[];
  cells: Cell[];
  gh: number;
  word: FlyingWord[];
  wm: WordCast | null;
  landed: number;
};

// plan lays the run out as trans.js's tiles start does; the panel starts filling as the first of the stream reaches
// it. When frame carries __axCast (on the website's sea floor), the wordmark's tiles are paired with incoming cells.
function plan(b: Box, P: Box, grid: Grid, frame?: HTMLElement): Run {
  const e = entry(b, P);
  const bits = buttonBits(b, P, e, grid);
  const arrivals = bits.map((p) => p.d + p.ft);
  const t0 = e.over ? 50 : bits.length ? Math.min(...arrivals) - 30 : 120;
  const { cells, gh } = panelCells(P, e, t0, grid);
  const {
    word,
    wm,
    landed: wordLanded,
  } = frame ? planWord(frame, cells, e.ex, e.ey) : { word: [], wm: null, landed: 0 };
  return { bits, cells, gh, word, wm, landed: Math.max(t0 + 560, wordLanded, ...arrivals) };
}

function drawCell(x: CanvasRenderingContext2D, cl: Cell, t: number, ink: Ink) {
  const on = clamp((t - cl.at) / 140, 0, 1);
  if (on <= 0) return;
  x.globalAlpha = on;
  x.fillStyle = ink.bg;
  x.fillRect(cl.x, cl.y, cl.w, cl.h);
  const a = on * (1 - clamp((t - cl.at - 110) / 150, 0, 1));
  if (a < 0.01) return;
  const fresh = 1 - clamp((t - cl.at - 60) / 140, 0, 1);
  const s = cl.word ? 1 : 0.4 + 0.6 * on;
  x.globalAlpha = a * (cl.word ? 0.6 : 0.3 + 0.4 * fresh);
  // near where the stream comes in (or the button's own outline, over the panel) a fresh cell is the button's colour
  x.fillStyle = fresh > 0.5 && cl.d < 0.08 ? ink.button : ink.tile;
  x.fillRect(
    cl.x + (cl.w * (1 - s)) / 2,
    cl.y + (cl.h * (1 - s)) / 2,
    Math.max(1, cl.w * s - 1),
    Math.max(1, cl.h * s - 1),
  );
  if (!cl.g) return;
  x.globalAlpha = a * 0.6;
  x.fillStyle = fresh > 0.6 ? ink.ring : ink.glyph;
  x.fillText(cl.g, cl.x + cl.w / 2, cl.y + cl.h / 2 + 0.5);
}

// Each panel cell comes on as a tile over the panel's ground and sets a moment later.
function drawCells(x: CanvasRenderingContext2D, run: Run, t: number, ink: Ink) {
  x.font = `500 ${Math.round(run.gh * 0.86)}px "Axiom Mono", ui-monospace, monospace`;
  x.textAlign = "center";
  x.textBaseline = "middle";
  for (const cl of run.cells) drawCell(x, cl, t, ink);
}

// The button: for a beat its own shape, then its cells, which run to the panel.
function drawButton(x: CanvasRenderingContext2D, run: Run, b: Box, t: number, ink: Ink, { cw, ch }: Grid) {
  if (t < 90) {
    x.globalAlpha = 1 - clamp((t - 30) / 60, 0, 1);
    x.fillStyle = ink.button;
    x.beginPath();
    x.roundRect(b.x, b.y, b.w, b.h, Math.min(10, b.h / 2));
    x.fill();
  }
  const iw = cw - 1;
  const ih = ch - 1;
  const enter = clamp((t - 30) / 60, 0, 1);
  for (const p of run.bits) {
    const lt = (t - p.d) / p.ft;
    if (lt >= 1 || (lt <= 0 && enter <= 0)) continue;
    if (lt <= 0) {
      x.globalAlpha = enter;
      x.fillStyle = ink.button;
      x.fillRect(p.sx, p.sy, iw, ih);
      continue;
    }
    const e = emph(lt);
    const k = 1 - 0.25 * Math.sin(Math.PI * e);
    x.globalAlpha = (1 - clamp((lt - 0.8) / 0.2, 0, 1)) * 0.95;
    x.fillStyle = e < 0.5 ? ink.button : ink.tile;
    x.fillRect(
      lerp(p.sx, p.tx, e) + (iw * (1 - k)) / 2,
      lerp(p.sy, p.ty, e) - Math.sin(Math.PI * e) * p.arc + (ih * (1 - k)) / 2,
      iw * k,
      ih * k,
    );
  }
}

// inkOf reads the resolved colours the run draws in from the page through CSSOM (handles light-dark() and brand overrides).
function inkOf(frame: HTMLElement, button?: HTMLElement): Ink {
  const background = button ? getComputedStyle(button).backgroundColor : "";
  const solid = background !== "" && background !== "transparent" && !background.endsWith(", 0)");
  return {
    bg: colour(frame, "var(--bg)"),
    ring: colour(frame, "var(--ring)"),
    tile: colour(frame, "light-dark(var(--n8), var(--n7))"),
    glyph: colour(frame, "light-dark(var(--n12), var(--n11))"),
    // a link without a ground of its own (the header's 登录) breaks into tiles of the text's colour
    button: solid ? background : colour(frame, "var(--text1)"),
  };
}

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

// Shared reversible clock on the frame loop: progress runs in [0, span] at wall-clock rate, flipping direction cleanly
// whenever reverse() is called midway.
function drive(
  canvas: HTMLCanvasElement,
  span: number,
  start: number,
  paint: (elapsed: number, dir: 1 | -1) => void,
  cleanup: () => void,
): Departure {
  let dir: 1 | -1 = 1;
  let pivot = 0;
  let pivotAt = start;
  let dead = false;
  let active = true;
  let onDone: ((end: 1 | -1) => void) | undefined;
  let release = () => {};
  const progress = (now: number) => clamp(pivot + dir * (now - pivotAt), 0, span);
  const tick = (_dt: number, now: number) => {
    if (dead) return;
    const elapsed = progress(now);
    paint(elapsed, dir);
    if (dir === 1 && elapsed >= span) {
      release();
      active = false;
      onDone?.(1);
    } else if (dir === -1 && elapsed <= 0) {
      dead = true;
      release();
      active = false;
      cleanup();
      onDone?.(-1);
    }
  };
  release = own(canvas, tick);
  tick(0, performance.now());
  return {
    get dir() {
      return dir;
    },
    remaining: () => {
      const e = progress(performance.now());
      return dir === 1 ? span - e : e;
    },
    reverse: (done) => {
      if (dead) return;
      const now = performance.now();
      pivot = progress(now);
      pivotAt = now;
      dir = dir === 1 ? -1 : 1;
      onDone = done;
      if (!active) {
        active = true;
        release = own(canvas, tick);
      }
      tick(0, now);
    },
    destroy: () => {
      if (dead) return;
      dead = true;
      release();
      active = false;
      cleanup();
    },
  };
}

// what fades as the homepage leaves (trans.js's tiles frame), each over [from, from + fade] ms of the engine's clock
const leaving = [
  [".hs > main h1, .hs > main .lede", 120, 360],
  [".cta, .fam", 200, 200],
  [".hs > .under, .isl", 60, 360],
  [".glow", 0, 460],
] as const;

// Shared scene setup and frame paint for depart, departLogin, and arriveHome.
function prepare(frame: HTMLElement, b: Box, P: Box, sea?: OceanEngine, button?: HTMLElement, withWord = false) {
  const W = window.innerWidth;
  const H = window.innerHeight;
  const grid = gridOf(sea);
  const run = plan(b, P, grid, withWord ? frame : undefined);
  const ink = inkOf(frame, button);
  const { canvas, x, dpr } = overlay(frame, W, H);
  return { W, H, grid, run, ink, canvas, x, dpr, b };
}
type Scene = ReturnType<typeof prepare>;

function paintScene(scene: Scene, t: number, withWord = false) {
  const { x, dpr, W, H, run, b, ink, grid } = scene;
  if (!x) return;
  x.setTransform(dpr, 0, 0, dpr, 0, 0);
  x.clearRect(0, 0, W, H);
  drawCells(x, run, t, ink);
  drawButton(x, run, b, t, ink, grid);
  if (withWord) drawWord(x, run.word, run.wm, t, ink.tile, emph);
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
  const faded = leaving.flatMap(([selector, from, fade]) =>
    [...frame.querySelectorAll<HTMLElement>(selector)].map((el) => ({ el, from, fade })),
  );
  return drive(
    scene.canvas,
    span,
    start,
    (elapsed) => {
      const t = elapsed * speed;
      button.style.visibility = t >= 60 ? "hidden" : "";
      for (const { el, from, fade } of faded) el.style.opacity = String(1 - out(clamp((t - from) / fade, 0, 1)));
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
  const fade = out(clamp(elapsed / (span * 0.42), 0, 1));
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
  return drive(
    scene.canvas,
    span,
    start,
    (elapsed, dir) => {
      syncLoginFrame(side, seaBox, faded, morphOn, elapsed, span, dir);
      paintScene(scene, (1 - elapsed / span) * scene.run.landed);
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
  const faded = leaving.flatMap(([selector]) => [...frame.querySelectorAll<HTMLElement>(selector)]);
  const cleanup = () => {
    scene.canvas.remove();
    for (const el of faded) el.style.opacity = "";
  };
  let release = () => {};
  let dead = false;
  const tick = (_dt: number, now: number) => {
    if (dead) return;
    const u = clamp((now - start) / span, 0, 1);
    const v = out(u);
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
