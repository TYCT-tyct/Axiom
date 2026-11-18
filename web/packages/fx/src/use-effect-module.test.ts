// @vitest-environment jsdom
import { act, createElement, useRef } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { own } from "./loop";
import { nearView, type Running, useEffectModule } from "./use-effect-module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The browser's scheduling, run by hand: frames and idle callbacks happen when the test says.
let frames: FrameRequestCallback[] = [];
// The last observer's callback, as the browser calls it when its element comes near the view or goes; gone once the
// observer is disconnected.
let near: ((isIntersecting: boolean) => void) | undefined;
let idle: IdleRequestCallback[] = [];
const flushFrames = () => {
  for (const callback of frames.splice(0)) callback(performance.now());
};
const flushIdle = () => {
  for (const callback of idle.splice(0)) callback({ didTimeout: false, timeRemaining: () => 50 });
};

function paintTiming(previous?: string) {
  let notify: ((name: string) => void) | undefined;
  vi.stubGlobal(
    "PerformanceObserver",
    class {
      static supportedEntryTypes = ["paint"];

      constructor(deliver: (list: { getEntries: () => { name: string }[] }) => void) {
        notify = (name) => deliver({ getEntries: () => [{ name }] });
      }

      observe({ buffered }: PerformanceObserverInit) {
        if (buffered && previous) notify?.(previous);
      }

      disconnect() {
        notify = undefined;
      }
    },
  );
  return (name: string) => notify?.(name);
}

beforeEach(() => {
  frames = [];
  idle = [];
  vi.stubGlobal("PerformanceObserver", undefined);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => frames.push(callback));
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("requestIdleCallback", (callback: IdleRequestCallback) => idle.push(callback));
  vi.stubGlobal("cancelIdleCallback", () => {});
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  near = undefined;
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(callback: IntersectionObserverCallback) {
        near = (isIntersecting) =>
          callback([{ isIntersecting } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
      }
      observe() {}
      unobserve() {}
      disconnect() {
        near = undefined;
      }
    },
  );
});

afterEach(() => vi.unstubAllGlobals());

// Written without JSX: a package with .tsx files is taken to have components with Tailwind classes, which every app
// must then name (scripts/sources.test.ts), and this one has none.
// An effect module as the sea is one: it owns its element on the loop until it is destroyed.
const effect = { start: (element: HTMLElement): Running => ({ destroy: own(element, () => {}) }) };

function Effect({ load }: { load: () => Promise<typeof effect> }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffectModule(ref, load, (module, element) => module.start(element));
  return createElement("div", { ref });
}

function mount(load: () => Promise<typeof effect>) {
  const host = document.body.appendChild(document.createElement("div"));
  const root = createRoot(host);
  act(() => root.render(createElement(Effect, { load })));
  return root;
}

it("waits for contentful paint even when the browser has already run a frame and gone idle", async () => {
  const paint = paintTiming();
  const load = vi.fn(async () => effect);
  const root = mount(load);
  flushFrames();
  paint("first-paint");
  await act(async () => flushIdle());
  expect(load).not.toHaveBeenCalled();

  paint("first-contentful-paint");
  expect(load).not.toHaveBeenCalled();
  await act(async () => flushIdle());
  expect(load).toHaveBeenCalledOnce();
  expect(window.axiomFx?.state).toMatchObject({ owners: 1, listening: true });

  act(() => root.unmount());
  expect(window.axiomFx?.state).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
});

it("stops observing paint if the effect unmounts before first content", async () => {
  const paint = paintTiming();
  const load = vi.fn(async () => effect);
  const root = mount(load);
  act(() => root.unmount());

  paint("first-contentful-paint");
  await act(async () => flushIdle());
  expect(load).not.toHaveBeenCalled();
});

it("can load when the browser painted its first content before the effect mounted", async () => {
  paintTiming("first-contentful-paint");
  const load = vi.fn(async () => effect);
  const root = mount(load);

  await act(async () => flushIdle());
  expect(load).toHaveBeenCalledOnce();
  act(() => root.unmount());
});

it("starts nothing when the page is left before the module arrives, and survives a module that fails", async () => {
  let arrive: (module: typeof effect) => void = () => {};
  const load = vi.fn(() => new Promise<typeof effect>((resolve) => (arrive = resolve)));
  const root = mount(load);
  flushFrames();
  await act(async () => flushIdle());
  expect(load).toHaveBeenCalledOnce();
  act(() => root.unmount());
  await act(async () => arrive(effect));
  expect(window.axiomFx?.state.owners).toBe(0);

  const failing = mount(() => Promise.reject(new Error("404")));
  flushFrames();
  await act(async () => flushIdle());
  expect(window.axiomFx?.state.owners).toBe(0);
  act(() => failing.unmount());
});

it("calls what lies below the first screen only once it nears the view, and not after it is cancelled", () => {
  const go = vi.fn();
  nearView(document.body.appendChild(document.createElement("div")), go);
  near?.(false);
  expect(go).not.toHaveBeenCalled();
  near?.(true);
  expect(go).toHaveBeenCalledOnce();

  const late = vi.fn();
  const cancel = nearView(document.body.appendChild(document.createElement("div")), late);
  cancel();
  expect(near).toBeUndefined();
});
