import { own, reducedMotion, timeline } from "@axiom/fx";
import type { Script } from "./script";

// The sea bed's script (Site.dc.html M6): a dune of glyphs with the wordmark half buried in it, stirred by the pointer,
// and under the page's closing words a reel of models that rolls up one every few seconds. The dune is drawn on the
// shared frame loop, which rests it while the screen is out of view or the page hidden; under reduced motion it is one
// still frame and the reel does not roll.
const models = 8;
// the glyph cell (and the glyph size) of the drawing: the narrow layout draws it smaller
const WIDE = { height: 280, cell: 10, glyph: 8 };
const NARROW = { height: 160, cell: 6, glyph: 5 };
const SAND = " .:-=+*#";
const WM = " .:;otX%S8#@";
const SOLID = "SSXX%%88@#t";
const CREST = "~~-=~-";
const SURGE_W = 260;
const EDGE = ":;ot.:";
// a frame of the dune each 50 ms is enough for sand
const step = 50;

const hash3 = (a: number, b: number, c: number) => {
  let h = (a * 73856093) ^ (b * 19349663) ^ (c * 83492791);
  h = (h ^ (h >>> 13)) * 1274126177;
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296;
};
const noise = (x: number, y: number) => {
  const xi = Math.floor(x);
  const yi = Math.floor(y);
  const u = smooth(x - xi);
  const w = smooth(y - yi);
  const a = hash3(xi, yi, 7);
  const b = hash3(xi + 1, yi, 7);
  const c = hash3(xi, yi + 1, 7);
  const d = hash3(xi + 1, yi + 1, 7);
  return a + (b - a) * u + (c - a) * w + (a - b - c + d) * u * w;
};
const smooth = (v: number) => v * v * (3 - 2 * v);

type Palette = { ink: string; tile: string; sand: string; accent: string };
type Bed = {
  ctx: CanvasRenderingContext2D;
  width: number;
  height: number;
  cols: number;
  rows: number;
  offset: number;
  mask: Float32Array;
  energy: Float32Array;
  wet: number[];
  cell: number;
  glyph: number;
};

// Where the dune's top lies at x: a slow swell, small ripples, and a surge that rolls across every seven seconds, piling
// the sand up over the letters as it passes.
const surgeAt = (x: number, t: number, width: number, height: number) => {
  const sx = (x - ((t % 7) / 7) * (width + 900) + 450) / SURGE_W;
  return height * 0.13 * Math.exp(-sx * sx) * (sx < 0 ? 1 : 0.55);
};
const topAt = (x: number, t: number, width: number, height: number) =>
  height * 0.72 +
  9 * Math.sin(x * 0.0042 - t * 0.32) +
  4 * Math.sin(x * 0.013 + t * 0.55) +
  2 * Math.sin(x * 0.04 - t * 0.9) -
  surgeAt(x, t, width, height);

// The wordmark, drawn once off screen and sampled cell by cell: how much of each cell the letters cover.
function wordmark(width: number, height: number, cols: number, rows: number, cell: number, narrow: boolean) {
  const off = document.createElement("canvas");
  off.width = width;
  off.height = height;
  const o = off.getContext("2d") as CanvasRenderingContext2D;
  o.font = "600 100px 'Axiom Sans', sans-serif";
  const w100 = o.measureText("Axiom").width;
  const size = Math.min(((width * (narrow ? 0.94 : 0.86)) / w100) * 100, height * 1.12);
  o.font = `600 ${size}px 'Axiom Sans', sans-serif`;
  const tw = o.measureText("Axiom").width;
  o.fillStyle = "#000";
  o.textBaseline = "alphabetic";
  o.fillText("Axiom", (width - tw) / 2, height * (narrow ? 0.08 : 0.16) + size * 0.72);
  const px = o.getImageData(0, 0, width, height).data;
  const offset = (width - cols * cell) / 2;
  const mask = new Float32Array(cols * rows);
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      let a = 0;
      let n = 0;
      for (let sy = 0.25; sy < 1; sy += 0.5) {
        for (let sx = 0.25; sx < 1; sx += 0.5) {
          const x = Math.floor(offset + (c + sx) * cell);
          const y = Math.floor((r + sy) * cell);
          a += (px[(y * width + x) * 4 + 3] ?? 0) / 255;
          n++;
        }
      }
      mask[r * cols + c] = a / n;
    }
  }
  return { mask, offset };
}

