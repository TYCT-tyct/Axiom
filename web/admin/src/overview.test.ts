import { expect, it } from "vitest";
import { callStatus, callsCoverage, callsDrill, ordersDrill, overviewRange, overviewWindow } from "./overview";

it.each([
  { case: "24h", value: "24h", want: "24h" },
  { case: "7d", value: "7d", want: "7d" },
  { case: "30d", value: "30d", want: "30d" },
  { case: "an unknown range falls back to the day", value: "90d", want: "24h" },
  { case: "nothing yet", value: undefined, want: "24h" },
])("$case", ({ value, want }) => {
  expect(overviewRange(value)).toBe(want);
});

it("ends the window at the visit and spans the chosen range", () => {
  const now = new Date("2026-10-30T12:00:00.000Z");
  expect(overviewWindow("24h", now)).toEqual({ since: "2026-10-29T12:00:00.000Z", until: "2026-10-30T12:00:00.000Z" });
  expect(overviewWindow("7d", now).since).toBe("2026-10-23T12:00:00.000Z");
  expect(overviewWindow("30d", now).since).toBe("2026-09-30T12:00:00.000Z");
});

it("drills into the call list with the window and only the filters that narrow", () => {
  const window = { since: "2026-10-29T12:00:00.000Z", until: "2026-10-30T12:00:00.000Z" };
  expect(callsDrill(window)).toEqual(window);
  expect(callsDrill(window, { site: "site_1", account: "acct_1", model: "m", status: "error" })).toEqual({
    ...window,
    site_id: "site_1",
    account_id: "acct_1",
    model: "m",
    status: "error",
  });
  expect(ordersDrill(window)).toEqual({ ...window, cash: "1" });
});

it("reads only the results the contract has", () => {
  expect(callStatus("ok")).toBe("ok");
  expect(callStatus("error")).toBe("error");
  expect(callStatus("cancelled")).toBeUndefined();
  expect(callStatus(undefined)).toBeUndefined();
});

it("reads the call list's coverage across its pages", () => {
  expect(callsCoverage([{ incomplete: false }, { incomplete: false }])).toBe(false);
  expect(callsCoverage([{ incomplete: false }, { incomplete: true }])).toBe(true);
  // An older node does not answer the field, so a list it answered part of cannot be called complete.
  expect(callsCoverage([{ incomplete: false }, {}])).toBeUndefined();
  expect(callsCoverage([])).toBe(false);
});
