// @vitest-environment jsdom
import { act, createElement, useRef } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { own } from "./loop";
import { nearView, type Running, useEffectModule } from "./use-effect-module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The browser's scheduling, run by hand: frames and idle callbacks happen when the test says.
let frames: FrameRequestCallback[] = [];
// The last observer's callback, as the browser calls it when its element comes near the view or goes.
let near: ((isIntersecting: boolean) => void) | undefined;
let idle: IdleRequestCallback[] = [];
const flushFrames = () => {
  for (const callback of frames.splice(0)) callback(performance.now());
};
const flushIdle = () => {
  for (const callback of idle.splice(0)) callback({ didTimeout: false, timeRemaining: () => 50 });
};

beforeEach(() => {
  frames = [];
  idle = [];
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
      disconnect() {}
    },
  );
});

afterEach(() => vi.unstubAllGlobals());

// Written without JSX: a package with .tsx files is taken to have components with Tailwind classes, which every app
// must then name (scripts/sources.test.ts), and this one has none.
// An effect module as the sea is one: it owns its element on the loop until it is destroyed.
const effect = { start: (element: HTMLElement): Running => ({ destroy: own(element, () => {}) }) };

function Effect({ load, below }: { load: () => Promise<typeof effect>; below?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffectModule(ref, load, (module, element) => module.start(element), below ? nearView : undefined);
  return createElement("div", { ref });
}

function mount(load: () => Promise<typeof effect>, below = false) {
  const host = document.body.appendChild(document.createElement("div"));
  const root = createRoot(host);
  act(() => root.render(createElement(Effect, { load, below })));
  return root;
}

it("loads the module only after a frame and an idle moment, and lets go of everything on unmount", async () => {
  const load = vi.fn(async () => effect);
  const root = mount(load);
  expect(load).not.toHaveBeenCalled();
  flushFrames();
  expect(load).not.toHaveBeenCalled();
  await act(async () => flushIdle());
  expect(load).toHaveBeenCalledOnce();
  expect(window.axiomFx?.state).toMatchObject({ owners: 1, listening: true });

  act(() => root.unmount());
  expect(window.axiomFx?.state).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
});

it("starts nothing when the page is left before the module arrives, and survives a module that fails", async () => {
  let arrive: (module: typeof effect) => void = () => {};
  const root = mount(() => new Promise((resolve) => (arrive = resolve)));
  flushFrames();
  await act(async () => flushIdle());
  act(() => root.unmount());
  await act(async () => arrive(effect));
  expect(window.axiomFx?.state.owners).toBe(0);

  const failing = mount(() => Promise.reject(new Error("404")));
  flushFrames();
  await act(async () => flushIdle());
  expect(window.axiomFx?.state.owners).toBe(0);
  act(() => failing.unmount());
});

it("loads what lies below the first screen only once it nears the view", async () => {
  const load = vi.fn(async () => effect);
  const root = mount(load, true);
  flushFrames();
  await act(async () => flushIdle());
  near?.(false);
  expect(load).not.toHaveBeenCalled();

  await act(async () => near?.(true));
  expect(load).toHaveBeenCalledOnce();
  expect(window.axiomFx?.state.owners).toBe(1);
  act(() => root.unmount());
  expect(window.axiomFx?.state.owners).toBe(0);
});
