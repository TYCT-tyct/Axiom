// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { markArrival, markOrigin, markReturn, peekReturn, takeArrival, takeOrigin, takeReturn } from "./arrival";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  sessionStorage.clear();
});

it.each([
  { name: "a fresh marker is taken once", wait: 0, first: true },
  { name: "a marker older than its ten seconds is stale", wait: 10_000, first: false },
])("$name", ({ wait, first }) => {
  vi.useFakeTimers({ now: 1_000_000 });
  markArrival();
  vi.setSystemTime(1_000_000 + wait);
  expect(takeArrival()).toBe(first);
  expect(takeArrival()).toBe(false);
  // nothing but the time it was set is kept
  expect(sessionStorage.length).toBe(0);
});

it("a return can be seen without being taken, until it is taken", () => {
  markReturn();
  expect(peekReturn()).toBe(true);
  expect(peekReturn()).toBe(true);
  expect(takeReturn()).toBe(true);
  expect(peekReturn()).toBe(false);
});

it("no marker is no arrival", () => {
  expect(takeArrival()).toBe(false);
  expect(takeReturn()).toBe(false);
  expect(takeOrigin()).toBeNull();
});

it("origin rect and return markers are one-shot and expire after ten seconds", () => {
  vi.useFakeTimers({ now: 2_000_000 });
  const rect = { x: 100, y: 20, w: 80, h: 36 };
  markOrigin(rect);
  markReturn();
  expect(takeOrigin()).toEqual(rect);
  expect(takeOrigin()).toBeNull();
  expect(takeReturn()).toBe(true);
  expect(takeReturn()).toBe(false);
  expect(sessionStorage.length).toBe(0);

  markOrigin(rect);
  markReturn();
  vi.setSystemTime(2_010_000);
  expect(takeOrigin()).toBeNull();
  expect(takeReturn()).toBe(false);
});

it("storage that refuses leaves the page a direct visit", () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("quota", "QuotaExceededError");
  });
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new DOMException("denied", "SecurityError");
  });
  expect(() => markArrival()).not.toThrow();
  expect(() => markOrigin({ x: 0, y: 0, w: 10, h: 10 })).not.toThrow();
  expect(() => markReturn()).not.toThrow();
  expect(takeArrival()).toBe(false);
  expect(takeOrigin()).toBeNull();
  expect(takeReturn()).toBe(false);
});
