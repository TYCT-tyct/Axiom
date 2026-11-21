// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";

// The browser parts the loop reads, driven by hand: frames run when the test says and the viewport intersects what the
// test says. matchMedia is not stubbed, and jsdom has none: the loop never reads the reduced-motion setting.
let queued = new Map<number, FrameRequestCallback>();
let next = 1;
let now = 0;
let observed: { callback: IntersectionObserverCallback; targets: Set<Element> } | undefined;
let hidden = false;

function runFrame(ms = 16) {
  now += ms;
  const callbacks = [...queued.values()];
  queued = new Map();
  for (const callback of callbacks) callback(now);
}

function intersect(target: Element, isIntersecting: boolean) {
  observed?.callback([{ target, isIntersecting } as IntersectionObserverEntry], {} as IntersectionObserver);
}

function setHidden(value: boolean) {
  hidden = value;
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.resetModules();
  queued = new Map();
  observed = undefined;
  hidden = false;
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    queued.set(next, callback);
    return next++;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => queued.delete(id));
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(callback: IntersectionObserverCallback) {
        observed = { callback, targets: new Set() };
      }
      observe(target: Element) {
        observed?.targets.add(target);
      }
      unobserve(target: Element) {
        observed?.targets.delete(target);
      }
      disconnect() {
        observed?.targets.clear();
      }
    },
  );
});

afterEach(() => {
  for (const leave of owned.splice(0)) leave();
  vi.unstubAllGlobals();
});

// Each test loads the loop afresh; what it leaves owned is let go after it, so its listeners do not reach the next one.
const owned: (() => void)[] = [];
// What the page exposes for the end-to-end tests, the loop's state as of now.
const state = () => {
  if (!window.axiomFx) throw new Error("the loop exposes no state");
  return window.axiomFx.state;
};

async function loop() {
  const module = await import("./loop");
  return {
    ...module,
    own: (element: Element, frame: (dt: number) => void) => {
      const leave = module.own(element, frame);
      owned.push(leave);
      return leave;
    },
  };
}

it("runs an owner's frames only while its element is in view and the page is visible", async () => {
  const { own } = await loop();
  const element = document.createElement("div");
  const frames: number[] = [];
  own(element, (dt) => frames.push(dt));
  runFrame();
  expect(frames).toEqual([]);
  expect(state().pending).toBe(false);

  intersect(element, true);
  runFrame();
  runFrame();
  expect(state().frames).toBe(2);
  // the first frame after a pause steps nothing; the next steps the time between them
  expect(frames).toEqual([0, 0.016]);

  setHidden(true);
  expect(state().pending).toBe(false);
  runFrame();
  expect(state().frames).toBe(2);
  setHidden(false);
  runFrame();
  expect(state().frames).toBe(3);

  intersect(element, false);
  runFrame();
  expect(state().frames).toBe(3);
  intersect(element, true);
  runFrame(1000);
  // after a pause the clock starts again rather than jumping by the time away
  expect(frames.at(-1)).toBe(0);
});

it("lets go of everything when the last owner leaves, and the counter starts again from 0", async () => {
  const { own } = await loop();
  const [a, b] = [document.createElement("div"), document.createElement("div")];
  const leaveA = own(a, () => {});
  const leaveB = own(b, () => {});
  intersect(a, true);
  intersect(b, true);
  runFrame();
  expect(state()).toMatchObject({ frames: 2, owners: 2, listening: true, pending: true });

  leaveA();
  expect(observed?.targets.has(a)).toBe(false);
  runFrame();
  expect(state().frames).toBe(3);

  leaveB();
  leaveB();
  expect(state()).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
  expect(queued.size).toBe(0);
  setHidden(false);
  expect(queued.size).toBe(0);
});

it("lets an owner leave from inside its own frame", async () => {
  const { own } = await loop();
  const element = document.createElement("div");
  const leave = own(element, () => leave());
  intersect(element, true);
  runFrame();
  expect(state()).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
});

it("moves a timeline's clock only on the loop's frames, and settles its sleeps false when it ends", async () => {
  await loop();
  const { timeline } = await import("./timeline");
  const element = document.createElement("div");
  const clock = timeline(element);
  const woke = vi.fn();
  clock.sleep(30).then(woke);
  const steps: number[] = [];
  clock.each((now) => steps.push(now) < 2);

  // out of view nothing moves: the screen stands still where it is
  runFrame();
  await Promise.resolve();
  expect([clock.now(), woke.mock.calls.length, steps.length]).toEqual([0, 0, 0]);

  intersect(element, true);
  runFrame(); // the first frame after a pause steps nothing
  runFrame(20);
  runFrame(20);
  await Promise.resolve();
  expect(clock.now()).toBe(40);
  expect(woke).toHaveBeenCalledWith(true);
  // a step that returns false runs no more
  expect(steps).toEqual([0, 20]);

  const later = vi.fn();
  clock.sleep(1000).then(later);
  clock.end();
  await Promise.resolve();
  expect(later).toHaveBeenCalledWith(false);
  expect(await clock.sleep(1)).toBe(false);
  expect(state().owners).toBe(0);
});

it("tells a step still waiting when its timeline ends, and a step added after the end at once", async () => {
  await loop();
  const { timeline } = await import("./timeline");
  const clock = timeline(document.createElement("div"));
  const ended = vi.fn();
  const done = vi.fn();
  clock.each(() => false, done);
  clock.each(() => true, ended);
  clock.end();
  expect(ended).toHaveBeenCalledTimes(1);
  expect(done).toHaveBeenCalledTimes(1);
  const late = vi.fn();
  clock.each(() => true, late);
  expect(late).toHaveBeenCalledTimes(1);
});

it("isolates a throwing owner so other owners keep running and the error logs once", async () => {
  const error = vi.spyOn(console, "error").mockImplementation(() => {});
  const { own } = await loop();
  const a = document.createElement("div");
  const b = document.createElement("div");
  const good: number[] = [];
  let badRuns = 0;
  const leaveBad = own(a, () => {
    badRuns++;
    throw new Error("boom");
  });
  own(b, (dt) => good.push(dt));
  intersect(a, true);
  intersect(b, true);

  runFrame();
  runFrame();
  expect(badRuns).toBe(1);
  expect(error).toHaveBeenCalledTimes(1);
  expect(good).toEqual([0, 0.016]);
  expect(state()).toMatchObject({ owners: 1, pending: true });
  expect(() => leaveBad()).not.toThrow();
  error.mockRestore();
});

it("stops cleanly when the only owner throws", async () => {
  const error = vi.spyOn(console, "error").mockImplementation(() => {});
  const { own } = await loop();
  const a = document.createElement("div");
  own(a, () => {
    throw new Error("solo boom");
  });
  intersect(a, true);
  runFrame();
  expect(error).toHaveBeenCalledTimes(1);
  expect(state()).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
  error.mockRestore();
});
