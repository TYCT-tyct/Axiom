// The console's sign-in pages' engines (#747, port design §9): the Login board's sea, the stage on it (login-ui.js's
// Stage: loose tiles in the sea's own cells gather into the thing the step is about and answer what is typed), and the
// title's decode. They run on the shared loop (loop.ts): nothing moves while the tab is hidden or the sea is out of
// view, and under reduced motion each is one still frame in its final state. The gathering from the panel's edge and
// the decode play only for a visitor who arrived through the website's departure (arrival.ts); a direct visit opens
// on the formed shape and a resolved title.
import { colour } from "./colour";
import { hash } from "./hash";
import { own, reducedMotion } from "./loop";
import { buildSea, type OceanEngine, type Sea, stillSea } from "./ocean";

export type Shape = "start" | "code" | "key" | "enter";
// What the page tells the stage: how much of an address is typed and whether it looks whole, how many digits of a
// code are in, a refused answer.
export type Heard = { type: "email"; len: number; ok: boolean } | { type: "digit"; n: number } | { type: "bad" };

const GLYPHS = ":;t%SX8#@/\\<>=+*";
const glyph = (i: number, t: number) => GLYPHS[Math.floor(hash(i, t) * GLYPHS.length)] ?? "";
const ease = (u: number) => 1 - (1 - u) ** 3;
const clamp01 = (u: number) => Math.max(0, Math.min(1, u));

// login-ui.js SHAPES, less the steps the product does not have (wechat, connect).
const SHAPES: Record<Shape, string[]> = {
  start: [
    "###################",
    "#v...............v#",
    "#.vv...........vv.#",
    "#...vv.......vv...#",
    "#.....vv+++vv.....#",
    "#.................#",
    "#.................#",
    "#.................#",
    "###################",
  ],
  code: ["###.###.###.###.###.###", "#0#.#1#.#2#.#3#.#4#.#5#", "#0#.#1#.#2#.#3#.#4#.#5#", "###.###.###.###.###.###"],
  key: [
    ".####................",
    "#....################",
    "#....#.......+.+.+...",
    "#....#.......+...+...",
    ".####................",
  ],
  enter: [
    "............##",
    "...........##.",
    "..........##..",
    "##.......##...",
    ".##.....##....",
    "..##...##.....",
    "...#####......",
  ],
};
type Cell = { x: number; y: number; c: string };
const CELLS = Object.fromEntries(
  Object.entries(SHAPES).map(([name, rows]) => {
    const cells = rows.flatMap((row, y) => [...row].flatMap((c, x) => (c === "." ? [] : [{ x, y, c }])));
    return [name, { cells, w: rows[0]?.length ?? 0, h: rows.length }];
  }),
) as Record<Shape, { cells: Cell[]; w: number; h: number }>;
const TILES = Math.max(...Object.values(CELLS).map((shape) => shape.cells.length));

type Tile = {
  x: number;
  y: number;
  fx: number;
  fy: number;
  cx: number;
  cy: number;
  ux: number;
  uy: number;
  tx: number;
  t0: number;
  d: number;
  c: string;
  free: boolean;
  s: number;
};
type Grid = { W: number; H: number; cw: number; ch: number; wide: boolean };
type Ink = { t1: string; t2: string; ring: string; err: string; bg: string };
type Frame = { now: number; g: Grid; o: { x: number; y: number }; k: Ink; shake: number; badT: number; tick: number };

class Stage {
  private readonly canvas = document.createElement("canvas");
  private readonly context: CanvasRenderingContext2D;
  private readonly tiles: Tile[];
  private shape: Shape | "" = "";
  private shapeAt = 0;
  private alpha = 0;
  private heard = { len: 0, ok: false, n: 0 };
  private bad = -1e9;
  private pulse = -1e9;
  private grid: Grid = { W: 0, H: 0, cw: 10, ch: 14, wide: true };
  private ink: Ink | undefined;
  private inkAt = 0;
  private mid = 0;
  private half = 1;

