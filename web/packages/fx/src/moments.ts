// The moments of the sea's easter eggs (eggs.ts) that the product's sea (ocean-orig.js) does not have: food, a
// whirlpool, fish drawn as 0 and 1, the tide line, the milestone's ring and the deep's glow. They are ported from the
// design's ocean-react.js, moments.js and dive.js onto the sea's own world and renderer, which run unedited.
import { colour } from "./colour";
import { hash } from "./hash";
import { mono } from "./mono";

type Fish = { x: number; y: number; vx: number; vy: number; alive: boolean; millDir: number };
type Shark = {
  nodes: Float32Array;
  hidden?: boolean;
  ghost?: boolean;
  lure?: { x: number; y: number; w: number } | null;
};
type World = {
  fish: Fish[];
  sharks: Shark[];
  pointer: { active: boolean };
  time: number;
  width: number;
  height: number;
  stepFish: (dt: number) => void;
};
type Renderer = {
  ctx: CanvasRenderingContext2D;
  cols: number;
  cw: number;
  ch: number;
  fishBuckets: number[][];
  fishStyles: string[];
  energy: Float32Array;
  stamp: (field: Float32Array, x: number, y: number, radius: number, amount: number) => void;
  drawFish: () => void;
};
// what the eggs use of the engine beyond what the page's types say (ocean-orig.js World, Renderer, Engine)
export type Engine = {
  world: World;
  renderer: Renderer;
  local: (e: PointerEvent) => { x: number; y: number; inside: boolean };
};

// ocean-react.js FISH_FLEE_K: the force a fish answers food and a whirlpool with
const pull = 1100;
const clamp = (v: number, a: number, b: number) => Math.min(b, Math.max(a, v));
// attack, hold, release (moments.js env)
const env = (t: number, a: number, hold: number, r: number) =>
  t < a ? t / a : t < a + hold ? 1 : clamp(1 - (t - a - hold) / r, 0, 1);

// tend gives the sea what the design's sea had: food the fish swim to (2.6 s), a whirlpool they circle, fish drawn as 0
// and 1. The world steps its fish as before; the food's and the whirlpool's pull is added after, from the same formulas.
export function tend({ world, renderer }: Engine) {
  const food: { x: number; y: number; t: number }[] = [];
  let swirl = { x: 0, y: 0, until: 0 };
  let binary = false;
  const stepFish = world.stepFish;
  world.stepFish = (dt) => {
    const swirling = world.time < swirl.until;
    const pointer = world.pointer.active;
    // the whirlpool borrows the pointer: the fish circle it instead of fleeing the pointer
    if (swirling) world.pointer.active = false;
    stepFish.call(world, dt);
    world.pointer.active = pointer;
    while (food[0] && world.time - food[0].t > 2.6) food.shift();
    if (food.length || swirling) pulls(world, food, swirling ? swirl : null, dt);
    if (swirling) renderer.stamp(renderer.energy, swirl.x, swirl.y, 1, 0.08);
  };
  const drawFish = renderer.drawFish;
  renderer.drawFish = () => {
    if (!binary) return drawFish.call(renderer);
    const { ctx, cw, ch, cols, fishBuckets, fishStyles } = renderer;
    fishBuckets.forEach((bucket, b) => {
      ctx.fillStyle = fishStyles[b] ?? "";
      for (const i of bucket)
        ctx.fillText(
          (Math.imul(i, 2654435761) >>> 0) % 3 ? "0" : "1",
          (i % cols) * cw + cw / 2,
          ((i / cols) | 0) * ch + ch / 2 + 1,
        );
    });
  };
  return {
    feed: (x: number, y: number) => {
      food.push({ x, y, t: world.time });
      renderer.stamp(renderer.energy, x, y, 3, 0.7);
    },
    whirl: (x: number, y: number, seconds: number) => {
      swirl = { x, y, until: world.time + seconds };
    },
    binary: (on: boolean) => {
      binary = on;
    },
    // the nearest shark comes over to x, y until the returned function lets it go
    lure: (x: number, y: number) => {
      const near = (s: Shark) => Math.hypot((s.nodes[0] ?? 0) - x, (s.nodes[1] ?? 0) - y);
      const shark = world.sharks.filter((s) => !s.hidden && !s.ghost).sort((a, b) => near(a) - near(b))[0];
      if (shark) shark.lure = { x, y, w: 2.4 };
      return () => {
        if (shark) shark.lure = null;
      };
    },
    restore: () => {
      world.stepFish = stepFish;
      renderer.drawFish = drawFish;
    },
  };
}

