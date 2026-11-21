// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { type OceanEngine, pacedTick } from "./ocean";

afterEach(() => vi.restoreAllMocks());

// An engine whose drawing takes cost ms on the clock performance.now reads, and that records what it was asked to do.
function engine(cost: number) {
  let now = 0;
  vi.spyOn(performance, "now").mockImplementation(() => now);
  const asked: boolean[] = [];
  const fake = {
    tick: (_dt: number, draw = true) => {
      asked.push(draw);
      if (draw) now += cost;
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
])("$name", ({ cost, drawn }) => {
  const { fake, asked } = engine(cost);
  const step = pacedTick(fake);
  for (let i = 0; i < drawn.length; i++) step(1 / 60);
  expect(asked).toEqual(drawn);
});
