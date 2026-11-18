// The website's half of the tiles transition (#747, port design §9): the button pressed breaks into the sea's tiles,
// which stream to where the sign-in page's form panel will be and start filling it, and then the browser goes to the
// button's page. It is the departure of trans.js's tiles style (STYLES.tiles start and frame), which cannot run on
// its own here: trans.js plays both pages in one document, and the sign-in page is the console's. The geometry and
// the easing are the engine's; its clock runs faster, so the stream lands in about half a second. Under reduced motion
// the website does not load it: the link goes at once (R7).
import { hash } from "./hash";
import { own } from "./loop";
import type { OceanEngine } from "./sea";

const GLYPHS = ":;t%SX8#@/\\<>=+*";
const clamp = (v: number, a: number, b: number) => Math.min(Math.max(v, a), b);
const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
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
function panel(W: number, H: number) {
  if (W < 768) return { x: 0, y: 208, w: W, h: Math.max(1, H - 208) };
  const w = Math.max(440, W * 0.44);
  return { x: W - w, y: 0, w, h: H };
}

// colour resolves a CSS colour (a token, light-dark()) to what the canvas can take.
function colour(root: HTMLElement, value: string) {
  const probe = document.createElement("i");
  probe.style.color = value;
  root.append(probe);
  const resolved = getComputedStyle(probe).color;
  probe.remove();
  return resolved;
}

type Box = { x: number; y: number; w: number; h: number };
type Bit = { sx: number; sy: number; tx: number; ty: number; d: number; ft: number; arc: number };
type Cell = Box & { at: number; d: number; g: string };
type Grid = { cw: number; ch: number };

// Where the stream enters the panel: the button itself if it is over the panel (a phone), else the nearest edge; and
// how far a point of the panel is from where it fills from (the button's whole outline when it is over the panel).
function entry(b: DOMRect, P: Box) {
  const bx = b.x + b.width / 2;
  const by = b.y + b.height / 2;
  const ex = clamp(bx, P.x, P.x + P.w);
  const ey = clamp(by, P.y, P.y + P.h);
  const gap = Math.hypot(ex - bx, ey - by);
  const over = gap < 4;
  const far = (x: number, y: number) =>
    over
      ? Math.hypot(Math.max(b.x - x, 0, x - b.right), Math.max(b.y - y, 0, y - b.bottom))
      : Math.hypot(x - ex, y - ey);
  return { ex, ey, gap, over, far };
}
type Entry = ReturnType<typeof entry>;

