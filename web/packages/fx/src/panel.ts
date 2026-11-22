// Shared primitives for both the two-document tiles departure (depart.ts) and the single-document tiles driver
// (tiles.ts): trans.js.txt's easings, trigger-to-panel cell geometry, incremental offscreen bake, cell/button drawing,
// CSSOM colour resolution, and the reversible wall-clock loop on loop.ts.
import type { OriginRect } from "./arrival";
import { colour } from "./colour";
import { hash } from "./hash";
import { own } from "./loop";
import type { OceanEngine } from "./ocean";
import { clamp, type FlyingWord, GLYPHS, lerp, planWord, type WordCast, type WordCell } from "./word-tiles";

export type Ease = (t: number) => number;
export type Box = { x: number; y: number; w: number; h: number };
export type Bit = { sx: number; sy: number; tx: number; ty: number; d: number; ft: number; arc: number };
export type Cell = WordCell & { d: number; g: string };
export type Grid = { cw: number; ch: number };
export type Ink = { bg: string; ring: string; dim: string; tile: string; glyph: string; button: string };

// Cubic Bezier easing via monotone bisection (trans.js.txt:18–37).
function bezier(x1: number, y1: number, x2: number, y2: number): Ease {
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

export const E = {
  lin: (t: number) => t,
  out: bezier(0.16, 1, 0.3, 1), // the house ease-out
  emph: bezier(0.2, 0, 0, 1), // opens fast, settles long
  std: bezier(0.3, 0, 0, 1),
  drop: bezier(0.6, 0, 0.2, 1), // starts late, lands softly
} as const;

// The sign-in page's form panel, where the Login board puts it when no mounted .lg-side is present: the right 44%
// (at least 440px) of a wide window, everything under the 208px sea on a phone.
export function panel(W: number, H: number): Box {
  if (W < 768) return { x: 0, y: 208, w: W, h: Math.max(1, H - 208) };
  const w = Math.max(440, W * 0.44);
  return { x: W - w, y: 0, w, h: H };
}

export const toBox = (r: DOMRect | OriginRect): Box =>
  "width" in r ? { x: r.x, y: r.y, w: r.width, h: r.height } : { x: r.x, y: r.y, w: r.w, h: r.h };

export function gridOf(sea?: OceanEngine): Grid {
  const R = sea?.renderer;
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

// The button as the sea's cells, leaving nearest the panel first, each landing around where the stream comes in
// (trans.js.txt:984–996).
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
// big screen a cell is two or three sea cells to a side, so a frame never has more than a few thousand to consider
// (trans.js.txt:999–1007).
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

export type TilePlan = {
  bits: Bit[];
  cells: Cell[];
  gh: number;
  word: FlyingWord[];
  wm: WordCast | null;
  wordLanded: number;
  t0: number;
  off: number;
  landed: number;
  T: number;
};

// planTiles lays the run out as trans.js's tiles start does (trans.js.txt:978–1027): panel cells start filling as the
// first button bits arrive, optional seabed wordmark tiles pair with incoming cells, and cells are sorted by onset for
// incremental baking and binary-search active-band rendering.
export function planTiles(b: Box, P: Box, grid: Grid, frame?: HTMLElement): TilePlan {
  const e = entry(b, P);
  const bits = buttonBits(b, P, e, grid);
  const arrivals = bits.map((p) => p.d + p.ft);
  const t0 = e.over ? 50 : bits.length ? Math.max(40, Math.min(...arrivals) - 30) : 120;
  const off = Math.max(0, t0 - 220);
  const { cells, gh } = panelCells(P, e, t0, grid);
  const {
    word,
    wm,
    landed: wordLanded,
  } = frame ? planWord(frame, cells, e.ex, e.ey) : { word: [], wm: null, landed: 0 };
  cells.sort((p, q) => p.at - q.at);
  const lastAt = cells[cells.length - 1]?.at ?? 0;
  const T = Math.max(1080, cells.length ? lastAt + 300 : 0, off + 1080);
  return { bits, cells, gh, word, wm, wordLanded, t0, off, landed: Math.max(t0 + 560, wordLanded, ...arrivals), T };
}

// inkOf reads the resolved colours the run draws in from the page through CSSOM (handles light-dark() and brand overrides).
export function inkOf(frame: HTMLElement, button?: HTMLElement | null): Ink {
  const background = button ? getComputedStyle(button).backgroundColor : "";
  const solid = background !== "" && background !== "transparent" && !background.endsWith(", 0)");
  return {
    bg: colour(frame, "var(--bg)"),
    ring: colour(frame, "var(--ring)"),
    dim: colour(frame, "var(--text2)"),
    tile: colour(frame, "light-dark(var(--n8), var(--n7))"),
    glyph: colour(frame, "light-dark(var(--n12), var(--n11))"),
    // a link without a ground of its own (the header's 登录) breaks into tiles of the text's colour
    button: solid ? background : colour(frame, "var(--text1)"),
  };
}

// drawCell paints one panel cell at timeline time t; drawBg is false when the cell's ground is already baked or owned
// by .lg-side itself.
function drawCell(x: CanvasRenderingContext2D, cl: Cell, t: number, ink: Ink, drawBg = true) {
  const on = clamp((t - cl.at) / 140, 0, 1);
  if (on <= 0) return;
  if (drawBg) {
    x.globalAlpha = on;
    x.fillStyle = ink.bg;
    x.fillRect(cl.x, cl.y, cl.w, cl.h);
  }
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

// Unbaked draw for depart.ts (short cross-document run without ground hand-off).
export function drawCells(
  x: CanvasRenderingContext2D,
  cells: Cell[],
  gh: number,
  t: number,
  ink: Ink,
  family = '"Axiom Mono", ui-monospace, monospace',
) {
  x.font = `500 ${Math.round(gh * 0.86)}px ${family}`;
  x.textAlign = "center";
  x.textBaseline = "middle";
  for (const cl of cells) drawCell(x, cl, t, ink);
}

export type Bake = {
  canvas: HTMLCanvasElement | null;
  ctx: CanvasRenderingContext2D | null;
  count: number;
};

export const createBake = (): Bake => ({ canvas: null, ctx: null, count: 0 });

// First index in sorted cells whose onset is strictly greater than limit (trans.js.txt:1068).
function upperBound(cells: Cell[], limit: number): number {
  let lo = 0;
  let hi = cells.length;
  while (lo < hi) {
    const m = (lo + hi) >> 1;
    if ((cells[m] as Cell).at <= limit) lo = m + 1;
    else hi = m;
  }
  return lo;
}

function syncBake(bake: Bake, cells: Cell[], nOn: number, width: number, height: number, W: number, bg: string) {
  if (!bake.canvas || bake.canvas.width !== width || bake.canvas.height !== height) {
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext("2d");
    const scale = W > 0 ? width / W : 1;
    ctx?.setTransform(scale, 0, 0, scale, 0, 0);
    bake.canvas = canvas;
    bake.ctx = ctx;
    bake.count = 0;
  }
  const bx = bake.ctx;
  if (!bx) return;
  if (nOn > bake.count) {
    bx.fillStyle = bg;
    for (let i = bake.count; i < nOn; i++) {
      const cl = cells[i] as Cell;
      bx.fillRect(cl.x, cl.y, cl.w, cl.h);
    }
  } else if (nOn < bake.count) {
    for (let i = nOn; i < bake.count; i++) {
      const cl = cells[i] as Cell;
      bx.clearRect(cl.x, cl.y, cl.w, cl.h);
    }
  }
  bake.count = nOn;
}

// Incremental offscreen bake + active-band cell rendering (trans.js.txt:1064–1086): settled cells live on the offscreen
// layer (updated only by the delta since the previous frame and blitted once until ground takes over), so every frame
// only loops over the ~260ms wave in flight.
export function drawBakedCells(
  x: CanvasRenderingContext2D,
  bake: Bake,
  cells: Cell[],
  gh: number,
  t: number,
  ground: boolean,
  view: { W: number; uiWidth: number; uiHeight: number; dpr: number },
  ink: Ink,
  family: string,
) {
  x.font = `500 ${Math.round(gh * 0.86)}px ${family}`;
  x.textAlign = "center";
  x.textBaseline = "middle";
  const nOn = upperBound(cells, t - 140);
  const end = upperBound(cells, t);
  if (!ground) {
    syncBake(bake, cells, nOn, view.uiWidth, view.uiHeight, view.W, ink.bg);
    if (bake.canvas && typeof x.drawImage === "function") {
      x.setTransform(1, 0, 0, 1, 0, 0);
      x.globalAlpha = 1;
      x.drawImage(bake.canvas, 0, 0);
      x.setTransform(view.dpr, 0, 0, view.dpr, 0, 0);
    }
  }
  for (let i = upperBound(cells, t - 260); i < end; i++) {
    drawCell(x, cells[i] as Cell, t, ink, !ground && i >= nOn);
  }
}

function drawBit(
  x: CanvasRenderingContext2D,
  p: Bit,
  t: number,
  enter: number,
  home: number,
  iw: number,
  ih: number,
  ink: Ink,
) {
  const lt = (t - p.d) / p.ft;
  if (lt >= 1 || (lt <= 0 && enter <= 0)) return;
  if (lt <= 0) {
    x.globalAlpha = enter * home;
    x.fillStyle = ink.button;
    x.fillRect(p.sx, p.sy, iw, ih);
    return;
  }
  const e = E.emph(lt);
  const k = 1 - 0.25 * Math.sin(Math.PI * e);
  x.globalAlpha = (1 - clamp((lt - 0.8) / 0.2, 0, 1)) * 0.95 * home;
  x.fillStyle = e < 0.5 ? ink.button : ink.tile;
  x.fillRect(
    lerp(p.sx, p.tx, e) + (iw * (1 - k)) / 2,
    lerp(p.sy, p.ty, e) - Math.sin(Math.PI * e) * p.arc + (ih * (1 - k)) / 2,
    iw * k,
    ih * k,
  );
}

// The button: for a beat its own shape, then its cells, which run to the panel (trans.js.txt:1088–1098).
export function drawButton(
  x: CanvasRenderingContext2D,
  bits: Bit[],
  b: Box,
  t: number,
  ink: Ink,
  grid: Grid,
  lands = true,
) {
  const { cw, ch } = grid;
  const home = lands ? 1 : clamp((t - 60) / 220, 0, 1);
  if (home <= 0) return;
  if (t < 90 && lands) {
    x.globalAlpha = 1 - clamp((t - 30) / 60, 0, 1);
    x.fillStyle = ink.button;
    x.beginPath();
    if (typeof x.roundRect === "function") x.roundRect(b.x, b.y, b.w, b.h, Math.min(10, b.h / 2));
    else x.rect(b.x, b.y, b.w, b.h);
    x.fill();
  }
  const iw = cw - 1;
  const ih = ch - 1;
  const enter = clamp((t - 30) / 60, 0, 1);
  for (const p of bits) drawBit(x, p, t, enter, home, iw, ih, ink);
}

export type ClockOptions = {
  canvas: HTMLCanvasElement;
  span: number;
  start: number;
  dir?: 1 | -1;
  // When true (depart.ts), reaching span forward holds instead of ending so reverse() can still pull back.
  holdAtEnd?: boolean;
  paint: (pos: number, dir: 1 | -1, landing: boolean) => void;
  onEnd: (end: 1 | -1) => void;
};

export type Clock = {
  readonly dir: 1 | -1;
  readonly pos: number;
  remaining: () => number;
  reverse: (onDone?: (end: 1 | -1) => void) => void;
  seek: (ms: number) => void;
  stop: () => void;
};

type ClockState = {
  dir: 1 | -1;
  pivot: number;
  pivotAt: number;
  dead: boolean;
  active: boolean;
  doneCb?: (end: 1 | -1) => void;
  release: () => void;
};

const clockPos = (s: ClockState, span: number, now: number) =>
  clamp(s.pivot + s.dir * Math.max(0, now - s.pivotAt), 0, span);

function finishClock(
  s: ClockState,
  end: 1 | -1,
  endPos: number,
  opts: Pick<ClockOptions, "holdAtEnd" | "paint" | "onEnd">,
) {
  const holding = end === 1 && Boolean(opts.holdAtEnd);
  if (!holding) s.dead = true;
  s.release();
  s.active = false;
  opts.paint(endPos, end, !opts.holdAtEnd);
  if (!holding) opts.onEnd(end);
  s.doneCb?.(end);
}

// Shared reversible wall-clock loop on loop.ts: pos runs in [0, span] (0 -> span when dir === 1, span -> 0 when
// dir === -1), flipping cleanly on reverse() and jumping deterministically on seek().
export function drive(opts: ClockOptions): Clock {
  const { canvas, span, start, paint } = opts;
  const initialDir = opts.dir ?? 1;
  const s: ClockState = {
    dir: initialDir,
    pivot: initialDir === 1 ? 0 : span,
    pivotAt: start,
    dead: false,
    active: true,
    release: () => {},
  };
  const tick = (_dt: number, now: number) => {
    if (s.dead) return;
    const pos = clockPos(s, span, now);
    if (s.dir === 1 && pos >= span) return finishClock(s, 1, span, opts);
    if (s.dir === -1 && pos <= 0) return finishClock(s, -1, 0, opts);
    paint(pos, s.dir, false);
  };
  s.release = own(canvas, tick);
  tick(0, performance.now());
  return {
    get dir() {
      return s.dir;
    },
    get pos() {
      return clockPos(s, span, performance.now());
    },
    remaining: () => {
      const p = clockPos(s, span, performance.now());
      return s.dir === 1 ? span - p : p;
    },
    reverse: (done) => {
      if (s.dead) return;
      const now = performance.now();
      s.pivot = clockPos(s, span, now);
      s.pivotAt = now;
      s.dir = s.dir === 1 ? -1 : 1;
      if (done !== undefined) s.doneCb = done;
      if (!s.active) {
        s.active = true;
        s.release = own(canvas, tick);
      }
      tick(0, now);
    },
    seek: (ms) => {
      if (s.dead) return;
      const clamped = clamp(ms, 0, span);
      s.pivot = s.dir === 1 ? clamped : span - clamped;
      s.pivotAt = performance.now();
      if (ms >= span) return finishClock(s, s.dir, s.dir === 1 ? span : 0, opts);
      paint(s.pivot, s.dir, false);
    },
    stop: () => {
      if (s.dead) return;
      s.dead = true;
      s.release();
      s.active = false;
    },
  };
}
