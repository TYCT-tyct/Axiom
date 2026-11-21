import { describe, expect, it, vi } from "vitest";
import { type Engine, tend } from "./moments";

// A sea with one shark heading nowhere in particular, length 100, its head at the origin; the world's own steps do
// nothing, so what the eggs add is all that moves.
function sea() {
  const shark = { nodes: new Float32Array(40), length: 100, orbitDir: 1, lure: null as unknown };
  const stamp = vi.fn();
  const world = {
    fish: [],
    sharks: [shark],
    pointer: { active: true },
    time: 0,
    width: 1000,
    height: 800,
    stepFish: () => {},
  };
  const renderer = { energy: new Float32Array(1), stamp, drawFish: () => {}, depositWake: () => {} };
  return { engine: { world, renderer } as unknown as Engine, shark, stamp, world };
}

const heading = (lure: { x: number; y: number }) => Math.atan2(lure.y, lure.x);

describe("the shark's visit", () => {
  it.each([
    // far out it heads mostly in, a little round; on its ring (0.6 of its length) it swims straight round
    { at: { x: 600, y: 0 }, want: Math.atan2(1.3, 2.4 * 1.5) },
    { at: { x: 60, y: 0 }, want: Math.PI / 2 },
  ])("steers round the point as ocean-react's lure does ($at.x)", ({ at, want }) => {
    const { engine, shark } = sea();
    const free = tend(engine).lure(at.x, at.y);
    engine.world.stepFish(1 / 60);
    const lure = shark.lure as { x: number; y: number; w: number };
    expect(heading(lure)).toBeCloseTo(want, 5);
    free();
    expect(shark.lure).toBeNull();
  });
});

it("leaves a whirlpool's wake as the design's 0.25 of the pointer's", () => {
  const { engine, stamp, world } = sea();
  tend(engine).whirl(50, 60, 7);
  engine.renderer.depositWake(world as unknown as Engine["world"]);
  expect(stamp).toHaveBeenCalledWith(engine.renderer.energy, 50, 60, 1, 0.02);
});
