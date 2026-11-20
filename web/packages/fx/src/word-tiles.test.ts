// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { drawWord, planWord, type RawWordTile, type WordCast, type WordCell } from "./word-tiles";

function makeCells(n: number): WordCell[] {
  return Array.from({ length: n }, (_, i) => ({
    x: 800 + (i % 10) * 20,
    y: Math.floor(i / 10) * 20,
    w: 20,
    h: 20,
    at: 100 + i * 15,
  }));
}

function makeCast(tiles: RawWordTile[]): WordCast {
  return {
    tiles,
    tile: "#888888",
    ink: "#111111",
    gf: 8,
    hide: vi.fn(),
    show: vi.fn(),
  };
}

it("returns empty when the frame has no __axCast or the sea floor is out of view", () => {
  const frame = document.createElement("div");
  expect(planWord(frame, makeCells(20), 800, 500)).toEqual({ word: [], wm: null, landed: 0 });
  (frame as HTMLElement & { __axCast?: { wordTiles: () => WordCast | null } }).__axCast = {
    wordTiles: () => null,
  };
  expect(planWord(frame, makeCells(20), 800, 500)).toEqual({ word: [], wm: null, landed: 0 });
});

it("hides seabed letters, pairs each tile with an incoming panel cell, and draws flying tiles", () => {
  const frame = document.createElement("div");
  const tiles: RawWordTile[] = Array.from({ length: 12 }, (_, i) => ({
    x: 100 + i * 10,
    y: 700,
    w: 10,
    h: 10,
    m: 0.8,
    li: i % 5,
  }));
  const cast = makeCast(tiles);
  (frame as HTMLElement & { __axCast?: { wordTiles: () => WordCast | null } }).__axCast = {
    wordTiles: () => cast,
  };
  const cells = makeCells(40);
  const planned = planWord(frame, cells, 800, 600);
  expect(cast.hide).toHaveBeenCalledTimes(1);
  expect(planned.word).toHaveLength(12);
  expect(planned.landed).toBeGreaterThan(400);
  expect(cells.some((c) => c.word)).toBe(true);

  const rects: number[][] = [];
  const ctx = {
    font: "",
    textAlign: "",
    textBaseline: "",
    globalAlpha: 1,
    fillStyle: "",
    fillRect: (...args: number[]) => rects.push(args),
    fillText: vi.fn(),
  } as unknown as CanvasRenderingContext2D;
  drawWord(ctx, planned.word, planned.wm, 300, "#aaaaaa", (u) => u);
  expect(rects.length).toBeGreaterThan(0);
  expect(ctx.fillText).toHaveBeenCalled();
});

it("coalesces large wordmark grids into blocks so wide screens stay bounded", () => {
  const frame = document.createElement("div");
  const tiles: RawWordTile[] = [];
  for (let r = 0; r < 40; r++) {
    for (let c = 0; c < 40; c++) {
      tiles.push({ x: c * 10, y: 600 + r * 10, w: 10, h: 10, m: 0.9, li: c % 5 });
    }
  }
  const cast = makeCast(tiles);
  (frame as HTMLElement & { __axCast?: { wordTiles: () => WordCast | null } }).__axCast = {
    wordTiles: () => cast,
  };
  const planned = planWord(frame, makeCells(600), 800, 600);
  expect(planned.word.length).toBeLessThanOrEqual(900);
  expect(planned.word.length).toBeGreaterThan(200);
  expect(planned.word[0]?.w).toBe(20);
});