// pulls draws the living fish to the food and round the whirlpool, if there is one
function pulls(
  world: World,
  food: { x: number; y: number; t: number }[],
  swirl: { x: number; y: number } | null,
  dt: number,
) {
  for (const f of world.fish) {
    if (!f.alive) continue;
    for (const fd of food) toFood(f, fd, (1 - (world.time - fd.t) / 2.6) * dt);
    if (swirl) orbit(f, swirl, dt);
  }
}

// ocean-react.js's food: a fish within 300 px swims to it, less as the food ages (fresh is 1)
function toFood(f: Fish, at: { x: number; y: number }, fresh: number) {
  const dx = at.x - f.x;
  const dy = at.y - f.y;
  const d = Math.hypot(dx, dy);
  if (d >= 300 || d <= 6) return;
  const w = (1 - d / 300) * fresh * pull * 0.55;
  f.vx += (dx / d) * w;
  f.vy += (dy / d) * w;
}

// ocean-react.js's pointer in "orbit" mode: the fish within 240 px are drawn to a ring 64 px out and round it
function orbit(f: Fish, at: { x: number; y: number }, dt: number) {
  const dx = f.x - at.x;
  const dy = f.y - at.y;
  const d = Math.hypot(dx, dy);
  if (d >= 240 || d < 0.001) return;
  const w = (1 - d / 240) * pull * dt;
  const ring = clamp((d - 64) / 64, -1, 1);
  f.vx += ((-dx / d) * ring * 0.55 - (dy / d) * 0.32 * f.millDir) * w;
  f.vy += ((-dy / d) * ring * 0.55 + (dx / d) * 0.32 * f.millDir) * w;
}

type Effect = { t: number; dur: number; draw: (t: number) => void };
type Glow = { c: number; r: number; p: number; o: number; th: number };

// a point of the deep at night (dive.js): the points under the glow's level each pulse slowly on their own period
function glowOf(p: Glow, bio: number, time: number) {
  if (p.th > bio) return 0;
  return (0.5 + 0.5 * Math.sin((time * 6.283) / p.p + p.o)) ** 3 * 0.75 * Math.min(1, (bio - p.th) * 4);
}

type Grid = { W: number; H: number; cw: number; ch: number; points: Glow[] };
type Pen = (text: string, x: number, y: number, alpha: number) => void;

// measure sizes the canvas to its box (at most 1.5 device pixels a pixel) and lays the moments.js grid over it, with
// the night's points of the deep (dive.js)
function measure(canvas: HTMLCanvasElement): Grid {
  const W = canvas.clientWidth;
  const H = canvas.clientHeight;
  const dpr = Math.min(window.devicePixelRatio || 1, 1.5);
  canvas.width = Math.round(W * dpr);
  canvas.height = Math.round(H * dpr);
  const cw = clamp(Math.round(W / 124), 8, 13);
  const ch = Math.round(cw * 1.45);
  const cols = Math.ceil(W / cw);
  const rows = Math.ceil(H / ch);
  const points = Array.from({ length: 46 }, (_, i) => ({
    c: Math.floor(hash(i, 3) * cols),
    r: Math.floor(hash(i, 7) * rows * 0.86),
    p: 3.5 + hash(i, 11) * 4,
    o: hash(i, 13) * 6.28,
    th: hash(i, 17),
  }));
  return { W, H, cw, ch, points };
}