// What a frame draws, by colour (0 the ink, 1 the sand, 2 the accent) and opacity in tenths: the tiles under the glyphs
// and the glyphs, each as flat lists of positions.
class Layers {
  readonly tiles = new Map<number, number[]>();
  readonly glyphs = new Map<number, (string | number)[]>();
  private static key = (colour: number, alpha: number) =>
    colour * 16 + Math.max(1, Math.min(10, Math.round(alpha * 10)));
  tile(colour: number, alpha: number, x: number, y: number) {
    if (alpha < 0.05) return;
    Layers.list(this.tiles, Layers.key(colour, alpha)).push(x, y);
  }
  glyph(colour: number, alpha: number, ch: string | undefined, x: number, y: number) {
    Layers.list(this.glyphs, Layers.key(colour, alpha)).push(ch ?? " ", x, y);
  }
  private static list<T>(map: Map<number, T[]>, key: number) {
    const list = map.get(key) ?? [];
    map.set(key, list);
    return list;
  }
}

// One column of the dune at a moment: where its sand stands, and what the surge and the sweep are doing to it.
type Column = { c: number; x: number; t: number; top: number; damp: number; lift: number; sweep: number };

const pick = (chars: string, h: number) => chars[Math.floor(h * chars.length)];

// The crest: one bright row riding on top of the sand, lit cyan where the surge is breaking.
function crest(layers: Layers, { c, x, t, lift }: Column, y: number, height: number) {
  const foam = Math.max(0, lift / (height * 0.13));
  layers.tile(foam > 0.45 ? 2 : 1, 0.16 + 0.14 * foam, x, y);
  layers.glyph(foam > 0.45 ? 2 : 1, 0.75 + 0.25 * foam, pick(CREST, hash3(c, 1, Math.floor(t * 3))), x, y);
}

// A cell under the sand's top: the buried letters read through the first few rows, then sink out of sight; below them
// the sand lies in soft layers that drift sideways with the swell.
function sand(bed: Bed, layers: Layers, column: Column, r: number, m: number) {
  const { c, x, t, top } = column;
  const y = (r + 0.5) * bed.cell;
  const d = y - top;
  if (d < bed.cell) return crest(layers, column, y, bed.height);
  const show = m > 0.3 ? m * Math.max(0, 1 - d / (bed.height * 0.34)) ** 1.6 : 0;
  if (show > 0.06) {
    layers.tile(1, 0.1, x, y);
    layers.tile(0, 0.22 * show, x, y);
    const h = hash3(c, r, Math.floor(t * 0.5 + hash3(c, r, 3) * 9));
    return layers.glyph(0, 0.12 + 0.5 * show, pick(SOLID, h), x, y);
  }
  const dep = Math.min(1, d / Math.max(bed.cell, bed.height - top));
  const band = 0.5 + 0.5 * Math.sin(d * 0.22 + noise(c * 0.05 - t * 0.12, r * 0.2) * 3);
  const grain = hash3(c, r, Math.floor(t * 0.8 + c * 0.13)) - 0.5;
  const idx = Math.max(1, Math.min(SAND.length - 1, Math.round(1 + dep * 4 + band * 1.6 + grain * 0.8)));
  // a faint tile under every grain, so the floor reads as one solid mass in the same language as the sharks
  layers.tile(1, 0.05 + 0.07 * (1 - dep) + 0.03 * band, x, y);
  layers.glyph(1, 0.12 + 0.3 * dep + 0.12 * band, SAND[idx], x, y);
}

