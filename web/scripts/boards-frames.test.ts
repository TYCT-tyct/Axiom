// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { installFrameGate } from "../e2e/design/frames.ts";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  delete window.designFrames;
});

it("anchors performance and animation frames to the paired wall clock despite a different loading epoch", () => {
  vi.stubGlobal("requestAnimationFrame", window.requestAnimationFrame);
  vi.stubGlobal("cancelAnimationFrame", window.cancelAnimationFrame);
  vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] });
  vi.setSystemTime(new Date("2026-10-24T04:00:00Z"));
  const origin = Date.now();
  for (const elapsed of [21, 4251]) {
    vi.stubGlobal("performance", { now: () => elapsed });
    installFrameGate(origin);
    const times: number[] = [];
    expect(performance.now()).toBe(0);
    requestAnimationFrame((time) => times.push(time));
    vi.advanceTimersByTime(16);
    expect(times).toEqual([16]);
    expect(performance.now()).toBe(16);
    vi.setSystemTime(origin);
  }
});

it("holds pending and newly requested frames during capture, then resumes uncancelled callbacks", () => {
  const native = new Map<number, FrameRequestCallback>();
  let next = 100;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    native.set(++next, callback);
    return next;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => native.delete(id));
  installFrameGate();
  const before = vi.fn(),
    during = vi.fn(),
    cancelled = vi.fn();
  window.requestAnimationFrame(before);
  window.designFrames?.pause();
  window.requestAnimationFrame(during);
  window.cancelAnimationFrame(window.requestAnimationFrame(cancelled));
  expect(native.size).toBe(0);
  window.designFrames?.resume();
  expect(native.size).toBe(2);
  for (const callback of native.values()) callback(5000);
  expect(before).toHaveBeenCalledWith(5000);
  expect(during).toHaveBeenCalledWith(5000);
  expect(cancelled).not.toHaveBeenCalled();
});