  constructor(
    private readonly lg: HTMLElement,
    private readonly sea: HTMLElement,
    private readonly engine: OceanEngine,
  ) {
    // Above the sea's canvas and under the brand and the pitch, as one more layer of the sea; CSSOM, not markup.
    const style = this.canvas.style;
    style.position = "absolute";
    style.inset = "0";
    style.width = "100%";
    style.height = "100%";
    style.display = "block";
    style.pointerEvents = "none";
    this.canvas.setAttribute("aria-hidden", "true");
    sea.querySelector("canvas.sea")?.after(this.canvas);
    this.context = this.canvas.getContext("2d") as CanvasRenderingContext2D;
    this.tiles = Array.from({ length: TILES }, (_, i) => ({
      x: 0,
      y: 0,
      fx: 0,
      fy: 0,
      cx: 0,
      cy: 0,
      ux: 0,
      uy: 0,
      tx: 0,
      t0: 0,
      d: 0,
      c: "",
      free: true,
      s: hash(i, 7),
    }));
  }

  // first places the tiles for the first shape: streaming in from the edge the form panel is on (the right of a wide
  // window, the bottom of a phone's sea) for an arrival, already formed otherwise.
  first(shape: Shape, now: number, arrived: boolean) {
    const g = this.size();
    this.tiles.forEach((tile, i) => {
      tile.x = arrived ? (g.wide ? g.W - g.cw * (1 + hash(i, 21) * 2) : hash(i, 21) * g.W) : hash(i, 3) * g.W;
      tile.y = arrived ? (g.wide ? hash(i, 23) * g.H : g.H - g.ch * (1 + hash(i, 23) * 2)) : hash(i, 5) * g.H;
    });
    this.to(shape, now, arrived ? 240 : 0);
    if (!arrived) this.settle();
  }

  // settle puts every tile where it is going at once: a direct visit's first shape, and every shape under reduced
  // motion.
  settle() {
    for (const tile of this.tiles) tile.t0 = -1e9;
    this.shapeAt = -1e9;
    this.alpha = 1;
  }

  // to sends every tile to a cell of the shape, or to a place to drift; where that is on screen is worked out each
  // frame, so the shape stays on the sea's grid while the window changes size.
  to(name: Shape, now: number, delay = 0) {
    if (name === this.shape) return;
    this.shape = name;
    const g = this.size();
    const cells = CELLS[name].cells.slice().sort((a, b) => a.x - b.x || a.y - b.y);
    this.tiles.forEach((tile, i) => {
      tile.fx = tile.x;
      tile.fy = tile.y;
      const cell = cells[i];
      if (cell) {
        Object.assign(tile, { cx: cell.x, cy: cell.y, c: cell.c, free: false });
        tile.t0 = now + delay + 40 + cell.x * 16 + tile.s * 120;
        tile.d = 620 + tile.s * 240;
      } else {
        tile.ux = hash(i, name.length + 11);
        tile.uy = hash(i, name.length + 17) * (g.wide ? 0.8 : 0.9);
        Object.assign(tile, { c: "", free: true, t0: now + delay + tile.s * 200, d: 900 });
      }
    });
    this.shapeAt = now + delay;
  }

  // recolour reads the page's colours again on the next frame drawn: the scheme has changed
  recolour() {
    this.ink = undefined;
  }

  hear(heard: Heard, now: number) {
    if (heard.type === "email") this.heard = { ...this.heard, len: heard.len, ok: heard.ok };
    else if (heard.type === "digit") {
      this.heard.n = heard.n;
      this.pulse = now;
    } else this.bad = now;
  }

  // The cell size of the sea under it: twice the sea's on a wide desk, at most 7px on a phone's short sea.
  private size(): Grid {
    const W = this.sea.offsetWidth;
    const H = this.sea.offsetHeight;
    const wide = H > 420;
    let cw = this.engine.renderer.cw || 10;
    let ch = this.engine.renderer.ch || Math.round(cw * 1.45);
    if (!wide) {
      const k = Math.min(1, 7 / cw);
      cw = Math.round(cw * k);
      ch = Math.round(ch * k);
    } else if (W >= 1100) {
      cw *= 2;
      ch *= 2;
    }
    this.grid = { W, H, cw, ch, wide };
    return this.grid;
  }

  private home(w: number, h: number) {
    const g = this.grid;
    let x = g.wide ? (g.W - w * g.cw) / 2 : g.W - 20 - w * g.cw;
    const y = g.wide ? g.H * 0.42 - (h * g.ch) / 2 : 52 + Math.max(0, (g.H - 52 - 12 - h * g.ch) / 2);
    if (!g.wide) x = Math.max(x, 20);
    return { x: Math.round(x / g.cw) * g.cw, y: Math.round(y / g.ch) * g.ch };
  }