// A cell in the water above the sand: the wordmark, tiles and glyphs like the sharks (the sweep lights it as it
// passes), or now and then a speck of the sea.
function water(layers: Layers, { c, x, t, top, sweep }: Column, r: number, y: number, m: number) {
  if (m <= 0.08) {
    if (y > top - 48 && hash3(c, r, Math.floor(t * 0.6 + r)) > 0.975) layers.glyph(1, 0.3, ".", x, y);
    return;
  }
  const sd = x - sweep;
  const ping = sd < 0 && sd > -220 ? (1 + sd / 220) ** 2 : 0;
  layers.tile(0, 0.26 + 0.52 * m, x, y);
  if (ping > 0.02) layers.tile(2, 0.32 * ping * m, x, y);
  // the same alphabet the sharks are made of; each tile keeps its letter, a few swap now and then
  const h = hash3(c, r, Math.floor(t * 0.5 + hash3(c, r, 3) * 9));
  layers.glyph(ping > 0.35 ? 2 : 0, 0.5 + 0.5 * m, pick(m > 0.55 ? SOLID : EDGE, h), x, y);
}

function cell(bed: Bed, layers: Layers, column: Column, r: number) {
  const { c, x, top, damp } = column;
  const y = (r + 0.5) * bed.cell;
  const i = r * bed.cols + c;
  const m = bed.mask[i] ?? 0;
  const e = bed.energy[i] ?? 0;
  if (e > 0.04) {
    // stirred: the cell lights up and tumbles through glyphs
    layers.tile(2, 0.12 + 0.3 * e, x, y);
    layers.glyph(2, 0.4 + 0.6 * e, WM[2 + Math.floor(Math.random() * 8)], x, y);
    bed.energy[i] = e * 0.88;
    return;
  }
  bed.energy[i] = 0;
  if (y >= top) return sand(bed, layers, column, r, m);
  // freshly uncovered: a damp trace fading back to clear water
  if (y >= damp && m <= 0.08) return layers.glyph(1, 0.08 + 0.2 * (1 - (top - y) / Math.max(1, top - damp)), ".", x, y);
  water(layers, column, r, y, m);
}

class BedRun implements Script<object> {
  private readonly clock;
  private readonly still = reducedMotion();
  private readonly layout;
  private bed: Bed | undefined;
  private palette: Palette | undefined;
  private release = () => {};
  private readonly resize: ResizeObserver;
  private last = 0;
  private swapped = 0;
  private dead = false;

  constructor(
    private readonly stage: HTMLElement,
    private readonly changed: () => void,
    private readonly narrow: boolean,
  ) {
    this.layout = narrow ? NARROW : WIDE;
    this.clock = timeline(stage);
    const art = this.art();
    // the glyphs are laid out by the canvas's own width, which a resize changes
    this.resize = new ResizeObserver(() => this.build());
    this.resize.observe(art);
    this.place(0, false);
    if (!this.still) {
      stage.addEventListener("pointermove", this.move);
      stage.addEventListener("pointerdown", this.press);
      this.roll();
    }
  }

  private art() {
    return this.stage.querySelector<HTMLCanvasElement>("canvas.art") as HTMLCanvasElement;
  }

  // The dune's colours, as the page's tokens give them now: the canvas takes a resolved colour, which a custom property
  // holding light-dark() is not until an element has it as its colour.
  retheme() {
    const probe = document.createElement("span");
    probe.hidden = true;
    this.stage.append(probe);
    const resolve = (name: string) => {
      probe.style.color = `var(${name})`;
      return getComputedStyle(probe).color;
    };
    this.palette = {
      ink: resolve("--artInk"),
      tile: resolve("--artTile"),
      sand: resolve("--artSand"),
      accent: resolve("--accent"),
    };
    probe.remove();
    this.draw(this.still ? 0 : performance.now() / 1000);
  }

  private build() {
    if (this.dead) return;
    const art = this.art();
    const { height, cell, glyph } = this.layout;
    const width = art.clientWidth;
    if (!width) return;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    art.width = Math.round(width * dpr);
    art.height = Math.round(height * dpr);
    const ctx = art.getContext("2d") as CanvasRenderingContext2D;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const cols = Math.floor(width / cell);
    const rows = Math.floor(height / cell);
    const { mask, offset } = wordmark(width, height, cols, rows, cell, this.narrow);
    this.bed = {
      ctx,
      width,
      height,
      cols,
      rows,
      offset,
      mask,
      energy: new Float32Array(cols * rows),
      wet: [],
      cell,
      glyph,
    };
    this.release();
    this.release = this.still ? () => {} : own(art, (_, now) => this.frame(now));
    this.retheme();
  }

  private frame(now: number) {
    if (now - this.last < step) return;
    this.last = now;
    this.draw(now / 1000);
  }