// the hour: one slow tide line rises from the bottom to the top (7 s)
function tideLine({ W, H, cw, ch }: Grid, glyph: Pen, t: number) {
  const y = H * (1 - t / 6.5);
  for (let c = 0; c < Math.ceil(W / cw); c++) {
    if (hash(c, Math.floor(t * 4)) < 0.45) continue;
    const off = Math.sin(c * 0.18 + t * 2) * ch * 0.6 + (hash(c, 1) - 0.5) * ch;
    glyph(hash(c, 3) < 0.8 ? "~" : "-", c * cw + cw / 2, y + off, 0.42 * env(t, 0.5, 5.5, 1));
  }
}

// a milestone: one slow ring from the centre of the open water (2.6 s)
function ring({ W, H, cw }: Grid, glyph: Pen, t: number) {
  const R = t * 230;
  const n = Math.max(12, Math.round((R * 6.283) / (cw * 1.4)));
  for (let i = 0; i < n; i++) {
    const q = (i / n) * 6.283;
    glyph("~", W * 0.5 + Math.cos(q) * R, H * 0.8 + Math.sin(q) * R * 0.55, (1 - t / 2.6) * 0.8);
  }
}

// glyphs is the light layer over the sea for what the engine does not draw (moments.js, dive.js): the tide line, the
// milestone's ring and, at night, the faint points of the deep. It draws in the page's text colour on its own canvas.
export function glyphs(canvas: HTMLCanvasElement, ho: HTMLElement) {
  const g = canvas.getContext("2d") as CanvasRenderingContext2D;
  let grid = measure(canvas);
  let time = 0;
  let bio = 0;
  let ink = { scheme: "", value: "" };
  const effects: Effect[] = [];
  const resized = new ResizeObserver(() => {
    grid = measure(canvas);
  });
  resized.observe(canvas);
  const glyph: Pen = (text, x, y, alpha) => {
    g.globalAlpha = clamp(alpha, 0, 1);
    g.fillText(text, x, y);
  };
  // the glyphs' font and colour, the page's text colour in the scheme shown
  const pen = () => {
    const scheme = ho.classList.contains("light") ? "light" : "dark";
    if (ink.scheme !== scheme) ink = { scheme, value: colour(ho, "var(--text1)") };
    g.setTransform(canvas.width / (grid.W || 1), 0, 0, canvas.height / (grid.H || 1), 0, 0);
    g.font = `600 ${Math.round(grid.ch * 0.74)}px ${mono()}`;
    g.textAlign = "center";
    g.textBaseline = "middle";
    g.fillStyle = ink.value;
  };
  const glow = () => {
    for (const p of grid.points) {
      const a = glowOf(p, bio, time);
      if (a >= 0.02) glyph(a > 0.4 ? "+" : ".", p.c * grid.cw + grid.cw / 2, p.r * grid.ch + grid.ch / 2, a);
    }
  };
  const add = (dur: number, draw: (grid: Grid, glyph: Pen, t: number) => void) =>
    effects.push({ t: 0, dur, draw: (t) => draw(grid, glyph, t) });
  return {
    busy: () => effects.length > 0,
    night: (level: number) => {
      bio = level;
    },
    draw: (dt: number) => {
      time += dt;
      for (const e of effects) e.t += dt;
      // an effect past its end is gone from this frame on, so the layer is clear when the last one ends
      effects.splice(0, effects.length, ...effects.filter((e) => e.t < e.dur));
      g.setTransform(1, 0, 0, 1, 0, 0);
      g.clearRect(0, 0, canvas.width, canvas.height);
      if (bio < 0.01 && !effects.length) return;
      pen();
      if (bio >= 0.01) glow();
      for (const e of effects) e.draw(e.t);
      g.globalAlpha = 1;
    },
    tide: () => add(7, tideLine),
    milestone: () => add(2.6, ring),
    destroy: () => resized.disconnect(),
  };
}