  // lit says how a tile of the shape answers: 1 lit in the ring colour, 0 plain, -1 a dot for a digit not yet typed.
  // The envelope's flap lights from its ends inwards as the address grows, its seal once the address looks whole.
  private lit(tile: Tile) {
    const c = tile.c;
    if (c === "v") return this.heard.len > 0 && this.reach(tile) >= 1 - clamp01(this.heard.len / 14) ? 1 : 0;
    if (c === "+") return this.shape !== "start" || this.heard.ok ? 1 : 0;
    if (c >= "0" && c <= "5") return +c < this.heard.n ? 1 : -1;
    return 0;
  }

  private reach(tile: Tile) {
    return Math.abs(tile.tx - this.mid) / this.half;
  }

  private colours(now: number): Ink {
    if (!this.ink || now - this.inkAt > 400) {
      const read = (name: string) => colour(this.lg, `var(${name})`);
      this.ink = {
        t1: read("--text1"),
        t2: read("--text2"),
        ring: read("--ring"),
        err: read("--err"),
        bg: read("--bg"),
      };
      this.inkAt = now;
    }
    return this.ink;
  }

  draw(now: number) {
    const g = this.size();
    if (!g.W || !g.H || !this.shape) return;
    const x = this.context;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    if (this.canvas.width !== Math.round(g.W * dpr) || this.canvas.height !== Math.round(g.H * dpr)) {
      this.canvas.width = Math.round(g.W * dpr);
      this.canvas.height = Math.round(g.H * dpr);
    }
    this.alpha += (1 - this.alpha) * 0.2;
    x.setTransform(dpr, 0, 0, dpr, 0, 0);
    x.clearRect(0, 0, g.W, g.H);
    const k = this.colours(now);
    const S = CELLS[this.shape];
    const o = this.home(S.w, S.h);
    this.mid = o.x + ((S.w - 1) * g.cw) / 2;
    this.half = ((S.w - 1) * g.cw) / 2 || 1;
    const badT = now - this.bad;
    const shake = badT < 420 ? Math.sin((badT / 420) * Math.PI * 6) * (1 - badT / 420) * g.cw * 0.5 : 0;
    const tick = Math.floor(now / 90);
    // a plate in the page colour under the object, so a shark passing behind does not show through it
    const gather = clamp01((now - (this.shapeAt + 560)) / 400);
    if (gather > 0) {
      x.globalAlpha = this.alpha * gather * 0.9;
      x.fillStyle = k.bg;
      x.fillRect(o.x - g.cw + Math.round(shake), o.y - g.ch, (S.w + 2) * g.cw + 1, (S.h + 2) * g.ch + 1);
    }
    x.font = `600 ${Math.round(g.ch * 0.62)}px "Geist Mono","Axiom Mono",ui-monospace,monospace`;
    x.textAlign = "center";
    x.textBaseline = "middle";
    const frame: Frame = { now, g, o, k, shake, badT, tick };
    for (const [i, p] of this.tiles.entries()) this.tile(p, i, frame);
    x.globalAlpha = 1;
  }

  // place moves a tile along its path towards where it is going (u is how far, 0 to 1), with the drift of a loose
  // tile and the bob and shake of a placed one.
  private place(p: Tile, i: number, f: Frame) {
    const { now, g, o } = f;
    const u = clamp01((now - p.t0) / p.d);
    const e = ease(u);
    const arc = Math.sin(Math.PI * u) * (p.s - 0.5) * 90;
    const tx = p.free ? Math.round((p.ux * g.W) / g.cw) * g.cw : o.x + p.cx * g.cw;
    const ty = p.free ? Math.round((p.uy * g.H) / g.ch) * g.ch : o.y + p.cy * g.ch;
    p.tx = tx;
    p.x = p.fx + (tx - p.fx) * e + arc * 0.4;
    p.y = p.fy + (ty - p.fy) * e - Math.abs(arc) * 0.5;
    let px = p.x;
    let py = p.y;
    if (p.free) {
      px += Math.sin(now / 2600 + i) * g.cw * 1.5 * e;
      py += Math.cos(now / 3100 + i * 1.7) * g.ch * e;
    } else if (u >= 1) {
      py += Math.round(Math.sin(now / 900 + (tx / g.cw) * 0.42) * 1.5);
      px += f.shake;
    }
    return { px: Math.round(px), py: Math.round(py), u };
  }