  // stirring: the pointer (or a tap) disturbs the glyphs around it, like a hand through water
  private stir(e: PointerEvent, k: number) {
    const bed = this.bed;
    if (!bed) return;
    const box = this.art().getBoundingClientRect();
    const s = box.width / bed.width;
    const x = (e.clientX - box.left) / s;
    const y = (e.clientY - box.top) / s;
    if (y < -60 || y > bed.height + 20) return;
    const reach = this.narrow ? 36 : 64;
    const { cols, rows, offset, cell, energy } = bed;
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const d = Math.hypot(offset + (c + 0.5) * cell - x, (r + 0.5) * cell - y);
        if (d < reach) {
          const i = r * cols + c;
          energy[i] = Math.min(1, (energy[i] ?? 0) + k * (1 - d / reach));
        }
      }
    }
  }
  private readonly move = (e: PointerEvent) => this.stir(e, 0.35);
  private readonly press = (e: PointerEvent) => this.stir(e, 1);

  private draw(t: number) {
    const bed = this.bed;
    const palette = this.palette;
    if (!bed || !palette) return;
    const layers = new Layers();
    const sweep = ((t % 9) / 9) * (bed.width + 900) - 300;
    for (let c = 0; c < bed.cols; c++) {
      const x = bed.offset + (c + 0.5) * bed.cell;
      const top = topAt(x, t, bed.width, bed.height);
      // the backwash: where the sand stood a moment ago stays damp for a while
      const damp = Math.min((bed.wet[c] ?? top) + 0.9, top);
      bed.wet[c] = damp;
      const column = { c, x, t, top, damp, lift: surgeAt(x, t, bed.width, bed.height), sweep };
      for (let r = 0; r < bed.rows; r++) cell(bed, layers, column, r);
    }
    this.paint(bed, palette, layers);
  }

  private paint({ ctx, width, height, cell, glyph }: Bed, palette: Palette, { tiles, glyphs }: Layers) {
    const colours = [palette.ink, palette.sand, palette.accent];
    const tileColours = [palette.tile, palette.sand, palette.accent];
    ctx.clearRect(0, 0, width, height);
    const side = cell - 1;
    for (const [k, list] of tiles) {
      ctx.fillStyle = tileColours[Math.floor(k / 16)] as string;
      ctx.globalAlpha = (k % 16) / 10;
      for (let j = 0; j < list.length; j += 2)
        ctx.fillRect((list[j] as number) - side / 2, (list[j + 1] as number) - side / 2, side, side);
    }
    ctx.font = `${glyph}px 'Axiom Mono', monospace`;
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    for (const [k, list] of glyphs) {
      ctx.fillStyle = colours[Math.floor(k / 16)] as string;
      ctx.globalAlpha = (k % 16) / 10;
      for (let j = 0; j < list.length; j += 3)
        ctx.fillText(list[j] as string, list[j + 1] as number, (list[j + 2] as number) + 0.5);
    }
    ctx.globalAlpha = 1;
  }

  // the reel rolls up one model every few seconds; at the end it jumps back without a seam
  private place(p: number, animate: boolean) {
    const reel = this.stage.querySelector<HTMLElement>(".reel");
    if (!reel) return;
    reel.style.transition = animate ? "transform 250ms ease-out" : "none";
    reel.style.transform = `translateY(${-p * 26}px)`;
    for (const [j, child] of [...reel.children].entries()) child.classList.toggle("on", j === p + 1);
  }

  private async roll() {
    let p = 0;
    while (await this.clock.sleep(2400)) {
      p += 1;
      this.place(p, true);
      this.swapped++;
      this.changed();
      if (p === models) {
        if (!(await this.clock.sleep(300))) return;
        p = 0;
        this.place(0, false);
      }
    }
  }

  values() {
    return { m6_mswap: this.swapped };
  }

  destroy() {
    this.dead = true;
    this.release();
    this.resize.disconnect();
    this.stage.removeEventListener("pointermove", this.move);
    this.stage.removeEventListener("pointerdown", this.press);
    this.clock.end();
  }
}

export const start = (stage: HTMLElement, changed: () => void, narrow: boolean): BedRun =>
  new BedRun(stage, changed, narrow);
