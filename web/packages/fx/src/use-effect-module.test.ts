// @vitest-environment jsdom
import { act, createElement, useRef } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { own } from "./loop";
import { type Running, useEffectModule } from "./use-effect-module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The browser's scheduling, run by hand: frames and idle callbacks happen when the test says.
let frames: FrameRequestCallback[] = [];
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
  vi.stubGlobal(
    "IntersectionObserver",
    class {
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