  private tile(p: Tile, i: number, f: Frame) {
    const { px, py, u } = this.place(p, i, f);
    const lit = p.free ? 0 : this.lit(p);
    if (lit < 0) {
      if (u >= 1) this.dot(px, py, f);
      return;
    }
    const hot = lit > 0;
    this.fill(p, hot, f);
    this.context.fillRect(px + 1, py + 1, f.g.cw - 1, f.g.ch - 1);
    const flicker = u < 1 || hash(i, f.tick) < 0.05 || (hot && f.now - this.pulse < 260);
    if (!p.free && f.g.cw >= 8 && flicker) this.letter(px, py, i, f);
  }

  // a tile's ink: faint while loose, the ring colour when lit, the error colour for a moment after a refusal
  private fill(p: Tile, hot: boolean, f: Frame) {
    const x = this.context;
    const warn = f.badT < 900 && !p.free && p.c !== "#";
    x.globalAlpha = this.alpha * (p.free ? 0.16 + p.s * 0.14 : hot ? 1 : 0.74 + p.s * 0.2);
    x.fillStyle = warn ? f.k.err : hot ? f.k.ring : f.k.t1;
  }

  // a digit not typed yet: a dot in its cell
  private dot(px: number, py: number, f: Frame) {
    const x = this.context;
    x.globalAlpha = this.alpha * 0.5;
    x.fillStyle = f.k.t2;
    x.fillRect(px + Math.round(f.g.cw / 2) - 1, py + Math.round(f.g.ch / 2) - 1, 2, 2);
  }

  // a glyph cut out of a tile, as it settles or now and then after
  private letter(px: number, py: number, i: number, f: Frame) {
    const x = this.context;
    x.globalAlpha *= 0.8;
    x.fillStyle = f.k.bg;
    x.fillText(glyph(i, f.tick), px + f.g.cw / 2 + 0.5, py + f.g.ch / 2 + 1);
  }

  destroy() {
    this.canvas.remove();
  }
}

type TitleCell = { x: number; y: number; r: number; blank: boolean };

// The title's characters where they are drawn, in the decode canvas's box.
function cellsOf(chars: HTMLElement[], box: HTMLElement): TitleCell[] {
  const frame = box.getBoundingClientRect();
  return chars.map((n) => {
    const b = n.getBoundingClientRect();
    return {
      x: b.left - frame.left + b.width / 2,
      y: b.top - frame.top + b.height / 2,
      r: b.right - frame.left,
      blank: (n.textContent ?? "").trim() === "",
    };
  });
}

// A character d ms before it resolves: a dot while it waits, a cycling glyph as it comes close, the ring colour next.
function waiting(
  x: CanvasRenderingContext2D,
  c: TitleCell,
  d: number,
  g: string,
  ink: { ring: string; dim: string; fs: number },
) {
  x.globalAlpha = d <= 60 ? 1 : d <= 240 ? 0.85 : 0.5;
  x.fillStyle = d <= 60 ? ink.ring : ink.dim;
  if (d <= 240) {
    x.fillText(g, c.x, c.y);
    return;
  }
  x.beginPath();
  x.arc(c.x, c.y, Math.max(1.5, ink.fs * 0.04), 0, Math.PI * 2);
  x.fill();
}

// decode resolves the title's characters one after another, as the website's hero does, and blinks the caret twice
// after the last. The characters' colour is set through the CSSOM while they wait, and given back as each resolves.
function decode(title: HTMLElement, canvas: HTMLCanvasElement): () => void {
  const chars = [...title.querySelectorAll<HTMLElement>(".ch")];
  const box = canvas.parentElement;
  if (!chars.length || !box) return () => {};
  const cells = cellsOf(chars, box);
  const ink = {
    ring: colour(box, "var(--ring)"),
    dim: colour(box, "var(--text2)"),
    fs: Number.parseFloat(getComputedStyle(chars[0] as HTMLElement).fontSize) || 40,
  };
  const at = (i: number) => 80 + i * 60;
  const end = at(cells.length - 1);
  const last = cells[cells.length - 1] as TitleCell;
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  const W = box.offsetWidth;
  const H = box.offsetHeight;
  canvas.width = Math.round(W * dpr);
  canvas.height = Math.round(H * dpr);
  const x = canvas.getContext("2d") as CanvasRenderingContext2D;
  let ms = 0;
  for (const c of chars) c.style.color = "transparent";
  const release = own(canvas, (dt) => {
    ms += dt * 1000;
    const tick = Math.floor(ms / 50);
    x.setTransform(dpr, 0, 0, dpr, 0, 0);
    x.clearRect(0, 0, W, H);
    x.textAlign = "center";
    x.textBaseline = "middle";
    x.font = `500 ${Math.round(ink.fs * 0.62)}px "Axiom Mono", ui-monospace, monospace`;
    cells.forEach((c, i) => {
      const d = at(i) - ms;
      if (d <= 0) (chars[i] as HTMLElement).style.removeProperty("color");
      else if (!c.blank) waiting(x, c, d, glyph(i, tick), ink);
    });
    const after = ms - end;
    if (after >= 0 && (after < 260 || (after >= 400 && after < 660))) caret(x, last, ink);
    x.globalAlpha = 1;
    if (after >= 700) stop();
  });
  const stop = () => {
    release();
    for (const c of chars) c.style.removeProperty("color");
    x.clearRect(0, 0, canvas.width, canvas.height);
  };
  return stop;
}