// The button as the sea's cells, leaving nearest the panel first, each landing around where the stream comes in.
function buttonBits(b: DOMRect, P: Box, e: Entry, { cw, ch }: Grid): Bit[] {
  if (e.over) return [];
  const found: { sx: number; sy: number; d0: number; h: number }[] = [];
  for (let r = Math.floor(b.y / ch); r < Math.ceil(b.bottom / ch); r++) {
    for (let c = Math.floor(b.x / cw); c < Math.ceil(b.right / cw); c++) {
      const sx = c * cw + cw / 2;
      const sy = r * ch + ch / 2;
      if (sx < b.x || sx > b.right || sy < b.y || sy > b.bottom) continue;
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

// plan lays the run out as trans.js's tiles start does; the panel starts filling as the first of the stream reaches
// it. landed is when the stream is all in, on the engine's clock.
function plan(b: DOMRect, W: number, H: number, grid: Grid) {
  const P = panel(W, H);
  const e = entry(b, P);
  const bits = buttonBits(b, P, e, grid);
  const arrivals = bits.map((p) => p.d + p.ft);
  const t0 = e.over ? 50 : bits.length ? Math.min(...arrivals) - 30 : 120;
  return { bits, ...panelCells(P, e, t0, grid), landed: Math.max(t0 + 560, ...arrivals) };
}
type Run = ReturnType<typeof plan>;
type Ink = { bg: string; ring: string; tile: string; glyph: string; button: string };

// Each panel cell comes on as a tile over the panel's ground and sets a moment later.
function drawCells(x: CanvasRenderingContext2D, run: Run, t: number, ink: Ink) {
  x.font = `500 ${Math.round(run.gh * 0.86)}px "Axiom Mono", ui-monospace, monospace`;
  x.textAlign = "center";
  x.textBaseline = "middle";
  for (const cl of run.cells) {
    const on = clamp((t - cl.at) / 140, 0, 1);
    if (on <= 0) continue;
    x.globalAlpha = on;
    x.fillStyle = ink.bg;
    x.fillRect(cl.x, cl.y, cl.w, cl.h);
    const a = on * (1 - clamp((t - cl.at - 110) / 150, 0, 1));
    if (a < 0.01) continue;
    const fresh = 1 - clamp((t - cl.at - 60) / 140, 0, 1);
    const s = 0.4 + 0.6 * on;
    x.globalAlpha = a * (0.3 + 0.4 * fresh);
    // near where the stream comes in (or the button's own outline, over the panel) a fresh cell is the button's colour
    x.fillStyle = fresh > 0.5 && cl.d < 0.08 ? ink.button : ink.tile;
    x.fillRect(
      cl.x + (cl.w * (1 - s)) / 2,
      cl.y + (cl.h * (1 - s)) / 2,
      Math.max(1, cl.w * s - 1),
      Math.max(1, cl.h * s - 1),
    );
    if (cl.g) {
      x.globalAlpha = a * 0.6;
      x.fillStyle = fresh > 0.6 ? ink.ring : ink.glyph;
      x.fillText(cl.g, cl.x + cl.w / 2, cl.y + cl.h / 2 + 0.5);
    }
  }
}

// The button: for a beat its own shape, then its cells, which run to the panel.
function drawButton(x: CanvasRenderingContext2D, run: Run, b: DOMRect, t: number, ink: Ink, { cw, ch }: Grid) {
  if (t < 90) {
    x.globalAlpha = 1 - clamp((t - 30) / 60, 0, 1);
    x.fillStyle = ink.button;
    x.beginPath();
    x.roundRect(b.x, b.y, b.width, b.height, Math.min(10, b.height / 2));
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

// inkOf reads the colours the run draws in from the page.
function inkOf(frame: HTMLElement, button: HTMLElement): Ink {
  const css = getComputedStyle(frame);
  const background = getComputedStyle(button).backgroundColor;
  return {
    bg: css.getPropertyValue("--bg").trim(),
    ring: css.getPropertyValue("--ring").trim(),
    tile: colour(frame, "light-dark(var(--n8), var(--n7))"),
    glyph: colour(frame, "light-dark(var(--n12), var(--n11))"),
    // a link without a ground of its own (the header's 登录) breaks into tiles of the text's colour
    button:
      background === "transparent" || background.endsWith(", 0)") ? css.getPropertyValue("--text1").trim() : background,
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

// what fades as the homepage leaves (trans.js's tiles frame), each over [from, from + span] ms of the engine's clock
const leaving = [
  [".hs > main h1, .hs > main .lede", 120, 360],
  [".cta, .fam", 200, 200],
  [".hs > .under, .isl", 60, 360],
  [".glow", 0, 460],
] as const;

export type Departure = { destroy: () => void };

// depart draws the departure from the button pressed, on the frame the website's board draws (.tr), over its sea: the
// run started at start (performance.now()) and lands its stream span ms later, on the wall clock, so a page that
// leaves at start + span leaves on the run's last frame however few frames came. It only draws: the caller goes. After
// the last frame it holds the picture without frames; destroy takes everything it changed off the page.
export function depart(
  frame: HTMLElement,
  button: HTMLElement,
  start: number,
  span: number,
  sea?: OceanEngine,
): Departure {
  const W = window.innerWidth;
  const H = window.innerHeight;
  const R = (sea as { renderer?: { cw?: number; ch?: number } } | undefined)?.renderer;
  const grid = { cw: R?.cw || 10, ch: R?.ch || R?.cw || 10 };
  const b = button.getBoundingClientRect();
  const run = plan(b, W, H, grid);
  const speed = run.landed / span;
  const ink = inkOf(frame, button);
  const { canvas, x, dpr } = overlay(frame, W, H);
  const faded = leaving.flatMap(([selector, from, fade]) =>
    [...frame.querySelectorAll<HTMLElement>(selector)].map((el) => ({ el, from, fade })),
  );
  let release = () => {};
  const draw = (_dt: number, now: number) => {
    const elapsed = clamp(now - start, 0, span);
    const t = elapsed * speed;
    button.style.visibility = t >= 60 ? "hidden" : "";
    for (const { el, from, fade } of faded) el.style.opacity = String(1 - out(clamp((t - from) / fade, 0, 1)));
    if (elapsed >= span) release();
    if (!x) return;
    x.setTransform(dpr, 0, 0, dpr, 0, 0);
    x.clearRect(0, 0, W, H);
    drawCells(x, run, t, ink);
    drawButton(x, run, b, t, ink, grid);
    x.globalAlpha = 1;
  };
  release = own(canvas, draw);
  draw(0, performance.now());
  return {
    destroy: () => {
      release();
      canvas.remove();
      button.style.visibility = "";
      for (const { el } of faded) el.style.opacity = "";
    },
  };
}
