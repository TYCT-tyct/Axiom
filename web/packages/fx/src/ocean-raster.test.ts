// @vitest-environment jsdom
import "@axiom/boards/engines/ocean-orig.js";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

// The sea's faster rasterize must cover every cell exactly as the design's (rasterizeRef): both run on the same
// recorded worlds, the sea's roster and every other kind, swimming, turning hard and leaving a solid all at once.

type Shark = { kind: string; nodes: Float32Array | number[]; turn: number; length: number };
type World = {
  sharks: Shark[];
  time: number;
  step(dt: number): void;
  resize(w: number, h: number, cw: number, ch: number): void;
};
type Renderer = {
  cw: number;
  ch: number;
  resize(w: number, h: number, dpr: number): void;
  rasterize(sharks: Shark[], t: number): void;
  rasterizeRef(sharks: Shark[], t: number): void;
} & Record<"flag" | "shade" | "key" | "sdepth" | "kind", ArrayLike<number>>;
type Ocean = {
  World: new (specs: object[], opts: object) => World;
  Renderer: new (canvas: HTMLCanvasElement, font: string) => Renderer;
};

const ocean = (window as unknown as { AxiomOceanOrig: Ocean }).AxiomOceanOrig;
const fields = ["flag", "shade", "key", "sdepth", "kind"] as const;

beforeEach(() => {
  // a seeded Math.random, so a failure replays
  let seed = 7;
  vi.spyOn(Math, "random").mockImplementation(() => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  });
  const ctx = new Proxy({}, { get: (_, key) => (key === "measureText" ? () => ({ width: 8 }) : () => {}) });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
});
afterEach(() => vi.restoreAllMocks());

function sea(specs: object[], width: number, height: number, whale: object = { whale: null }) {
  const world = new ocean.World(specs, { sizeK: 0.64, maxLen: 640, ...whale });
  const renderer = new ocean.Renderer(document.createElement("canvas"), "monospace");
  renderer.resize(width, height, 1);
  world.resize(width, height, renderer.cw, renderer.ch);
  return { world, renderer };
}

// the cells of both rasterizes for the sharks as they are; the counts of differing cells per field
function differences({ world, renderer }: ReturnType<typeof sea>) {
  renderer.rasterizeRef(world.sharks, world.time);
  const want = fields.map((f) => Array.from(renderer[f]));
  renderer.rasterize(world.sharks, world.time);
  const covered = want[0]?.filter(Boolean).length ?? 0;
  return { covered, diff: fields.map((f, i) => Array.from(renderer[f]).filter((v, j) => v !== want[i]?.[j]).length) };
}

// what Morph.release does: every shark leaves the middle nose first, each its own way
function leaveTogether(world: World, cx: number, cy: number) {
  world.sharks.forEach((sh, j) => {
    const n = sh.nodes,
      count = n.length / 2,
      seg = sh.length / (count - 1),
      a = (j * Math.PI * 2) / world.sharks.length;
    for (let k = 0; k < count; k++) {
      const t = sh.length * 0.42 - k * seg;
      n[k * 2] = cx + Math.cos(a) * t;
      n[k * 2 + 1] = cy + Math.sin(a) * t;
    }
  });
}

const home = [
  { kind: "hammerhead", size: 1 },
  { kind: "classic", size: 0.72 },
  { kind: "classic", size: 0.58 },
  { kind: "classic", size: 0.46 },
];
const kinds = ["tiger", "zebra", "greathammer", "mako", "thresher", "nurse"];
// a whale, drawn by the design's own loop in both (side and top views)
const whales = [
  { whale: true, whaleFirst: 0 },
  { whale: true, whaleFirst: 0, whaleKind: "top" },
];

type Case = { name: string; specs: object[]; width: number; height: number; whale?: object };
const cases: Case[] = [
  { name: "the home sea, wide", specs: home, width: 1440, height: 900 },
  { name: "the sign-in sea", specs: home.slice(0, 2), width: 806, height: 900 },
  { name: "the home sea on a phone", specs: home, width: 390, height: 844 },
  { name: "every other kind", specs: kinds.map((kind) => ({ kind, size: 0.8 })), width: 1440, height: 900 },
  ...whales.map((whale) => ({
    name: `a ${whale.whaleKind ?? "side"} whale`,
    specs: home.slice(0, 2),
    width: 1440,
    height: 900,
    whale,
  })),
];

it.each(cases)(
  "$name: the faster rasterize covers the cells the design's does",
  ({ specs, width, height, whale }) => {
    const s = sea(specs, width, height, whale);
    let covered = 0;
    for (let frame = 0; frame < 360; frame++) {
      s.world.step(1 / 60);
      if (frame % 6) continue;
      // a third of the frames turn every shark as hard as it banks, the other way each time
      if (frame % 18 === 0) for (const sh of s.world.sharks) sh.turn = frame % 36 ? 1 : -1;
      if (frame === 198) leaveTogether(s.world, width / 2, height / 2);
      const d = differences(s);
      covered += d.covered;
      expect(d.diff, `frame ${frame}`).toEqual([0, 0, 0, 0, 0]);
    }
    expect(covered).toBeGreaterThan(1000);
  },
  30_000,
);
