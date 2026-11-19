// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";

// The browser parts the loop reads, driven by hand: frames run when the test says, the viewport intersects what the
// test says, and motion is reduced when the test says.
let queued = new Map<number, FrameRequestCallback>();
let next = 1;
let now = 0;
let observed: { callback: IntersectionObserverCallback; targets: Set<Element> } | undefined;
let reduce = false;
const motionListeners = new Set<() => void>();
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
  reduce = false;
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
  vi.stubGlobal("matchMedia", () => ({
    get matches() {
      return reduce;
    },
    addEventListener: (_: string, listener: () => void) => motionListeners.add(listener),
    removeEventListener: (_: string, listener: () => void) => motionListeners.delete(listener),
  }));
});

afterEach(() => {
  for (const leave of owned.splice(0)) leave();
  vi.unstubAllGlobals();
  motionListeners.clear();
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

it("schedules nothing under reduced motion, and stops when it is turned on", async () => {
  reduce = true;
  const { own } = await loop();
  const element = document.createElement("div");
  const frame = vi.fn();
  own(element, frame);
  expect(observed).toBeUndefined();
  expect(state()).toEqual({ frames: 0, owners: 0, listening: false, pending: false });

  reduce = false;
  own(element, frame);
  intersect(element, true);
  runFrame();
  expect(frame).toHaveBeenCalledTimes(1);
  reduce = true;
  for (const listener of motionListeners) listener();
  runFrame();
  expect(frame).toHaveBeenCalledTimes(1);
  expect(state().pending).toBe(false);
});

it("tells a script each change of the setting, as it happens, until it stops listening", async () => {
  const { onMotion } = await loop();
  const heard: boolean[] = [];
  const stop = onMotion((allowed) => heard.push(allowed));
  const change = (to: boolean) => {
    reduce = to;
    for (const listener of [...motionListeners]) listener();
  };
  change(true);
  change(false);
  stop();
  change(true);
  expect(heard).toEqual([false, true]);
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
  expect(motionListeners.size).toBe(0);
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