function caret(x: CanvasRenderingContext2D, last: TitleCell, ink: { ring: string; fs: number }) {
  x.globalAlpha = 1;
  x.fillStyle = ink.ring;
  const w = Math.max(2, Math.round(ink.fs * 0.05));
  x.fillRect(Math.round(last.r + ink.fs * 0.06), Math.round(last.y - ink.fs * 0.42), w, Math.round(ink.fs * 0.84));
}

// The Login board's sea (its ROSTER and the options its boot passes the engine).
const roster = [
  { kind: "hammerhead", size: 1 },
  { kind: "classic", size: 0.72 },
  { kind: "classic", size: 0.52 },
];

export type Login = {
  readonly sea: Sea;
  show: (shape: Shape) => void;
  hear: (heard: Heard) => void;
  // setLight turns the sea and the stage to the light or the dark page
  setLight: (light: boolean) => void;
  // decode plays the title's decode once, for an arrival; the page passes its title and the form's decode canvas.
  decode: (title: HTMLElement, canvas: HTMLCanvasElement) => void;
  destroy: () => void;
};

// The options the Login board's boot passes its sea, sized to the frame.
function seaOptions(frame: HTMLElement, canvas: HTMLCanvasElement, light: boolean) {
  const side = Math.min(frame.clientWidth || innerWidth, frame.clientHeight || innerHeight);
  return {
    font: '"Geist Mono", ui-monospace, Menlo, monospace',
    light,
    whale: null,
    bgLight: "#030302",
    bgDark: "#111113",
    sizeK: (0.64 * side) / Math.max(1, Math.min(canvas.clientWidth || innerWidth, canvas.clientHeight || innerHeight)),
    maxLen: 640,
    warm: 2,
    virtualScale: 1,
    cell: Math.min(13, Math.max(8, Math.round((frame.clientWidth || innerWidth) / 124))),
  };
}

// startLogin starts the sea and the stage on frame, the Login board's .lg, showing shape.
export function startLogin(frame: HTMLElement, light: boolean, shape: Shape, arrived: boolean): Login {
  const canvas = frame.querySelector<HTMLCanvasElement>("canvas.sea");
  const seaBox = frame.querySelector<HTMLElement>(".lg-sea");
  if (!canvas || !seaBox) throw new Error("startLogin: the frame has no .lg-sea with a canvas.sea");
  const still = reducedMotion();
  const opts = seaOptions(frame, canvas, light);
  const engine = buildSea(canvas, roster, opts, still);
  const stage = new Stage(frame, seaBox, engine);
  stage.first(shape, performance.now(), arrived && !still);
  let stopDecode = () => {};
  let sea: Sea;
  let release = () => {};
  if (still) {
    sea = stillSea(engine);
    const redraw = () => stage.draw(performance.now());
    const resized = new ResizeObserver(redraw);
    resized.observe(seaBox);
    release = () => resized.disconnect();
    redraw();
  } else {
    sea = { engine, destroy: () => engine.destroy() };
    release = own(canvas, (dt, at) => {
      engine.tick(dt);
      stage.draw(at);
    });
  }
  const redrawStill = () => still && stage.draw(performance.now());
  return {
    sea,
    show: (next) => {
      stage.to(next, performance.now());
      if (still) stage.settle();
      redrawStill();
    },
    hear: (heard) => {
      stage.hear(heard, performance.now());
      redrawStill();
    },
    setLight: (light) => {
      sea.engine.setLight(light);
      stage.recolour();
      redrawStill();
    },
    decode: (title, decodeCanvas) => {
      if (!still) stopDecode = decode(title, decodeCanvas);
    },
    destroy: () => {
      stopDecode();
      release();
      stage.destroy();
      sea.destroy();
    },
  };
}
