// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { type OceanEngine, pacedTick } from "./ocean";

afterEach(() => vi.restoreAllMocks());

// An engine whose drawings take cost ms each on the clock performance.now reads (a list: one after another, then the
// last again), and that records what it was asked to do.
function engine(cost: number | number[]) {
  const costs = typeof cost === "number" ? [cost] : [...cost];
  let now = 0;
  vi.spyOn(performance, "now").mockImplementation(() => now);
  const asked: boolean[] = [];
  const fake = {
    tick: (_dt: number, draw = true) => {
      asked.push(draw);
      if (draw) now += (costs.length > 1 ? costs.shift() : costs[0]) ?? 0;
    },
  } as unknown as OceanEngine;
  return { fake, asked };
}

it.each([
  { name: "a cheap sea is drawn every frame", cost: 2, drawn: [true, true, true, true, true, true] },
  {
    name: "a dear one every other frame, the water stepped on the others",
    cost: 12,
    drawn: [true, false, true, false, true, false],
  },
  {
    name: "a dear first drawing (a cold start) does not hold a cheap sea to every other frame",
    cost: [100, 2],
    drawn: [true, false, true, false, true, false, true, true, true],
  },
])("$name", ({ cost, drawn }) => {
  const { fake, asked } = engine(cost);
  const step = pacedTick(fake);
  for (let i = 0; i < drawn.length; i++) step(1 / 60);
  expect(asked).toEqual(drawn);
});
