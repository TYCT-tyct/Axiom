// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type EggsOptions, startEggs } from "./eggs";
import type { Engine } from "./moments";
import type { Sea } from "./sea";

// Tests for startEggs's night dimming (dark only, never light) and the sea-floor "back to the surface" button
// hiding however the reader leaves the floor.

class StubObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function makeSea(): Sea {
  const engine: Engine = {
    world: {
      fish: [],
      sharks: [],
      pointer: { active: false },
      time: 0,
      width: 1000,
      height: 800,
      stepFish: () => {},
    },
    renderer: {
      ctx: {} as CanvasRenderingContext2D,
      cols: 1,
      cw: 10,
      ch: 14,
      fishBuckets: [],
      fishStyles: [],
      energy: new Float32Array(1),
      stamp: () => {},
      drawFish: () => {},
      depositWake: () => {},
    },
    local: () => ({ x: 0, y: 0, inside: true }),
  };
  return {
    engine: engine as unknown as Sea["engine"],
    awake: () => () => {},
    destroy: () => {},
  };
}

function makeFrame(scheme: "light" | "dark", scroll = { top: 0, height: 4000, view: 1000 }) {
  const frame = document.createElement("div");
  frame.className = `tr ${scheme}`;
  const sea = document.createElement("canvas");
  sea.className = "sea";
  const ho = document.createElement("div");
  ho.className = `ho ${scheme}`;
  const depth = document.createElement("div");
  depth.className = "depth";
  const hs = document.createElement("div");
  hs.className = "hs";
  hs.append(document.createElement("main"));
  Object.defineProperty(hs, "scrollHeight", { configurable: true, get: () => scroll.height });
  Object.defineProperty(hs, "clientHeight", { configurable: true, get: () => scroll.view });
  Object.defineProperty(hs, "scrollTop", {
    configurable: true,
    get: () => scroll.top,
    set: (v: number) => {
      scroll.top = v;
    },
  });
  ho.append(depth, hs);
  frame.append(sea, ho);
  document.body.append(frame);
  return { frame, ho, depth, hs, scroll };
}

function options(floor: (shown: boolean) => void): EggsOptions {
  return {
    glow: "glow",
    terminal: { open: () => false, show: () => {} },
    gauge: () => {},
    floor,
  };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("ResizeObserver", StubObserver);
  vi.stubGlobal("IntersectionObserver", StubObserver);
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
    setTransform: () => {},
    clearRect: () => {},
    fillText: () => {},
    globalAlpha: 1,
    fillStyle: "",
    font: "",
    textAlign: "center",
    textBaseline: "middle",
  } as unknown as CanvasRenderingContext2D);
});

afterEach(() => {
  document.body.innerHTML = "";
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("night dimming", () => {
  it("leaves the light theme untouched between 22:00 and 06:00 and dims only the dark sea", async () => {
    vi.setSystemTime(new Date(2026, 10, 20, 23, 0, 0));
    const { frame, ho, depth } = makeFrame("light");
    depth.dataset.dim = "0.000";
    depth.style.opacity = "0.000";
    const eggs = startEggs(
      frame,
      makeSea(),
      options(() => {}),
    );

    vi.advanceTimersByTime(10_500);
    expect(depth.dataset.floor).toBeUndefined();
    expect(Number(depth.style.opacity)).toBe(0);

    // switching to dark at night applies the 0.12 floor
    ho.classList.remove("light");
    ho.classList.add("dark");
    await Promise.resolve();
    expect(depth.dataset.floor).toBe("0.12");
    expect(Number(depth.style.opacity)).toBeCloseTo(0.12, 3);

    // switching back to light clears the floor and restores the scroll dim
    depth.dataset.dim = "0.250";
    ho.classList.remove("dark");
    ho.classList.add("light");
    await Promise.resolve();
    expect(depth.dataset.floor).toBeUndefined();
    expect(Number(depth.style.opacity)).toBeCloseTo(0.25, 3);

    eggs.destroy();
  });

  it("applies the 0.12 floor at night when opened in the dark theme", () => {
    vi.setSystemTime(new Date(2026, 10, 20, 2, 30, 0));
    const { frame, depth } = makeFrame("dark");
    const eggs = startEggs(
      frame,
      makeSea(),
      options(() => {}),
    );

    vi.advanceTimersByTime(10_500);
    expect(depth.dataset.floor).toBe("0.12");
    expect(Number(depth.style.opacity)).toBeCloseTo(0.12, 3);

    eggs.destroy();
  });
});

describe("sea-floor return button", () => {
  it("hides when the reader scrolls back toward the surface manually and offers again on the next rest", () => {
    vi.setSystemTime(new Date(2026, 10, 20, 10, 0, 0));
    const floor = vi.fn();
    const { frame, hs, scroll } = makeFrame("dark", { top: 3000, height: 4000, view: 1000 });
    const eggs = startEggs(frame, makeSea(), options(floor));

    // rest 3 s on the sea floor -> button appears
    vi.advanceTimersByTime(3_500);
    expect(floor).toHaveBeenCalledTimes(1);
    expect(floor).toHaveBeenLastCalledWith(true);

    // reader scrolls back to the surface manually (wheel/scrollbar)
    scroll.top = 0;
    hs.dispatchEvent(new Event("scroll"));
    expect(floor).toHaveBeenCalledTimes(2);
    expect(floor).toHaveBeenLastCalledWith(false);

    // after fade-out (250 ms), returning to the sea floor for 3 s offers it again
    vi.advanceTimersByTime(300);
    scroll.top = 3000;
    vi.advanceTimersByTime(3_500);
    expect(floor).toHaveBeenCalledTimes(3);
    expect(floor).toHaveBeenLastCalledWith(true);

    // leaving the floor even without a scroll event (observed on the next tick) still hides it
    scroll.top = 2000;
    vi.advanceTimersByTime(250);
    expect(floor).toHaveBeenCalledTimes(4);
    expect(floor).toHaveBeenLastCalledWith(false);

    eggs.destroy();
  });
});
