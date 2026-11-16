// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { installFrameGate } from "../e2e/design/frames.ts";

afterEach(() => {
  vi.unstubAllGlobals();
  delete window.designFrames;
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
