// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { markArrival, takeArrival } from "./arrival";

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

it("no marker is no arrival", () => {
  expect(takeArrival()).toBe(false);
});

it("storage that refuses leaves the page a direct visit", () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("quota", "QuotaExceededError");
  });
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new DOMException("denied", "SecurityError");
  });
  expect(() => markArrival()).not.toThrow();
  expect(takeArrival()).toBe(false);
});
