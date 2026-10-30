import { expect, it } from "vitest";
import { callWindow } from "./audit-entry";

it("covers a call from a month before the audit entry", () => {
  const window = callWindow("2026-10-30T12:00:00.000Z");
  expect(window).toEqual({ since: "2026-09-30T12:00:00.000Z", until: "2026-10-31T12:00:00.000Z" });
  expect(Date.parse(window.until ?? "") - Date.parse(window.since ?? "")).toBe(31 * 24 * 60 * 60 * 1000);
});

it("leaves the window to the call list when the entry has no time", () => {
  expect(callWindow(undefined)).toEqual({});
});
