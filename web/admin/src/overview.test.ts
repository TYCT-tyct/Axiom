import { expect, it } from "vitest";
import {
  callStatus,
  callsArchivedWhole,
  callsCoverage,
  callsDrill,
  callsEmptyKind,
  cashView,
  ordersDrill,
  overviewRange,
  overviewWindow,
  paidOrdersDrill,
  retentionOrdersDrill,
} from "./overview";

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
  expect(cashView("1")).toBe("1");
  expect(cashView('"1"')).toBe("1");
  expect(cashView(1)).toBe("1");
  expect(cashView("ledger")).toBeUndefined();
  expect(paidOrdersDrill(window)).toEqual({ ...window, paid: "batches" });
});

it("opens retention at the previous UTC month's cohort", () => {
  expect(retentionOrdersDrill({ since: "2026-10-29T12:00:00.000Z", until: "2026-10-30T12:00:00.000Z" })).toEqual({
    since: "2026-09-01T00:00:00.000Z",
    until: "2026-10-30T12:00:00.000Z",
    paid: "batches",
  });
  // The window ends at midnight on the first: its last instant is still in the previous month.
  expect(retentionOrdersDrill({ since: "2026-10-25T00:00:00.000Z", until: "2026-11-01T00:00:00.000Z" })).toEqual({
    since: "2026-09-01T00:00:00.000Z",
    until: "2026-11-01T00:00:00.000Z",
    paid: "batches",
  });
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

it("calls a window wholly archived only when its end is at or before the boundary", () => {
  expect(callsArchivedWhole(true, "2026-11-01T00:00:00.000Z", "2026-11-01T00:00:00.000Z")).toBe(true);
  expect(callsArchivedWhole(true, "2026-11-01T00:00:00.000Z", "2026-10-31T00:00:00.000Z")).toBe(true);
  expect(callsArchivedWhole(true, "2026-11-01T00:00:00.000Z", "2026-12-15T00:00:00.000Z")).toBe(false);
  // No monthly partition left: the node has archived every call it could have answered.
  expect(callsArchivedWhole(true, undefined, "2026-12-15T00:00:00.000Z")).toBe(true);
  expect(callsArchivedWhole(false, "2026-11-01T00:00:00.000Z", "2026-12-15T00:00:00.000Z")).toBe(false);
  expect(callsArchivedWhole(undefined, "2026-11-01T00:00:00.000Z", "2026-12-15T00:00:00.000Z")).toBe(false);
});

it("does not describe a partly archived empty list as archived", () => {
  expect(callsEmptyKind(true, false, false)).toBe("retainedWindow");
  expect(callsEmptyKind(true, false, true)).toBe("retainedFiltered");
  expect(callsEmptyKind(true, true, true)).toBe("archived");
  expect(callsEmptyKind(false, false, true)).toBe("filtered");
  expect(callsEmptyKind(undefined, false, false)).toBe("window");
});
